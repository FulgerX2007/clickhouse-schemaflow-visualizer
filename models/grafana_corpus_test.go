package models

import (
	"os"
	"testing"
)

// TestCorpusResolution measures the resolver against a real dashboard tree.
//
// It is the plan's measurement instrument rather than a pass/fail check on
// fixtures: the thresholds are deliberately loose, because its job is to make a
// regression in parse rate visible, not to pin an exact number. Set
// GRAFANA_DASHBOARDS_DIR to a provisioned dashboard directory to run it.
func TestCorpusResolution(t *testing.T) {
	dir := os.Getenv("GRAFANA_DASHBOARDS_DIR")
	if dir == "" {
		t.Skip("set GRAFANA_DASHBOARDS_DIR to measure against a real dashboard tree")
	}

	scan, err := newDirSource(dir, "").Dashboards()
	if err != nil {
		t.Fatalf("scan %s: %v", dir, err)
	}
	if len(scan.Dashboards) == 0 {
		t.Fatalf("%s holds no readable dashboards", dir)
	}

	queries, parsed, exact, heuristic := 0, 0, 0, 0
	for _, dashboard := range scan.Dashboards {
		for _, sql := range dashboardQueries(dashboard) {
			queries++
			result := ResolveQuery(sql, dashboard.Variables, "default", 120, nil)
			if result.ParseError == "" {
				parsed++
			}
			for _, reference := range result.References {
				if reference.Confidence == ConfidenceExact {
					exact++
				} else {
					heuristic++
				}
			}
		}
	}

	rate := float64(parsed) / float64(queries)
	t.Logf("dashboards=%d queries=%d parsed=%d (%.1f%%) refs exact=%d heuristic=%d",
		len(scan.Dashboards), queries, parsed, 100*rate, exact, heuristic)

	// The plan's gate: below this the AST is not earning its dependency.
	if rate < 0.70 {
		t.Errorf("AST parse rate %.1f%% is below the 70%% gate", 100*rate)
	}
	if exact == 0 {
		t.Error("no exact references at all, which would make every verdict unknown")
	}
}

// dashboardQueries flattens every SQL string a dashboard carries.
func dashboardQueries(dashboard Dashboard) []string {
	queries := []string{}
	for _, panel := range dashboard.Panels {
		queries = append(queries, panel.Queries...)
	}
	for _, variable := range dashboard.Variables {
		if variable.Query != "" {
			queries = append(queries, variable.Query)
		}
	}
	return queries
}
