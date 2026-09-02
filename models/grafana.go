package models

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
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
	// Mode names the source that actually produced this scan, which is not always
	// the configured one: an API failure that falls back to disk reports
	// api-fallback-dir, so status never misstates provenance.
	Mode       GrafanaMode
	Dashboards []Dashboard
	Skipped    int
	Warnings   []string
}

// maxScanWarnings bounds what a single bad directory can accumulate; the count
// keeps rising after the list stops.
const maxScanWarnings = 50

func (s DashboardScan) withSkip(format string, args ...any) DashboardScan {
	s.Skipped++
	return s.withWarning(format, args...)
}

// withWarning records a note that did not cost a dashboard, such as a fallback
// having fired.
func (s DashboardScan) withWarning(format string, args ...any) DashboardScan {
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

// rawDashboard is the subset of the dashboard object this pass reads.
type rawDashboard struct {
	UID        string     `json:"uid"`
	Title      string     `json:"title"`
	Panels     []rawPanel `json:"panels"`
	Templating struct {
		List []rawVariable `json:"list"`
	} `json:"templating"`
}

// rawPanel is one panel. Panels nests the children of a collapsed row, which a
// flat walk over the top-level list would silently miss.
type rawPanel struct {
	ID         int             `json:"id"`
	Type       string          `json:"type"`
	Title      string          `json:"title"`
	Datasource json.RawMessage `json:"datasource"`
	Targets    []rawTarget     `json:"targets"`
	Panels     []rawPanel      `json:"panels"`
}

// rawTarget is one query on a panel. PanelID is set only on targets that reuse
// another panel's data; Grafana panel ids start at 1, so 0 means absent.
type rawTarget struct {
	RefID      string          `json:"refId"`
	Datasource json.RawMessage `json:"datasource"`
	RawSQL     string          `json:"rawSql"`
	PanelID    int             `json:"panelId"`
}

// rawVariable is one templating entry.
type rawVariable struct {
	Name    string          `json:"name"`
	Type    string          `json:"type"`
	Query   json.RawMessage `json:"query"`
	Current json.RawMessage `json:"current"`
	Options []struct {
		Value json.RawMessage `json:"value"`
	} `json:"options"`
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

	scan := DashboardScan{Mode: GrafanaModeDir}
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

		dashboard, contentWarnings, loadErr := s.load(path)
		if loadErr != nil {
			scan = scan.withSkip("%s: %v", path, loadErr)
			return nil
		}
		if first, duplicate := seen[dashboard.UID]; duplicate {
			scan = scan.withSkip("%s: duplicate dashboard uid %q, already loaded from %s", path, dashboard.UID, first)
			return nil
		}

		for _, warning := range contentWarnings {
			scan = scan.withWarning("%s: %s", dashboard.UID, warning)
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

// load reads one dashboard file in either envelope shape, returning any
// extraction warnings alongside it.
func (s dirSource) load(path string) (Dashboard, []string, error) {
	content, err := os.ReadFile(path) //nolint:gosec // operator-configured dashboard directory
	if err != nil {
		return Dashboard{}, nil, err
	}

	envelope := dashboardEnvelope{}
	if err := json.Unmarshal(content, &envelope); err != nil {
		return Dashboard{}, nil, fmt.Errorf("not valid JSON: %w", err)
	}

	body := envelope.Dashboard
	if len(body) == 0 {
		body = content // bare dashboard object
	}

	raw := rawDashboard{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Dashboard{}, nil, fmt.Errorf("not a dashboard object: %w", err)
	}

	folder := envelope.Meta.FolderTitle
	if folder == "" {
		// Provisioned trees carry the folder in the layout rather than the file.
		if rel, relErr := filepath.Rel(s.dir, filepath.Dir(path)); relErr == nil && rel != "." {
			folder = filepath.ToSlash(rel)
		}
	}

	dashboard, err := newDashboard(raw.UID, raw.Title, folder, "dir", s.baseURL)
	if err != nil {
		return Dashboard{}, nil, err
	}

	panels, variables, warnings := extractContent(raw)
	dashboard.Panels = panels
	dashboard.Variables = variables
	return dashboard, warnings, nil
}

// ─── Grafana HTTP API source ─────────────────────────────────────────────────

const (
	// grafanaSearchPageSize is well under Grafana's 5000 cap and keeps each
	// response small enough to decode without buffering a whole instance.
	grafanaSearchPageSize = 500
	// grafanaMaxSearchPages bounds the paging loop against a server that keeps
	// answering with full pages.
	grafanaMaxSearchPages = 200
	// grafanaMaxResponseBytes caps what one response may cost us in memory.
	grafanaMaxResponseBytes = 32 << 20
)

// apiSource reads dashboards from the Grafana HTTP API.
type apiSource struct {
	baseURL string
	token   string
	client  http.Client
}

func newAPISource(cfg GrafanaConfig) apiSource {
	transport := http.DefaultTransport
	if cfg.SkipVerify {
		// Mirrors CLICKHOUSE_SKIP_VERIFY: opt-in, never the default, for internal
		// instances behind a private CA.
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // opt-in via GRAFANA_SKIP_VERIFY
		}
	}

	return apiSource{
		baseURL: strings.TrimRight(strings.TrimSpace(cfg.URL), "/"),
		token:   cfg.Token,
		client:  http.Client{Timeout: cfg.Timeout, Transport: transport},
	}
}

// searchHit is one entry of GET /api/search.
type searchHit struct {
	UID         string `json:"uid"`
	Title       string `json:"title"`
	FolderTitle string `json:"folderTitle"`
}

// Dashboards lists dashboards and fetches each one.
//
// A failure of the listing call is fatal to the scan, because "the search
// returned nothing" and "every column is unused" are indistinguishable
// downstream. A single dashboard that fails to load is skipped and counted —
// unless every one of them fails, which is a systemic problem wearing the
// costume of an empty Grafana.
func (s apiSource) Dashboards() (DashboardScan, error) {
	scan := DashboardScan{Mode: GrafanaModeAPI}

	hits, err := s.search()
	if err != nil {
		return scan, err
	}

	for _, hit := range hits {
		dashboard, contentWarnings, dashErr := s.dashboard(hit)
		if dashErr != nil {
			scan = scan.withSkip("dashboard %q: %v", hit.UID, dashErr)
			continue
		}
		for _, warning := range contentWarnings {
			scan = scan.withWarning("%s: %s", dashboard.UID, warning)
		}
		scan.Dashboards = append(scan.Dashboards, dashboard)
	}

	if len(scan.Dashboards) == 0 && scan.Skipped > 0 {
		return scan, fmt.Errorf("all %d dashboards failed to load, first: %s", scan.Skipped, scan.Warnings[0])
	}

	return scan, nil
}

// search pages through the dashboard listing.
func (s apiSource) search() ([]searchHit, error) {
	all := []searchHit{}

	for page := 1; page <= grafanaMaxSearchPages; page++ {
		query := url.Values{}
		query.Set("type", "dash-db")
		query.Set("limit", strconv.Itoa(grafanaSearchPageSize))
		query.Set("page", strconv.Itoa(page))

		body, err := s.get("/api/search?" + query.Encode())
		if err != nil {
			return nil, err
		}

		hits := []searchHit{}
		if err := json.Unmarshal(body, &hits); err != nil {
			return nil, fmt.Errorf("decode search page %d: %w", page, err)
		}

		all = append(all, hits...)
		if len(hits) < grafanaSearchPageSize {
			return all, nil
		}
	}

	return all, nil
}

// dashboard fetches one dashboard and reduces it to the envelope.
func (s apiSource) dashboard(hit searchHit) (Dashboard, []string, error) {
	if !grafanaUIDPattern.MatchString(hit.UID) {
		return Dashboard{}, nil, fmt.Errorf("search returned an invalid uid %q", hit.UID)
	}

	body, err := s.get("/api/dashboards/uid/" + hit.UID)
	if err != nil {
		if isV2SchemaRefusal(body) {
			// Grafana cannot convert these for us, and guessing at the v2 shape
			// would be worse than declaring the gap.
			return Dashboard{}, nil, fmt.Errorf("v2-schema dashboard, not readable through the classic API")
		}
		return Dashboard{}, nil, err
	}

	envelope := dashboardEnvelope{}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Dashboard{}, nil, fmt.Errorf("not valid JSON: %w", err)
	}
	if len(envelope.Dashboard) == 0 {
		return Dashboard{}, nil, fmt.Errorf("response carried no dashboard object")
	}

	raw := rawDashboard{}
	if err := json.Unmarshal(envelope.Dashboard, &raw); err != nil {
		return Dashboard{}, nil, fmt.Errorf("not a dashboard object: %w", err)
	}

	// The search hit is the fallback for both, since a dashboard object may omit
	// its own uid or title.
	uid := raw.UID
	if uid == "" {
		uid = hit.UID
	}
	title := raw.Title
	if strings.TrimSpace(title) == "" {
		title = hit.Title
	}
	folder := envelope.Meta.FolderTitle
	if folder == "" {
		folder = hit.FolderTitle
	}

	dashboard, err := newDashboard(uid, title, folder, "api", s.baseURL)
	if err != nil {
		return Dashboard{}, nil, err
	}

	panels, variables, warnings := extractContent(raw)
	dashboard.Panels = panels
	dashboard.Variables = variables
	return dashboard, warnings, nil
}

// get performs an authenticated GET. The body is returned even on a non-200 so
// the caller can classify the refusal; the token never appears in an error.
func (s apiSource) get(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		// url.Error stringifies the request URL, which carries no credentials.
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, grafanaMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("GET %s: %s", path, grafanaHTTPError(resp.StatusCode, body))
	}
	return body, nil
}

// grafanaHTTPError renders a status line plus Grafana's own message when it sent
// one, truncated so a HTML error page cannot flood a log line.
func grafanaHTTPError(status int, body []byte) string {
	message := grafanaErrorMessage(body)
	if message == "" {
		return http.StatusText(status)
	}
	if len(message) > 200 {
		message = message[:200] + "…"
	}
	return fmt.Sprintf("%s: %s", http.StatusText(status), message)
}

func grafanaErrorMessage(body []byte) string {
	payload := struct {
		Message string `json:"message"`
	}{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Message)
}

// isV2SchemaRefusal recognises Grafana declining to serve a v2-schema dashboard
// through the classic API.
func isV2SchemaRefusal(body []byte) bool {
	return strings.Contains(strings.ToLower(grafanaErrorMessage(body)), "dashboard api version not supported")
}

// ─── Source selection ────────────────────────────────────────────────────────

// fallbackSource tries the API and drops to the directory when it fails, so a
// Grafana outage degrades to the provisioned copy on disk instead of reporting
// that nothing is used.
type fallbackSource struct {
	primary  DashboardSource
	fallback DashboardSource
}

func (s fallbackSource) Dashboards() (DashboardScan, error) {
	scan, err := s.primary.Dashboards()
	if err == nil {
		return scan, nil
	}

	fallbackScan, fallbackErr := s.fallback.Dashboards()
	if fallbackErr != nil {
		return DashboardScan{}, fmt.Errorf("grafana api failed (%v) and the dashboards directory failed too: %w", err, fallbackErr)
	}

	// Say so rather than passing the result off as a normal API scan.
	fallbackScan.Mode = GrafanaModeAPIFallbackDir
	return fallbackScan.withWarning("grafana api unavailable, read dashboards from disk instead: %v", err), nil
}

// NewDashboardSource builds the source the configuration calls for.
func NewDashboardSource(cfg GrafanaConfig) (DashboardSource, error) {
	switch cfg.Mode {
	case GrafanaModeAPI:
		api := newAPISource(cfg)
		if cfg.DashboardsDir != "" {
			return fallbackSource{primary: api, fallback: newDirSource(cfg.DashboardsDir, cfg.URL)}, nil
		}
		return api, nil
	case GrafanaModeDir:
		return newDirSource(cfg.DashboardsDir, cfg.URL), nil
	default:
		return nil, fmt.Errorf("no grafana dashboard source is configured")
	}
}

// ─── Panel and variable extraction ───────────────────────────────────────────

// dashboardDatasourceUID marks a target that reuses another panel's data.
const dashboardDatasourceUID = "-- Dashboard --"

// nonSQLDatasources are datasource types that never carry ClickHouse SQL.
//
// The filter is a denylist rather than an allowlist of
// grafana-clickhouse-datasource, and the asymmetry is deliberate: wrongly
// excluding a query makes the columns it reads look unused, which is the one
// error this feature must not make, while wrongly including one only invents
// usage that keeps a column alive. Anything unrecognised that carries rawSql is
// therefore kept.
var nonSQLDatasources = map[string]bool{
	"yesoreyeram-infinity-datasource": true,
	"prometheus":                      true,
	"loki":                            true,
	"elasticsearch":                   true,
	"influxdb":                        true,
	"graphite":                        true,
	"jaeger":                          true,
	"tempo":                           true,
	"testdata":                        true,
	"grafana-testdata-datasource":     true,
	"grafana":                         true,
	// Other SQL dialects: their rawSql would resolve against same-named tables
	// and invent usage in a different database entirely.
	"postgres":                      true,
	"mysql":                         true,
	"mssql":                         true,
	"grafana-postgresql-datasource": true,
	"grafana-mysql-datasource":      true,
}

// datasourceRef is a panel or target datasource in either the modern object form
// or the legacy bare-string form.
type datasourceRef struct {
	Type string
	UID  string
}

func parseDatasourceRef(raw json.RawMessage) datasourceRef {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return datasourceRef{}
	}

	object := struct {
		Type string `json:"type"`
		UID  string `json:"uid"`
	}{}
	if err := json.Unmarshal(raw, &object); err == nil {
		return datasourceRef{Type: object.Type, UID: object.UID}
	}

	// Legacy dashboards name the datasource directly.
	name := ""
	if err := json.Unmarshal(raw, &name); err == nil {
		return datasourceRef{UID: name}
	}
	return datasourceRef{}
}

func (d datasourceRef) isDashboardReference() bool {
	return d.UID == dashboardDatasourceUID
}

func (d datasourceRef) carriesSQL() bool {
	return !nonSQLDatasources[d.Type]
}

// flattenPanels walks the panel tree. A row is a container, not a panel with
// queries, so it contributes only its children.
func flattenPanels(panels []rawPanel) []rawPanel {
	flat := []rawPanel{}
	for _, panel := range panels {
		if panel.Type != "row" {
			flat = append(flat, panel)
		}
		if len(panel.Panels) > 0 {
			flat = append(flat, flattenPanels(panel.Panels)...)
		}
	}
	return flat
}

// extractContent pulls the queries and variables out of a decoded dashboard.
// The warnings it returns name panels whose data source could not be resolved,
// so those become "unknown" downstream rather than silently contributing no
// usage at all.
func extractContent(raw rawDashboard) ([]Panel, []TemplateVariable, []string) {
	flat := flattenPanels(raw.Panels)

	byID := make(map[int]rawPanel, len(flat))
	for _, panel := range flat {
		byID[panel.ID] = panel
	}

	panels := []Panel{}
	warnings := []string{}

	for _, raw := range flat {
		panel := Panel{ID: raw.ID, Title: raw.Title, Type: raw.Type}

		for _, target := range raw.Targets {
			source := parseDatasourceRef(target.Datasource)
			if source == (datasourceRef{}) {
				// A target without its own datasource inherits the panel's.
				source = parseDatasourceRef(raw.Datasource)
			}

			if source.isDashboardReference() {
				panel.RefPanelID = target.PanelID
				referenced, ok := byID[target.PanelID]
				if !ok {
					warnings = append(warnings, fmt.Sprintf(
						"panel %d reuses panel %d, which is not in this dashboard", raw.ID, target.PanelID))
					continue
				}
				// The referenced panel's queries are this panel's queries too: it
				// really does display those columns.
				queries := panelQueries(referenced)
				if len(queries) == 0 {
					warnings = append(warnings, fmt.Sprintf(
						"panel %d reuses panel %d, which has no readable query", raw.ID, target.PanelID))
				}
				panel.Queries = append(panel.Queries, queries...)
				continue
			}

			if sql := targetSQL(source, target); sql != "" {
				panel.Queries = append(panel.Queries, sql)
			}
		}

		panels = append(panels, panel)
	}

	return panels, extractVariables(raw.Templating.List), warnings
}

// panelQueries returns a panel's own SQL, ignoring any reuse targets so a chain
// of references cannot recurse.
func panelQueries(panel rawPanel) []string {
	queries := []string{}
	for _, target := range panel.Targets {
		source := parseDatasourceRef(target.Datasource)
		if source == (datasourceRef{}) {
			source = parseDatasourceRef(panel.Datasource)
		}
		if source.isDashboardReference() {
			continue
		}
		if sql := targetSQL(source, target); sql != "" {
			queries = append(queries, sql)
		}
	}
	return queries
}

// targetSQL returns the target's SQL when its datasource could plausibly be the
// ClickHouse this app is connected to. Hidden targets are included: a hidden
// query still names the columns someone relies on.
func targetSQL(source datasourceRef, target rawTarget) string {
	sql := strings.TrimSpace(target.RawSQL)
	if sql == "" || !source.carriesSQL() {
		return ""
	}
	return sql
}

// listValueTypes hold a comma-separated value list in their query field rather
// than a query.
var listValueTypes = map[string]bool{"custom": true, "interval": true}

// extractVariables reduces the templating list to names, SQL and known values.
// Values are what make ${var} inside a table name resolvable, which is the
// single largest source of false "unused" verdicts if it is missed.
func extractVariables(raw []rawVariable) []TemplateVariable {
	variables := []TemplateVariable{}

	for _, entry := range raw {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}

		variable := TemplateVariable{Name: name}
		query := decodeVariableQuery(entry.Query)

		switch {
		case listValueTypes[entry.Type]:
			variable.Values = splitCommaList(query)
		case entry.Type == "query" || (entry.Type == "" && looksLikeSQL(query)):
			variable.Query = query
		}

		for _, option := range entry.Options {
			variable.Values = append(variable.Values, decodeStringOrList(option.Value)...)
		}
		variable.Values = append(variable.Values, decodeCurrentValue(entry.Current)...)
		variable.Values = dedupeNonEmpty(variable.Values)

		variables = append(variables, variable)
	}

	return variables
}

// decodeVariableQuery handles both the bare-string form and the object form
// ({"query": "…"}) that newer datasources write.
func decodeVariableQuery(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	text := ""
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}

	object := struct {
		Query  string `json:"query"`
		RawSQL string `json:"rawSql"`
	}{}
	if err := json.Unmarshal(raw, &object); err == nil {
		if object.RawSQL != "" {
			return strings.TrimSpace(object.RawSQL)
		}
		return strings.TrimSpace(object.Query)
	}
	return ""
}

// decodeCurrentValue reads {"value": …}, which is a string for a single-value
// variable and a list for a multi-value one.
func decodeCurrentValue(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	current := struct {
		Value json.RawMessage `json:"value"`
	}{}
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil
	}
	return decodeStringOrList(current.Value)
}

func decodeStringOrList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}

	text := ""
	if err := json.Unmarshal(raw, &text); err == nil {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			return []string{trimmed}
		}
		return nil
	}

	list := []string{}
	if err := json.Unmarshal(raw, &list); err == nil {
		return dedupeNonEmpty(list)
	}
	return nil
}

func splitCommaList(value string) []string {
	parts := []string{}
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}

func looksLikeSQL(value string) bool {
	return strings.Contains(strings.ToLower(value), "select ")
}

func dedupeNonEmpty(values []string) []string {
	seen := map[string]bool{}
	unique := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		// "$__all" is Grafana's own sentinel, not a value any table name uses.
		if trimmed == "" || trimmed == "$__all" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		unique = append(unique, trimmed)
	}
	return unique
}
