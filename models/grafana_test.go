package models

import (
	"encoding/json"
	"errors"
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
