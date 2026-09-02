package models

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ─── Schema snapshot ─────────────────────────────────────────────────────────

// TableKeys holds the columns a table's storage depends on. A column named in
// any of them cannot be dropped — ALTER TABLE … DROP COLUMN refuses — so the
// report must never present one as unused however little Grafana reads it.
type TableKeys struct {
	Primary   []string
	Sorting   []string
	Partition []string
}

// Contains reports whether a column is part of any key.
func (k TableKeys) Contains(column string) bool {
	for _, group := range [][]string{k.Primary, k.Sorting, k.Partition} {
		for _, name := range group {
			if strings.EqualFold(name, column) {
				return true
			}
		}
	}
	return false
}

// Role names the key a column belongs to, for the report's reason field.
func (k TableKeys) Role(column string) string {
	for _, candidate := range []struct {
		role    string
		columns []string
	}{
		{role: "primary-key", columns: k.Primary},
		{role: "sorting-key", columns: k.Sorting},
		{role: "partition-key", columns: k.Partition},
	} {
		for _, name := range candidate.columns {
			if strings.EqualFold(name, column) {
				return candidate.role
			}
		}
	}
	return ""
}

// SchemaSnapshot is everything the usage analysis needs to know about
// ClickHouse, as plain data. Keeping it free of the client is what makes the
// verdict logic testable: api.Handler holds a concrete *ClickHouseClient whose
// connection is unexported and whose constructor pings on creation, so anything
// that reached for the client directly could not be tested at all.
type SchemaSnapshot struct {
	// Columns maps "database.table" to its columns, in position order.
	Columns map[string][]ColumnInfo
	// Keys maps "database.table" to its storage keys.
	Keys map[string]TableKeys
	// Engines maps "database.table" to its raw engine name.
	Engines map[string]string
	// ViewQueries maps a materialized view to its stored CREATE statement. It is
	// how usage on a view's destination is traced back to the source columns the
	// view reads.
	ViewQueries map[string]string
	// Relations is the table-to-table edge list, copied from the relation cache.
	Relations []TableRelation
}

// Tables returns every known table, sorted, so callers iterate deterministically.
func (s SchemaSnapshot) Tables() []string {
	names := make([]string, 0, len(s.Columns))
	for name := range s.Columns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ColumnNames returns the column names of one table.
func (s SchemaSnapshot) ColumnNames(table string) []string {
	columns := s.Columns[table]
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, column.Name)
	}
	return names
}

// Lookup adapts the snapshot to the resolver's ColumnLookup, expanding a
// pattern name to the union of every table it matches.
func (s SchemaSnapshot) Lookup() ColumnLookup {
	return func(table string) []string {
		if columns, ok := s.Columns[table]; ok {
			names := make([]string, 0, len(columns))
			for _, column := range columns {
				names = append(names, column.Name)
			}
			return names
		}
		if !IsTablePattern(table) {
			return nil
		}

		union := []string{}
		seen := map[string]bool{}
		for _, candidate := range s.Tables() {
			if !TablePatternMatches(table, candidate) {
				continue
			}
			for _, column := range s.Columns[candidate] {
				if !seen[column.Name] {
					seen[column.Name] = true
					union = append(union, column.Name)
				}
			}
		}
		return union
	}
}

// MatchTables resolves a table reference to the concrete tables it names. A
// plain name resolves to itself when it exists; a pattern resolves to every
// table it matches, because the dashboard reads whichever value the viewer picks.
func (s SchemaSnapshot) MatchTables(reference string) []string {
	if _, ok := s.Columns[reference]; ok {
		return []string{reference}
	}
	if !IsTablePattern(reference) {
		return nil
	}

	matches := []string{}
	for _, candidate := range s.Tables() {
		if TablePatternMatches(reference, candidate) {
			matches = append(matches, candidate)
		}
	}
	return matches
}

// LoadSchemaSnapshot reads the schema in two queries.
//
// Two, not one per table: the verdict pass needs every column of every table,
// and calling GetTableColumns per table would cost two round trips each — some
// 350 against the reference schema — on every refresh. BuildColumnIndex already
// returns all columns in a single query; this adds one more for the storage keys
// and engines, which system.columns does not carry.
//
// Both are SELECTs against system tables, so the read-only contract holds.
func LoadSchemaSnapshot(client *ClickHouseClient) (SchemaSnapshot, error) {
	snapshot := SchemaSnapshot{
		Columns:     map[string][]ColumnInfo{},
		Keys:        map[string]TableKeys{},
		Engines:     map[string]string{},
		ViewQueries: map[string]string{},
	}

	index, err := client.BuildColumnIndex()
	if err != nil {
		return snapshot, fmt.Errorf("failed to build column index: %w", err)
	}
	for _, entry := range index {
		key := entry.Database + "." + entry.Table
		snapshot.Columns[key] = append(snapshot.Columns[key], ColumnInfo{
			Name: entry.Name,
			Type: entry.Type,
		})
	}

	if err := client.loadTableKeys(snapshot); err != nil {
		return snapshot, err
	}

	// getTablesRelations is lazy — nothing in main.go calls it, and it fires on
	// the first /api/databases or /api/dataflow request. A scan that ran before
	// any of those would index against nil relations and silently report that
	// nothing is connected to anything, so warm it here rather than hoping a user
	// clicked something first. It caches, so a later call is free.
	relations, err := client.getTablesRelations()
	if err != nil {
		return snapshot, fmt.Errorf("failed to warm the relation cache: %w", err)
	}
	snapshot.Relations = append(snapshot.Relations, relations...)
	return snapshot, nil
}

// loadTableKeys fills the engine and key maps from system.tables.
//
// The database filter repeats the list in BuildColumnIndex and allowedDatabase
// on purpose — .ai/rules.md rule 4 requires the three to change together, and a
// snapshot that covered databases the sidebar hides would report unused columns
// for tables the user cannot see.
func (c *ClickHouseClient) loadTableKeys(snapshot SchemaSnapshot) error {
	query := `
		SELECT database, name, engine, primary_key, sorting_key, partition_key, create_table_query
		FROM system.tables
		WHERE database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA', 'performance_schema', 'mysql')
	`
	rows, err := c.conn.Query(context.Background(), query)
	if err != nil {
		return fmt.Errorf("failed to query system.tables for keys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var database, name, engine, primary, sorting, partition, createQuery string
		if err := rows.Scan(&database, &name, &engine, &primary, &sorting, &partition, &createQuery); err != nil {
			return fmt.Errorf("failed to scan table key row: %w", err)
		}

		key := database + "." + name
		snapshot.Engines[key] = engine
		snapshot.Keys[key] = TableKeys{
			Primary:   ParseKeyExpression(primary),
			Sorting:   ParseKeyExpression(sorting),
			Partition: ParseKeyExpression(partition),
		}
		if ClassifyEngine(engine) == EngineMView {
			snapshot.ViewQueries[key] = createQuery
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating table key rows: %w", err)
	}
	return nil
}

// keyIdentifierPattern finds the names inside a key expression.
var keyIdentifierPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// keyExpressionFunctions are the wrappers that routinely appear in a sorting or
// partition key. They are names in the expression but not columns of the table.
var keyExpressionFunctions = map[string]bool{
	"tuple": true, "toyyyymm": true, "toyyyymmdd": true, "todate": true,
	"todatetime": true, "tostartofday": true, "tostartofhour": true,
	"tostartofmonth": true, "tostartofweek": true, "tostartofminute": true,
	"tostartofinterval": true, "cityhash64": true, "siphash64": true,
	"intdiv": true, "modulo": true, "torelativedaynum": true,
	"tomonday": true, "toyear": true, "tohour": true, "tominute": true,
	"tosecond": true, "tounixtimestamp": true, "murmurhash3_64": true,
	"halfmd5": true, "xxhash64": true, "coalesce": true, "ifnull": true,
	"interval": true, "and": true, "or": true, "not": true,
}

// ParseKeyExpression extracts the column names from a key expression.
//
// system.tables stores these as expressions, not name lists: a sorting key can
// read "(toYYYYMM(ts), user_id)". Every identifier that is not a known wrapper
// function is taken to be a column, which errs toward protecting a column from
// being reported unused.
func ParseKeyExpression(expression string) []string {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return nil
	}

	names := []string{}
	seen := map[string]bool{}
	for _, candidate := range keyIdentifierPattern.FindAllString(expression, -1) {
		if keyExpressionFunctions[strings.ToLower(candidate)] || seen[candidate] {
			continue
		}
		seen[candidate] = true
		names = append(names, candidate)
	}
	return names
}

// ─── Usage index ─────────────────────────────────────────────────────────────

// UsageRef is one dashboard panel reading one thing, and the evidence for it.
type UsageRef struct {
	DashboardUID   string     `json:"dashboard_uid"`
	DashboardTitle string     `json:"dashboard_title"`
	DashboardURL   string     `json:"dashboard_url"`
	PanelID        int        `json:"panel_id"`
	PanelTitle     string     `json:"panel_title"`
	Via            string     `json:"via,omitempty"`
	Confidence     Confidence `json:"confidence"`
	Snippet        string     `json:"snippet,omitempty"`
}

// key identifies a column reference for deduplication. Confidence is part of it
// because the same panel can reach one column exactly and another only by guess.
func (r UsageRef) key() string {
	return fmt.Sprintf("%s|%d|%s|%s", r.DashboardUID, r.PanelID, r.Via, r.Confidence)
}

// tableKey identifies a table-level reference. Confidence is left out: at table
// level the question is only whether this panel reads the table at all, and
// including it would list the same panel once per confidence level.
func (r UsageRef) tableKey() string {
	return fmt.Sprintf("%s|%d|%s", r.DashboardUID, r.PanelID, r.Via)
}

// ResolvedQuery pairs one query's resolution with the panel it came from.
type ResolvedQuery struct {
	Source UsageRef
	Result QueryParseResult
}

// UsageIndex is what the dashboards turned out to read. Verdicts are assigned
// from it separately, so this stage stays a pure accumulation of evidence.
type UsageIndex struct {
	// columns maps "db.table" to column name to the references that read it.
	columns map[string]map[string][]UsageRef
	// tables maps "db.table" to references that read the table at all, whether or
	// not any column could be pinned down.
	tables map[string][]UsageRef
	// opaque maps "db.table" to the reasons its column set could not be
	// enumerated: a star projection, a variable column, or a failed parse.
	opaque map[string][]string
	// unlinkedDistributed names Distributed tables whose local table could not be
	// identified, so nothing behind them can be judged.
	unlinkedDistributed []string
}

// Columns returns the references that read one column.
func (i UsageIndex) Columns(table, column string) []UsageRef {
	return i.columns[table][column]
}

// TableRefs returns the references that read a table at all.
func (i UsageIndex) TableRefs(table string) []UsageRef {
	return i.tables[table]
}

// IsOpaque reports whether a table's column set could not be enumerated, and why.
func (i UsageIndex) IsOpaque(table string) (bool, string) {
	reasons := i.opaque[table]
	if len(reasons) == 0 {
		return false, ""
	}
	return true, reasons[0]
}

// UnlinkedDistributed names Distributed tables with no resolvable local table.
func (i UsageIndex) UnlinkedDistributed() []string {
	return i.unlinkedDistributed
}

func newUsageIndex() UsageIndex {
	return UsageIndex{
		columns: map[string]map[string][]UsageRef{},
		tables:  map[string][]UsageRef{},
		opaque:  map[string][]string{},
	}
}

func (i UsageIndex) addColumn(table, column string, ref UsageRef) {
	byColumn, ok := i.columns[table]
	if !ok {
		byColumn = map[string][]UsageRef{}
		i.columns[table] = byColumn
	}
	for _, existing := range byColumn[column] {
		if existing.key() == ref.key() {
			return
		}
	}
	byColumn[column] = append(byColumn[column], ref)
}

func (i UsageIndex) addTable(table string, ref UsageRef) {
	for _, existing := range i.tables[table] {
		if existing.tableKey() == ref.tableKey() {
			return
		}
	}
	// Confidence belongs to a column, not to "this panel reads this table".
	ref.Confidence = ""
	i.tables[table] = append(i.tables[table], ref)
}

func (i UsageIndex) addOpaque(table, reason string) {
	for _, existing := range i.opaque[table] {
		if existing == reason {
			return
		}
	}
	i.opaque[table] = append(i.opaque[table], reason)
}

// BuildUsageIndex turns resolved queries into per-column evidence, then
// propagates that evidence along ClickHouse's own lineage.
//
// It is a pure function of its inputs so the whole thing can be exercised from
// literals.
func BuildUsageIndex(snapshot SchemaSnapshot, queries []ResolvedQuery) UsageIndex {
	index := newUsageIndex()

	for _, query := range queries {
		index.recordQuery(snapshot, query)
	}

	index.propagate(snapshot)
	// Assigned here rather than inside a method: the maps are reference types, but
	// a slice field set on a value receiver would not survive the call.
	index.unlinkedDistributed = findUnlinkedDistributed(snapshot)
	return index
}

// recordQuery attributes one query's references to concrete tables.
func (i UsageIndex) recordQuery(snapshot SchemaSnapshot, query ResolvedQuery) {
	for _, reference := range query.Result.References {
		source := query.Source
		source.Confidence = reference.Confidence
		if reference.Snippet != "" {
			source.Snippet = reference.Snippet
		}

		for _, table := range snapshot.MatchTables(reference.Database + "." + reference.Table) {
			i.addColumn(table, reference.Column, source)
			i.addTable(table, source)
		}
	}

	// A table the query reads is recorded even when no column could be pinned
	// down: that is what separates "referenced but unreadable" from "nothing
	// references this at all".
	reasons := query.Result.opacityReasons()
	for _, referenced := range query.Result.Tables {
		for _, table := range snapshot.MatchTables(referenced) {
			i.addTable(table, query.Source)
			for _, reason := range reasons {
				i.addOpaque(table, reason)
			}
		}
	}
}

// opacityReasons names every way this query's column set escaped enumeration.
func (r QueryParseResult) opacityReasons() []string {
	reasons := []string{}
	if r.SelectStar {
		reasons = append(reasons, "select-star")
	}
	if r.OpaqueColumn {
		reasons = append(reasons, "variable-column")
	}
	if r.ParseError != "" {
		reasons = append(reasons, "unparsed-query")
	}
	return reasons
}

// propagate carries usage along ClickHouse's lineage.
//
// The two engine families point in opposite directions and must be handled
// separately. getTablesRelations emits {DependsOnTable: distributed, Table: local}
// for a Distributed wrapper but {DependsOnTable: source, Table: view} for a
// materialized view, so a single traversal would follow one of them backwards.
func (i UsageIndex) propagate(snapshot SchemaSnapshot) {
	for depth := 0; depth < maxRelationDepth; depth++ {
		// Both must run every round: || would skip the view pass whenever the
		// distributed pass had changed something.
		distributed := i.propagateDistributed(snapshot)
		views := i.propagateViews(snapshot)
		if !distributed && !views {
			return
		}
	}
}

// propagateDistributed copies usage from a Distributed wrapper to the local
// table that actually stores the data.
func (i UsageIndex) propagateDistributed(snapshot SchemaSnapshot) bool {
	changed := false

	for _, relation := range snapshot.Relations {
		wrapper, local := relation.DependsOnTable, relation.Table
		if wrapper == "" || local == "" {
			continue
		}
		if ClassifyEngine(snapshot.Engines[wrapper]) != EngineDistributed {
			continue
		}

		for column, refs := range i.columns[wrapper] {
			for _, ref := range refs {
				derived := ref
				derived.Via = "distributed:" + wrapper
				if i.addIfNew(local, column, derived) {
					changed = true
				}
			}
		}
		if len(i.tables[wrapper]) > 0 {
			for _, ref := range i.tables[wrapper] {
				derived := ref
				derived.Via = "distributed:" + wrapper
				i.addTable(local, derived)
			}
			if opaque, reason := i.IsOpaque(wrapper); opaque {
				i.addOpaque(local, reason)
			}
		}
	}

	return changed
}

// propagateViews carries usage of a view's destination back to the columns the
// view reads from its sources.
//
// Deliberately table-level rather than column-level: extractColumnMappings
// resolves at most one source column per projection expression and leaves its
// SourceTable empty, so "SELECT a + b AS c" would attribute only a and quietly
// make b droppable. If anything downstream of a view is read, every column the
// view's SELECT touches — projection, WHERE, GROUP BY and joins alike — is read.
func (i UsageIndex) propagateViews(snapshot SchemaSnapshot) bool {
	changed := false

	for _, relation := range snapshot.Relations {
		view, destination := relation.DependsOnTable, relation.Table
		if view == "" || destination == "" {
			continue
		}
		if ClassifyEngine(snapshot.Engines[view]) != EngineMView {
			continue
		}
		if len(i.tables[destination]) == 0 {
			continue
		}

		createQuery, ok := snapshot.ViewQueries[view]
		if !ok {
			continue
		}
		for _, reference := range viewSourceColumns(snapshot, view, createQuery) {
			for _, table := range snapshot.MatchTables(reference.Database + "." + reference.Table) {
				for _, ref := range i.tables[destination] {
					derived := ref
					derived.Via = "mv:" + view
					derived.Confidence = reference.Confidence
					if i.addIfNew(table, reference.Column, derived) {
						changed = true
					}
					i.addTable(table, derived)
				}
			}
		}
	}

	return changed
}

// viewSourceColumns reads the columns a materialized view's SELECT depends on.
func viewSourceColumns(snapshot SchemaSnapshot, view, createQuery string) []QueryReference {
	selectIndex := strings.Index(createQuery, "SELECT")
	if selectIndex < 0 {
		return nil
	}

	database, _ := splitQualified(view)
	result := ResolveQuery(createQuery[selectIndex:], nil, database, 0, snapshot.Lookup())
	return result.References
}

// addIfNew records a reference and reports whether it was new, which is how
// propagation knows it has reached a fixed point.
func (i UsageIndex) addIfNew(table, column string, ref UsageRef) bool {
	before := len(i.columns[table][column])
	i.addColumn(table, column, ref)
	return len(i.columns[table][column]) != before
}

// findUnlinkedDistributed names Distributed tables whose local table could not
// be parsed out of engine_full.
//
// getTablesRelations emits a bare node with no DependsOnTable when engine_full
// splits into fewer than six parts. The local table behind such a wrapper cannot
// be identified, so no claim can be made about its columns.
func findUnlinkedDistributed(snapshot SchemaSnapshot) []string {
	linked := map[string]bool{}
	for _, relation := range snapshot.Relations {
		if relation.DependsOnTable != "" && relation.Table != "" {
			linked[relation.DependsOnTable] = true
		}
	}

	unlinked := []string{}
	for table, engine := range snapshot.Engines {
		if ClassifyEngine(engine) == EngineDistributed && !linked[table] {
			unlinked = append(unlinked, table)
		}
	}
	sort.Strings(unlinked)
	return unlinked
}

// ─── Verdicts ────────────────────────────────────────────────────────────────

// ColumnVerdict is what can be said about one column. Only VerdictUnused
// licenses dropping it, and it is deliberately the hardest to reach.
type ColumnVerdict string

const (
	// VerdictUsed means something reads the column: a dashboard, a view
	// downstream of it, or ClickHouse itself through a storage key.
	VerdictUsed ColumnVerdict = "used"
	// VerdictUnused means the table is read, every query that reads it was fully
	// understood, and none of them named this column.
	VerdictUnused ColumnVerdict = "unused"
	// VerdictUnknown means the table is read but at least one of those queries
	// could not be enumerated, so no claim is made.
	VerdictUnknown ColumnVerdict = "unknown"
	// VerdictNoCoverage means nothing observed reads the table at all, which is
	// not evidence that its columns are dead.
	VerdictNoCoverage ColumnVerdict = "no-coverage"
)

// Reasons carried on a non-actionable verdict.
const (
	ReasonNoDashboard        = "no-dashboard-reads-this-table"
	ReasonGrafanaUnavailable = "grafana-usage-not-available"
	ReasonUnlinkedWrapper    = "distributed-wrapper-has-no-resolvable-local-table"
	ReasonViaLineage         = "read-through-lineage"
)

// virtualEngines hold no data of their own. A Distributed table routes reads to
// shards, a Merge table fans a read across sibling tables, and a materialized
// view is a trigger over its source — none of them store a column you could
// drop. ALTER TABLE … DROP COLUMN on one either fails or edits a routing
// definition, so the question always belongs to the tables underneath.
var virtualEngines = map[string]string{
	"Distributed":      ReasonDistributed,
	"Merge":            "merge-table-judge-the-underlying-tables",
	"MaterializedView": "materialized-view-judge-its-source-and-destination",
	"View":             "view-judge-the-tables-it-reads",
}

// ReasonDistributed is kept as a named constant because tests and callers refer
// to the most common of these by name.
const ReasonDistributed = "distributed-wrapper-judge-the-local-table"

// virtualEngineReason names why a table cannot be judged, or "" if it can.
func virtualEngineReason(engine string) string {
	return virtualEngines[engine]
}

// ColumnUsage is one column's verdict and the evidence behind it.
type ColumnUsage struct {
	Column  string        `json:"column"`
	Type    string        `json:"type"`
	Verdict ColumnVerdict `json:"verdict"`
	Reason  string        `json:"reason,omitempty"`
	// Direct references read this table itself; derived ones arrived through a
	// Distributed wrapper or a materialized view.
	Direct  []UsageRef `json:"direct,omitempty"`
	Derived []UsageRef `json:"derived,omitempty"`
}

// TableUsage is the inspector payload for one table.
type TableUsage struct {
	Database     string        `json:"database"`
	Table        string        `json:"table"`
	Engine       string        `json:"engine"`
	Columns      []ColumnUsage `json:"columns"`
	Dashboards   []UsageRef    `json:"dashboards"`
	UsedCount    int           `json:"used_count"`
	UnusedCount  int           `json:"unused_count"`
	UnknownCount int           `json:"unknown_count"`
}

// BuildTableUsage assigns a verdict to every column of one table.
//
// state gates the whole thing: unless the scan actually completed, every column
// is no-coverage. A Grafana that is switched off, still scanning or unreachable
// produces exactly the same evidence as a Grafana in which nothing is used, and
// the difference between those two is the difference between a safe schema
// change and a broken dashboard.
func BuildTableUsage(snapshot SchemaSnapshot, index UsageIndex, state GrafanaState, table string) TableUsage {
	database, name := splitQualified(table)
	usage := TableUsage{Database: database, Table: name, Engine: snapshot.Engines[table]}

	if state == GrafanaStateOK {
		usage.Dashboards = index.TableRefs(table)
	}

	keys := snapshot.Keys[table]
	virtualReason := virtualEngineReason(snapshot.Engines[table])
	unlinked := false
	for _, wrapper := range index.UnlinkedDistributed() {
		if wrapper == table {
			unlinked = true
		}
	}
	opaque, opaqueReason := index.IsOpaque(table)
	referenced := len(index.TableRefs(table)) > 0

	for _, column := range snapshot.Columns[table] {
		entry := ColumnUsage{Column: column.Name, Type: column.Type}

		for _, ref := range index.Columns(table, column.Name) {
			if ref.Via == "" {
				entry.Direct = append(entry.Direct, ref)
			} else {
				entry.Derived = append(entry.Derived, ref)
			}
		}

		entry.Verdict, entry.Reason = columnVerdict(verdictInputs{
			state:         state,
			hasDirect:     len(entry.Direct) > 0,
			hasDerived:    len(entry.Derived) > 0,
			referenced:    referenced,
			opaque:        opaque,
			opaqueReason:  opaqueReason,
			isKey:         keys.Contains(column.Name),
			keyRole:       keys.Role(column.Name),
			virtualReason: virtualReason,
			unlinked:      unlinked,
		})

		switch entry.Verdict {
		case VerdictUsed:
			usage.UsedCount++
		case VerdictUnused:
			usage.UnusedCount++
		case VerdictUnknown:
			usage.UnknownCount++
		}
		usage.Columns = append(usage.Columns, entry)
	}

	return usage
}

// verdictInputs is everything one column's verdict depends on.
type verdictInputs struct {
	state         GrafanaState
	hasDirect     bool
	hasDerived    bool
	referenced    bool
	opaque        bool
	opaqueReason  string
	isKey         bool
	keyRole       string
	virtualReason string
	unlinked      bool
}

// columnVerdict applies the rules in one place, in priority order.
func columnVerdict(in verdictInputs) (ColumnVerdict, string) {
	// Without a completed scan there is no evidence at all, only absence of it.
	if in.state != GrafanaStateOK {
		return VerdictNoCoverage, ReasonGrafanaUnavailable
	}

	if in.hasDirect {
		return VerdictUsed, ""
	}
	if in.hasDerived {
		return VerdictUsed, ReasonViaLineage
	}

	// A key column is read by the storage engine whatever the dashboards do, and
	// ALTER TABLE … DROP COLUMN refuses to remove it. Presenting it as unused
	// would be recommending an impossible change.
	if in.isKey {
		return VerdictUsed, in.keyRole
	}

	// A wrapper whose local table could not be identified hides everything
	// behind it.
	if in.unlinked {
		return VerdictUnknown, ReasonUnlinkedWrapper
	}
	// A Distributed, Merge or view table stores nothing of its own; the question
	// belongs to the tables underneath it.
	if in.virtualReason != "" {
		return VerdictUnknown, in.virtualReason
	}

	if !in.referenced {
		return VerdictNoCoverage, ReasonNoDashboard
	}
	// The table is read, but at least one of those queries could not be
	// enumerated, so this column's absence proves nothing.
	if in.opaque {
		return VerdictUnknown, in.opaqueReason
	}

	return VerdictUnused, ""
}

// ─── Unused report ───────────────────────────────────────────────────────────

// UnusedReportRow is one row of the cross-schema report.
type UnusedReportRow struct {
	Database   string        `json:"database"`
	Table      string        `json:"table"`
	Engine     string        `json:"engine"`
	Column     string        `json:"column"`
	Type       string        `json:"type"`
	Verdict    ColumnVerdict `json:"verdict"`
	Reason     string        `json:"reason,omitempty"`
	Dashboards int           `json:"dashboards"`
}

// UnusedReportFilters narrows the report.
type UnusedReportFilters struct {
	Database string
	Verdict  string
}

// UnusedReport is the report plus the caveats a reader needs to judge it.
type UnusedReport struct {
	Rows []UnusedReportRow `json:"rows"`
	// Caveats names conditions that limit the report's reach.
	Caveats []string `json:"caveats,omitempty"`
	Totals  struct {
		Used       int `json:"used"`
		Unused     int `json:"unused"`
		Unknown    int `json:"unknown"`
		NoCoverage int `json:"no_coverage"`
	} `json:"totals"`
}

// BuildUnusedReport walks every table and collects column verdicts.
//
// Tables whose engine stores nothing — Distributed, Merge, MaterializedView,
// View — are skipped entirely: a column of one is never the thing you drop.
// Rows come out ordered by database, table and column position, so the report is
// stable between refreshes.
func BuildUnusedReport(snapshot SchemaSnapshot, index UsageIndex, state GrafanaState, filters UnusedReportFilters) UnusedReport {
	report := UnusedReport{Rows: []UnusedReportRow{}}

	for _, table := range snapshot.Tables() {
		// Engines that store nothing never reach the report: a column of one is
		// not the thing you drop, and listing it would send a reader to alter a
		// routing definition instead of a table.
		if virtualEngineReason(snapshot.Engines[table]) != "" {
			continue
		}

		usage := BuildTableUsage(snapshot, index, state, table)
		if filters.Database != "" && !strings.EqualFold(filters.Database, usage.Database) {
			continue
		}

		dashboards := len(usage.Dashboards)
		for _, column := range usage.Columns {
			switch column.Verdict {
			case VerdictUsed:
				report.Totals.Used++
			case VerdictUnused:
				report.Totals.Unused++
			case VerdictUnknown:
				report.Totals.Unknown++
			case VerdictNoCoverage:
				report.Totals.NoCoverage++
			}

			if filters.Verdict != "" && string(column.Verdict) != filters.Verdict {
				continue
			}
			report.Rows = append(report.Rows, UnusedReportRow{
				Database:   usage.Database,
				Table:      usage.Table,
				Engine:     usage.Engine,
				Column:     column.Column,
				Type:       column.Type,
				Verdict:    column.Verdict,
				Reason:     column.Reason,
				Dashboards: dashboards,
			})
		}
	}

	report.Caveats = reportCaveats(index, state)
	return report
}

// reportCaveats states plainly what the report cannot see.
func reportCaveats(index UsageIndex, state GrafanaState) []string {
	caveats := []string{
		"Reflects Grafana dashboards only. Ad-hoc queries, applications and scheduled jobs are invisible to it.",
	}
	if state != GrafanaStateOK {
		caveats = append(caveats,
			"No dashboard scan has completed, so no column can be judged unused.")
	}
	if wrappers := index.UnlinkedDistributed(); len(wrappers) > 0 {
		caveats = append(caveats, fmt.Sprintf(
			"%d Distributed table(s) have no resolvable local table, so nothing behind them was judged: %s",
			len(wrappers), strings.Join(wrappers, ", ")))
	}
	return caveats
}
