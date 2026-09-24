package models

import (
	"reflect"
	"testing"
)

// TestViewRelationsSubquerySource pins where a materialized view's source
// comes from.
//
// aggregated.cnxh323_mv_voip_cdr is "... AS SELECT ... FROM (SELECT ... FROM
// probe_raw.cnxh323 JOIN ...)". Splitting its DDL on the first "FROM " gave the
// source "(SELECT", which the Data Flow tab drew as a node with no database and
// nothing behind it to open. The source is now what ClickHouse records on
// probe_raw.cnxh323's dependencies columns.
func TestViewRelationsSubquerySource(t *testing.T) {
	known := map[string]TableInfo{
		"probe_raw.cnxh323":              {Engine: "MergeTree"},
		"aggregated.cnxh323_mv_voip_cdr": {Engine: "MaterializedView"},
		"aggregated.voip_cdr_day":        {Engine: "AggregatingMergeTree"},
	}
	views := []pendingView{{Name: "aggregated.cnxh323_mv_voip_cdr", Target: "aggregated.voip_cdr_day"}}
	sources := map[string][]string{"aggregated.cnxh323_mv_voip_cdr": {"probe_raw.cnxh323"}}

	got := viewRelations(views, sources, known)
	want := []TableRelation{
		{DependsOnTable: "probe_raw.cnxh323", Table: "aggregated.cnxh323_mv_voip_cdr"},
		{DependsOnTable: "aggregated.cnxh323_mv_voip_cdr", Table: "aggregated.voip_cdr_day"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("viewRelations = %+v, want %+v", got, want)
	}

	if src := viewSource(got, known, "aggregated.cnxh323_mv_voip_cdr"); src != "probe_raw.cnxh323" {
		t.Errorf("viewSource = %q, want probe_raw.cnxh323", src)
	}
}

// TestViewRelationsDropsUnknownEnds checks that a name which is not a table
// the app knows — a misread DDL word, or a table in a hidden database — never
// becomes a node.
func TestViewRelationsDropsUnknownEnds(t *testing.T) {
	known := map[string]TableInfo{"db.mv": {Engine: "MaterializedView"}}
	views := []pendingView{{Name: "db.mv", Target: "(`Country`"}}
	sources := map[string][]string{"db.mv": {"system.query_log"}}

	got := viewRelations(views, sources, known)
	want := []TableRelation{{Table: "db.mv"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("viewRelations = %+v, want %+v", got, want)
	}
	if src := viewSource(got, known, "db.mv"); src != "" {
		t.Errorf("viewSource = %q, want empty", src)
	}
}

// TestViewSourceSkipsDistributedWrapper: a Distributed table over a view is an
// edge {DependsOnTable: wrapper, Table: view}, the reverse direction, and must
// not be taken for the view's source.
func TestViewSourceSkipsDistributedWrapper(t *testing.T) {
	known := map[string]TableInfo{
		"db.src":    {Engine: "MergeTree"},
		"db.mv":     {Engine: "MaterializedView"},
		"db.mv_all": {Engine: "Distributed"},
	}
	relations := []TableRelation{
		{DependsOnTable: "db.mv_all", Table: "db.mv"},
		{DependsOnTable: "db.src", Table: "db.mv"},
	}
	if src := viewSource(relations, known, "db.mv"); src != "db.src" {
		t.Errorf("viewSource = %q, want db.src", src)
	}
}
