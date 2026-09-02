package models

import (
	"fmt"
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
