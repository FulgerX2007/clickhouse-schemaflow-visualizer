package main

import (
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/fulgerX2007/clickhouse-schemaflow-visualizer/api"
	"github.com/fulgerX2007/clickhouse-schemaflow-visualizer/models"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	// Load environment variables from .env file
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env file not found, using environment variables")
	}

	// Set Gin mode based on environment
	if os.Getenv("GIN_MODE") == "release" {
		gin.SetMode(gin.ReleaseMode)
	}

	// Load ClickHouse configuration
	clickhouseConfig := models.Config{
		Host:     getEnv("CLICKHOUSE_HOST", "localhost"),
		Port:     getEnvAsInt("CLICKHOUSE_PORT", 9000),
		User:     getEnv("CLICKHOUSE_USER", "default"),
		Password: getEnv("CLICKHOUSE_PASSWORD", ""),
		Database: getEnv("CLICKHOUSE_DATABASE", "default"),
		// TLS configuration
		Secure:     getEnvAsBool("CLICKHOUSE_SECURE", false),
		SkipVerify: getEnvAsBool("CLICKHOUSE_SKIP_VERIFY", false),
		CertPath:   getEnv("CLICKHOUSE_CERT_PATH", ""),
		KeyPath:    getEnv("CLICKHOUSE_KEY_PATH", ""),
		CAPath:     getEnv("CLICKHOUSE_CA_PATH", ""),
		ServerName: getEnv("CLICKHOUSE_SERVER_NAME", ""),
	}

	// Create ClickHouse client
	clickhouseClient, err := models.NewClickHouseClient(clickhouseConfig)
	if err != nil {
		log.Fatalf("Failed to connect to ClickHouse: %v", err)
	}
	defer clickhouseClient.Close()

	// Initialize router
	router := gin.Default()

	// Create API handlers
	handler := api.NewHandler(clickhouseClient, clickhouseConfig)
	handler.RegisterRoutes(router)

	// Serve static files from the frontend directory. The no-cache header tells
	// the browser it may keep a copy but must revalidate before using it; combined
	// with the Last-Modified header Gin already sends, unchanged files round-trip
	// as cheap 304s and any change reaches users on the next page load.
	noCache := func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
	}
	router.Use(noCache)
	router.Static("/static", "./static")

	// Serve index.html through html/template so script/css URLs carry a build-id
	// query string. That guarantees a hard cache-bust when the visualizer is
	// upgraded, which the no-cache header alone can't enforce against browsers
	// that aggressively reuse defer-loaded asset cache entries.
	indexTmpl := template.Must(template.ParseFiles("./static/html/index.html"))
	buildID := strconv.FormatInt(time.Now().Unix(), 10)
	router.GET("/", func(c *gin.Context) {
		c.Status(http.StatusOK)
		c.Header("Content-Type", "text/html; charset=utf-8")
		if err := indexTmpl.Execute(c.Writer, gin.H{"BuildID": buildID}); err != nil {
			log.Printf("Failed to render index.html: %v", err)
		}
	})

	// Get server address from environment or use default
	serverAddr := getEnv("SERVER_ADDR", ":8080")

	// Start the server
	log.Printf("Server starting on %s", serverAddr)
	if err := router.Run(serverAddr); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

// Helper function to get environment variable with a default value
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

// Helper function to get environment variable as an integer
func getEnvAsInt(key string, defaultValue int) int {
	valueStr := getEnv(key, "")
	if valueStr == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(valueStr)
	if err != nil {
		log.Printf("Warning: invalid value for %s, using default: %v", key, err)
		return defaultValue
	}
	return value
}

// Helper function to get environment variable as a boolean
func getEnvAsBool(key string, defaultValue bool) bool {
	valueStr := getEnv(key, "")
	if valueStr == "" {
		return defaultValue
	}
	value, err := strconv.ParseBool(valueStr)
	if err != nil {
		log.Printf("Warning: invalid value for %s, using default: %v", key, err)
		return defaultValue
	}
	return value
}
