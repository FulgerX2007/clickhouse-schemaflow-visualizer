package api

import (
	"net/http"

	"github.com/fulgerX2007/clickhouse-schemaflow-visualizer/models"
	"github.com/gin-gonic/gin"
)

// Handler holds the dependencies for API handlers
type Handler struct {
	clickhouse *models.ClickHouseClient
}

// NewHandler creates a new Handler instance
func NewHandler(clickhouse *models.ClickHouseClient) *Handler {
	return &Handler{
		clickhouse: clickhouse,
	}
}

// RegisterRoutes registers all API routes
func (h *Handler) RegisterRoutes(router *gin.Engine) {
	api := router.Group("/api")
	{
		api.GET("/databases", h.GetDatabases)
		api.GET("/columns", h.GetColumnIndex)
		api.GET("/dataflow/:database/:table", h.GetDataFlowGraph)
		api.GET("/relationships/:database/:table", h.GetRelationshipsGraph)
		api.GET("/table/:database/:table", h.GetTableDetails)
	}
}

// GetDatabases returns a list of all databases and their tables
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

// GetTableDetails returns detailed information about the selected table
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
