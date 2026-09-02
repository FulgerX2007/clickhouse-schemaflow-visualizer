package models

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// GrafanaMode identifies where dashboards are read from. The configured mode is
// decided once at startup; api-fallback-dir is only ever reported at runtime,
// when an API scan failed and the directory source took over.
type GrafanaMode string

const (
	GrafanaModeDisabled       GrafanaMode = "disabled"
	GrafanaModeAPI            GrafanaMode = "api"
	GrafanaModeDir            GrafanaMode = "dir"
	GrafanaModeAPIFallbackDir GrafanaMode = "api-fallback-dir"
)

// GrafanaState is the lifecycle state reported by GET /api/grafana/status. The
// frontend branches on it, and every state other than ok means no column may be
// presented as unused: a misconfigured or still-scanning Grafana looks exactly
// like a Grafana in which nothing is used, and the two must never be confused.
type GrafanaState string

const (
	GrafanaStateDisabled GrafanaState = "disabled"
	GrafanaStateScanning GrafanaState = "scanning"
	GrafanaStateError    GrafanaState = "error"
	GrafanaStateOK       GrafanaState = "ok"
)

const (
	defaultGrafanaTimeout      = 30 * time.Second
	defaultGrafanaCacheTTL     = 15 * time.Minute
	defaultGrafanaSnippetChars = 200
)

// GrafanaConfig holds the dashboard-source settings. It is built in main.go
// from the environment, like models.Config, rather than in the dead config/
// package.
type GrafanaConfig struct {
	Mode            GrafanaMode
	URL             string
	Token           string
	DashboardsDir   string
	SkipVerify      bool
	Timeout         time.Duration
	CacheTTL        time.Duration
	DefaultDatabase string
	SnippetChars    int
}

// Enabled reports whether a dashboard source is usable.
func (g GrafanaConfig) Enabled() bool {
	return g.Mode != GrafanaModeDisabled
}

// GrafanaStatus is the payload of GET /api/grafana/status. It deliberately
// carries no token field: the status endpoint is the one place where the
// service-account credential would be easiest to leak by accident.
type GrafanaStatus struct {
	State  GrafanaState `json:"state"`
	Mode   GrafanaMode  `json:"mode,omitempty"`
	Reason string       `json:"reason,omitempty"`
	Error  string       `json:"error,omitempty"`
}

// LoadGrafanaConfig resolves the dashboard source from the environment. getenv
// is injected so mode resolution is testable without mutating the process
// environment. clickhouseDatabase supplies the default for unqualified table
// references, which GRAFANA_DEFAULT_DATABASE can override.
//
// A returned error means the configuration is wrong, not that the process
// should stop: the caller logs it and keeps serving. When a usable fallback
// exists (a dashboards directory alongside a half-configured API), the returned
// config still names that fallback mode, so a partial misconfiguration degrades
// rather than disabling the feature outright.
func LoadGrafanaConfig(getenv func(string) string, clickhouseDatabase string) (GrafanaConfig, error) {
	cfg := GrafanaConfig{
		Mode:            GrafanaModeDisabled,
		URL:             strings.TrimSpace(getenv("GRAFANA_URL")),
		Token:           strings.TrimSpace(getenv("GRAFANA_TOKEN")),
		DashboardsDir:   strings.TrimSpace(getenv("GRAFANA_DASHBOARDS_DIR")),
		Timeout:         defaultGrafanaTimeout,
		CacheTTL:        defaultGrafanaCacheTTL,
		DefaultDatabase: clickhouseDatabase,
		SnippetChars:    defaultGrafanaSnippetChars,
	}

	if v := strings.TrimSpace(getenv("GRAFANA_DEFAULT_DATABASE")); v != "" {
		cfg.DefaultDatabase = v
	}

	hasURL := cfg.URL != ""
	hasToken := cfg.Token != ""
	hasDir := cfg.DashboardsDir != ""

	// Nothing configured is the common case and is not an error: the feature is
	// simply off and the rest of the app is unaffected.
	if !hasURL && !hasToken && !hasDir {
		return cfg, nil
	}

	cfg, err := parseGrafanaOptions(getenv, cfg)
	if err != nil {
		return cfg, err
	}

	switch {
	case hasURL && hasToken:
		cfg.Mode = GrafanaModeAPI
	case hasDir:
		cfg.Mode = GrafanaModeDir
	}

	// A half-configured API is reported even when a directory rescues it, so the
	// operator learns the API is not actually in use.
	switch {
	case hasURL && !hasToken:
		return cfg, fmt.Errorf("GRAFANA_URL is set without GRAFANA_TOKEN%s", grafanaFallbackNote(hasDir))
	case hasToken && !hasURL:
		return cfg, fmt.Errorf("GRAFANA_TOKEN is set without GRAFANA_URL%s", grafanaFallbackNote(hasDir))
	}

	return cfg, nil
}

// parseGrafanaOptions fills the tunables that only matter once a source is
// configured. It reports the first invalid value rather than accumulating.
func parseGrafanaOptions(getenv func(string) string, cfg GrafanaConfig) (GrafanaConfig, error) {
	if v := strings.TrimSpace(getenv("GRAFANA_SKIP_VERIFY")); v != "" {
		skip, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("GRAFANA_SKIP_VERIFY: %w", err)
		}
		cfg.SkipVerify = skip
	}

	timeout, err := grafanaDuration(getenv, "GRAFANA_TIMEOUT", defaultGrafanaTimeout)
	if err != nil {
		return cfg, err
	}
	cfg.Timeout = timeout

	ttl, err := grafanaDuration(getenv, "GRAFANA_CACHE_TTL", defaultGrafanaCacheTTL)
	if err != nil {
		return cfg, err
	}
	cfg.CacheTTL = ttl

	if v := strings.TrimSpace(getenv("GRAFANA_SNIPPET_CHARS")); v != "" {
		chars, err := strconv.Atoi(v)
		if err != nil {
			return cfg, fmt.Errorf("GRAFANA_SNIPPET_CHARS: %w", err)
		}
		if chars < 0 {
			return cfg, fmt.Errorf("GRAFANA_SNIPPET_CHARS: must not be negative, got %d", chars)
		}
		cfg.SnippetChars = chars
	}

	return cfg, nil
}

// grafanaDuration reads an optional duration, keeping the default when unset.
func grafanaDuration(getenv func(string) string, key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def, fmt.Errorf("%s: %w", key, err)
	}
	if d <= 0 {
		return def, fmt.Errorf("%s: must be positive, got %s", key, v)
	}
	return d, nil
}

func grafanaFallbackNote(hasDir bool) string {
	if hasDir {
		return "; falling back to GRAFANA_DASHBOARDS_DIR"
	}
	return ""
}

// NewGrafanaStatus derives the status from configuration alone, before any
// dashboard scan has run.
//
// TODO(task-11): replace with the live GrafanaIndex status once scanning exists.
// Until then a configured source reports "scanning", which is accurate — no scan
// has happened yet — and keeps the frontend from rendering an empty report as
// though it were a complete one.
func NewGrafanaStatus(cfg GrafanaConfig, configErr error) GrafanaStatus {
	if !cfg.Enabled() {
		if configErr != nil {
			return GrafanaStatus{State: GrafanaStateError, Error: configErr.Error()}
		}
		return GrafanaStatus{State: GrafanaStateDisabled, Reason: "not configured"}
	}

	status := GrafanaStatus{State: GrafanaStateScanning, Mode: cfg.Mode}
	if configErr != nil {
		// Usable, but not the way the operator asked for: surface why.
		status.Reason = configErr.Error()
	}
	return status
}

// ─── Dashboards ──────────────────────────────────────────────────────────────

// grafanaUIDPattern is Grafana's own uid shape. Uids reach the browser inside
// dashboard deep links, and a dashboard read off disk is no more trustworthy
// than one read from the API, so both sources validate through the same
// constructor.
var grafanaUIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Dashboard is a Grafana dashboard reduced to what column-usage analysis needs.
// Panels and Variables are filled by the extraction pass; a source only builds
// the envelope.
type Dashboard struct {
	UID       string
	Title     string
	Folder    string
	Source    string // "api" | "dir"
	URL       string // deep link, empty when no base URL is configured
	Panels    []Panel
	Variables []TemplateVariable
}

// Panel is one panel and the queries it issues.
type Panel struct {
	ID         int
	Title      string
	Type       string
	Queries    []string
	RefPanelID int // "-- Dashboard --" source panel, 0 when the panel has its own queries
}

// TemplateVariable is a dashboard variable. Values feeds ${var} expansion, which
// is what makes table names like agg_${period}_distributed resolvable.
type TemplateVariable struct {
	Name   string
	Query  string
	Values []string
}

// DashboardScan is the result of one pass over a source. Skipped and Warnings
// are carried rather than logged and forgotten: a scan that silently dropped
// half its dashboards would look exactly like a Grafana in which half the
// columns are unused.
type DashboardScan struct {
	Dashboards []Dashboard
	Skipped    int
	Warnings   []string
}

// maxScanWarnings bounds what a single bad directory can accumulate; the count
// keeps rising after the list stops.
const maxScanWarnings = 50

func (s DashboardScan) withSkip(format string, args ...any) DashboardScan {
	s.Skipped++
	if len(s.Warnings) < maxScanWarnings {
		s.Warnings = append(s.Warnings, fmt.Sprintf(format, args...))
	}
	return s
}

// DashboardSource supplies dashboards from somewhere — the Grafana API or a
// directory of JSON files. Everything downstream consumes DashboardScan and does
// not know which it got.
type DashboardSource interface {
	Dashboards() (DashboardScan, error)
}

// newDashboard builds the envelope shared by both sources, rejecting a uid that
// does not match Grafana's own shape.
func newDashboard(uid, title, folder, source, baseURL string) (Dashboard, error) {
	uid = strings.TrimSpace(uid)
	if !grafanaUIDPattern.MatchString(uid) {
		return Dashboard{}, fmt.Errorf("invalid dashboard uid %q", uid)
	}

	title = strings.TrimSpace(title)
	if title == "" {
		title = uid
	}

	dashboard := Dashboard{UID: uid, Title: title, Folder: folder, Source: source}
	if base := strings.TrimRight(strings.TrimSpace(baseURL), "/"); base != "" {
		dashboard.URL = base + "/d/" + uid
	}
	return dashboard, nil
}

// dashboardEnvelope covers the two shapes a dashboard file comes in: the bare
// dashboard object, and the {"dashboard": …, "meta": …} wrapper the API returns
// and the export button writes.
type dashboardEnvelope struct {
	Dashboard json.RawMessage `json:"dashboard"`
	Meta      struct {
		FolderTitle string `json:"folderTitle"`
	} `json:"meta"`
}

// rawDashboard is the subset of the dashboard object this pass reads. The panel
// and templating payloads are decoded by the extraction pass.
type rawDashboard struct {
	UID   string `json:"uid"`
	Title string `json:"title"`
}

// dirSource reads dashboards from a directory tree of JSON files. It needs no
// credentials, which is what makes it usable against a provisioned dashboard
// repository.
type dirSource struct {
	dir     string
	baseURL string
}

func newDirSource(dir, baseURL string) dirSource {
	return dirSource{dir: dir, baseURL: baseURL}
}

// Dashboards walks the directory tree. A missing or unreadable directory is a
// hard error — reporting zero dashboards for a mistyped path would present as
// "no dashboard uses anything", which is the one conclusion this feature must
// never reach by accident. Individual bad files are skipped and counted.
func (s dirSource) Dashboards() (DashboardScan, error) {
	info, err := os.Stat(s.dir)
	if err != nil {
		return DashboardScan{}, fmt.Errorf("grafana dashboards directory %q: %w", s.dir, err)
	}
	if !info.IsDir() {
		return DashboardScan{}, fmt.Errorf("grafana dashboards path %q is not a directory", s.dir)
	}

	scan := DashboardScan{}
	// Grafana keys dashboards by uid, so a provisioning tree that holds the same
	// dashboard in two folders still has one dashboard. Keeping both would
	// double-count every column they read and list the dashboard twice in the UI.
	// WalkDir yields lexical order, so first-wins is deterministic.
	seen := map[string]string{}

	walkErr := filepath.WalkDir(s.dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			scan = scan.withSkip("%s: %v", path, err)
			return nil
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			return nil
		}

		dashboard, loadErr := s.load(path)
		if loadErr != nil {
			scan = scan.withSkip("%s: %v", path, loadErr)
			return nil
		}
		if first, duplicate := seen[dashboard.UID]; duplicate {
			scan = scan.withSkip("%s: duplicate dashboard uid %q, already loaded from %s", path, dashboard.UID, first)
			return nil
		}

		seen[dashboard.UID] = path
		scan.Dashboards = append(scan.Dashboards, dashboard)
		return nil
	})
	if walkErr != nil {
		return scan, fmt.Errorf("walk grafana dashboards directory %q: %w", s.dir, walkErr)
	}

	return scan, nil
}

// load reads one dashboard file in either envelope shape.
func (s dirSource) load(path string) (Dashboard, error) {
	content, err := os.ReadFile(path) //nolint:gosec // operator-configured dashboard directory
	if err != nil {
		return Dashboard{}, err
	}

	envelope := dashboardEnvelope{}
	if err := json.Unmarshal(content, &envelope); err != nil {
		return Dashboard{}, fmt.Errorf("not valid JSON: %w", err)
	}

	body := envelope.Dashboard
	if len(body) == 0 {
		body = content // bare dashboard object
	}

	raw := rawDashboard{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Dashboard{}, fmt.Errorf("not a dashboard object: %w", err)
	}

	folder := envelope.Meta.FolderTitle
	if folder == "" {
		// Provisioned trees carry the folder in the layout rather than the file.
		if rel, relErr := filepath.Rel(s.dir, filepath.Dir(path)); relErr == nil && rel != "." {
			folder = filepath.ToSlash(rel)
		}
	}

	return newDashboard(raw.UID, raw.Title, folder, "dir", s.baseURL)
}
