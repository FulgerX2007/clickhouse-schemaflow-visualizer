package models

import (
	"sort"
	"strings"
	"testing"
	"time"
)

// testSnapshot builds a schema from literals, which is the whole point of
// keeping the domain free of the ClickHouse client.
func testSnapshot() SchemaSnapshot {
	return SchemaSnapshot{
		Columns: map[string][]ColumnInfo{
			"shop.orders": {
				{Name: "id", Type: "UInt64"},
				{Name: "customer_id", Type: "UInt64"},
				{Name: "amount", Type: "Decimal(18, 2)"},
				{Name: "created_at", Type: "DateTime"},
				{Name: "note", Type: "String"},
			},
			"shop.customers": {
				{Name: "id", Type: "UInt64"},
				{Name: "name", Type: "String"},
			},
			"traffic.agg_6min_distributed": {
				{Name: "region", Type: "String"},
				{Name: "bytes", Type: "UInt64"},
			},
			"traffic.agg_1h_distributed": {
				{Name: "region", Type: "String"},
				{Name: "bytes", Type: "UInt64"},
				{Name: "extra", Type: "UInt8"},
			},
			"traffic.unrelated": {{Name: "other", Type: "String"}},
		},
		Keys: map[string]TableKeys{
			"shop.orders": {
				Primary:   []string{"id"},
				Sorting:   []string{"id", "created_at"},
				Partition: []string{"created_at"},
			},
		},
		Engines: map[string]string{
			"shop.orders":                  "MergeTree",
			"shop.customers":               "MergeTree",
			"traffic.agg_6min_distributed": "Distributed",
		},
	}
}

func TestTableKeysContainsAndRole(t *testing.T) {
	keys := TableKeys{
		Primary:   []string{"id"},
		Sorting:   []string{"id", "created_at"},
		Partition: []string{"event_date"},
	}

	tests := []struct {
		column   string
		contains bool
		role     string
	}{
		{column: "id", contains: true, role: "primary-key"},
		{column: "ID", contains: true, role: "primary-key"},
		{column: "created_at", contains: true, role: "sorting-key"},
		{column: "event_date", contains: true, role: "partition-key"},
		{column: "amount", contains: false, role: ""},
	}

	for _, tt := range tests {
		t.Run(tt.column, func(t *testing.T) {
			if got := keys.Contains(tt.column); got != tt.contains {
				t.Errorf("Contains(%q) = %v, want %v", tt.column, got, tt.contains)
			}
			if got := keys.Role(tt.column); got != tt.role {
				t.Errorf("Role(%q) = %q, want %q", tt.column, got, tt.role)
			}
		})
	}
}

func TestParseKeyExpression(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		want       []string
	}{
		{name: "empty", expression: "", want: nil},
		{name: "single column", expression: "id", want: []string{"id"}},
		{name: "tuple", expression: "(user_id, event_date)", want: []string{"user_id", "event_date"}},
		{
			name:       "function wrapper is not a column",
			expression: "toYYYYMM(created_at)",
			want:       []string{"created_at"},
		},
		{
			name:       "mixed expression",
			expression: "(toYYYYMM(ts), cityHash64(user_id), status)",
			want:       []string{"ts", "user_id", "status"},
		},
		{
			name:       "repeated names collapse",
			expression: "(id, toDate(id))",
			want:       []string{"id"},
		},
		{
			name:       "tuple wrapper is dropped",
			expression: "tuple()",
			want:       []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseKeyExpression(tt.expression)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSchemaSnapshotTablesAndColumnNames(t *testing.T) {
	snapshot := testSnapshot()

	tables := snapshot.Tables()
	if !sort.StringsAreSorted(tables) {
		t.Errorf("Tables() is not sorted: %v", tables)
	}
	if len(tables) != 5 {
		t.Errorf("got %d tables, want 5: %v", len(tables), tables)
	}

	names := snapshot.ColumnNames("shop.customers")
	if strings.Join(names, ",") != "id,name" {
		t.Errorf("ColumnNames = %v, want id,name in position order", names)
	}
	if got := snapshot.ColumnNames("nope.nope"); len(got) != 0 {
		t.Errorf("ColumnNames of an unknown table = %v, want none", got)
	}
}

func TestSchemaSnapshotLookup(t *testing.T) {
	lookup := testSnapshot().Lookup()

	if got := strings.Join(lookup("shop.customers"), ","); got != "id,name" {
		t.Errorf("plain lookup = %q", got)
	}
	if got := lookup("nope.nope"); got != nil {
		t.Errorf("unknown table lookup = %v, want nil", got)
	}

	// A pattern unions every table it matches, deduped.
	union := lookup("traffic.agg___gfvar___distributed")
	sort.Strings(union)
	if strings.Join(union, ",") != "bytes,extra,region" {
		t.Errorf("pattern lookup = %v, want the union of both period tables", union)
	}
	// It must not reach a table outside the pattern.
	for _, name := range union {
		if name == "other" {
			t.Error("the pattern lookup reached an unrelated table")
		}
	}
}

func TestSchemaSnapshotMatchTables(t *testing.T) {
	snapshot := testSnapshot()

	if got := snapshot.MatchTables("shop.orders"); strings.Join(got, ",") != "shop.orders" {
		t.Errorf("plain match = %v", got)
	}
	if got := snapshot.MatchTables("shop.missing"); len(got) != 0 {
		t.Errorf("unknown table = %v, want none", got)
	}

	// The dashboard reads whichever value the viewer picks, so a pattern names
	// every matching table, not one of them.
	got := snapshot.MatchTables("traffic.agg___gfvar___distributed")
	sort.Strings(got)
	want := "traffic.agg_1h_distributed,traffic.agg_6min_distributed"
	if strings.Join(got, ",") != want {
		t.Errorf("pattern match = %v, want %s", got, want)
	}
}

// The resolver and the schema must agree, so wire them together once here.
func TestSchemaSnapshotFeedsTheResolver(t *testing.T) {
	snapshot := testSnapshot()

	got := ResolveQuery(
		"SELECT region FROM traffic.agg_${period}_distributed WHERE (((",
		[]TemplateVariable{{Name: "period"}}, "traffic", 100, snapshot.Lookup(),
	)

	if got.ParseError == "" {
		t.Fatal("expected the fallback path")
	}
	if len(got.References) == 0 {
		t.Fatal("the pattern produced no references")
	}
	for _, reference := range got.References {
		if reference.Column != "region" {
			t.Errorf("unexpected column %q", reference.Column)
		}
	}
}

// ─── Usage index ─────────────────────────────────────────────────────────────

func panelRef(dashboard string, panel int) UsageRef {
	return UsageRef{
		DashboardUID:   dashboard,
		DashboardTitle: strings.ToUpper(dashboard),
		DashboardURL:   "https://grafana.example/d/" + dashboard,
		PanelID:        panel,
		PanelTitle:     "Panel",
	}
}

// query resolves SQL against a snapshot the way the pipeline will.
func query(snapshot SchemaSnapshot, ref UsageRef, sql string, variables ...TemplateVariable) ResolvedQuery {
	return ResolvedQuery{
		Source: ref,
		Result: ResolveQuery(sql, variables, "shop", 200, snapshot.Lookup()),
	}
}

func columnsUsed(index UsageIndex, table string) []string {
	names := []string{}
	for column := range index.columns[table] {
		names = append(names, column)
	}
	sort.Strings(names)
	return names
}

func TestBuildUsageIndexDirect(t *testing.T) {
	snapshot := testSnapshot()

	index := BuildUsageIndex(snapshot, []ResolvedQuery{
		query(snapshot, panelRef("dash1", 1), "SELECT id, amount FROM shop.orders"),
		query(snapshot, panelRef("dash2", 7), "SELECT amount FROM shop.orders"),
	})

	if got := strings.Join(columnsUsed(index, "shop.orders"), ","); got != "amount,id" {
		t.Errorf("columns = %s, want amount,id", got)
	}
	// Two dashboards read amount; one reads id.
	if got := len(index.Columns("shop.orders", "amount")); got != 2 {
		t.Errorf("amount refs = %d, want 2", got)
	}
	if got := len(index.Columns("shop.orders", "id")); got != 1 {
		t.Errorf("id refs = %d, want 1", got)
	}
	// A column nothing reads has no evidence at all.
	if got := len(index.Columns("shop.orders", "note")); got != 0 {
		t.Errorf("note refs = %d, want 0", got)
	}
	if got := len(index.TableRefs("shop.orders")); got != 2 {
		t.Errorf("table refs = %d, want 2", got)
	}
	if got := index.Columns("shop.orders", "amount")[0]; got.DashboardURL == "" || got.Confidence != ConfidenceExact {
		t.Errorf("reference lost its provenance or confidence: %+v", got)
	}
}

func TestBuildUsageIndexDeduplicatesRepeatedPanels(t *testing.T) {
	snapshot := testSnapshot()
	ref := panelRef("dash1", 1)

	index := BuildUsageIndex(snapshot, []ResolvedQuery{
		query(snapshot, ref, "SELECT amount FROM shop.orders"),
		query(snapshot, ref, "SELECT amount FROM shop.orders"),
	})

	if got := len(index.Columns("shop.orders", "amount")); got != 1 {
		t.Errorf("amount refs = %d, want 1 after dedup", got)
	}
}

func TestBuildUsageIndexRecordsOpacity(t *testing.T) {
	snapshot := testSnapshot()

	tests := []struct {
		name string
		sql  string
		want string
	}{
		{name: "star", sql: "SELECT * FROM shop.orders", want: "select-star"},
		{name: "unparsed", sql: "SELECT id FROM shop.orders WHERE (((", want: "unparsed-query"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := BuildUsageIndex(snapshot, []ResolvedQuery{
				query(snapshot, panelRef("d", 1), tt.sql),
			})

			opaque, reason := index.IsOpaque("shop.orders")
			if !opaque {
				t.Fatalf("table is not marked opaque for %q", tt.sql)
			}
			if reason != tt.want {
				t.Errorf("reason = %q, want %q", reason, tt.want)
			}
		})
	}
}

func TestBuildUsageIndexRecordsVariableColumnOpacity(t *testing.T) {
	snapshot := testSnapshot()

	index := BuildUsageIndex(snapshot, []ResolvedQuery{
		query(snapshot, panelRef("d", 1), "SELECT ${metric} FROM shop.orders", TemplateVariable{Name: "metric"}),
	})

	opaque, reason := index.IsOpaque("shop.orders")
	if !opaque || reason != "variable-column" {
		t.Errorf("opaque = %v, reason = %q, want variable-column", opaque, reason)
	}
}

// A pattern table reference reaches every table it matches, because the
// dashboard reads whichever value the viewer picks.
func TestBuildUsageIndexExpandsPatternTables(t *testing.T) {
	snapshot := testSnapshot()

	index := BuildUsageIndex(snapshot, []ResolvedQuery{
		query(snapshot, panelRef("d", 1),
			"SELECT region FROM traffic.agg_${period}_distributed",
			TemplateVariable{Name: "period"}),
	})

	for _, table := range []string{"traffic.agg_6min_distributed", "traffic.agg_1h_distributed"} {
		if len(index.Columns(table, "region")) == 0 {
			t.Errorf("%s.region was not recorded", table)
		}
	}
	if len(index.Columns("traffic.unrelated", "other")) != 0 {
		t.Error("the pattern reached an unrelated table")
	}
}

// ─── Lineage propagation ─────────────────────────────────────────────────────

// distributedSnapshot models a Distributed wrapper over a local MergeTree.
func distributedSnapshot() SchemaSnapshot {
	return SchemaSnapshot{
		Columns: map[string][]ColumnInfo{
			"probe.siplog_distributed": {{Name: "callID"}, {Name: "method"}, {Name: "unused_col"}},
			"probe.siplog":             {{Name: "callID"}, {Name: "method"}, {Name: "unused_col"}},
		},
		Keys:        map[string]TableKeys{},
		ViewQueries: map[string]string{},
		Engines: map[string]string{
			"probe.siplog_distributed": "Distributed",
			"probe.siplog":             "MergeTree",
		},
		// getTablesRelations emits the wrapper as DependsOnTable and the local
		// table as Table.
		Relations: []TableRelation{
			{DependsOnTable: "probe.siplog_distributed", Table: "probe.siplog"},
		},
	}
}

// Dashboards query the Distributed wrapper, so without this the local MergeTree
// that actually stores the data looks entirely untouched.
func TestPropagateDistributedToLocalTable(t *testing.T) {
	snapshot := distributedSnapshot()

	index := BuildUsageIndex(snapshot, []ResolvedQuery{
		{Source: panelRef("d", 1), Result: ResolveQuery(
			"SELECT callID FROM probe.siplog_distributed", nil, "probe", 200, snapshot.Lookup())},
	})

	refs := index.Columns("probe.siplog", "callID")
	if len(refs) == 0 {
		t.Fatal("usage did not reach the local table")
	}
	if refs[0].Via != "distributed:probe.siplog_distributed" {
		t.Errorf("Via = %q, want the wrapper named", refs[0].Via)
	}
	// A column nobody reads stays unread on both sides.
	if len(index.Columns("probe.siplog", "unused_col")) != 0 {
		t.Error("propagation invented usage for a column nothing reads")
	}
}

func TestPropagateDistributedCarriesOpacity(t *testing.T) {
	snapshot := distributedSnapshot()

	index := BuildUsageIndex(snapshot, []ResolvedQuery{
		{Source: panelRef("d", 1), Result: ResolveQuery(
			"SELECT * FROM probe.siplog_distributed", nil, "probe", 200, snapshot.Lookup())},
	})

	if opaque, _ := index.IsOpaque("probe.siplog"); !opaque {
		t.Error("a star query on the wrapper left the local table looking enumerable")
	}
}

// viewSnapshot models source -> MV -> destination.
func viewSnapshot() SchemaSnapshot {
	return SchemaSnapshot{
		Columns: map[string][]ColumnInfo{
			"raw.events":    {{Name: "a"}, {Name: "b"}, {Name: "ts"}, {Name: "never_read"}},
			"agg.rollup":    {{Name: "c"}, {Name: "day"}},
			"raw.events_mv": {},
		},
		Keys: map[string]TableKeys{},
		Engines: map[string]string{
			"raw.events":    "MergeTree",
			"raw.events_mv": "MaterializedView",
			"agg.rollup":    "SummingMergeTree",
		},
		ViewQueries: map[string]string{
			"raw.events_mv": "CREATE MATERIALIZED VIEW raw.events_mv TO agg.rollup AS " +
				"SELECT a + b AS c, toDate(ts) AS day FROM raw.events WHERE b > 0",
		},
		Relations: []TableRelation{
			{DependsOnTable: "raw.events", Table: "raw.events_mv"},
			{DependsOnTable: "raw.events_mv", Table: "agg.rollup"},
		},
	}
}

// Table-level propagation is the point: "a + b AS c" must mark both a and b, and
// a column read only in the WHERE clause counts too. Column-level mapping
// through extractColumnMappings would have attributed only a.
func TestPropagateViewMarksEverySourceColumn(t *testing.T) {
	snapshot := viewSnapshot()

	index := BuildUsageIndex(snapshot, []ResolvedQuery{
		{Source: panelRef("d", 1), Result: ResolveQuery(
			"SELECT c FROM agg.rollup", nil, "agg", 200, snapshot.Lookup())},
	})

	for _, column := range []string{"a", "b", "ts"} {
		refs := index.Columns("raw.events", column)
		if len(refs) == 0 {
			t.Errorf("source column %q was not marked used", column)
			continue
		}
		if refs[0].Via != "mv:raw.events_mv" {
			t.Errorf("%q Via = %q, want the view named", column, refs[0].Via)
		}
	}
	// A source column the view never touches is still unread.
	if len(index.Columns("raw.events", "never_read")) != 0 {
		t.Error("propagation invented usage for a column the view does not read")
	}
}

func TestPropagateViewDoesNotFireWhenTheDestinationIsUnread(t *testing.T) {
	snapshot := viewSnapshot()

	index := BuildUsageIndex(snapshot, nil)

	if len(index.Columns("raw.events", "a")) != 0 {
		t.Error("view propagation ran with no downstream usage at all")
	}
}

// Relations can cycle; propagation must terminate.
func TestPropagateTerminatesOnCycles(t *testing.T) {
	snapshot := SchemaSnapshot{
		Columns: map[string][]ColumnInfo{
			"db.a": {{Name: "x"}},
			"db.b": {{Name: "x"}},
		},
		Keys:        map[string]TableKeys{},
		ViewQueries: map[string]string{},
		Engines: map[string]string{
			"db.a": "Distributed",
			"db.b": "Distributed",
		},
		Relations: []TableRelation{
			{DependsOnTable: "db.a", Table: "db.b"},
			{DependsOnTable: "db.b", Table: "db.a"},
		},
	}

	done := make(chan UsageIndex, 1)
	go func() {
		done <- BuildUsageIndex(snapshot, []ResolvedQuery{
			{Source: panelRef("d", 1), Result: ResolveQuery("SELECT x FROM db.a", nil, "db", 200, snapshot.Lookup())},
		})
	}()

	select {
	case index := <-done:
		if len(index.Columns("db.b", "x")) == 0 {
			t.Error("usage did not cross the cycle at all")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("propagation did not terminate on a cyclic relation set")
	}
}

// A Distributed table whose engine_full could not be parsed has no edge, so the
// local table behind it cannot be identified and nothing about it can be judged.
func TestUnlinkedDistributedIsReported(t *testing.T) {
	snapshot := SchemaSnapshot{
		Columns:     map[string][]ColumnInfo{"db.orphan_distributed": {{Name: "x"}}},
		Keys:        map[string]TableKeys{},
		ViewQueries: map[string]string{},
		Engines:     map[string]string{"db.orphan_distributed": "Distributed"},
	}

	index := BuildUsageIndex(snapshot, nil)

	unlinked := index.UnlinkedDistributed()
	if len(unlinked) != 1 || unlinked[0] != "db.orphan_distributed" {
		t.Errorf("UnlinkedDistributed = %v, want the orphan wrapper", unlinked)
	}
}

func TestLinkedDistributedIsNotReportedAsUnlinked(t *testing.T) {
	index := BuildUsageIndex(distributedSnapshot(), nil)

	if got := index.UnlinkedDistributed(); len(got) != 0 {
		t.Errorf("UnlinkedDistributed = %v, want none", got)
	}
}

// ─── Verdicts ────────────────────────────────────────────────────────────────

func verdictOf(usage TableUsage, column string) (ColumnVerdict, string) {
	for _, entry := range usage.Columns {
		if entry.Column == column {
			return entry.Verdict, entry.Reason
		}
	}
	return "", "missing"
}

func TestColumnVerdicts(t *testing.T) {
	snapshot := testSnapshot()

	index := BuildUsageIndex(snapshot, []ResolvedQuery{
		query(snapshot, panelRef("d", 1), "SELECT amount FROM shop.orders"),
	})
	usage := BuildTableUsage(snapshot, index, GrafanaStateOK, "shop.orders")

	tests := []struct {
		column  string
		verdict ColumnVerdict
		reason  string
	}{
		{column: "amount", verdict: VerdictUsed},
		// note is the only column that is genuinely droppable: the table is read,
		// the query was fully understood, and nothing names it.
		{column: "note", verdict: VerdictUnused},
		// The rest are storage keys, which ALTER TABLE … DROP COLUMN refuses.
		{column: "id", verdict: VerdictUsed, reason: "primary-key"},
		{column: "created_at", verdict: VerdictUsed, reason: "sorting-key"},
	}

	for _, tt := range tests {
		t.Run(tt.column, func(t *testing.T) {
			verdict, reason := verdictOf(usage, tt.column)
			if verdict != tt.verdict {
				t.Errorf("verdict = %q, want %q", verdict, tt.verdict)
			}
			if reason != tt.reason {
				t.Errorf("reason = %q, want %q", reason, tt.reason)
			}
		})
	}

	if usage.UnusedCount != 2 {
		// note and customer_id: customer_id is not a key and is not read.
		t.Errorf("UnusedCount = %d, want 2", usage.UnusedCount)
	}
}

// Nothing reads the table at all, which is not evidence that its columns are
// dead.
func TestVerdictNoCoverageWhenNothingReadsTheTable(t *testing.T) {
	snapshot := testSnapshot()
	index := BuildUsageIndex(snapshot, nil)

	usage := BuildTableUsage(snapshot, index, GrafanaStateOK, "shop.customers")

	verdict, reason := verdictOf(usage, "name")
	if verdict != VerdictNoCoverage {
		t.Errorf("verdict = %q, want %q", verdict, VerdictNoCoverage)
	}
	if reason != ReasonNoDashboard {
		t.Errorf("reason = %q, want %q", reason, ReasonNoDashboard)
	}
	if usage.UnusedCount != 0 {
		t.Errorf("UnusedCount = %d, want 0 — an unmonitored table has no dead columns", usage.UnusedCount)
	}
}

// The table is read, but a query touching it could not be enumerated, so a
// column's absence proves nothing.
func TestVerdictUnknownOnOpaqueQueries(t *testing.T) {
	snapshot := testSnapshot()

	tests := []struct {
		name   string
		sql    string
		reason string
	}{
		{name: "star", sql: "SELECT * FROM shop.orders", reason: "select-star"},
		{name: "unparsed", sql: "SELECT amount FROM shop.orders WHERE (((", reason: "unparsed-query"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := BuildUsageIndex(snapshot, []ResolvedQuery{query(snapshot, panelRef("d", 1), tt.sql)})
			usage := BuildTableUsage(snapshot, index, GrafanaStateOK, "shop.orders")

			verdict, reason := verdictOf(usage, "note")
			if verdict != VerdictUnknown {
				t.Errorf("verdict = %q, want %q", verdict, VerdictUnknown)
			}
			if reason != tt.reason {
				t.Errorf("reason = %q, want %q", reason, tt.reason)
			}
			if usage.UnusedCount != 0 {
				t.Errorf("UnusedCount = %d, want 0", usage.UnusedCount)
			}
		})
	}
}

// The single most dangerous failure mode: a Grafana that is off, still scanning
// or unreachable produces exactly the evidence of a Grafana in which nothing is
// used.
func TestVerdictNoCoverageUnlessTheScanCompleted(t *testing.T) {
	snapshot := testSnapshot()
	index := BuildUsageIndex(snapshot, []ResolvedQuery{
		query(snapshot, panelRef("d", 1), "SELECT amount FROM shop.orders"),
	})

	for _, state := range []GrafanaState{GrafanaStateDisabled, GrafanaStateScanning, GrafanaStateError} {
		t.Run(string(state), func(t *testing.T) {
			usage := BuildTableUsage(snapshot, index, state, "shop.orders")

			for _, column := range usage.Columns {
				if column.Verdict != VerdictNoCoverage {
					t.Errorf("column %q verdict = %q, want %q", column.Column, column.Verdict, VerdictNoCoverage)
				}
			}
			if usage.UnusedCount != 0 {
				t.Errorf("UnusedCount = %d, want 0 — nothing may be called droppable", usage.UnusedCount)
			}
			if len(usage.Dashboards) != 0 {
				t.Errorf("dashboards were reported despite state %q", state)
			}
		})
	}
}

// A column reached only through a Distributed wrapper or a view is used, and
// says how.
func TestVerdictUsedThroughLineage(t *testing.T) {
	snapshot := distributedSnapshot()
	index := BuildUsageIndex(snapshot, []ResolvedQuery{
		{Source: panelRef("d", 1), Result: ResolveQuery(
			"SELECT callID FROM probe.siplog_distributed", nil, "probe", 200, snapshot.Lookup())},
	})

	usage := BuildTableUsage(snapshot, index, GrafanaStateOK, "probe.siplog")

	verdict, reason := verdictOf(usage, "callID")
	if verdict != VerdictUsed || reason != ReasonViaLineage {
		t.Errorf("verdict = %q reason = %q, want used via lineage", verdict, reason)
	}
}

// A Distributed table stores nothing of its own, so a column of one is never the
// thing you drop.
func TestVerdictDistributedIsNeverUnused(t *testing.T) {
	snapshot := distributedSnapshot()
	index := BuildUsageIndex(snapshot, []ResolvedQuery{
		{Source: panelRef("d", 1), Result: ResolveQuery(
			"SELECT callID FROM probe.siplog_distributed", nil, "probe", 200, snapshot.Lookup())},
	})

	usage := BuildTableUsage(snapshot, index, GrafanaStateOK, "probe.siplog_distributed")

	verdict, reason := verdictOf(usage, "unused_col")
	if verdict != VerdictUnknown || reason != ReasonDistributed {
		t.Errorf("verdict = %q reason = %q, want unknown/distributed", verdict, reason)
	}
	if usage.UnusedCount != 0 {
		t.Errorf("UnusedCount = %d, want 0", usage.UnusedCount)
	}
}

func TestVerdictUnlinkedWrapperHidesEverythingBehindIt(t *testing.T) {
	snapshot := SchemaSnapshot{
		Columns:     map[string][]ColumnInfo{"db.orphan_distributed": {{Name: "x"}}},
		Keys:        map[string]TableKeys{},
		ViewQueries: map[string]string{},
		Engines:     map[string]string{"db.orphan_distributed": "Distributed"},
	}
	index := BuildUsageIndex(snapshot, nil)

	usage := BuildTableUsage(snapshot, index, GrafanaStateOK, "db.orphan_distributed")

	verdict, reason := verdictOf(usage, "x")
	if verdict != VerdictUnknown || reason != ReasonUnlinkedWrapper {
		t.Errorf("verdict = %q reason = %q, want unknown/unlinked", verdict, reason)
	}
}

// ─── Report ──────────────────────────────────────────────────────────────────

func TestBuildUnusedReport(t *testing.T) {
	snapshot := testSnapshot()
	index := BuildUsageIndex(snapshot, []ResolvedQuery{
		query(snapshot, panelRef("d", 1), "SELECT amount FROM shop.orders"),
	})

	report := BuildUnusedReport(snapshot, index, GrafanaStateOK, UnusedReportFilters{Verdict: string(VerdictUnused)})

	for _, row := range report.Rows {
		if row.Verdict != VerdictUnused {
			t.Errorf("filter leaked a %q row", row.Verdict)
		}
		// Distributed tables never appear: they store nothing of their own.
		if ClassifyEngine(row.Engine) == EngineDistributed {
			t.Errorf("a Distributed table reached the report: %+v", row)
		}
	}

	columns := []string{}
	for _, row := range report.Rows {
		columns = append(columns, row.Table+"."+row.Column)
	}
	sort.Strings(columns)
	if strings.Join(columns, ",") != "orders.customer_id,orders.note" {
		t.Errorf("unused rows = %v, want only the two droppable columns", columns)
	}

	if report.Totals.Unused != 2 {
		t.Errorf("Totals.Unused = %d, want 2", report.Totals.Unused)
	}
	// Totals count every column, not only the filtered rows.
	if report.Totals.Used == 0 || report.Totals.NoCoverage == 0 {
		t.Errorf("totals look wrong: %+v", report.Totals)
	}
}

func TestBuildUnusedReportDatabaseFilter(t *testing.T) {
	snapshot := testSnapshot()
	index := BuildUsageIndex(snapshot, nil)

	report := BuildUnusedReport(snapshot, index, GrafanaStateOK, UnusedReportFilters{Database: "traffic"})

	if len(report.Rows) == 0 {
		t.Fatal("the filter excluded everything")
	}
	for _, row := range report.Rows {
		if row.Database != "traffic" {
			t.Errorf("filter leaked %s.%s", row.Database, row.Table)
		}
	}
}

func TestBuildUnusedReportIsStablyOrdered(t *testing.T) {
	snapshot := testSnapshot()
	index := BuildUsageIndex(snapshot, nil)

	first := BuildUnusedReport(snapshot, index, GrafanaStateOK, UnusedReportFilters{})
	second := BuildUnusedReport(snapshot, index, GrafanaStateOK, UnusedReportFilters{})

	if len(first.Rows) != len(second.Rows) {
		t.Fatalf("row counts differ: %d vs %d", len(first.Rows), len(second.Rows))
	}
	for i := range first.Rows {
		if first.Rows[i] != second.Rows[i] {
			t.Fatalf("row %d differs between runs:\n%+v\n%+v", i, first.Rows[i], second.Rows[i])
		}
	}
}

// The report has to say what it cannot see, or a reader will over-trust it.
func TestBuildUnusedReportCaveats(t *testing.T) {
	snapshot := testSnapshot()
	index := BuildUsageIndex(snapshot, nil)

	ok := BuildUnusedReport(snapshot, index, GrafanaStateOK, UnusedReportFilters{})
	if len(ok.Caveats) == 0 || !strings.Contains(ok.Caveats[0], "Grafana dashboards only") {
		t.Errorf("caveats = %v, want the scope stated", ok.Caveats)
	}

	stale := BuildUnusedReport(snapshot, index, GrafanaStateError, UnusedReportFilters{})
	if !containsMatch(stale.Caveats, "No dashboard scan has completed") {
		t.Errorf("caveats = %v, want the missing scan called out", stale.Caveats)
	}
	if stale.Totals.Unused != 0 {
		t.Errorf("Totals.Unused = %d, want 0 without a completed scan", stale.Totals.Unused)
	}
}

// Engines that hold no data of their own are never judged: a Distributed table
// routes reads, a Merge table fans them out, a materialized view is a trigger.
// A column of one is not the thing you drop.
func TestVirtualEnginesAreNeverJudged(t *testing.T) {
	engines := map[string]string{
		"db.dist":  "Distributed",
		"db.merge": "Merge",
		"db.mv":    "MaterializedView",
		"db.view":  "View",
		"db.dict":  "Dictionary",
		"db.real":  "MergeTree",
	}
	columns := map[string][]ColumnInfo{}
	for name := range engines {
		columns[name] = []ColumnInfo{{Name: "x"}, {Name: "y"}}
	}
	snapshot := SchemaSnapshot{
		Columns: columns, Engines: engines,
		Keys: map[string]TableKeys{}, ViewQueries: map[string]string{},
	}

	// Something reads every table, so "unused" would otherwise be reachable.
	queries := []ResolvedQuery{}
	for name := range engines {
		queries = append(queries, ResolvedQuery{
			Source: panelRef("d", 1),
			Result: ResolveQuery("SELECT x FROM "+name, nil, "db", 200, snapshot.Lookup()),
		})
	}
	index := BuildUsageIndex(snapshot, queries)

	for table, engine := range engines {
		usage := BuildTableUsage(snapshot, index, GrafanaStateOK, table)
		verdict, reason := verdictOf(usage, "y")

		if engine == "MergeTree" {
			if verdict != VerdictUnused {
				t.Errorf("%s (%s): verdict = %q, want unused — a real table must still be judged", table, engine, verdict)
			}
			continue
		}
		if verdict != VerdictUnknown {
			t.Errorf("%s (%s): verdict = %q, want unknown", table, engine, verdict)
		}
		if reason == "" {
			t.Errorf("%s (%s): no reason given", table, engine)
		}
		if usage.UnusedCount != 0 {
			t.Errorf("%s (%s): UnusedCount = %d, want 0", table, engine, usage.UnusedCount)
		}
	}

	// And none of them reaches the report.
	report := BuildUnusedReport(snapshot, index, GrafanaStateOK, UnusedReportFilters{})
	for _, row := range report.Rows {
		if engine := engines[row.Database+"."+row.Table]; engine != "MergeTree" {
			t.Errorf("a %s table reached the report: %s.%s", engine, row.Database, row.Table)
		}
	}
	// The totals then describe judgeable columns, not the whole schema, so the
	// report has to say how many tables it left out.
	if !containsMatch(report.Caveats, "cannot be dropped") {
		t.Errorf("caveats = %v, want the excluded tables declared", report.Caveats)
	}
}
