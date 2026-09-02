package models

import (
	"encoding/json"
	"errors"
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
