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

func TestViewTarget(t *testing.T) {
	cases := map[string]string{
		"CREATE MATERIALIZED VIEW db.mv TO db.dst (`a` String) AS SELECT a FROM db.src": "db.dst",
		// Refreshable: REFRESH, SETTINGS and APPEND sit between the name and TO.
		"CREATE MATERIALIZED VIEW dashboard.member_list_serving_batch_mv REFRESH EVERY 1 MINUTE SETTINGS refresh_retries = 3 APPEND TO dashboard.member_list_serving_batches (`a` String) AS SELECT 1": "dashboard.member_list_serving_batches",
		"CREATE MATERIALIZED VIEW `my-db`.mv TO `my-db`.`dst-1` (`a` String) AS SELECT 1":                                                                                                              "my-db.dst-1",
		// An inner-table view has no TO; a TO inside the SELECT is not a target.
		"CREATE MATERIALIZED VIEW db.mv (`a` String) ENGINE = MergeTree ORDER BY a AS SELECT a FROM db.src": "",
	}
	for ddl, want := range cases {
		if got := viewTarget(ddl); got != want {
			t.Errorf("viewTarget(%q) = %q, want %q", ddl, got, want)
		}
	}
}

func TestRefreshSchedule(t *testing.T) {
	cases := map[string]string{
		"CREATE MATERIALIZED VIEW db.mv REFRESH EVERY 10 SECOND APPEND TO db.t AS SELECT 1":              "EVERY 10 SECOND",
		"CREATE MATERIALIZED VIEW db.mv REFRESH every 1 day offset 2 hour TO db.t AS SELECT 1":           "EVERY 1 DAY OFFSET 2 HOUR",
		"CREATE MATERIALIZED VIEW db.mv REFRESH AFTER 30 MINUTE TO db.t AS SELECT 1":                     "AFTER 30 MINUTE",
		"CREATE MATERIALIZED VIEW db.mv TO db.t AS SELECT refresh_every FROM db.src WHERE x = 'REFRESH'": "",
	}
	for ddl, want := range cases {
		if got := refreshSchedule(ddl); got != want {
			t.Errorf("refreshSchedule(%q) = %q, want %q", ddl, got, want)
		}
	}
}

func TestSelectReads(t *testing.T) {
	tables, dictionaries := selectReads(
		"SELECT a.id, dictGet('dims.country', 'name', a.cc), dictGetOrDefault('local_dict', 'x', a.k, '') "+
			"FROM db.events AS a LEFT JOIN other.users u ON a.uid = u.id "+
			"WHERE a.id IN (SELECT id FROM db.allow)",
		"db.mv")
	wantTables := []string{"db.events", "other.users", "db.allow"}
	if !reflect.DeepEqual(tables, wantTables) {
		t.Errorf("tables = %v, want %v", tables, wantTables)
	}
	wantDictionaries := []string{"dims.country", "db.local_dict"}
	if !reflect.DeepEqual(dictionaries, wantDictionaries) {
		t.Errorf("dictionaries = %v, want %v", dictionaries, wantDictionaries)
	}

	// The parser refuses ANTI LEFT JOIN; the FROM/JOIN fallback still finds
	// both tables (and the subquery alias "d", which callers drop as unknown).
	tables, _ = selectReads(
		"WITH (SELECT max(v) FROM dashboard.drained) AS high "+
			"SELECT s.id FROM dashboard.dirty AS s ANTI LEFT JOIN (SELECT id FROM dashboard.drained) AS d USING (id)",
		"dashboard.drain_mv")
	if want := []string{"dashboard.drained", "dashboard.dirty"}; !reflect.DeepEqual(tables, want) {
		t.Errorf("fallback tables = %v, want %v", tables, want)
	}

	if tables, dictionaries := selectReads("", "db.mv"); tables != nil || dictionaries != nil {
		t.Errorf("empty SELECT read %v %v", tables, dictionaries)
	}
}

func TestViewRelationsKinds(t *testing.T) {
	known := map[string]TableInfo{
		"db.events":   {Engine: "MergeTree"},
		"db.users":    {Engine: "MergeTree"},
		"db.country":  {Engine: "Dictionary"},
		"db.out":      {Engine: "MergeTree"},
		"db.trig_mv":  {Engine: "MaterializedView"},
		"db.batch_mv": {Engine: "MaterializedView"},
		"db.report_v": {Engine: "View"},
	}
	join := "SELECT e.id, dictGet('db.country', 'name', e.cc) FROM db.events e JOIN db.users u ON e.uid = u.id"
	views := []pendingView{
		// Insert-triggered: ClickHouse records db.events; the JOIN and the
		// dictionary are lookups.
		{Name: "db.trig_mv", Target: "db.out", Select: join},
		// Refreshable: no trigger is recorded, and everything read feeds it.
		{Name: "db.batch_mv", Target: "db.out", Select: join, Refresh: "EVERY 1 MINUTE"},
		{Name: "db.report_v", Select: "SELECT * FROM db.out", Plain: true},
	}
	sources := map[string][]string{"db.trig_mv": {"db.events"}}

	got := viewRelations(views, sources, known)
	want := []TableRelation{
		{DependsOnTable: "db.events", Table: "db.trig_mv"},
		{DependsOnTable: "db.users", Table: "db.trig_mv", Kind: RelationLookup},
		{DependsOnTable: "db.country", Table: "db.trig_mv", Kind: RelationLookup},
		{DependsOnTable: "db.trig_mv", Table: "db.out"},

		{DependsOnTable: "db.events", Table: "db.batch_mv"},
		{DependsOnTable: "db.users", Table: "db.batch_mv"},
		{DependsOnTable: "db.country", Table: "db.batch_mv", Kind: RelationLookup},
		{DependsOnTable: "db.batch_mv", Table: "db.out"},

		{DependsOnTable: "db.out", Table: "db.report_v"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("viewRelations =\n%+v\nwant\n%+v", got, want)
	}

	if src := viewSource(got, known, "db.trig_mv"); src != "db.events" {
		t.Errorf("viewSource = %q, want the trigger table, not a lookup", src)
	}
}
