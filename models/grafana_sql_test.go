package models

import (
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
