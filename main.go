package main

import (
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
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
	defer func() { _ = clickhouseClient.Close() }()

	// Load the optional Grafana dashboard source. Unlike ClickHouse, a failure
	// here is never fatal: the visualizer's core function does not depend on it,
	// and killing the process over a bad token would take the whole app down for
	// an add-on feature. The reason is carried into the status endpoint instead.
	grafanaConfig, grafanaErr := models.LoadGrafanaConfig(os.Getenv, clickhouseConfig.Database)
	if grafanaErr != nil {
		log.Printf("Warning: Grafana integration misconfigured: %v", grafanaErr)
	}
	grafanaReason := ""
	if grafanaErr != nil {
		grafanaReason = grafanaErr.Error()
	}

	var grafanaSource models.DashboardSource
	if grafanaConfig.Enabled() {
		grafanaSource, err = models.NewDashboardSource(grafanaConfig)
		if err != nil {
			log.Printf("Warning: Grafana dashboard source unavailable: %v", err)
			grafanaConfig.Mode = models.GrafanaModeDisabled
		} else {
			if grafanaConfig.Mode == models.GrafanaModeAPI && strings.HasPrefix(grafanaConfig.URL, "http://") {
				log.Printf("Warning: GRAFANA_URL is http://, so GRAFANA_TOKEN is sent in cleartext")
			}
			log.Printf("Grafana dashboard source enabled (mode: %s)", grafanaConfig.Mode)
		}
	}

	grafanaIndex := models.NewGrafanaIndex(grafanaConfig, grafanaSource, func() (models.SchemaSnapshot, error) {
		return models.LoadSchemaSnapshot(clickhouseClient)
	}, grafanaReason)
	// Scans in the background: a slow or unreachable Grafana must not delay the
	// listen call for a feature the rest of the app does not depend on.
	grafanaIndex.Start()

	// Initialize router
	router := gin.Default()

	// No proxy is trusted by default. Gin otherwise takes X-Forwarded-For from
	// any peer, so a client could choose the address that lands in the access
	// log. Set TRUSTED_PROXIES when the app really does run behind one.
	if err := router.SetTrustedProxies(trustedProxies()); err != nil {
		log.Fatalf("Failed to set trusted proxies: %v", err)
	}

	// Registered before the routes: Gin only applies middleware to routes added
	// after the Use call, so anything registered later would cover the static
	// files and miss the whole API.
	router.Use(securityHeaders)

	// Create API handlers
	handler := api.NewHandler(clickhouseClient, clickhouseConfig, grafanaIndex)
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

	// Start the server.
	//
	// Built explicitly rather than through router.Run, whose http.Server has
	// every timeout at zero: a client that opens a connection and dribbles out
	// a request header holds a goroutine for as long as it likes, and enough of
	// them exhaust the process. The write timeout is generous because a refresh
	// scan answers on the request goroutine.
	server := &http.Server{
		Addr:              serverAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Printf("Server starting on %s", serverAddr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

// securityHeaders sets the response headers that cost nothing and close off the
// classes of attack the frontend cannot defend against on its own.
//
// The CSP is the substantive one. Scripts may only come from this origin, so a
// table or column name that ever escaped escaping still could not execute.
// Styles need 'unsafe-inline' because the sidebar rows carry a style attribute
// built server-side (generateTableListContent in models/clickhouse.go), and the
// two stylesheet CDNs are the ones index.html actually loads.
func securityHeaders(c *gin.Context) {
	c.Header("Content-Security-Policy",
		"default-src 'self'; "+
			"script-src 'self'; "+
			"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com https://cdnjs.cloudflare.com; "+
			"font-src 'self' https://fonts.gstatic.com https://cdnjs.cloudflare.com; "+
			"img-src 'self' data:; "+
			"connect-src 'self'; "+
			"object-src 'none'; "+
			"base-uri 'none'; "+
			"form-action 'self'; "+
			"frame-ancestors 'none'")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
	c.Header("Referrer-Policy", "no-referrer")
}

// trustedProxies reads TRUSTED_PROXIES as a comma-separated list of IPs or
// CIDRs. Empty means trust none, which is the right default for a server whose
// documented deployment is a direct bind.
func trustedProxies() []string {
	raw := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	if raw == "" {
		return nil
	}
	var proxies []string
	for _, entry := range strings.Split(raw, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			proxies = append(proxies, entry)
		}
	}
	return proxies
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
