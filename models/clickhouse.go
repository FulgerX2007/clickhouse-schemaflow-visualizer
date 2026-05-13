package models

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"html"
	"log"
	"os"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
)

const maxRelationDepth = 50

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
var TableRelations []TableRelation
var TableMetadata map[string]TableInfo

type TableRelation struct {
	DependsOnTable string
	Table          string
	Icon           string
}

type TableInfo struct {
	Name       string
	Database   string
	TotalRows  *uint64
	TotalBytes *uint64
	Engine     string
	Icon       string
}

type ColumnInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Position uint64 `json:"position"`
	Comment  string `json:"comment"`
}

type TableDetails struct {
	Name       string       `json:"name"`
	Database   string       `json:"database"`
	Engine     string       `json:"engine"`
	TotalRows  *uint64      `json:"total_rows"`
	TotalBytes *uint64      `json:"total_bytes"`
	Columns    []ColumnInfo `json:"columns"`
}

// ColumnRelationship represents a column-to-column mapping
type ColumnRelationship struct {
	SourceTable    string
	SourceColumn   string
	TargetTable    string
	TargetColumn   string
	Transformation string // The expression used to transform the column (e.g., "sum", "toDate", etc.)
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
	totalRows   *uint64
	totalBytes  *uint64
}

func formatBytes(bytes *uint64) string {
	if bytes == nil {
		return "N/A"
	}
	const unit = 1024
	b := float64(*bytes)
	if b < unit {
		return fmt.Sprintf("%d B", *bytes)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", b/float64(div), "KMGTPE"[exp])
}

func formatRows(rows *uint64) string {
	if rows == nil {
		return "N/A"
	}
	if *rows < 1000 {
		return fmt.Sprintf("%d", *rows)
	}
	if *rows < 1000000 {
		return fmt.Sprintf("%.1fK", float64(*rows)/1000)
	}
	if *rows < 1000000000 {
		return fmt.Sprintf("%.1fM", float64(*rows)/1000000)
	}
	return fmt.Sprintf("%.1fB", float64(*rows)/1000000000)
}

// generateTableListContent creates the content for table display in the left sidebar
func generateTableListContent(icon, tableName string, totalRows *uint64, totalBytes *uint64) string {
	safeTableName := html.EscapeString(tableName)
	if totalRows == nil {
		return fmt.Sprintf(`%s %s`, icon, safeTableName)
	}

	return fmt.Sprintf(
		`%s %s<br><small style="color: #000; font-size: 0.8em;">Rows: <b>%s</b> | Size: <b>%s</b></small>`,
		icon, safeTableName, formatRows(totalRows), formatBytes(totalBytes),
	)
}

func (c *ClickHouseClient) getTablesRelations() ([]TableRelation, error) {
	if TableRelations != nil && DatabasesData != nil && TableMetadata != nil {
		log.Println("Using cached tables relations")
		return TableRelations, nil
	}

	log.Println("Querying tables relations")
	ctx := context.Background()
	query := "SELECT create_table_query, engine_full, engine, database, name, loading_dependencies_database, loading_dependencies_table, total_rows, total_bytes FROM system.tables ORDER BY name"
	rows, err := c.conn.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query tables: %v", err)
	}
	defer rows.Close()

	var tables []TableRelation
	if TableMetadata == nil {
		TableMetadata = make(map[string]TableInfo)
	}

	for rows.Next() {
		res := result{}
		database, table := "", ""
		var loadingDependenciesTable []string
		var loadingDependenciesDatabase []string
		if err := rows.Scan(&res.createQuery, &res.engineFull, &res.engine, &database, &table, &loadingDependenciesDatabase, &loadingDependenciesTable, &res.totalRows, &res.totalBytes); err != nil {
			return nil, fmt.Errorf("failed to scan table data: %v", err)
		}

		if !allowedDatabase(database) {
			continue
		}

		if DatabasesData == nil {
			DatabasesData = make(map[string]map[string]string)
		}

		if DatabasesData[database] == nil {
			DatabasesData[database] = make(map[string]string)
		}

		fullTableName := database + "." + table
		var icon string

		// Extract the relation from the creation query
		if res.engine == "MergeTree" { // Local Table
			queryParts := strings.Split(res.createQuery, " ")
			icon = `<i class="fa-solid fa-database"></i>`
			if len(queryParts) > 2 {
				tableName := queryParts[2]
				DatabasesData[database][table] = generateTableListContent(icon, table, res.totalRows, res.totalBytes)

				tables = append(tables, TableRelation{Table: tableName, Icon: icon})
			}
		} else if strings.HasPrefix(res.engine, "Replicated") { // Replicated Table
			queryParts := strings.Split(res.createQuery, " ")
			icon = `<i class="fa-solid fa-circle-nodes"></i>`
			DatabasesData[database][table] = generateTableListContent(icon, table, res.totalRows, res.totalBytes)
			if len(queryParts) > 2 {
				tableName := queryParts[2]

				tables = append(tables, TableRelation{Table: tableName, Icon: icon})
			}
		} else if strings.HasPrefix(res.engine, "Dictionary") { // Dictionary Table
			queryParts := strings.Split(res.createQuery, " ")
			icon = `<i class="fa-solid fa-book"></i>`
			DatabasesData[database][table] = generateTableListContent(icon, table, res.totalRows, res.totalBytes)
			if len(queryParts) > 2 {
				tableName := queryParts[2]

				if len(loadingDependenciesDatabase) > 0 && len(loadingDependenciesTable) > 0 {
					tables = append(tables, TableRelation{DependsOnTable: loadingDependenciesDatabase[0] + "." + loadingDependenciesTable[0], Table: tableName, Icon: icon})
				} else {
					tables = append(tables, TableRelation{Table: tableName, Icon: icon})
				}
			}
		} else if res.engine == "Distributed" { // Distributed Table
			queryParts := strings.Split(res.createQuery, " ")
			queryParts2 := strings.Split(res.engineFull, "'")
			icon = `<i class="fa-solid fa-diagram-project"></i>`
			DatabasesData[database][table] = generateTableListContent(icon, table, res.totalRows, res.totalBytes)
			if len(queryParts) > 2 {
				tableName := queryParts[2]
				if len(queryParts2) >= 6 {
					dstTable := queryParts2[3] + "." + queryParts2[5]
					tables = append(tables, TableRelation{DependsOnTable: tableName, Table: dstTable, Icon: icon})
				} else {
					tables = append(tables, TableRelation{Table: tableName, Icon: icon})
				}
			}
		} else if res.engine == "MaterializedView" { // Materialized View
			queryParts1 := strings.Split(res.createQuery, " ")
			queryParts2 := strings.Split(res.createQuery, "FROM ")
			icon = `<i class="fa-solid fa-eye"></i>`
			DatabasesData[database][table] = generateTableListContent(icon, table, res.totalRows, res.totalBytes)
			if len(queryParts1) > 3 && len(queryParts2) > 1 {
				mvTable := queryParts1[3]
				dstTable := queryParts1[5]
				queryParts3 := strings.Split(queryParts2[1], " ")
				srcTable := queryParts3[0]

				tables = append(tables, TableRelation{DependsOnTable: srcTable, Table: mvTable, Icon: icon})
				tables = append(tables, TableRelation{DependsOnTable: mvTable, Table: dstTable, Icon: icon})
			}
		} else {
			// Default case for other engines
			icon = `<i class="fa-solid fa-table"></i>`
			DatabasesData[database][table] = generateTableListContent(icon, table, res.totalRows, res.totalBytes)
			tables = append(tables, TableRelation{Table: table, Icon: icon})
		}

		// Store table metadata
		TableMetadata[fullTableName] = TableInfo{
			Name:       table,
			Database:   database,
			TotalRows:  res.totalRows,
			TotalBytes: res.totalBytes,
			Engine:     res.engine,
			Icon:       icon,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating table rows: %v", err)
	}

	TableRelations = tables

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

// Helper function to simplify column types for ER diagrams
func (c *ClickHouseClient) simplifyColumnType(columnType string) string {
	// Simplify complex ClickHouse types for better readability
	switch {
	case strings.Contains(columnType, "String"):
		return "string"
	case strings.Contains(columnType, "Int"):
		return "int"
	case strings.Contains(columnType, "UInt"):
		return "uint"
	case strings.Contains(columnType, "Float"):
		return "float"
	case strings.Contains(columnType, "Date"):
		return "date"
	case strings.Contains(columnType, "DateTime"):
		return "datetime"
	case strings.Contains(columnType, "UUID"):
		return "uuid"
	case strings.Contains(columnType, "Array"):
		return "array"
	case strings.Contains(columnType, "Nullable"):
		// Extract the inner type
		inner := strings.TrimPrefix(columnType, "Nullable(")
		inner = strings.TrimSuffix(inner, ")")
		return c.simplifyColumnType(inner)
	default:
		return "other"
	}
}

// Helper function to determine if two columns are related
func (c *ClickHouseClient) areColumnsRelated(col1, col2 ColumnInfo) bool {
	col1NameLower := strings.ToLower(col1.Name)
	col2NameLower := strings.ToLower(col2.Name)

	// Exact name match
	if col1NameLower == col2NameLower {
		return true
	}

	// Foreign key pattern: table_id matches id
	if strings.HasSuffix(col1NameLower, "_id") && col2NameLower == "id" {
		return true
	}
	if strings.HasSuffix(col2NameLower, "_id") && col1NameLower == "id" {
		return true
	}

	// UUID relationships
	if strings.Contains(col1.Type, "UUID") && strings.Contains(col2.Type, "UUID") {
		// Check if column names suggest a relationship
		if strings.Contains(col1NameLower, strings.TrimSuffix(col2NameLower, "_id")) ||
			strings.Contains(col2NameLower, strings.TrimSuffix(col1NameLower, "_id")) {
			return true
		}
	}

	// Timestamp relationships (common in ClickHouse for partitioning)
	if strings.Contains(col1.Type, "DateTime") && strings.Contains(col2.Type, "DateTime") {
		if (strings.Contains(col1NameLower, "time") || strings.Contains(col1NameLower, "date")) &&
			(strings.Contains(col2NameLower, "time") || strings.Contains(col2NameLower, "date")) {
			return true
		}
	}

	return false
}

// GetTableColumns returns detailed column information for a specific table
func (c *ClickHouseClient) GetTableColumns(database, table string) (*TableDetails, error) {
	ctx := context.Background()

	// First get basic table info
	tableQuery := `
		SELECT engine, total_rows, total_bytes 
		FROM system.tables 
		WHERE database = ? AND name = ?
	`

	var engine string
	var totalRows, totalBytes *uint64

	row := c.conn.QueryRow(ctx, tableQuery, database, table)
	if err := row.Scan(&engine, &totalRows, &totalBytes); err != nil {
		return nil, fmt.Errorf("failed to get table info: %v", err)
	}

	// Get column information
	columnsQuery := `
		SELECT name, type, position, comment
		FROM system.columns 
		WHERE database = ? AND table = ? 
		ORDER BY position
	`

	rows, err := c.conn.Query(ctx, columnsQuery, database, table)
	if err != nil {
		return nil, fmt.Errorf("failed to query columns: %v", err)
	}
	defer rows.Close()

	var columns []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		if err := rows.Scan(&col.Name, &col.Type, &col.Position, &col.Comment); err != nil {
			return nil, fmt.Errorf("failed to scan column: %v", err)
		}
		columns = append(columns, col)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating columns: %v", err)
	}

	return &TableDetails{
		Name:       table,
		Database:   database,
		Engine:     engine,
		TotalRows:  totalRows,
		TotalBytes: totalBytes,
		Columns:    columns,
	}, nil
}

// parseViewQuery extracts the SELECT statement from a CREATE MATERIALIZED VIEW query
func (c *ClickHouseClient) parseViewQuery(createQuery string) (selectQuery string, sourceTable string, destTable string) {
	// Extract SELECT query - find everything after "AS SELECT" or just "SELECT"
	selectIdx := strings.Index(createQuery, "SELECT")
	if selectIdx == -1 {
		return "", "", ""
	}
	selectQuery = createQuery[selectIdx:]

	// Extract source table from "FROM table_name"
	fromParts := strings.Split(createQuery, "FROM ")
	if len(fromParts) > 1 {
		// Get the part after FROM and extract table name
		afterFrom := strings.TrimSpace(fromParts[1])
		// Split by space or other separators to get just the table name
		sourceTableParts := strings.FieldsFunc(afterFrom, func(r rune) bool {
			return r == ' ' || r == '\n' || r == '\r' || r == '(' || r == ';'
		})
		if len(sourceTableParts) > 0 {
			sourceTable = sourceTableParts[0]
		}
	}

	// Extract destination table from "TO database.table" or "MATERIALIZED VIEW database.table"
	toParts := strings.Split(createQuery, "TO ")
	if len(toParts) > 1 {
		afterTo := strings.TrimSpace(toParts[1])
		destTableParts := strings.FieldsFunc(afterTo, func(r rune) bool {
			return r == ' ' || r == '\n' || r == '\r' || r == '('
		})
		if len(destTableParts) > 0 {
			destTable = destTableParts[0]
		}
	} else {
		// Try to get from CREATE MATERIALIZED VIEW line
		queryParts := strings.Fields(createQuery)
		for i, part := range queryParts {
			if part == "VIEW" && i+1 < len(queryParts) {
				destTable = queryParts[i+1]
				break
			}
		}
	}

	return selectQuery, sourceTable, destTable
}

// extractColumnMappings parses a SELECT query to extract column mappings
func (c *ClickHouseClient) extractColumnMappings(selectQuery string, sourceColumns []ColumnInfo) []ColumnRelationship {
	var relationships []ColumnRelationship

	// Find the SELECT part and FROM part
	selectIdx := strings.Index(selectQuery, "SELECT")
	fromIdx := strings.Index(selectQuery, "FROM")

	if selectIdx == -1 || fromIdx == -1 || fromIdx <= selectIdx {
		return relationships
	}

	// Extract just the column list between SELECT and FROM
	columnsPart := selectQuery[selectIdx+6 : fromIdx]
	columnsPart = strings.TrimSpace(columnsPart)

	// Split by comma (basic parsing - doesn't handle nested functions perfectly)
	columnDefs := strings.Split(columnsPart, ",")

	for _, colDef := range columnDefs {
		colDef = strings.TrimSpace(colDef)
		if colDef == "" {
			continue
		}

		var sourceCol, targetCol, transformation string
		originalExpr := colDef

		// Check for AS alias: "expression AS alias"
		if strings.Contains(strings.ToUpper(colDef), " AS ") {
			parts := strings.SplitN(colDef, " AS ", 2)
			if len(parts) == 2 {
				targetCol = strings.TrimSpace(parts[1])
				originalExpr = strings.TrimSpace(parts[0])
				sourceCol = c.extractBaseColumnName(originalExpr, sourceColumns)
				// Use the full expression if it's different from just the column name
				if !strings.EqualFold(originalExpr, sourceCol) {
					transformation = originalExpr
				}
			}
		} else {
			// No alias - column name is the same
			sourceCol = c.extractBaseColumnName(colDef, sourceColumns)
			targetCol = sourceCol
			// Use the full expression if it's different from just the column name
			if !strings.EqualFold(colDef, sourceCol) {
				transformation = colDef
			}
		}

		if sourceCol != "" && targetCol != "" {
			log.Printf("Column mapping: %s -> %s (transformation: '%s', original expr: '%s')", sourceCol, targetCol, transformation, originalExpr)
			relationships = append(relationships, ColumnRelationship{
				SourceColumn:   sourceCol,
				TargetColumn:   targetCol,
				Transformation: transformation,
			})
		}
	}

	return relationships
}

// extractBaseColumnName extracts the actual column name from an expression
func (c *ClickHouseClient) extractBaseColumnName(expression string, sourceColumns []ColumnInfo) string {
	expression = strings.TrimSpace(expression)

	// Remove common functions to find the column name
	// Handle patterns like: toDate(timestamp), sum(amount), COUNT(*), etc.

	// Check if it's a simple column reference first
	for _, col := range sourceColumns {
		if strings.EqualFold(expression, col.Name) {
			return col.Name
		}
	}

	// Try to extract column name from function calls
	// Look for column names in parentheses or after function names
	for _, col := range sourceColumns {
		colName := col.Name
		// Check if column name appears in the expression
		if strings.Contains(strings.ToLower(expression), strings.ToLower(colName)) {
			return colName
		}
	}

	// If we can't find a source column, check if it's an aggregate or constant
	upperExpr := strings.ToUpper(expression)
	if strings.Contains(upperExpr, "COUNT(") ||
	   strings.Contains(upperExpr, "SUM(") ||
	   strings.Contains(upperExpr, "AVG(") ||
	   strings.Contains(upperExpr, "MIN(") ||
	   strings.Contains(upperExpr, "MAX(") {
		// Try to extract column from inside parentheses
		start := strings.Index(expression, "(")
		end := strings.LastIndex(expression, ")")
		if start != -1 && end != -1 && end > start {
			inner := strings.TrimSpace(expression[start+1 : end])
			// Recursively check the inner part
			return c.extractBaseColumnName(inner, sourceColumns)
		}
	}

	return ""
}

// isDistributedTable checks if a table is a Distributed table
func (c *ClickHouseClient) isDistributedTable(dbName, tableName string) bool {
	ctx := context.Background()
	var engine string
	query := "SELECT engine FROM system.tables WHERE database = ? AND name = ?"
	row := c.conn.QueryRow(ctx, query, dbName, tableName)
	if err := row.Scan(&engine); err != nil {
		return false
	}
	return engine == "Distributed"
}


// Close closes the ClickHouse connection
func (c *ClickHouseClient) Close() error {
	return c.conn.Close()
}
