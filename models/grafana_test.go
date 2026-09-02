package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// envFunc builds a getenv stand-in from a map, so mode resolution is exercised
// without mutating the process environment.
func envFunc(vars map[string]string) func(string) string {
	return func(key string) string {
		return vars[key]
	}
}

func TestLoadGrafanaConfigMode(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		wantMode GrafanaMode
		wantErr  string
	}{
		{
			name:     "nothing configured is not an error",
			env:      map[string]string{},
			wantMode: GrafanaModeDisabled,
		},
		{
			name:     "url and token select api mode",
			env:      map[string]string{"GRAFANA_URL": "https://grafana.example", "GRAFANA_TOKEN": "t"},
			wantMode: GrafanaModeAPI,
		},
		{
			name:     "directory alone selects dir mode",
			env:      map[string]string{"GRAFANA_DASHBOARDS_DIR": "/dash"},
			wantMode: GrafanaModeDir,
		},
		{
			name: "api wins when both are configured",
			env: map[string]string{
				"GRAFANA_URL":            "https://grafana.example",
				"GRAFANA_TOKEN":          "t",
				"GRAFANA_DASHBOARDS_DIR": "/dash",
			},
			wantMode: GrafanaModeAPI,
		},
		{
			name:     "url without token is an error and disables the feature",
			env:      map[string]string{"GRAFANA_URL": "https://grafana.example"},
			wantMode: GrafanaModeDisabled,
			wantErr:  "GRAFANA_URL is set without GRAFANA_TOKEN",
		},
		{
			name:     "token without url is an error and disables the feature",
			env:      map[string]string{"GRAFANA_TOKEN": "t"},
			wantMode: GrafanaModeDisabled,
			wantErr:  "GRAFANA_TOKEN is set without GRAFANA_URL",
		},
		{
			name: "half-configured api still falls back to the directory",
			env: map[string]string{
				"GRAFANA_URL":            "https://grafana.example",
				"GRAFANA_DASHBOARDS_DIR": "/dash",
			},
			wantMode: GrafanaModeDir,
			wantErr:  "falling back to GRAFANA_DASHBOARDS_DIR",
		},
		{
			name:     "whitespace-only values count as unset",
			env:      map[string]string{"GRAFANA_URL": "   ", "GRAFANA_TOKEN": "\t"},
			wantMode: GrafanaModeDisabled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadGrafanaConfig(envFunc(tt.env), "default")

			if cfg.Mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", cfg.Mode, tt.wantMode)
			}
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadGrafanaConfigDefaults(t *testing.T) {
	cfg, err := LoadGrafanaConfig(envFunc(map[string]string{"GRAFANA_DASHBOARDS_DIR": "/dash"}), "probe_raw")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Timeout != defaultGrafanaTimeout {
		t.Errorf("Timeout = %v, want %v", cfg.Timeout, defaultGrafanaTimeout)
	}
	if cfg.CacheTTL != defaultGrafanaCacheTTL {
		t.Errorf("CacheTTL = %v, want %v", cfg.CacheTTL, defaultGrafanaCacheTTL)
	}
	if cfg.SnippetChars != defaultGrafanaSnippetChars {
		t.Errorf("SnippetChars = %d, want %d", cfg.SnippetChars, defaultGrafanaSnippetChars)
	}
	// The ClickHouse database is the default for unqualified table references.
	if cfg.DefaultDatabase != "probe_raw" {
		t.Errorf("DefaultDatabase = %q, want %q", cfg.DefaultDatabase, "probe_raw")
	}
}

func TestLoadGrafanaConfigOverrides(t *testing.T) {
	cfg, err := LoadGrafanaConfig(envFunc(map[string]string{
		"GRAFANA_DASHBOARDS_DIR":   "/dash",
		"GRAFANA_TIMEOUT":          "5s",
		"GRAFANA_CACHE_TTL":        "1h",
		"GRAFANA_SKIP_VERIFY":      "true",
		"GRAFANA_SNIPPET_CHARS":    "0",
		"GRAFANA_DEFAULT_DATABASE": "aggregated",
	}), "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", cfg.Timeout)
	}
	if cfg.CacheTTL != time.Hour {
		t.Errorf("CacheTTL = %v, want 1h", cfg.CacheTTL)
	}
	if !cfg.SkipVerify {
		t.Error("SkipVerify = false, want true")
	}
	// 0 is meaningful: it switches SQL snippets off entirely.
	if cfg.SnippetChars != 0 {
		t.Errorf("SnippetChars = %d, want 0", cfg.SnippetChars)
	}
	if cfg.DefaultDatabase != "aggregated" {
		t.Errorf("DefaultDatabase = %q, want %q", cfg.DefaultDatabase, "aggregated")
	}
}

func TestLoadGrafanaConfigInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "unparseable timeout",
			env:     map[string]string{"GRAFANA_DASHBOARDS_DIR": "/dash", "GRAFANA_TIMEOUT": "soon"},
			wantErr: "GRAFANA_TIMEOUT",
		},
		{
			name:    "zero timeout",
			env:     map[string]string{"GRAFANA_DASHBOARDS_DIR": "/dash", "GRAFANA_TIMEOUT": "0s"},
			wantErr: "must be positive",
		},
		{
			name:    "negative cache ttl",
			env:     map[string]string{"GRAFANA_DASHBOARDS_DIR": "/dash", "GRAFANA_CACHE_TTL": "-5m"},
			wantErr: "must be positive",
		},
		{
			name:    "non-boolean skip verify",
			env:     map[string]string{"GRAFANA_DASHBOARDS_DIR": "/dash", "GRAFANA_SKIP_VERIFY": "yes please"},
			wantErr: "GRAFANA_SKIP_VERIFY",
		},
		{
			name:    "non-numeric snippet chars",
			env:     map[string]string{"GRAFANA_DASHBOARDS_DIR": "/dash", "GRAFANA_SNIPPET_CHARS": "lots"},
			wantErr: "GRAFANA_SNIPPET_CHARS",
		},
		{
			name:    "negative snippet chars",
			env:     map[string]string{"GRAFANA_DASHBOARDS_DIR": "/dash", "GRAFANA_SNIPPET_CHARS": "-1"},
			wantErr: "must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadGrafanaConfig(envFunc(tt.env), "default")
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// An invalid option must not be silently ignored just because no source is
// configured — but neither should it enable anything.
func TestLoadGrafanaConfigIgnoresOptionsWhenDisabled(t *testing.T) {
	cfg, err := LoadGrafanaConfig(envFunc(map[string]string{"GRAFANA_TIMEOUT": "nonsense"}), "default")
	if err != nil {
		t.Errorf("unexpected error when no source is configured: %v", err)
	}
	if cfg.Mode != GrafanaModeDisabled {
		t.Errorf("mode = %q, want %q", cfg.Mode, GrafanaModeDisabled)
	}
}

func TestNewGrafanaStatus(t *testing.T) {
	tests := []struct {
		name       string
		cfg        GrafanaConfig
		configErr  error
		wantState  GrafanaState
		wantMode   GrafanaMode
		wantReason string
		wantError  string
	}{
		{
			name:       "disabled with no error",
			cfg:        GrafanaConfig{Mode: GrafanaModeDisabled},
			wantState:  GrafanaStateDisabled,
			wantReason: "not configured",
		},
		{
			name:      "disabled by a configuration error",
			cfg:       GrafanaConfig{Mode: GrafanaModeDisabled},
			configErr: errors.New("GRAFANA_URL is set without GRAFANA_TOKEN"),
			wantState: GrafanaStateError,
			wantError: "GRAFANA_URL is set without GRAFANA_TOKEN",
		},
		{
			name:      "configured sources have not scanned yet",
			cfg:       GrafanaConfig{Mode: GrafanaModeAPI},
			wantState: GrafanaStateScanning,
			wantMode:  GrafanaModeAPI,
		},
		{
			name:       "degraded but usable reports why",
			cfg:        GrafanaConfig{Mode: GrafanaModeDir},
			configErr:  errors.New("GRAFANA_URL is set without GRAFANA_TOKEN"),
			wantState:  GrafanaStateScanning,
			wantMode:   GrafanaModeDir,
			wantReason: "GRAFANA_URL is set without GRAFANA_TOKEN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewGrafanaStatus(tt.cfg, tt.configErr)

			if got.State != tt.wantState {
				t.Errorf("State = %q, want %q", got.State, tt.wantState)
			}
			if got.Mode != tt.wantMode {
				t.Errorf("Mode = %q, want %q", got.Mode, tt.wantMode)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if got.Error != tt.wantError {
				t.Errorf("Error = %q, want %q", got.Error, tt.wantError)
			}
		})
	}
}

// The status endpoint is the easiest place to leak the service-account token by
// accident, so assert it cannot appear there no matter what the config holds.
func TestGrafanaStatusNeverCarriesTheToken(t *testing.T) {
	const token = "glsa-supersecret-value"

	cfg, err := LoadGrafanaConfig(envFunc(map[string]string{
		"GRAFANA_URL":   "https://grafana.example",
		"GRAFANA_TOKEN": token,
	}), "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Token != token {
		t.Fatalf("config did not capture the token; the test would pass vacuously")
	}

	encoded, err := json.Marshal(NewGrafanaStatus(cfg, nil))
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if strings.Contains(string(encoded), token) {
		t.Errorf("status payload leaked the token: %s", encoded)
	}
}

// ─── Directory source ────────────────────────────────────────────────────────

func TestDirSourceReadsFixtures(t *testing.T) {
	scan, err := newDirSource("testdata/dashboards", "https://grafana.example/").Dashboards()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scan.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0 (warnings: %v)", scan.Skipped, scan.Warnings)
	}
	if len(scan.Dashboards) != 2 {
		t.Fatalf("got %d dashboards, want 2", len(scan.Dashboards))
	}

	byUID := map[string]Dashboard{}
	for _, d := range scan.Dashboards {
		byUID[d.UID] = d
	}

	// Bare dashboard object.
	simple, ok := byUID["sales-overview"]
	if !ok {
		t.Fatalf("sales-overview missing, got %v", byUID)
	}
	if simple.Title != "Sales Overview" {
		t.Errorf("Title = %q, want %q", simple.Title, "Sales Overview")
	}
	if simple.Source != "dir" {
		t.Errorf("Source = %q, want %q", simple.Source, "dir")
	}
	// The trailing slash on the base URL must not double up.
	if simple.URL != "https://grafana.example/d/sales-overview" {
		t.Errorf("URL = %q, want %q", simple.URL, "https://grafana.example/d/sales-overview")
	}

	// {"dashboard": …, "meta": …} wrapper, with the folder from meta.
	wrapped, ok := byUID["ops_traffic"]
	if !ok {
		t.Fatalf("ops_traffic missing, got %v", byUID)
	}
	if wrapped.Title != "Traffic" {
		t.Errorf("Title = %q, want %q", wrapped.Title, "Traffic")
	}
	if wrapped.Folder != "Ops" {
		t.Errorf("Folder = %q, want %q", wrapped.Folder, "Ops")
	}
}

func TestDirSourceWithoutBaseURL(t *testing.T) {
	scan, err := newDirSource("testdata/dashboards", "").Dashboards()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, d := range scan.Dashboards {
		if d.URL != "" {
			t.Errorf("%s: URL = %q, want empty when no base URL is configured", d.UID, d.URL)
		}
	}
}

// writeDashboards materialises a throwaway directory so the error paths do not
// need broken files checked into testdata.
func writeDashboards(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return dir
}

func TestDirSourceSkipsBadFiles(t *testing.T) {
	dir := writeDashboards(t, map[string]string{
		"good.json":       `{"uid":"good-one","title":"Good"}`,
		"broken.json":     `{"uid":"oops", NOT JSON`,
		"not-a-dash.json": `["a","list","is","not","a","dashboard"]`,
		"no-uid.json":     `{"title":"Anonymous"}`,
		"bad-uid.json":    `{"uid":"../../etc/passwd","title":"Traversal"}`,
		"notes.txt":       `ignored, wrong extension`,
	})

	scan, err := newDirSource(dir, "").Dashboards()
	if err != nil {
		t.Fatalf("one bad file must not fail the whole scan: %v", err)
	}

	if len(scan.Dashboards) != 1 || scan.Dashboards[0].UID != "good-one" {
		t.Fatalf("got %+v, want only good-one", scan.Dashboards)
	}
	// broken, not-a-dash, no-uid, bad-uid — the .txt is not a candidate at all.
	if scan.Skipped != 4 {
		t.Errorf("Skipped = %d, want 4 (warnings: %v)", scan.Skipped, scan.Warnings)
	}
	if len(scan.Warnings) != 4 {
		t.Errorf("got %d warnings, want 4: %v", len(scan.Warnings), scan.Warnings)
	}
}

func TestDirSourceDerivesFolderFromLayout(t *testing.T) {
	dir := writeDashboards(t, map[string]string{
		"oinis/common/pdd.json": `{"uid":"pdd","title":"PDD"}`,
		"root.json":             `{"uid":"root","title":"Root"}`,
	})

	scan, err := newDirSource(dir, "").Dashboards()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	folders := map[string]string{}
	for _, d := range scan.Dashboards {
		folders[d.UID] = d.Folder
	}
	// Provisioned trees carry the folder in the directory layout, not in the file.
	if folders["pdd"] != "oinis/common" {
		t.Errorf("nested folder = %q, want %q", folders["pdd"], "oinis/common")
	}
	if folders["root"] != "" {
		t.Errorf("root folder = %q, want empty", folders["root"])
	}
}

func TestDirSourceEmptyDirectory(t *testing.T) {
	scan, err := newDirSource(t.TempDir(), "").Dashboards()
	if err != nil {
		t.Fatalf("an empty directory is not an error: %v", err)
	}
	if len(scan.Dashboards) != 0 || scan.Skipped != 0 {
		t.Errorf("got %d dashboards and %d skipped, want 0 and 0", len(scan.Dashboards), scan.Skipped)
	}
}

// A mistyped path must fail loudly. Reporting zero dashboards would present as
// "no dashboard uses anything", which is the one conclusion this feature must
// never reach by accident.
func TestDirSourceMissingDirectoryIsAnError(t *testing.T) {
	_, err := newDirSource(filepath.Join(t.TempDir(), "nope"), "").Dashboards()
	if err == nil {
		t.Fatal("expected an error for a missing directory, got nil")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %q, want it to name the missing path", err)
	}
}

func TestDirSourceRejectsNonDirectory(t *testing.T) {
	dir := writeDashboards(t, map[string]string{"a.json": `{"uid":"a","title":"A"}`})

	_, err := newDirSource(filepath.Join(dir, "a.json"), "").Dashboards()
	if err == nil {
		t.Fatal("expected an error when the path is a file, got nil")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error = %q, want it to mention a non-directory", err)
	}
}

func TestNewDashboardUIDValidation(t *testing.T) {
	tests := []struct {
		name    string
		uid     string
		wantErr bool
	}{
		{name: "plain", uid: "abc123"},
		{name: "hyphen and underscore", uid: "sales-overview_v2"},
		{name: "max length", uid: strings.Repeat("a", 64)},
		{name: "empty", uid: "", wantErr: true},
		{name: "too long", uid: strings.Repeat("a", 65), wantErr: true},
		{name: "path traversal", uid: "../../etc/passwd", wantErr: true},
		{name: "url breakout", uid: "a/../b", wantErr: true},
		{name: "quote for attribute injection", uid: `a" onload="x`, wantErr: true},
		{name: "space", uid: "a b", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newDashboard(tt.uid, "Title", "", "dir", "https://grafana.example")
			if tt.wantErr && err == nil {
				t.Errorf("uid %q was accepted, want rejected", tt.uid)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("uid %q was rejected: %v", tt.uid, err)
			}
		})
	}
}

func TestNewDashboardFallsBackToUIDForTitle(t *testing.T) {
	dashboard, err := newDashboard("untitled-1", "  ", "", "dir", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dashboard.Title != "untitled-1" {
		t.Errorf("Title = %q, want the uid as a fallback", dashboard.Title)
	}
}

func TestDashboardScanWarningsAreBounded(t *testing.T) {
	scan := DashboardScan{}
	for i := 0; i < maxScanWarnings+10; i++ {
		scan = scan.withSkip("file %d is broken", i)
	}

	if scan.Skipped != maxScanWarnings+10 {
		t.Errorf("Skipped = %d, want %d — the count must keep rising", scan.Skipped, maxScanWarnings+10)
	}
	if len(scan.Warnings) != maxScanWarnings {
		t.Errorf("got %d warnings, want them capped at %d", len(scan.Warnings), maxScanWarnings)
	}
}

// A provisioning tree can hold the same dashboard in two folders. Grafana keys
// dashboards by uid, so keeping both would double-count every column they read.
func TestDirSourceDeduplicatesByUID(t *testing.T) {
	dir := writeDashboards(t, map[string]string{
		"a-common/report.json": `{"uid":"shared1","title":"Report"}`,
		"b-osn/report.json":    `{"uid":"shared1","title":"Report"}`,
		"b-osn/other.json":     `{"uid":"unique1","title":"Other"}`,
	})

	scan, err := newDirSource(dir, "").Dashboards()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(scan.Dashboards) != 2 {
		t.Fatalf("got %d dashboards, want 2 (uid collision must collapse)", len(scan.Dashboards))
	}
	if scan.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", scan.Skipped)
	}
	// First wins, and WalkDir's lexical order makes that deterministic.
	if scan.Dashboards[0].Folder != "a-common" {
		t.Errorf("kept folder = %q, want the lexically first %q", scan.Dashboards[0].Folder, "a-common")
	}
	if len(scan.Warnings) != 1 || !strings.Contains(scan.Warnings[0], "duplicate dashboard uid") {
		t.Errorf("warnings = %v, want one naming the duplicate uid", scan.Warnings)
	}
}

// ─── Grafana API source ──────────────────────────────────────────────────────

// grafanaServer stands in for Grafana. search returns the listing; dashboards
// maps uid to the per-dashboard response body.
type grafanaServer struct {
	search     string
	dashboards map[string]string
	status     map[string]int // per-path status override
	delay      time.Duration
	authSeen   []string
	requests   []string
}

func (g *grafanaServer) start(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.delay > 0 {
			time.Sleep(g.delay)
		}
		g.authSeen = append(g.authSeen, r.Header.Get("Authorization"))
		g.requests = append(g.requests, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		if code, ok := g.status[r.URL.Path]; ok {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(g.dashboards[r.URL.Path]))
			return
		}

		if r.URL.Path == "/api/search" {
			// Only the first page carries results; the second ends the paging loop.
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(g.search))
			return
		}

		uid := strings.TrimPrefix(r.URL.Path, "/api/dashboards/uid/")
		body, ok := g.dashboards[uid]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Dashboard not found"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func apiConfig(url, token string) GrafanaConfig {
	return GrafanaConfig{Mode: GrafanaModeAPI, URL: url, Token: token, Timeout: 5 * time.Second}
}

func TestAPISourceHappyPath(t *testing.T) {
	grafana := &grafanaServer{
		search: `[{"uid":"abc123","title":"Search Title","folderTitle":"Ops"}]`,
		dashboards: map[string]string{
			"abc123": `{"meta":{"folderTitle":"Ops"},"dashboard":{"uid":"abc123","title":"Real Title"}}`,
		},
	}
	server := grafana.start(t)

	scan, err := newAPISource(apiConfig(server.URL, "tok")).Dashboards()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if scan.Mode != GrafanaModeAPI {
		t.Errorf("Mode = %q, want %q", scan.Mode, GrafanaModeAPI)
	}
	if len(scan.Dashboards) != 1 {
		t.Fatalf("got %d dashboards, want 1", len(scan.Dashboards))
	}

	dashboard := scan.Dashboards[0]
	if dashboard.Title != "Real Title" {
		t.Errorf("Title = %q, want the dashboard object's title", dashboard.Title)
	}
	if dashboard.Folder != "Ops" {
		t.Errorf("Folder = %q, want %q", dashboard.Folder, "Ops")
	}
	if dashboard.Source != "api" {
		t.Errorf("Source = %q, want %q", dashboard.Source, "api")
	}
	if dashboard.URL != server.URL+"/d/abc123" {
		t.Errorf("URL = %q, want %q", dashboard.URL, server.URL+"/d/abc123")
	}
	for _, auth := range grafana.authSeen {
		if auth != "Bearer tok" {
			t.Errorf("Authorization = %q, want a bearer token", auth)
		}
	}
}

func TestAPISourceFallsBackToSearchMetadata(t *testing.T) {
	grafana := &grafanaServer{
		search: `[{"uid":"abc123","title":"Search Title","folderTitle":"FromSearch"}]`,
		// The dashboard object omits its own uid, title and folder.
		dashboards: map[string]string{"abc123": `{"dashboard":{}}`},
	}
	server := grafana.start(t)

	scan, err := newAPISource(apiConfig(server.URL, "tok")).Dashboards()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scan.Dashboards) != 1 {
		t.Fatalf("got %d dashboards, want 1", len(scan.Dashboards))
	}
	if got := scan.Dashboards[0]; got.UID != "abc123" || got.Title != "Search Title" || got.Folder != "FromSearch" {
		t.Errorf("got %+v, want the search hit's uid, title and folder", got)
	}
}

// A listing failure must fail the scan: "search returned nothing" and "no column
// is used" are indistinguishable downstream.
func TestAPISourceSearchFailuresAreFatal(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"message":"Unauthorized"}`, wantErr: "Unauthorized"},
		{name: "server error", status: http.StatusInternalServerError, body: `{"message":"boom"}`, wantErr: "boom"},
		{name: "not json", status: http.StatusOK, body: `<html>login</html>`, wantErr: "decode search page 1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			grafana := &grafanaServer{
				search:     tt.body,
				status:     map[string]int{},
				dashboards: map[string]string{"/api/search": tt.body},
			}
			if tt.status != http.StatusOK {
				grafana.status["/api/search"] = tt.status
			}
			server := grafana.start(t)

			_, err := newAPISource(apiConfig(server.URL, "tok")).Dashboards()
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestAPISourceEmptyResultIsNotAnError(t *testing.T) {
	server := (&grafanaServer{search: `[]`}).start(t)

	scan, err := newAPISource(apiConfig(server.URL, "tok")).Dashboards()
	if err != nil {
		t.Fatalf("an instance with no dashboards is not an error: %v", err)
	}
	if len(scan.Dashboards) != 0 || scan.Skipped != 0 {
		t.Errorf("got %d dashboards / %d skipped, want 0 / 0", len(scan.Dashboards), scan.Skipped)
	}
}

// One dashboard failing is survivable; the scan keeps the rest and counts it.
func TestAPISourceSkipsOneBadDashboard(t *testing.T) {
	grafana := &grafanaServer{
		search: `[{"uid":"good1","title":"Good"},{"uid":"v2dash","title":"V2"},{"uid":"gone","title":"Gone"}]`,
		dashboards: map[string]string{
			"good1":  `{"dashboard":{"uid":"good1","title":"Good"}}`,
			"v2dash": `{"message":"dashboard api version not supported, use /apis/dashboard.grafana.app/v2beta1/"}`,
		},
		status: map[string]int{"/api/dashboards/uid/v2dash": http.StatusBadRequest},
	}
	grafana.dashboards["/api/dashboards/uid/v2dash"] = grafana.dashboards["v2dash"]
	server := grafana.start(t)

	scan, err := newAPISource(apiConfig(server.URL, "tok")).Dashboards()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(scan.Dashboards) != 1 || scan.Dashboards[0].UID != "good1" {
		t.Fatalf("got %+v, want only good1", scan.Dashboards)
	}
	if scan.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2 (the v2 dashboard and the missing one)", scan.Skipped)
	}

	joined := strings.Join(scan.Warnings, "\n")
	// The v2 refusal is named rather than reported as a generic failure.
	if !strings.Contains(joined, "v2-schema dashboard") {
		t.Errorf("warnings = %v, want the v2 refusal named", scan.Warnings)
	}
	if !strings.Contains(joined, "v2dash") || !strings.Contains(joined, "gone") {
		t.Errorf("warnings = %v, want both failures to name their uid", scan.Warnings)
	}
}

// Every dashboard failing is a systemic problem dressed as an empty Grafana.
func TestAPISourceAllDashboardsFailingIsAnError(t *testing.T) {
	grafana := &grafanaServer{
		search:     `[{"uid":"a1","title":"A"},{"uid":"b2","title":"B"}]`,
		dashboards: map[string]string{},
	}
	server := grafana.start(t)

	_, err := newAPISource(apiConfig(server.URL, "tok")).Dashboards()
	if err == nil {
		t.Fatal("expected an error when no dashboard could be loaded, got nil")
	}
	if !strings.Contains(err.Error(), "all 2 dashboards failed") {
		t.Errorf("error = %q, want it to report that every dashboard failed", err)
	}
}

func TestAPISourceRejectsInvalidUIDFromSearch(t *testing.T) {
	grafana := &grafanaServer{
		search:     `[{"uid":"../../admin","title":"Traversal"}]`,
		dashboards: map[string]string{},
	}
	server := grafana.start(t)

	_, err := newAPISource(apiConfig(server.URL, "tok")).Dashboards()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	// It must be rejected before any request is made for it.
	for _, path := range grafana.requests {
		if strings.Contains(path, "admin") {
			t.Errorf("a request was made for the invalid uid: %s", path)
		}
	}
}

func TestAPISourceTimeout(t *testing.T) {
	grafana := &grafanaServer{search: `[]`, delay: 150 * time.Millisecond}
	server := grafana.start(t)

	cfg := apiConfig(server.URL, "tok")
	cfg.Timeout = 20 * time.Millisecond

	_, err := newAPISource(cfg).Dashboards()
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "/api/search") {
		t.Errorf("error = %q, want it to name the failing call", err)
	}
}

// An error must never carry the credential.
func TestAPISourceErrorsDoNotLeakTheToken(t *testing.T) {
	const token = "glsa-supersecret-value"

	grafana := &grafanaServer{
		search:     `{"message":"Unauthorized"}`,
		status:     map[string]int{"/api/search": http.StatusUnauthorized},
		dashboards: map[string]string{"/api/search": `{"message":"Unauthorized"}`},
	}
	server := grafana.start(t)

	_, err := newAPISource(apiConfig(server.URL, token)).Dashboards()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error leaked the token: %v", err)
	}
}

func TestAPISourcePagesThroughResults(t *testing.T) {
	// A full first page forces a second request; the stub answers page 2 empty.
	hits := make([]string, grafanaSearchPageSize)
	dashboards := map[string]string{}
	for i := range hits {
		uid := fmt.Sprintf("dash%d", i)
		hits[i] = fmt.Sprintf(`{"uid":%q,"title":"D%d"}`, uid, i)
		dashboards[uid] = fmt.Sprintf(`{"dashboard":{"uid":%q,"title":"D%d"}}`, uid, i)
	}
	grafana := &grafanaServer{search: "[" + strings.Join(hits, ",") + "]", dashboards: dashboards}
	server := grafana.start(t)

	scan, err := newAPISource(apiConfig(server.URL, "tok")).Dashboards()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scan.Dashboards) != grafanaSearchPageSize {
		t.Errorf("got %d dashboards, want %d", len(scan.Dashboards), grafanaSearchPageSize)
	}

	searches := 0
	for _, path := range grafana.requests {
		if path == "/api/search" {
			searches++
		}
	}
	if searches != 2 {
		t.Errorf("made %d search requests, want 2 (a full page must be followed by another)", searches)
	}
}

// ─── Source selection and fallback ───────────────────────────────────────────

func TestFallbackSourceUsesDirectoryWhenAPIFails(t *testing.T) {
	// No server: the API call fails outright.
	cfg := GrafanaConfig{
		Mode:          GrafanaModeAPI,
		URL:           "http://127.0.0.1:1",
		Token:         "tok",
		DashboardsDir: "testdata/dashboards",
		Timeout:       200 * time.Millisecond,
	}

	source, err := NewDashboardSource(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	scan, err := source.Dashboards()
	if err != nil {
		t.Fatalf("the directory should have rescued the scan: %v", err)
	}
	if len(scan.Dashboards) != 2 {
		t.Errorf("got %d dashboards, want the 2 fixtures", len(scan.Dashboards))
	}
	// The fallback must be visible, not passed off as a normal API scan.
	if scan.Mode != GrafanaModeAPIFallbackDir {
		t.Errorf("Mode = %q, want %q", scan.Mode, GrafanaModeAPIFallbackDir)
	}
	if len(scan.Warnings) == 0 || !strings.Contains(scan.Warnings[0], "grafana api unavailable") {
		t.Errorf("warnings = %v, want one explaining the fallback", scan.Warnings)
	}
	// A fallback is not a lost dashboard.
	if scan.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", scan.Skipped)
	}
}

func TestFallbackSourceReportsBothFailures(t *testing.T) {
	cfg := GrafanaConfig{
		Mode:          GrafanaModeAPI,
		URL:           "http://127.0.0.1:1",
		Token:         "tok",
		DashboardsDir: filepath.Join(t.TempDir(), "missing"),
		Timeout:       200 * time.Millisecond,
	}

	source, err := NewDashboardSource(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err = source.Dashboards(); err == nil {
		t.Fatal("expected an error when both sources fail, got nil")
	}
	if !strings.Contains(err.Error(), "grafana api failed") || !strings.Contains(err.Error(), "directory failed too") {
		t.Errorf("error = %q, want it to name both failures", err)
	}
}

func TestNewDashboardSourceSelection(t *testing.T) {
	tests := []struct {
		name    string
		cfg     GrafanaConfig
		want    string
		wantErr bool
	}{
		{
			name: "api alone",
			cfg:  GrafanaConfig{Mode: GrafanaModeAPI, URL: "https://g", Token: "t"},
			want: "models.apiSource",
		},
		{
			name: "api with a directory becomes a fallback pair",
			cfg:  GrafanaConfig{Mode: GrafanaModeAPI, URL: "https://g", Token: "t", DashboardsDir: "/dash"},
			want: "models.fallbackSource",
		},
		{
			name: "directory alone",
			cfg:  GrafanaConfig{Mode: GrafanaModeDir, DashboardsDir: "/dash"},
			want: "models.dirSource",
		},
		{
			name:    "disabled has no source",
			cfg:     GrafanaConfig{Mode: GrafanaModeDisabled},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, err := NewDashboardSource(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := fmt.Sprintf("%T", source); got != tt.want {
				t.Errorf("source type = %s, want %s", got, tt.want)
			}
		})
	}
}
