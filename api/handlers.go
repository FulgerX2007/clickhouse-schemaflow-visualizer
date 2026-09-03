package api

import (
	"net/http"

	"github.com/fulgerX2007/clickhouse-schemaflow-visualizer/models"
	"github.com/gin-gonic/gin"
)

// Handler holds the dependencies for API handlers
type Handler struct {
	clickhouse *models.ClickHouseClient
	config     models.Config
	grafana    *models.GrafanaIndex
}

// NewHandler creates a new Handler instance. grafana may be nil, which every
// Grafana endpoint reports as "disabled" rather than as an error.
func NewHandler(clickhouse *models.ClickHouseClient, config models.Config, grafana *models.GrafanaIndex) *Handler {
	return &Handler{
		clickhouse: clickhouse,
		config:     config,
		grafana:    grafana,
	}
}

// RegisterRoutes registers all API routes
func (h *Handler) RegisterRoutes(router *gin.Engine) {
	api := router.Group("/api")
	{
		api.GET("/connection", h.GetConnection)
		api.GET("/databases", h.GetDatabases)
		api.GET("/columns", h.GetColumnIndex)
		api.GET("/dataflow/:database/:table", h.GetDataFlowGraph)
		api.GET("/relationships/:database/:table", h.GetRelationshipsGraph)
		api.GET("/table/:database/:table", h.GetTableDetails)
		api.GET("/grafana/status", h.GetGrafanaStatus)
		api.GET("/grafana/usage/:database/:table", h.GetGrafanaUsage)
		api.GET("/grafana/unused", h.GetGrafanaUnusedReport)
		api.POST("/grafana/refresh", h.RefreshGrafana)
	}
}

// grafanaReady writes the status envelope and reports false when dashboard usage
// is not available.
//
// Every Grafana endpoint answers with a top-level "state", so the frontend has
// one branch rather than four. A state other than "ok" carries no payload at
// all: an empty usage payload and a real one showing nothing read are
// indistinguishable to a reader, and the difference between them is the
// difference between a safe schema change and a broken dashboard.
func (h *Handler) grafanaReady(c *gin.Context) bool {
	status := h.grafana.Status()
	if status.State == models.GrafanaStateOK {
		return true
	}
	c.JSON(http.StatusOK, status)
	return false
}

// GetGrafanaStatus reports whether dashboard-usage data is available and, when it
// is not, why. Every state other than "ok" means the caller must not present any
// column as unused.
//
// This deviates from the 400/500/200 contract the other handlers follow: a
// disabled or misconfigured feature answers 200 with a state field rather than an
// error status, so the frontend has a single unambiguous branch and a switched-off
// integration does not look like a server fault.
func (h *Handler) GetGrafanaStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.grafana.Status())
}

// GetGrafanaUsage returns one table's column verdicts and the dashboards behind
// them, for the inspector panel.
func (h *Handler) GetGrafanaUsage(c *gin.Context) {
	database := c.Param("database")
	table := c.Param("table")

	if database == "" || table == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "database and table parameters are required"})
		return
	}
	if !h.grafanaReady(c) {
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"state": models.GrafanaStateOK,
		"usage": h.grafana.Usage(database + "." + table),
	})
}

// GetGrafanaUnusedReport returns column verdicts across the whole schema.
// Filters narrow it by database and by verdict.
func (h *Handler) GetGrafanaUnusedReport(c *gin.Context) {
	if !h.grafanaReady(c) {
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"state": models.GrafanaStateOK,
		"report": h.grafana.Report(models.UnusedReportFilters{
			Database: c.Query("database"),
			Verdict:  c.Query("verdict"),
		}),
	})
}

// RefreshGrafana re-scans the dashboard source.
//
// The only non-GET route in the API. It writes nothing to ClickHouse — it
// rebuilds an in-process cache — but it does cost one request per dashboard
// against Grafana, so it is debounced by GRAFANA_CACHE_TTL and reports when a
// call was turned away.
func (h *Handler) RefreshGrafana(c *gin.Context) {
	status, debounced := h.grafana.Refresh()
	status.Debounced = debounced
	c.JSON(http.StatusOK, status)
}

// GetConnection returns the host, port, and TLS mode the server is connected
// to. The frontend uses it to render the connection chip in the header. The
// password is never exposed.
func (h *Handler) GetConnection(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"host":     h.config.Host,
		"port":     h.config.Port,
		"user":     h.config.User,
		"database": h.config.Database,
		"secure":   h.config.Secure,
	})
}

// GetDatabases returns every visible database with the tables it contains.
// The underlying client populates an in-memory cache (DatabasesData /
// TableRelations) on first call and reuses it for every subsequent request
// until the process restarts.
func (h *Handler) GetDatabases(c *gin.Context) {
	databases, err := h.clickhouse.GetDatabases()
	if err != nil {
		c.JSON(
			http.StatusInternalServerError, gin.H{
				"error": err.Error(),
			},
		)
		return
	}

	c.JSON(http.StatusOK, databases)
}

// GetColumnIndex returns every column across every allowed database. Used by
// the frontend search palette so it can match column names without paying a
// per-keystroke roundtrip cost.
func (h *Handler) GetColumnIndex(c *gin.Context) {
	idx, err := h.clickhouse.BuildColumnIndex()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, idx)
}

// GetDataFlowGraph returns a structured DAG of upstream/downstream tables for
// the selected table. The frontend lays this out with Dagre and renders it as SVG.
func (h *Handler) GetDataFlowGraph(c *gin.Context) {
	database := c.Param("database")
	table := c.Param("table")

	if database == "" || table == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "database and table parameters are required"})
		return
	}

	graph, err := h.clickhouse.BuildDataFlowGraph(database, table)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, graph)
}

// GetRelationshipsGraph returns a column-level graph for the selected table, with
// edges carrying transformation expressions where the backend can infer them.
func (h *Handler) GetRelationshipsGraph(c *gin.Context) {
	database := c.Param("database")
	table := c.Param("table")

	if database == "" || table == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "database and table parameters are required"})
		return
	}

	graph, err := h.clickhouse.BuildRelationshipsGraph(database, table)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, graph)
}

// GetTableDetails returns the column list (name, type, default expression, etc.)
// for the selected table. Used by the inspector panel that opens when a node
// is clicked in either diagram view.
func (h *Handler) GetTableDetails(c *gin.Context) {
	database := c.Param("database")
	table := c.Param("table")

	if database == "" || table == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "database and table parameters are required"})
		return
	}

	details, err := h.clickhouse.GetTableColumns(database, table)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, details)
}
