package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fulgerX2007/clickhouse-schemaflow-visualizer/models"
	"github.com/gin-gonic/gin"
)

// newTestRouter builds a router with no ClickHouse client. Every Grafana status
// path is reachable without a database, which is the point of keeping the status
// derived from configuration alone.
func newTestRouter(index *models.GrafanaIndex) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewHandler(nil, models.Config{}, index).RegisterRoutes(router)
	return router
}

// disabledIndex is what the handlers hold when no dashboard source is set.
func disabledIndex() *models.GrafanaIndex {
	return models.NewGrafanaIndex(models.GrafanaConfig{Mode: models.GrafanaModeDisabled}, nil, nil, "")
}

func getJSON(t *testing.T, router *gin.Engine, path string) (int, map[string]any) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	body := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s response %q: %v", path, rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestGetGrafanaStatusDisabled(t *testing.T) {
	router := newTestRouter(disabledIndex())

	code, body := getJSON(t, router, "/api/grafana/status")

	// A switched-off integration is not a server fault: 200 with a state field,
	// so the frontend has one branch instead of an error path.
	if code != http.StatusOK {
		t.Errorf("status = %d, want %d", code, http.StatusOK)
	}
	if body["state"] != string(models.GrafanaStateDisabled) {
		t.Errorf("state = %v, want %q", body["state"], models.GrafanaStateDisabled)
	}
	if body["reason"] != "not configured" {
		t.Errorf("reason = %v, want %q", body["reason"], "not configured")
	}
	// mode is omitted when there is no source at all.
	if _, ok := body["mode"]; ok {
		t.Errorf("mode should be omitted when disabled, got %v", body["mode"])
	}
}

func TestGetGrafanaStatusConfigured(t *testing.T) {
	cfg := models.GrafanaConfig{Mode: models.GrafanaModeDir, DashboardsDir: "/dash"}
	router := newTestRouter(models.NewGrafanaIndex(cfg, nil, nil, ""))

	code, body := getJSON(t, router, "/api/grafana/status")

	if code != http.StatusOK {
		t.Errorf("status = %d, want %d", code, http.StatusOK)
	}
	// No scan has run yet, and "scanning" must not be mistaken for "nothing used".
	if body["state"] != string(models.GrafanaStateScanning) {
		t.Errorf("state = %v, want %q", body["state"], models.GrafanaStateScanning)
	}
	if body["mode"] != string(models.GrafanaModeDir) {
		t.Errorf("mode = %v, want %q", body["mode"], models.GrafanaModeDir)
	}
}

func TestGetGrafanaStatusDoesNotLeakToken(t *testing.T) {
	const token = "glsa-supersecret-value"

	cfg := models.GrafanaConfig{Mode: models.GrafanaModeAPI, URL: "https://grafana.example", Token: token}
	router := newTestRouter(models.NewGrafanaIndex(cfg, nil, nil, ""))

	req := httptest.NewRequest(http.MethodGet, "/api/grafana/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), token) {
		t.Errorf("status response leaked the token: %s", rec.Body.String())
	}
}

// The six pre-existing routes must keep answering exactly as before. This asserts
// the contract that matters without a ClickHouse connection: the routes are still
// registered, and the parameter validation still fires before any query is
// attempted.
func TestExistingRoutesStillRegistered(t *testing.T) {
	router := newTestRouter(disabledIndex())

	registered := map[string]bool{}
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}

	want := []string{
		"GET /api/connection",
		"GET /api/databases",
		"GET /api/columns",
		"GET /api/dataflow/:database/:table",
		"GET /api/relationships/:database/:table",
		"GET /api/table/:database/:table",
		"GET /api/grafana/status",
	}
	for _, route := range want {
		if !registered[route] {
			t.Errorf("route %q is not registered", route)
		}
	}
	if len(router.Routes()) != len(want) {
		t.Errorf("route count = %d, want %d: %v", len(router.Routes()), len(want), router.Routes())
	}
}

func TestGetConnectionNeverExposesThePassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	cfg := models.Config{Host: "ch.example", Port: 9000, User: "reader", Password: "hunter2", Database: "default"}
	NewHandler(nil, cfg, disabledIndex()).RegisterRoutes(router)

	code, body := getJSON(t, router, "/api/connection")

	if code != http.StatusOK {
		t.Errorf("status = %d, want %d", code, http.StatusOK)
	}
	if _, ok := body["password"]; ok {
		t.Error("connection payload exposed the password")
	}
	if body["host"] != "ch.example" {
		t.Errorf("host = %v, want %q", body["host"], "ch.example")
	}
}
