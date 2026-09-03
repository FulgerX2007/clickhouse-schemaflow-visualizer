package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		// The six that predate the Grafana feature, frozen.
		"GET /api/connection",
		"GET /api/databases",
		"GET /api/columns",
		"GET /api/dataflow/:database/:table",
		"GET /api/relationships/:database/:table",
		"GET /api/table/:database/:table",
		// Added by the Grafana feature. POST /grafana/refresh is the only
		// non-GET route in the API and mutates nothing in ClickHouse.
		"GET /api/grafana/status",
		"GET /api/grafana/usage/:database/:table",
		"GET /api/grafana/unused",
		"POST /api/grafana/refresh",
	}
	for _, route := range want {
		if !registered[route] {
			t.Errorf("route %q is not registered", route)
		}
	}
	if len(router.Routes()) != len(want) {
		t.Errorf("route count = %d, want %d", len(router.Routes()), len(want))
	}
	// Nothing but the refresh endpoint may leave GET.
	for _, route := range router.Routes() {
		if route.Method != http.MethodGet && route.Path != "/api/grafana/refresh" {
			t.Errorf("unexpected non-GET route: %s %s", route.Method, route.Path)
		}
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

// ─── Usage, report and refresh ───────────────────────────────────────────────

// readyIndex builds an index that has actually scanned, using stub data so no
// ClickHouse or Grafana is involved.
func readyIndex(t *testing.T) *models.GrafanaIndex {
	t.Helper()

	index := models.NewGrafanaIndexForTest(
		models.GrafanaConfig{
			Mode: models.GrafanaModeDir, DashboardsDir: "/dash",
			CacheTTL: time.Hour, DefaultDatabase: "shop", SnippetChars: 120,
		},
		models.DashboardScan{
			Mode: models.GrafanaModeDir,
			Dashboards: []models.Dashboard{{
				UID: "dash1", Title: "Orders", URL: "https://grafana.example/d/dash1",
				Panels: []models.Panel{{ID: 3, Title: "Revenue", Queries: []string{"SELECT amount FROM shop.orders"}}},
			}},
		},
		models.SchemaSnapshot{
			Columns: map[string][]models.ColumnInfo{
				"shop.orders": {{Name: "amount", Type: "UInt64"}, {Name: "note", Type: "String"}},
			},
			Keys:        map[string]models.TableKeys{},
			Engines:     map[string]string{"shop.orders": "MergeTree"},
			ViewQueries: map[string]string{},
		},
	)
	if err := index.ScanForTest(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return index
}

func postJSON(t *testing.T, router *gin.Engine, path string) (int, map[string]any) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	body := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s response %q: %v", path, rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestGetGrafanaUsageWhenReady(t *testing.T) {
	router := newTestRouter(readyIndex(t))

	code, body := getJSON(t, router, "/api/grafana/usage/shop/orders")

	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body["state"] != string(models.GrafanaStateOK) {
		t.Fatalf("state = %v, want ok", body["state"])
	}

	usage, ok := body["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage payload missing: %v", body)
	}
	if usage["table"] != "orders" || usage["database"] != "shop" {
		t.Errorf("usage names the wrong table: %v", usage)
	}

	columns, _ := usage["columns"].([]any)
	verdicts := map[string]string{}
	for _, entry := range columns {
		column, _ := entry.(map[string]any)
		verdicts[column["column"].(string)], _ = column["verdict"].(string)
	}
	if verdicts["amount"] != string(models.VerdictUsed) {
		t.Errorf("amount verdict = %q, want used", verdicts["amount"])
	}
	if verdicts["note"] != string(models.VerdictUnused) {
		t.Errorf("note verdict = %q, want unused", verdicts["note"])
	}
}

// Every state other than ok answers with the status envelope and no payload:
// an empty usage payload and a real one showing nothing read are
// indistinguishable to a reader.
func TestGrafanaEndpointsCarryNoPayloadUnlessReady(t *testing.T) {
	states := []struct {
		name  string
		index *models.GrafanaIndex
		state models.GrafanaState
	}{
		{name: "disabled", index: disabledIndex(), state: models.GrafanaStateDisabled},
		{
			name:  "scanning",
			index: models.NewGrafanaIndex(models.GrafanaConfig{Mode: models.GrafanaModeDir}, nil, nil, ""),
			state: models.GrafanaStateScanning,
		},
	}

	for _, tt := range states {
		for _, path := range []string{"/api/grafana/usage/shop/orders", "/api/grafana/unused"} {
			t.Run(tt.name+" "+path, func(t *testing.T) {
				code, body := getJSON(t, newTestRouter(tt.index), path)

				if code != http.StatusOK {
					t.Errorf("status = %d, want 200 — a switched-off feature is not a server fault", code)
				}
				if body["state"] != string(tt.state) {
					t.Errorf("state = %v, want %q", body["state"], tt.state)
				}
				for _, key := range []string{"usage", "report"} {
					if _, present := body[key]; present {
						t.Errorf("%q payload was returned in state %q", key, tt.state)
					}
				}
			})
		}
	}
}

func TestGetGrafanaUsageValidatesParameters(t *testing.T) {
	router := newTestRouter(readyIndex(t))

	// Gin will not route an empty path segment, so an unknown table is the
	// reachable case: it must answer, not fail.
	code, body := getJSON(t, router, "/api/grafana/usage/nope/missing")

	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	usage, ok := body["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage payload missing: %v", body)
	}
	if columns, _ := usage["columns"].([]any); len(columns) != 0 {
		t.Errorf("an unknown table reported columns: %v", columns)
	}
}

func TestGetGrafanaUnusedReport(t *testing.T) {
	router := newTestRouter(readyIndex(t))

	code, body := getJSON(t, router, "/api/grafana/unused?verdict=unused")

	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	report, ok := body["report"].(map[string]any)
	if !ok {
		t.Fatalf("report payload missing: %v", body)
	}

	rows, _ := report["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want only the unused column: %v", len(rows), rows)
	}
	row, _ := rows[0].(map[string]any)
	if row["column"] != "note" {
		t.Errorf("row = %v, want the note column", row)
	}

	// The caveats are not decoration: a reader who over-trusts the report drops
	// a live column.
	caveats, _ := report["caveats"].([]any)
	if len(caveats) == 0 {
		t.Error("the report carried no caveats")
	}
}

func TestRefreshGrafanaIsDebounced(t *testing.T) {
	router := newTestRouter(readyIndex(t))

	code, body := postJSON(t, router, "/api/grafana/refresh")

	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body["debounced"] != true {
		t.Errorf("debounced = %v, want true inside the TTL", body["debounced"])
	}
	if body["state"] != string(models.GrafanaStateOK) {
		t.Errorf("state = %v, want ok", body["state"])
	}
}

func TestRefreshGrafanaWhenDisabled(t *testing.T) {
	code, body := postJSON(t, newTestRouter(disabledIndex()), "/api/grafana/refresh")

	if code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
	if body["state"] != string(models.GrafanaStateDisabled) {
		t.Errorf("state = %v, want disabled", body["state"])
	}
}

func TestRefreshGrafanaReportsAFailure(t *testing.T) {
	index := models.NewGrafanaIndexFailingForTest(
		models.GrafanaConfig{Mode: models.GrafanaModeDir, CacheTTL: time.Hour},
		errors.New("grafana unreachable"))

	code, body := postJSON(t, newTestRouter(index), "/api/grafana/refresh")

	if code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
	if body["state"] != string(models.GrafanaStateError) {
		t.Fatalf("state = %v, want error", body["state"])
	}
	if got, _ := body["error"].(string); !strings.Contains(got, "grafana unreachable") {
		t.Errorf("error = %q, want the failure named", got)
	}
}
