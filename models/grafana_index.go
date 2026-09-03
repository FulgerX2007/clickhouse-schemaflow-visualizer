package models

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// GrafanaScanStats describes the last scan.
type GrafanaScanStats struct {
	Dashboards int `json:"dashboards"`
	Panels     int `json:"panels"`
	Queries    int `json:"queries"`
	Parsed     int `json:"parsed"`
	Heuristic  int `json:"heuristic"`
	Failed     int `json:"failed"`
	Skipped    int `json:"skipped"`
}

// SchemaLoader reads the ClickHouse schema. Injected so the index can be
// exercised without a database.
type SchemaLoader func() (SchemaSnapshot, error)

// GrafanaIndex owns the dashboard scan and everything derived from it.
//
// Used through a pointer because it carries a mutex; the data it holds is
// otherwise passed around by value.
type GrafanaIndex struct {
	config GrafanaConfig
	source DashboardSource
	schema SchemaLoader

	mu        sync.RWMutex
	scanning  bool
	state     GrafanaState
	mode      GrafanaMode
	reason    string
	failure   string
	scannedAt time.Time
	stats     GrafanaScanStats
	warnings  []string
	snapshot  SchemaSnapshot
	usage     UsageIndex
}

// NewGrafanaIndex builds an index that has not scanned yet.
func NewGrafanaIndex(config GrafanaConfig, source DashboardSource, schema SchemaLoader, configReason string) *GrafanaIndex {
	index := &GrafanaIndex{
		config: config,
		source: source,
		schema: schema,
		state:  GrafanaStateScanning,
		mode:   config.Mode,
		reason: configReason,
		usage:  newUsageIndex(),
	}
	if !config.Enabled() {
		index.state = GrafanaStateDisabled
		index.reason = "not configured"
		// Mode names the source the scan used, and there is none.
		index.mode = ""
	}
	return index
}

// Start kicks off the first scan in the background.
//
// Asynchronous so a slow or hung Grafana cannot delay the listen call: the
// visualizer's own function does not depend on this feature, and blocking
// startup on it would take the whole app down for an add-on.
func (g *GrafanaIndex) Start() {
	if g == nil || !g.config.Enabled() {
		return
	}
	if !g.claimScan() {
		return
	}
	go func() {
		// A panic here would take the whole process down: this runs on its own
		// goroutine, and the scan feeds dashboard-authored SQL through a
		// third-party parser. The visualizer does not depend on this feature,
		// so it must not be able to end the process.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Grafana dashboard scan panicked: %v", r)
				g.recordFailure(fmt.Errorf("dashboard scan panicked: %v", r))
			}
		}()
		defer g.releaseScan()

		if err := g.scan(); err != nil {
			log.Printf("Grafana dashboard scan failed: %v", err)
		}
	}()
}

// Status reports what the index knows, for GET /api/grafana/status.
func (g *GrafanaIndex) Status() GrafanaStatus {
	if g == nil {
		return GrafanaStatus{State: GrafanaStateDisabled, Reason: "not configured"}
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	status := GrafanaStatus{
		State:  g.state,
		Mode:   g.mode,
		Reason: g.reason,
		Error:  g.failure,
		Stats:  g.stats,
	}
	if !g.scannedAt.IsZero() {
		status.ScannedAt = g.scannedAt.UTC().Format(time.RFC3339)
	}
	status.Warnings = append(status.Warnings, g.warnings...)
	return status
}

// Refresh re-scans unless the last scan is still inside the TTL, or one is
// already running.
//
// The TTL doubles as the debounce for POST /api/grafana/refresh. The HTTP API
// has no authentication or rate limiting, and a refresh costs one request per
// dashboard against Grafana plus a full schema read against ClickHouse, so an
// undebounced endpoint is a free amplification lever.
//
// The TTL alone does not close it. scannedAt is only written when a scan
// finishes, so between the first Start() and its completion — 147 dashboards
// take a while — every concurrent call read a zero timestamp, passed the check
// and launched its own full scan. Twenty simultaneous requests bought twenty
// scans. Claiming the slot under the write lock is what actually bounds it:
// whoever wins scans, everyone else is told they were debounced.
func (g *GrafanaIndex) Refresh() (GrafanaStatus, bool) {
	if g == nil || !g.config.Enabled() {
		return g.Status(), false
	}

	if !g.claimScan() {
		return g.Status(), true
	}
	defer g.releaseScan()

	if err := g.scan(); err != nil {
		log.Printf("Grafana dashboard refresh failed: %v", err)
	}
	return g.Status(), false
}

// claimScan takes the single scan slot, reporting false when the caller must
// not scan: one is in flight, or the last one is still inside the TTL.
func (g *GrafanaIndex) claimScan() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.scanning {
		return false
	}
	if !g.scannedAt.IsZero() && time.Since(g.scannedAt) < g.config.CacheTTL {
		return false
	}
	g.scanning = true
	return true
}

func (g *GrafanaIndex) releaseScan() {
	g.mu.Lock()
	g.scanning = false
	g.mu.Unlock()
}

// Usage returns one table's column verdicts.
func (g *GrafanaIndex) Usage(table string) TableUsage {
	if g == nil {
		return TableUsage{}
	}

	g.mu.RLock()
	defer g.mu.RUnlock()
	return BuildTableUsage(g.snapshot, g.usage, g.state, table)
}

// Report returns the cross-schema column report.
func (g *GrafanaIndex) Report(filters UnusedReportFilters) UnusedReport {
	if g == nil {
		return UnusedReport{Rows: []UnusedReportRow{}}
	}

	g.mu.RLock()
	defer g.mu.RUnlock()
	return BuildUnusedReport(g.snapshot, g.usage, g.state, filters)
}

// Enabled reports whether a dashboard source is configured at all.
func (g *GrafanaIndex) Enabled() bool {
	return g != nil && g.config.Enabled()
}

// scan reads the dashboards and the schema and rebuilds the usage index.
//
// A failure leaves the previous good index in place: stale usage is far more
// useful than an empty one, which would present as "nothing reads anything".
func (g *GrafanaIndex) scan() error {
	snapshot, err := g.schema()
	if err != nil {
		g.recordFailure(err)
		return err
	}

	scan, err := g.source.Dashboards()
	if err != nil {
		g.recordFailure(err)
		return err
	}

	queries, stats := resolveDashboards(scan, snapshot, g.config)
	index := BuildUsageIndex(snapshot, queries)
	stats.Dashboards = len(scan.Dashboards)
	stats.Skipped = scan.Skipped

	g.mu.Lock()
	defer g.mu.Unlock()
	g.state = GrafanaStateOK
	g.mode = scan.Mode
	g.failure = ""
	g.scannedAt = time.Now()
	g.stats = stats
	g.warnings = scan.Warnings
	g.snapshot = snapshot
	g.usage = index
	return nil
}

// resolveDashboards turns a scan into resolved queries and counts what happened.
func resolveDashboards(scan DashboardScan, snapshot SchemaSnapshot, config GrafanaConfig) ([]ResolvedQuery, GrafanaScanStats) {
	lookup := snapshot.Lookup()
	resolved := []ResolvedQuery{}
	stats := GrafanaScanStats{}

	for _, dashboard := range scan.Dashboards {
		source := UsageRef{
			DashboardUID:   dashboard.UID,
			DashboardTitle: dashboard.Title,
			DashboardURL:   dashboard.URL,
		}

		for _, panel := range dashboard.Panels {
			stats.Panels++
			for _, sql := range panel.Queries {
				ref := source
				ref.PanelID = panel.ID
				ref.PanelTitle = panel.Title
				resolved = append(resolved, resolveOne(sql, dashboard, ref, config, lookup, &stats))
			}
		}

		for _, variable := range dashboard.Variables {
			if variable.Query == "" {
				continue
			}
			ref := source
			ref.PanelTitle = "variable: " + variable.Name
			resolved = append(resolved, resolveOne(variable.Query, dashboard, ref, config, lookup, &stats))
		}
	}

	return resolved, stats
}

func resolveOne(sql string, dashboard Dashboard, ref UsageRef, config GrafanaConfig, lookup ColumnLookup, stats *GrafanaScanStats) ResolvedQuery {
	stats.Queries++
	result := ResolveQuery(sql, dashboard.Variables, config.DefaultDatabase, config.SnippetChars, lookup)
	if result.ParseError == "" {
		stats.Parsed++
	} else {
		stats.Failed++
	}
	for _, reference := range result.References {
		if reference.Confidence == ConfidenceHeuristic {
			stats.Heuristic++
		}
	}
	return ResolvedQuery{Source: ref, Result: result}
}

// recordFailure marks the index errored while keeping whatever it already holds.
func (g *GrafanaIndex) recordFailure(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state = GrafanaStateError
	g.failure = err.Error()
}

// ─── Test seams ──────────────────────────────────────────────────────────────
//
// The api package cannot reach the unexported source and loader fields, and
// building a real index there would need a live ClickHouse and a live Grafana.
// These constructors exist so the handlers can be tested against a scanned index.

// fixedSource returns a scan that was prepared in advance.
type fixedSource struct {
	scan DashboardScan
	err  error
}

func (s fixedSource) Dashboards() (DashboardScan, error) {
	if s.err != nil {
		return DashboardScan{}, s.err
	}
	return s.scan, nil
}

// NewGrafanaIndexForTest builds an index over a prepared scan and schema.
func NewGrafanaIndexForTest(config GrafanaConfig, scan DashboardScan, snapshot SchemaSnapshot) *GrafanaIndex {
	return NewGrafanaIndex(config, fixedSource{scan: scan},
		func() (SchemaSnapshot, error) { return snapshot, nil }, "")
}

// NewGrafanaIndexFailingForTest builds an index whose source always fails.
func NewGrafanaIndexFailingForTest(config GrafanaConfig, err error) *GrafanaIndex {
	return NewGrafanaIndex(config, fixedSource{err: err},
		func() (SchemaSnapshot, error) { return SchemaSnapshot{}, nil }, "")
}

// ScanForTest runs one scan synchronously.
func (g *GrafanaIndex) ScanForTest() error {
	return g.scan()
}
