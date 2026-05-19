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
}

// NewHandler creates a new Handler instance
func NewHandler(clickhouse *models.ClickHouseClient, config models.Config) *Handler {
	return &Handler{
		clickhouse: clickhouse,
		config:     config,
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
	}
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
