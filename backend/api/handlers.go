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
		api.GET("/schema/:database/:table", h.GetTableSchema)
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

// GetTableSchema returns a Mermaid schema for the selected table
func (h *Handler) GetTableSchema(c *gin.Context) {
	database := c.Param("database")
	table := c.Param("table")

	if database == "" || table == "" {
		c.JSON(
			http.StatusBadRequest, gin.H{
				"error": "database and table parameters are required",
			},
		)
		return
	}

	schema, err := h.clickhouse.GenerateMermaidSchema(database, table)
	if err != nil {
		c.JSON(
			http.StatusInternalServerError, gin.H{
				"error": err.Error(),
			},
		)
		return
	}

	c.JSON(
		http.StatusOK, gin.H{
			"schema": schema,
		},
	)
}
