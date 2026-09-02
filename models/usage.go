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
		Columns: map[string][]ColumnInfo{},
		Keys:    map[string]TableKeys{},
		Engines: map[string]string{},
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

	// The relation cache is filled lazily by getTablesRelations; the caller is
	// responsible for warming it before this point.
	snapshot.Relations = append(snapshot.Relations, TableRelations...)
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
		SELECT database, name, engine, primary_key, sorting_key, partition_key
		FROM system.tables
		WHERE database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA', 'performance_schema', 'mysql')
	`
	rows, err := c.conn.Query(context.Background(), query)
	if err != nil {
		return fmt.Errorf("failed to query system.tables for keys: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var database, name, engine, primary, sorting, partition string
		if err := rows.Scan(&database, &name, &engine, &primary, &sorting, &partition); err != nil {
			return fmt.Errorf("failed to scan table key row: %w", err)
		}

		key := database + "." + name
		snapshot.Engines[key] = engine
		snapshot.Keys[key] = TableKeys{
			Primary:   ParseKeyExpression(primary),
			Sorting:   ParseKeyExpression(sorting),
			Partition: ParseKeyExpression(partition),
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
