package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all configuration for the application
type Config struct {
	// ClickHouse connection settings
	ClickHouse struct {
		Host     string
		Port     int
		User     string
		Password string
		Database string
		// TLS configuration
		Secure     bool   // Enable TLS
		SkipVerify bool   // Skip TLS certificate verification
		CertPath   string // Path to client certificate file
		KeyPath    string // Path to client key file
		CAPath     string // Path to CA certificate file
		ServerName string // Server name for certificate verification
	}

	// Web interface settings
	Server struct {
		Addr string
		Mode string
	}
}

// LoadConfig loads configuration from environment variables
func LoadConfig() (*Config, error) {
	config := &Config{}

	// Load ClickHouse settings
	config.ClickHouse.Host = getEnv("CLICKHOUSE_HOST", "localhost")

	portStr := getEnv("CLICKHOUSE_PORT", "9000")
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid CLICKHOUSE_PORT: %v", err)
	}
	config.ClickHouse.Port = port

	config.ClickHouse.User = getEnv("CLICKHOUSE_USER", "default")
	config.ClickHouse.Password = getEnv("CLICKHOUSE_PASSWORD", "")
	config.ClickHouse.Database = getEnv("CLICKHOUSE_DATABASE", "default")

	// Load TLS settings
	config.ClickHouse.Secure = getEnv("CLICKHOUSE_SECURE", "false") == "true"
	config.ClickHouse.SkipVerify = getEnv("CLICKHOUSE_SKIP_VERIFY", "false") == "true"
	config.ClickHouse.CertPath = getEnv("CLICKHOUSE_CERT_PATH", "")
	config.ClickHouse.KeyPath = getEnv("CLICKHOUSE_KEY_PATH", "")
	config.ClickHouse.CAPath = getEnv("CLICKHOUSE_CA_PATH", "")
	config.ClickHouse.ServerName = getEnv("CLICKHOUSE_SERVER_NAME", "")

	// Load server settings
	config.Server.Addr = getEnv("SERVER_ADDR", ":8080")
	config.Server.Mode = getEnv("GIN_MODE", "debug")

	return config, nil
}

// GetClickHouseDSN returns the ClickHouse connection string
func (c *Config) GetClickHouseDSN() string {
	return fmt.Sprintf(
		"clickhouse://%s:%d?username=%s&password=%s&database=%s",
		c.ClickHouse.Host,
		c.ClickHouse.Port,
		c.ClickHouse.User,
		c.ClickHouse.Password,
		c.ClickHouse.Database,
	)
}

// Helper function to get environment variable with a default value
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
