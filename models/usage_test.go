package models

import (
	"sort"
	"strings"
	"testing"
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
