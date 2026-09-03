package models

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubSource hands back a fixed scan, optionally failing.
type stubSource struct {
	mu    sync.Mutex
	scan  DashboardScan
	err   error
	calls int
	// delay stands in for the cost of a real scan — one HTTP request per
	// dashboard. Slept outside the lock, so concurrent callers overlap here
	// exactly as they would against a live Grafana.
	delay time.Duration
}

func (s *stubSource) Dashboards() (DashboardScan, error) {
	s.mu.Lock()
	s.calls++
	scan, err, delay := s.scan, s.err, s.delay
	s.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if err != nil {
		return DashboardScan{}, err
	}
	return scan, nil
}

func (s *stubSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *stubSource) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func indexFixture(t *testing.T) (*GrafanaIndex, *stubSource) {
	t.Helper()

	snapshot := testSnapshot()
	source := &stubSource{scan: DashboardScan{
		Mode: GrafanaModeDir,
		Dashboards: []Dashboard{{
			UID:   "dash1",
			Title: "Orders",
			Panels: []Panel{{
				ID:      1,
				Title:   "Revenue",
				Queries: []string{"SELECT amount FROM shop.orders"},
			}},
			Variables: []TemplateVariable{{Name: "period", Query: "SELECT name FROM shop.customers"}},
		}},
	}}

	config := GrafanaConfig{
		Mode: GrafanaModeDir, DashboardsDir: "/dash",
		CacheTTL: time.Hour, DefaultDatabase: "shop", SnippetChars: 100,
	}
	index := NewGrafanaIndex(config, source, func() (SchemaSnapshot, error) { return snapshot, nil }, "")
	return index, source
}

func TestGrafanaIndexScanPopulatesStatusAndUsage(t *testing.T) {
	index, _ := indexFixture(t)

	if got := index.Status().State; got != GrafanaStateScanning {
		t.Errorf("state before scanning = %q, want %q", got, GrafanaStateScanning)
	}
	// Nothing may be called droppable before the first scan completes.
	if index.Report(UnusedReportFilters{}).Totals.Unused != 0 {
		t.Error("a column was reported unused before the first scan")
	}

	if err := index.scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	status := index.Status()
	if status.State != GrafanaStateOK {
		t.Fatalf("state = %q, want %q", status.State, GrafanaStateOK)
	}
	if status.Mode != GrafanaModeDir {
		t.Errorf("mode = %q, want the scan's mode", status.Mode)
	}
	if status.ScannedAt == "" {
		t.Error("ScannedAt is empty after a successful scan")
	}
	if status.Stats.Dashboards != 1 || status.Stats.Panels != 1 {
		t.Errorf("stats = %+v, want 1 dashboard and 1 panel", status.Stats)
	}
	// One panel query plus one variable query.
	if status.Stats.Queries != 2 || status.Stats.Parsed != 2 {
		t.Errorf("stats = %+v, want 2 queries both parsed", status.Stats)
	}

	usage := index.Usage("shop.orders")
	verdict, _ := verdictOf(usage, "amount")
	if verdict != VerdictUsed {
		t.Errorf("amount verdict = %q, want used", verdict)
	}
	if verdict, _ := verdictOf(usage, "note"); verdict != VerdictUnused {
		t.Errorf("note verdict = %q, want unused", verdict)
	}
}

// A failed refresh must leave the previous good index in place: stale usage is
// far more useful than an empty one, which would read as "nothing uses anything".
func TestGrafanaIndexFailedRefreshKeepsThePreviousIndex(t *testing.T) {
	index, source := indexFixture(t)
	if err := index.scan(); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	before := index.Status().ScannedAt

	source.fail(errors.New("grafana exploded"))
	if err := index.scan(); err == nil {
		t.Fatal("expected the second scan to fail")
	}

	status := index.Status()
	if status.State != GrafanaStateError {
		t.Errorf("state = %q, want %q", status.State, GrafanaStateError)
	}
	if !strings.Contains(status.Error, "grafana exploded") {
		t.Errorf("Error = %q, want the failure named", status.Error)
	}
	// The timestamp still points at the last good scan.
	if status.ScannedAt != before {
		t.Errorf("ScannedAt = %q, want the previous good scan %q", status.ScannedAt, before)
	}
	if status.Stats.Dashboards != 1 {
		t.Errorf("stats were cleared by a failed scan: %+v", status.Stats)
	}
	// But nothing may be called droppable while the state is errored.
	if index.Report(UnusedReportFilters{}).Totals.Unused != 0 {
		t.Error("a column was reported unused while the scan state is errored")
	}
}

func TestGrafanaIndexSchemaFailureIsRecorded(t *testing.T) {
	source := &stubSource{}
	index := NewGrafanaIndex(
		GrafanaConfig{Mode: GrafanaModeDir, CacheTTL: time.Hour}, source,
		func() (SchemaSnapshot, error) { return SchemaSnapshot{}, errors.New("clickhouse down") }, "")

	if err := index.scan(); err == nil {
		t.Fatal("expected the scan to fail")
	}
	if got := index.Status().Error; !strings.Contains(got, "clickhouse down") {
		t.Errorf("Error = %q", got)
	}
	// The dashboard source must not be asked for anything when the schema failed.
	if source.callCount() != 0 {
		t.Errorf("the source was called %d times despite the schema failing", source.callCount())
	}
}

// The TTL doubles as the refresh debounce: the endpoint is unauthenticated and a
// refresh costs one request per dashboard against Grafana.
func TestGrafanaIndexRefreshIsDebounced(t *testing.T) {
	index, source := indexFixture(t)
	if err := index.scan(); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	calls := source.callCount()

	status, debounced := index.Refresh()
	if !debounced {
		t.Error("a refresh inside the TTL was not debounced")
	}
	if !status.Debounced && status.State != GrafanaStateOK {
		t.Errorf("status = %+v", status)
	}
	if source.callCount() != calls {
		t.Errorf("the source was re-read despite the debounce")
	}
}

// A burst of refreshes before the first scan finishes must still cost one scan.
//
// The TTL cannot decide this on its own: scannedAt is written only when a scan
// completes, so during the first one every caller reads a zero timestamp. This
// is the regression that let twenty concurrent POST /api/grafana/refresh calls
// buy twenty full scans of Grafana and ClickHouse on an endpoint with no
// authentication and no rate limiting.
func TestGrafanaIndexConcurrentRefreshScansOnce(t *testing.T) {
	index, source := indexFixture(t)
	source.mu.Lock()
	source.delay = 100 * time.Millisecond
	source.mu.Unlock()

	const callers = 20
	var wg sync.WaitGroup
	var scanned int64
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, debounced := index.Refresh(); !debounced {
				atomic.AddInt64(&scanned, 1)
			}
		}()
	}
	wg.Wait()

	if got := source.callCount(); got != 1 {
		t.Errorf("%d concurrent refreshes read the source %d times, want 1", callers, got)
	}
	if got := atomic.LoadInt64(&scanned); got != 1 {
		t.Errorf("%d refreshes reported %d scans, want 1", callers, got)
	}
}

func TestGrafanaIndexRefreshRunsOnceTheTTLExpires(t *testing.T) {
	index, source := indexFixture(t)
	index.config.CacheTTL = time.Nanosecond
	if err := index.scan(); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	calls := source.callCount()

	if _, debounced := index.Refresh(); debounced {
		t.Error("a refresh after the TTL was debounced")
	}
	if source.callCount() <= calls {
		t.Error("the source was not re-read after the TTL expired")
	}
}

// Reads must stay safe while a refresh is rewriting the index.
func TestGrafanaIndexConcurrentReadsDuringRefresh(t *testing.T) {
	index, _ := indexFixture(t)
	index.config.CacheTTL = time.Nanosecond
	if err := index.scan(); err != nil {
		t.Fatalf("first scan: %v", err)
	}

	stop := make(chan struct{})
	var group sync.WaitGroup

	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for {
				select {
				case <-stop:
					return
				default:
					index.Status()
					index.Usage("shop.orders")
					index.Report(UnusedReportFilters{})
				}
			}
		}()
	}

	for i := 0; i < 20; i++ {
		index.Refresh()
	}
	close(stop)
	group.Wait()
}

func TestGrafanaIndexDisabledDoesNothing(t *testing.T) {
	index := NewGrafanaIndex(GrafanaConfig{Mode: GrafanaModeDisabled}, nil, nil, "")

	if index.Enabled() {
		t.Error("a disabled index reports itself enabled")
	}
	status := index.Status()
	if status.State != GrafanaStateDisabled || status.Reason != "not configured" {
		t.Errorf("status = %+v", status)
	}
	// Must not panic or touch a nil source.
	index.Start()
	if _, debounced := index.Refresh(); debounced {
		t.Error("a disabled index debounced a refresh")
	}
	if index.Report(UnusedReportFilters{}).Totals.Unused != 0 {
		t.Error("a disabled index reported an unused column")
	}
}

// A nil index is what the handlers hold when the feature is off.
func TestGrafanaIndexNilIsSafe(t *testing.T) {
	var index *GrafanaIndex

	if index.Enabled() {
		t.Error("a nil index reports itself enabled")
	}
	if got := index.Status().State; got != GrafanaStateDisabled {
		t.Errorf("state = %q, want %q", got, GrafanaStateDisabled)
	}
	index.Start()
	index.Refresh()
	index.Usage("shop.orders")
	index.Report(UnusedReportFilters{})
}

func TestGrafanaIndexCountsFallbackQueries(t *testing.T) {
	snapshot := testSnapshot()
	source := &stubSource{scan: DashboardScan{
		Mode: GrafanaModeDir,
		Dashboards: []Dashboard{{
			UID: "d", Title: "D",
			Panels: []Panel{{ID: 1, Queries: []string{
				"SELECT amount FROM shop.orders",
				"SELECT amount FROM shop.orders WHERE (((",
			}}},
		}},
	}}
	index := NewGrafanaIndex(
		GrafanaConfig{Mode: GrafanaModeDir, CacheTTL: time.Hour, DefaultDatabase: "shop"},
		source, func() (SchemaSnapshot, error) { return snapshot, nil }, "")

	if err := index.scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	stats := index.Status().Stats
	if stats.Queries != 2 || stats.Parsed != 1 || stats.Failed != 1 {
		t.Errorf("stats = %+v, want 2 queries, 1 parsed, 1 failed", stats)
	}
	if stats.Heuristic == 0 {
		t.Error("the fallback's references were not counted as heuristic")
	}
}
