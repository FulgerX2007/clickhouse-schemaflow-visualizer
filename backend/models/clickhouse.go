package models

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/go-faster/city"
)

// Config holds the ClickHouse connection configuration
type Config struct {
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

var DatabasesData map[string]map[string]string

type TableRelation struct {
	DependsOnTable string
	Table          string
	Type           string
}

// TableRelations represents a list of table relations
type TableRelations struct {
	Relations []TableRelation
}

// Table represents a ClickHouse table
type Table struct {
	Name string `json:"name"`
}

// Column represents a column in a ClickHouse table
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Position uint64 `json:"position"`
}

// ClickHouseClient represents a client for interacting with ClickHouse
type ClickHouseClient struct {
	conn clickhouse.Conn
}

// NewClickHouseClient creates a new ClickHouse client
func NewClickHouseClient(config Config) (*ClickHouseClient, error) {
	options := &clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%d", config.Host, config.Port)},
		Auth: clickhouse.Auth{
			Database: config.Database,
			Username: config.User,
			Password: config.Password,
		},
	}

	// Configure TLS if enabled
	if config.Secure {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: config.SkipVerify,
		}

		// Set server name if provided
		if config.ServerName != "" {
			tlsConfig.ServerName = config.ServerName
		}

		// Load client certificate if provided
		if config.CertPath != "" && config.KeyPath != "" {
			cert, err := tls.LoadX509KeyPair(config.CertPath, config.KeyPath)
			if err != nil {
				return nil, fmt.Errorf("failed to load client certificate: %v", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}

		// Load CA certificate if provided
		if config.CAPath != "" {
			caCert, err := os.ReadFile(config.CAPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read CA certificate: %v", err)
			}
			caCertPool := x509.NewCertPool()
			if !caCertPool.AppendCertsFromPEM(caCert) {
				return nil, fmt.Errorf("failed to append CA certificate")
			}
			tlsConfig.RootCAs = caCertPool
		}

		options.TLS = tlsConfig
	}

	conn, err := clickhouse.Open(options)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to ClickHouse: %v", err)
	}

	// Test connection
	if err := conn.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to ping ClickHouse: %v", err)
	}

	return &ClickHouseClient{conn: conn}, nil
}

type result struct {
	createQuery string
	engineFull  string
	engine      string
}

func (c *ClickHouseClient) getTablesRelations() ([]TableRelation, error) {
	ctx := context.Background()

	// Query to get all tables in the database
	query := fmt.Sprintf("SELECT create_table_query, engine_full, engine, database, name FROM system.tables ORDER BY name")
	rows, err := c.conn.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query tables: %v", err)
	}
	defer rows.Close()

	var tables []TableRelation
	for rows.Next() {
		res := result{}
		database, table := "", ""
		if err := rows.Scan(&res.createQuery, &res.engineFull, &res.engine, &database, &table); err != nil {
			return nil, fmt.Errorf("failed to scan table data: %v", err)
		}

		if allowedDatabase(database) {
			if DatabasesData == nil {
				DatabasesData = make(map[string]map[string]string)
			}

			if DatabasesData[database] == nil {
				DatabasesData[database] = make(map[string]string)
			}

			DatabasesData[database][table] = table
		}

		// Extract the relation from the creation query
		if strings.HasPrefix(res.createQuery, "CREATE TABLE") && res.engine != "Distributed" {
			queryParts := strings.Split(res.createQuery, " ")
			if len(queryParts) > 2 {
				tableName := queryParts[2]

				tables = append(tables, TableRelation{Table: tableName})
			}
		} else if strings.HasPrefix(res.createQuery, "CREATE TABLE") && res.engine == "Distributed" {
			queryParts := strings.Split(res.createQuery, " ")
			queryParts2 := strings.Split(res.engineFull, "'")
			if len(queryParts) > 2 {
				tableName := queryParts[2]
				dstTable := queryParts2[3] + "." + queryParts2[5]

				tables = append(tables, TableRelation{DependsOnTable: tableName, Table: dstTable})
			}
		} else if strings.HasPrefix(res.createQuery, "CREATE MATERIALIZED VIEW") {
			queryParts1 := strings.Split(res.createQuery, " ")
			queryParts2 := strings.Split(res.createQuery, "FROM ")
			queryParts3 := strings.Split(queryParts2[1], " ")
			if len(queryParts1) > 3 {
				mvTable := queryParts1[3]
				dstTable := queryParts1[5]
				srcTable := queryParts3[0]

				tables = append(tables, TableRelation{DependsOnTable: srcTable, Table: mvTable})
				tables = append(tables, TableRelation{DependsOnTable: mvTable, Table: dstTable})
			}
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating table rows: %v", err)
	}

	return tables, nil
}

func allowedDatabase(database string) bool {
	switch {
	case database == "":
		return false
	case database == "system":
		return false
	case strings.ToLower(database) == "information_schema":
		return false
	case database == "performance_schema":
		return false
	case database == "mysql":
		return false
	default:
		return true
	}
}

// GetDatabases returns a list of all databases
func (c *ClickHouseClient) GetDatabases() (map[string]map[string]string, error) {
	if DatabasesData == nil {
		_, err := c.getTablesRelations()
		if err != nil {
			return nil, fmt.Errorf("failed to get table relations: %v", err)
		}
	}

	return DatabasesData, nil
}

// GenerateMermaidSchema generates a Mermaid schema for a table and its relationships
func (c *ClickHouseClient) GenerateMermaidSchema(dbName, tableName string) (string, error) {
	// Get the table schema
	table := dbName + "." + tableName

	// Start building the Mermaid schema
	var sb strings.Builder
	sb.WriteString("flowchart TB\n")

	tablesRelations, err := c.getTablesRelations()
	if err != nil {
		return "", fmt.Errorf("failed to get table relations: %v", err)
	}

	sb.WriteString(fmt.Sprintf("    %d[\"%s\"]\n\n", city.Hash32([]byte(table)), table))
	sb.WriteString(fmt.Sprintf("    style %d fill:#FF6D00,stroke:#AA00FF,color:#FFFFFF\n\n", city.Hash32([]byte(table))))

	getRelationsNext(&sb, tablesRelations, table)
	getRelationsBack(&sb, tablesRelations, table)

	return sb.String(), nil
}

func getRelationsNext(sb *strings.Builder, tablesRelations []TableRelation, table string) {
	for _, rel := range tablesRelations {
		if rel.DependsOnTable == table && table != "" {
			sb.WriteString(fmt.Sprintf("    %d[\"%s\"] --> %d[\"%s\"]\n", city.Hash32([]byte(rel.DependsOnTable)), rel.DependsOnTable, city.Hash32([]byte(rel.Table)), rel.Table))
			getRelationsNext(sb, tablesRelations, rel.Table)
		}
	}
}

func getRelationsBack(sb *strings.Builder, tablesRelations []TableRelation, table string) {
	for _, rel := range tablesRelations {
		if rel.Table == table && rel.DependsOnTable != "" {
			sb.WriteString(fmt.Sprintf("    %d[\"%s\"] --> %d[\"%s\"]\n", city.Hash32([]byte(rel.DependsOnTable)), rel.DependsOnTable, city.Hash32([]byte(rel.Table)), rel.Table))
			getRelationsBack(sb, tablesRelations, rel.DependsOnTable)
		}
	}
}

// Close closes the ClickHouse connection
func (c *ClickHouseClient) Close() error {
	return c.conn.Close()
}
