package models

import "testing"

// TestSimplifyColumnTypeTerminates pins the termination of simplifyColumnType.
//
// It recursed on an unchanging string for any type that mentions Nullable
// without being one — ClickHouse writes
// "SimpleAggregateFunction(groupBitOr, Nullable(Bool))", and the old
// Contains-based branch found no "Nullable(" prefix to trim while TrimSuffix
// could only remove the final ")" once. The result was a stack overflow, which
// Go cannot recover from: a single GET /api/relationships/:db/:table for a table
// carrying such a column killed the server process.
//
// A test that recursed forever would hang the suite rather than fail it, so the
// cases below are the shapes that used to diverge; reaching the assertion at all
// is the property under test.
func TestSimplifyColumnTypeTerminates(t *testing.T) {
	client := &ClickHouseClient{}

	cases := []struct {
		columnType string
		want       string
	}{
		// The shapes that used to diverge: Nullable nested inside another
		// wrapper, so it is present but not a prefix.
		{"SimpleAggregateFunction(groupBitOr, Nullable(Bool))", "other"},
		{"AggregateFunction(argMax, Nullable(Bool), DateTime)", "date"},
		{"Map(String, Nullable(Bool))", "string"},

		// Real Nullable wrappers still unwrap to the inner type.
		{"Nullable(Bool)", "other"},
		{"Nullable(String)", "string"},
		{"Nullable(Float64)", "float"},

		// Ordinary types are unaffected.
		{"String", "string"},
		{"UInt64", "int"},
		{"DateTime64(3)", "date"},
		{"Array(String)", "string"},
		{"Bool", "other"},
		{"", "other"},
	}

	for _, tc := range cases {
		if got := client.simplifyColumnType(tc.columnType); got != tc.want {
			t.Errorf("simplifyColumnType(%q) = %q, want %q", tc.columnType, got, tc.want)
		}
	}
}
