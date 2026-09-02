package models

import (
	"sort"
	"strings"
	"testing"
)

func vars(names ...string) []TemplateVariable {
	list := make([]TemplateVariable, 0, len(names))
	for _, name := range names {
		list = append(list, TemplateVariable{Name: name, Values: []string{"6min", "1h"}})
	}
	return list
}

func TestNormalizeQueryTablePosition(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		declared []string
		want     string
		pattern  bool
	}{
		{
			name:     "brace variable inside a table name",
			sql:      "SELECT a FROM aggregated.newcust_${period2}_distributed",
			declared: []string{"period2"},
			want:     "SELECT a FROM aggregated.newcust___gfvar___distributed",
			pattern:  true,
		},
		{
			name:     "variable as the whole table name",
			sql:      "SELECT a FROM ${table}",
			declared: []string{"table"},
			want:     "SELECT a FROM __gfvar__",
			pattern:  true,
		},
		{
			name:     "join target",
			sql:      "SELECT a FROM db.t JOIN db.agg_${period}_local ON 1",
			declared: []string{"period"},
			want:     "SELECT a FROM db.t JOIN db.agg___gfvar___local ON 1",
			pattern:  true,
		},
		{
			name:     "no variables leaves the query untouched",
			sql:      "SELECT a FROM db.t WHERE $__timeFilter(ts)",
			declared: []string{"period"},
			want:     "SELECT a FROM db.t WHERE $__timeFilter(ts)",
		},
		{
			name:     "subquery source is not a table token",
			sql:      "SELECT a FROM (SELECT a FROM db.t) s",
			declared: []string{"period"},
			want:     "SELECT a FROM (SELECT a FROM db.t) s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeQuery(tt.sql, vars(tt.declared...))

			if got.SQL != tt.want {
				t.Errorf("SQL  = %q\nwant = %q", got.SQL, tt.want)
			}
			if got.TablePattern != tt.pattern {
				t.Errorf("TablePattern = %v, want %v", got.TablePattern, tt.pattern)
			}
		})
	}
}

// A declared variable is replaced by the marker even when its values are known.
// Choosing one value would resolve the query to a single table and leave that
// table's siblings looking untouched.
func TestNormalizeQueryIgnoresKnownValues(t *testing.T) {
	variables := []TemplateVariable{{Name: "period", Values: []string{"6min", "1h"}}}

	got := normalizeQuery("SELECT a FROM db.agg_${period}_distributed", variables)

	if strings.Contains(got.SQL, "6min") {
		t.Errorf("SQL = %q, want the marker rather than one chosen value", got.SQL)
	}
	if !IsTablePattern(got.SQL) {
		t.Errorf("SQL = %q, want a pattern", got.SQL)
	}
}

func TestNormalizeQueryOtherPositions(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		declared  []string
		want      string
		opaqueVar bool
	}{
		{
			name:      "projection",
			sql:       "SELECT ${metric} FROM db.t",
			declared:  []string{"metric"},
			want:      "SELECT __gfvar__ FROM db.t",
			opaqueVar: true,
		},
		{
			name:      "filter list",
			sql:       "SELECT a FROM db.t WHERE probe IN (${probe})",
			declared:  []string{"probe"},
			want:      "SELECT a FROM db.t WHERE probe IN (__gfvar__)",
			opaqueVar: true,
		},
		{
			name:      "bare variable",
			sql:       "SELECT a FROM db.t WHERE host = $host",
			declared:  []string{"host"},
			want:      "SELECT a FROM db.t WHERE host = __gfvar__",
			opaqueVar: true,
		},
		{
			name:      "inside a string literal",
			sql:       "SELECT a FROM db.t WHERE n LIKE '%${name}%'",
			declared:  []string{"name"},
			want:      "SELECT a FROM db.t WHERE n LIKE '%__gfvar__%'",
			opaqueVar: true,
		},
		{
			name:     "undeclared bare word is left alone",
			sql:      "SELECT a FROM db.t WHERE n LIKE '%$x%'",
			declared: []string{"period"},
			want:     "SELECT a FROM db.t WHERE n LIKE '%$x%'",
		},
		{
			name:     "format suffix is consumed",
			sql:      "SELECT a FROM db.t WHERE p = ${probe:raw}",
			declared: []string{"probe"},
			want:     "SELECT a FROM db.t WHERE p = __gfvar__",

			opaqueVar: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeQuery(tt.sql, vars(tt.declared...))

			if got.SQL != tt.want {
				t.Errorf("SQL  = %q\nwant = %q", got.SQL, tt.want)
			}
			if got.OpaqueVar != tt.opaqueVar {
				t.Errorf("OpaqueVar = %v, want %v", got.OpaqueVar, tt.opaqueVar)
			}
		})
	}
}

// The macro family parses natively and carries column names as arguments;
// rewriting it would risk losing them.
func TestNormalizeQueryLeavesMacrosAlone(t *testing.T) {
	macros := []string{
		"SELECT a FROM db.t WHERE $__timeFilter(ProbeTimestamp)",
		"SELECT toStartOfInterval(ts, INTERVAL $__interval_ms millisecond) FROM db.t",
		"SELECT a FROM db.t WHERE $__conditionalAll(host = 1, 1)",
		"SELECT $__timeGroup(ts, '1m') FROM db.t",
	}

	for _, sql := range macros {
		t.Run(sql[:30], func(t *testing.T) {
			got := normalizeQuery(sql, vars("period"))
			if got.SQL != sql {
				t.Errorf("SQL  = %q\nwant = %q (unchanged)", got.SQL, sql)
			}
		})
	}
}

// Built-in brace variables have no declared values and must become literals.
func TestNormalizeQueryBuiltinValues(t *testing.T) {
	tests := []struct {
		sql  string
		want string
	}{
		{sql: "SELECT a FROM db.t WHERE ts > ${__from}", want: "SELECT a FROM db.t WHERE ts > 0"},
		{sql: "SELECT a FROM db.t WHERE ts < ${__to}", want: "SELECT a FROM db.t WHERE ts < 0"},
		{sql: "SELECT a FROM db.t LIMIT ${__interval_ms}", want: "SELECT a FROM db.t LIMIT 60000"},
		{sql: "SELECT ${__unknown_macro} FROM db.t", want: "SELECT __gfvar__ FROM db.t"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := normalizeQuery(tt.sql, nil)
			if got.SQL != tt.want {
				t.Errorf("SQL  = %q\nwant = %q", got.SQL, tt.want)
			}
			// A built-in is not a template variable, so it is not reported as one.
			if len(got.Variables) != 0 {
				t.Errorf("Variables = %v, want none", got.Variables)
			}
		})
	}
}

func TestNormalizeQueryReportsVariableNames(t *testing.T) {
	got := normalizeQuery(
		"SELECT ${metric} FROM db.agg_${period}_distributed WHERE p = ${period}",
		vars("metric", "period"),
	)

	if len(got.Variables) != 2 {
		t.Fatalf("Variables = %v, want metric and period once each", got.Variables)
	}
	joined := strings.Join(got.Variables, ",")
	if !strings.Contains(joined, "period") || !strings.Contains(joined, "metric") {
		t.Errorf("Variables = %v", got.Variables)
	}
	if !got.TablePattern || !got.OpaqueVar {
		t.Errorf("TablePattern = %v, OpaqueVar = %v, want both true", got.TablePattern, got.OpaqueVar)
	}
}

func TestTablePatternMatches(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		table   string
		want    bool
	}{
		{name: "exact name without a marker", pattern: "db.orders", table: "db.orders", want: true},
		{name: "different name without a marker", pattern: "db.orders", table: "db.other", want: false},
		{name: "marker in the middle", pattern: "db.agg___gfvar___distributed", table: "db.agg_6min_distributed", want: true},
		{name: "marker matches a longer value", pattern: "db.agg___gfvar___distributed", table: "db.agg_1hour_distributed", want: true},
		{name: "suffix must still match", pattern: "db.agg___gfvar___distributed", table: "db.agg_6min_local", want: false},
		{name: "prefix must still match", pattern: "db.agg___gfvar___distributed", table: "db.raw_6min_distributed", want: false},
		{name: "marker does not cross the database dot", pattern: "db.__gfvar__", table: "other.orders", want: false},
		{name: "whole table name is a variable", pattern: "db.__gfvar__", table: "db.orders", want: true},
		{name: "case insensitive", pattern: "DB.Agg___gfvar___Distributed", table: "db.agg_6min_distributed", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TablePatternMatches(tt.pattern, tt.table); got != tt.want {
				t.Errorf("TablePatternMatches(%q, %q) = %v, want %v", tt.pattern, tt.table, got, tt.want)
			}
		})
	}
}

// A pattern must not swallow an unrelated table that merely shares a prefix.
func TestTablePatternDoesNotOverMatch(t *testing.T) {
	pattern := "aggregated.newcust___gfvar___distributed"

	shouldNotMatch := []string{
		"aggregated.newcust_distributed",
		"aggregated.oldcust_6min_distributed",
		"probe_raw.newcust_6min_distributed",
		"aggregated.newcust_6min_distributed_backup",
	}
	for _, table := range shouldNotMatch {
		if TablePatternMatches(pattern, table) {
			t.Errorf("pattern %q wrongly matched %q", pattern, table)
		}
	}

	if !TablePatternMatches(pattern, "aggregated.newcust_6min_distributed") {
		t.Error("pattern failed to match the table it was derived from")
	}
}

// ─── AST resolution ──────────────────────────────────────────────────────────

// resolve normalises and parses in one step, the way the pipeline will.
func resolve(t *testing.T, sql string, variables []TemplateVariable) QueryParseResult {
	t.Helper()

	normalized := normalizeQuery(sql, variables)
	return resolveQueryAST(normalized, "defaultdb", truncateSnippet(sql, 200))
}

// refKey renders a reference for compact comparison.
func refKey(r QueryReference) string {
	return r.Database + "." + r.Table + ":" + r.Column + "(" + string(r.Confidence) + ")"
}

func refKeys(result QueryParseResult) []string {
	keys := make([]string, 0, len(result.References))
	for _, reference := range result.References {
		keys = append(keys, refKey(reference))
	}
	sort.Strings(keys)
	return keys
}

func TestResolveSingleTable(t *testing.T) {
	got := resolve(t, "SELECT a, b FROM shop.orders WHERE c > 1 GROUP BY d", nil)

	if got.ParseError != "" {
		t.Fatalf("ParseError = %q", got.ParseError)
	}
	want := []string{
		"shop.orders:a(exact)",
		"shop.orders:b(exact)",
		"shop.orders:c(exact)",
		"shop.orders:d(exact)",
	}
	if diff := strings.Join(refKeys(got), " "); diff != strings.Join(want, " ") {
		t.Errorf("references = %v\nwant       = %v", refKeys(got), want)
	}
	if len(got.Tables) != 1 || got.Tables[0] != "shop.orders" {
		t.Errorf("Tables = %v, want [shop.orders]", got.Tables)
	}
}

// A column that could come from either side of a join is attributed to both,
// at heuristic confidence. Over-reporting usage keeps a column alive;
// under-reporting would mark a live column droppable.
func TestResolveJoinQualifiedAndUnqualified(t *testing.T) {
	got := resolve(t, `
		SELECT a.name, b.total, shared
		FROM shop.customers AS a
		JOIN shop.orders AS b ON a.id = b.customer_id`, nil)

	if got.ParseError != "" {
		t.Fatalf("ParseError = %q", got.ParseError)
	}

	keys := strings.Join(refKeys(got), " ")
	for _, want := range []string{
		"shop.customers:name(exact)",
		"shop.orders:total(exact)",
		"shop.customers:id(exact)",
		"shop.orders:customer_id(exact)",
		// Unqualified: nobody can say which side owns it.
		"shop.customers:shared(heuristic)",
		"shop.orders:shared(heuristic)",
	} {
		if !strings.Contains(keys, want) {
			t.Errorf("missing %s in %v", want, refKeys(got))
		}
	}
}

// In a single-table query an unqualified column is unambiguous.
func TestResolveUnqualifiedColumnInSingleTableQueryIsExact(t *testing.T) {
	got := resolve(t, "SELECT plain FROM shop.orders", nil)

	if len(got.References) != 1 {
		t.Fatalf("references = %v, want one", refKeys(got))
	}
	if got.References[0].Confidence != ConfidenceExact {
		t.Errorf("Confidence = %q, want exact", got.References[0].Confidence)
	}
}

func TestResolveUnqualifiedTableTakesDefaultDatabase(t *testing.T) {
	got := resolve(t, "SELECT a FROM orders", nil)

	if len(got.Tables) != 1 || got.Tables[0] != "defaultdb.orders" {
		t.Fatalf("Tables = %v, want [defaultdb.orders]", got.Tables)
	}
	if got.References[0].Database != "defaultdb" {
		t.Errorf("Database = %q, want defaultdb", got.References[0].Database)
	}
}

// A CTE is not a table, and a column read through it must not be attributed to
// a table that does not exist.
func TestResolveCTE(t *testing.T) {
	got := resolve(t, `
		WITH call_time AS (
			SELECT callID, minIf(timestamp, responseCode = 0) AS inviteTime
			FROM probe.siplog
			WHERE method = 'INVITE'
		)
		SELECT call_time.inviteTime FROM call_time`, nil)

	if got.ParseError != "" {
		t.Fatalf("ParseError = %q", got.ParseError)
	}
	if len(got.Tables) != 1 || got.Tables[0] != "probe.siplog" {
		t.Errorf("Tables = %v, want only the real table", got.Tables)
	}

	keys := strings.Join(refKeys(got), " ")
	for _, want := range []string{"probe.siplog:callID", "probe.siplog:timestamp", "probe.siplog:responseCode", "probe.siplog:method"} {
		if !strings.Contains(keys, want) {
			t.Errorf("missing %s in %v", want, refKeys(got))
		}
	}
	// The CTE's own output name is not a column of any real table.
	if strings.Contains(keys, "inviteTime") {
		t.Errorf("CTE output attributed to a table: %v", refKeys(got))
	}
}

func TestResolveNestedSubquery(t *testing.T) {
	got := resolve(t, "SELECT outer_col FROM (SELECT inner_col AS outer_col FROM shop.orders) s", nil)

	if got.ParseError != "" {
		t.Fatalf("ParseError = %q", got.ParseError)
	}
	keys := strings.Join(refKeys(got), " ")
	if !strings.Contains(keys, "shop.orders:inner_col") {
		t.Errorf("missing the inner column: %v", refKeys(got))
	}
}

// Every projection this analysis cannot enumerate must be flagged, so the
// verdict pass can downgrade the whole table rather than call its columns unused.
func TestResolveStarVariants(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{name: "bare star", sql: "SELECT * FROM shop.orders"},
		{name: "qualified star", sql: "SELECT o.* FROM shop.orders AS o"},
		{name: "star except", sql: "SELECT * EXCEPT (secret) FROM shop.orders"},
		{name: "columns regex", sql: "SELECT COLUMNS('^m') FROM shop.orders"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolve(t, tt.sql, nil)
			if got.ParseError != "" {
				t.Fatalf("ParseError = %q", got.ParseError)
			}
			if !got.SelectStar {
				t.Errorf("SelectStar = false for %q", tt.sql)
			}
		})
	}
}

// A template variable standing where a column belongs has the same consequence
// as a star: the set of columns read is not knowable.
func TestResolveOpaqueColumn(t *testing.T) {
	got := resolve(t, "SELECT ${metric} FROM shop.orders", []TemplateVariable{{Name: "metric"}})

	if got.ParseError != "" {
		t.Fatalf("ParseError = %q", got.ParseError)
	}
	if !got.OpaqueColumn {
		t.Error("OpaqueColumn = false, want true")
	}
	for _, reference := range got.References {
		if reference.Column == varMarker {
			t.Errorf("the marker leaked into a reference: %v", refKeys(got))
		}
	}
}

// A variable inside a table name survives as a pattern that the schema pass can
// match, rather than as a table that does not exist.
func TestResolveTablePatternSurvives(t *testing.T) {
	got := resolve(t, "SELECT region FROM traffic.agg_${period}_distributed", []TemplateVariable{{Name: "period"}})

	if got.ParseError != "" {
		t.Fatalf("ParseError = %q", got.ParseError)
	}
	if len(got.Tables) != 1 {
		t.Fatalf("Tables = %v, want one", got.Tables)
	}
	if !IsTablePattern(got.Tables[0]) {
		t.Errorf("Tables = %v, want a pattern", got.Tables)
	}
	if !TablePatternMatches(got.Tables[0], "traffic.agg_6min_distributed") {
		t.Errorf("pattern %q does not match the real table", got.Tables[0])
	}
}

// Function names, aliases and table names must never be mistaken for columns.
func TestResolveDoesNotTreatNamesAsColumns(t *testing.T) {
	got := resolve(t, "SELECT sum(amount) AS revenue FROM shop.orders AS o", nil)

	keys := strings.Join(refKeys(got), " ")
	if !strings.Contains(keys, "shop.orders:amount") {
		t.Errorf("missing the real column: %v", refKeys(got))
	}
	// Match the column half of the key, so a table's own name does not trip it.
	for _, bad := range []string{":sum(", ":revenue(", ":orders(", ":shop(", ":o("} {
		if strings.Contains(keys, bad) {
			t.Errorf("%q was treated as a column: %v", bad, refKeys(got))
		}
	}
}

// A parse failure must be reported, not swallowed, and must not panic.
func TestResolveParseFailure(t *testing.T) {
	got := resolve(t, "SELECT FROM WHERE ((", nil)

	if got.ParseError == "" {
		t.Error("ParseError is empty for unparseable SQL")
	}
	if len(got.References) != 0 || len(got.Tables) != 0 {
		t.Errorf("a failed parse produced results: %v / %v", refKeys(got), got.Tables)
	}
}

func TestResolveNoPanicOnOddInput(t *testing.T) {
	inputs := []string{"", "   ", ";", "SELECT", "WITH", "/* just a comment */", "SELECT 1", "SELECT 1 FROM"}

	for _, sql := range inputs {
		t.Run(sql, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on %q: %v", sql, r)
				}
			}()
			resolve(t, sql, nil)
		})
	}
}

func TestTruncateSnippet(t *testing.T) {
	tests := []struct {
		name  string
		sql   string
		limit int
		want  string
	}{
		{name: "zero disables snippets", sql: "SELECT a FROM t", limit: 0, want: ""},
		{name: "negative disables snippets", sql: "SELECT a FROM t", limit: -1, want: ""},
		{name: "whitespace is collapsed", sql: "SELECT a\n  FROM   t", limit: 100, want: "SELECT a FROM t"},
		{name: "long input is cut", sql: strings.Repeat("x", 50), limit: 10, want: strings.Repeat("x", 10) + "…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateSnippet(tt.sql, tt.limit); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveCarriesSnippet(t *testing.T) {
	got := resolve(t, "SELECT a FROM shop.orders", nil)

	if len(got.References) == 0 {
		t.Fatal("no references")
	}
	if got.References[0].Snippet != "SELECT a FROM shop.orders" {
		t.Errorf("Snippet = %q", got.References[0].Snippet)
	}
}

// A variable holding an interval leaves a bare identifier where ClickHouse
// demands INTERVAL <number> <unit>, which fails the whole query rather than one
// name. The concrete duration is irrelevant: an interval operand names no column.
func TestNormalizeQueryRepairsIntervalOperands(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "variable carries the count and the unit",
			sql:  "SELECT toStartOfInterval(ts, INTERVAL ${period}) FROM db.t",
			want: "SELECT toStartOfInterval(ts, INTERVAL 60 SECOND) FROM db.t",
		},
		{
			name: "variable carries only the count",
			sql:  "SELECT toStartOfInterval(ts, INTERVAL ${n} MINUTE) FROM db.t",
			want: "SELECT toStartOfInterval(ts, INTERVAL 60 MINUTE) FROM db.t",
		},
		{
			// The parser accepts only the singular spelling.
			name: "plural unit is singularised",
			sql:  "SELECT ts - INTERVAL ${n} days FROM db.t",
			want: "SELECT ts - INTERVAL 60 day FROM db.t",
		},
		{
			name: "an interval with no variable is untouched",
			sql:  "SELECT ts - INTERVAL 7 DAY FROM db.t",
			want: "SELECT ts - INTERVAL 7 DAY FROM db.t",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeQuery(tt.sql, vars("period", "n"))
			if got.SQL != tt.want {
				t.Errorf("SQL  = %q\nwant = %q", got.SQL, tt.want)
			}
			// It must now actually parse.
			if result := resolveQueryAST(got, "db", ""); result.ParseError != "" {
				t.Errorf("still unparseable: %s", result.ParseError)
			}
		})
	}
}
