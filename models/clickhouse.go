package models

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
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
	SourceTable  string
	SourceColumn string
	TargetTable  string
	TargetColumn string
}

// FlowchartNode represents a column node in the flowchart
type FlowchartNode struct {
	TableName  string
	ColumnName string
	ColumnType string
	NodeID     string
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
	if totalRows == nil {
		return fmt.Sprintf(`%s %s`, icon, tableName)
	}

	return fmt.Sprintf(
		`%s %s<br><small style="color: #000; font-size: 0.8em;">Rows: <b>%s</b> | Size: <b>%s</b></small>`,
		icon, tableName, formatRows(totalRows), formatBytes(totalBytes),
	)
}

func (c *ClickHouseClient) getTablesRelations() ([]TableRelation, error) {
	if TableRelations != nil && DatabasesData != nil && TableMetadata != nil {
		log.Println("Using cached tables relations")
		return TableRelations, nil
	}

	log.Println("Querying tables relations")
	ctx := context.Background()
	query := fmt.Sprintf("SELECT create_table_query, engine_full, engine, database, name, loading_dependencies_database, loading_dependencies_table, total_rows, total_bytes FROM system.tables ORDER BY name")
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

	// Generate node for the main table with additional info
	nodeContent := c.generateTableNodeContent(table)
	sb.WriteString(fmt.Sprintf("    %d[\"%s\"]\n\n", city.Hash32([]byte(table)), nodeContent))
	sb.WriteString(fmt.Sprintf("    style %d fill:#FF6D00,stroke:#AA00FF,color:#FFFFFF\n\n", city.Hash32([]byte(table))))

	seen := make(map[string]bool)
	c.getRelationsNext(&sb, tablesRelations, table, &seen)
	c.getRelationsBack(&sb, tablesRelations, table, &seen)

	return sb.String(), nil
}

// GenerateRelationshipsSchema generates a detailed flowchart schema for a table showing column-level relationships
func (c *ClickHouseClient) GenerateRelationshipsSchema(dbName, tableName string) (string, error) {
	// Use the new flowchart-based approach for column-level visualization
	return c.buildColumnFlowchart(dbName, tableName)
}

// Helper function to sanitize table names for ER diagrams
func (c *ClickHouseClient) sanitizeTableName(tableName string) string {
	// Remove special characters and replace with underscores
	sanitized := strings.ReplaceAll(tableName, ".", "_")
	sanitized = strings.ReplaceAll(sanitized, "-", "_")
	return sanitized
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

// Helper function to find related tables
func (c *ClickHouseClient) findRelatedTables(table string, relations []TableRelation) []string {
	var relatedTables []string
	seen := make(map[string]bool)

	for _, rel := range relations {
		if rel.Table == table && rel.DependsOnTable != "" && !seen[rel.DependsOnTable] {
			relatedTables = append(relatedTables, rel.DependsOnTable)
			seen[rel.DependsOnTable] = true
		}
		if rel.DependsOnTable == table && rel.Table != "" && !seen[rel.Table] {
			relatedTables = append(relatedTables, rel.Table)
			seen[rel.Table] = true
		}
	}

	return relatedTables
}

// Helper function to generate ER relationships
func (c *ClickHouseClient) generateERRelationships(sb *strings.Builder, table string, relations []TableRelation) {
	for _, rel := range relations {
		if rel.Table == table && rel.DependsOnTable != "" {
			// This table depends on another table
			sourceParts := strings.Split(rel.DependsOnTable, ".")
			targetParts := strings.Split(rel.Table, ".")

			if len(sourceParts) == 2 && len(targetParts) == 2 {
				sourceTable := c.sanitizeTableName(sourceParts[1])
				targetTable := c.sanitizeTableName(targetParts[1])

				// Determine relationship type based on engine
				relationshipType := c.getRelationshipType(rel)
				sb.WriteString(fmt.Sprintf("    %s %s %s : \"%s\"\n",
					sourceTable, relationshipType, targetTable, c.getRelationshipLabel(rel)))
			}
		}
	}
}

// Helper function to determine relationship type
func (c *ClickHouseClient) getRelationshipType(rel TableRelation) string {
	// Based on ClickHouse table types, determine the relationship
	if strings.Contains(rel.Icon, "fa-eye") { // Materialized View
		return "||--o{"
	} else if strings.Contains(rel.Icon, "fa-diagram-project") { // Distributed
		return "||--||"
	} else if strings.Contains(rel.Icon, "fa-book") { // Dictionary
		return "}o--||"
	}
	return "||--||" // Default relationship
}

// Helper function to get relationship label
func (c *ClickHouseClient) getRelationshipLabel(rel TableRelation) string {
	if strings.Contains(rel.Icon, "fa-eye") {
		return "materializes"
	} else if strings.Contains(rel.Icon, "fa-diagram-project") {
		return "distributes"
	} else if strings.Contains(rel.Icon, "fa-book") {
		return "references"
	}
	return "relates_to"
}

// Helper function to get column key indicators for ER diagrams
func (c *ClickHouseClient) getColumnKeyIndicator(columnName, columnType string) string {
	columnNameLower := strings.ToLower(columnName)

	// Check for primary key patterns
	if columnNameLower == "id" || strings.HasSuffix(columnNameLower, "_id") {
		return "PK"
	}

	// Check for foreign key patterns
	if strings.HasSuffix(columnNameLower, "_id") && columnNameLower != "id" {
		return "FK"
	}

	// Check for UUID types (often used as keys in ClickHouse)
	if strings.Contains(columnType, "UUID") {
		return "UK"
	}

	// Check for timestamp columns (often used for partitioning)
	if strings.Contains(columnType, "DateTime") && (strings.Contains(columnNameLower, "time") || strings.Contains(columnNameLower, "date")) {
		return "TS"
	}

	return ""
}

// Helper function to generate column-level relationships
func (c *ClickHouseClient) generateColumnRelationships(sb *strings.Builder, mainTableName string, allTableColumns map[string][]ColumnInfo, relations []TableRelation) {
	// Find column relationships based on naming patterns and types
	mainColumns := allTableColumns[mainTableName]

	for relatedTableName, relatedColumns := range allTableColumns {
		if relatedTableName == mainTableName {
			continue
		}

		// Look for column relationships between tables
		for _, mainCol := range mainColumns {
			for _, relatedCol := range relatedColumns {
				if c.areColumnsRelated(mainCol, relatedCol) {
					// Generate relationship line between specific columns
					sb.WriteString(fmt.Sprintf("    %s ||--|| %s : \"%s.%s -> %s.%s\"\n",
						mainTableName, relatedTableName, mainTableName, mainCol.Name, relatedTableName, relatedCol.Name))
				}
			}
		}
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

// Helper function to get column relationship type
func (c *ClickHouseClient) getColumnRelationshipType(col1, col2 ColumnInfo) string {
	col1NameLower := strings.ToLower(col1.Name)
	col2NameLower := strings.ToLower(col2.Name)

	// Primary key to foreign key relationship
	if col1NameLower == "id" && strings.HasSuffix(col2NameLower, "_id") {
		return "||--o{"
	}
	if col2NameLower == "id" && strings.HasSuffix(col1NameLower, "_id") {
		return "}o--||"
	}

	// UUID relationships (one-to-one)
	if strings.Contains(col1.Type, "UUID") && strings.Contains(col2.Type, "UUID") {
		return "||--||"
	}

	// Default many-to-many
	return "||--||"
}

func (c *ClickHouseClient) generateTableNodeContent(table string) string {
	if metadata, exists := TableMetadata[table]; exists && metadata.TotalRows != nil {
		return fmt.Sprintf(
			"%s<br><small>Rows: <b>%s</b> Size: <b>%s</b></small>",
			table,
			formatRows(metadata.TotalRows),
			formatBytes(metadata.TotalBytes),
		)
	}
	return table
}

func (c *ClickHouseClient) getRelationsNext(sb *strings.Builder, tablesRelations []TableRelation, table string, seen *map[string]bool) {
	for _, rel := range tablesRelations {
		if rel.DependsOnTable == table && table != "" {
			depContent := c.generateTableNodeContent(rel.DependsOnTable)
			relContent := c.generateTableNodeContent(rel.Table)

			mermaidRow := fmt.Sprintf(
				"    %d[\"%s\"] --> %d[\"%s\"]\n",
				city.Hash32([]byte(rel.DependsOnTable)), depContent,
				city.Hash32([]byte(rel.Table)), relContent,
			)

			if !(*seen)[mermaidRow] {
				(*seen)[mermaidRow] = true
				sb.WriteString(mermaidRow)
			}
			c.getRelationsNext(sb, tablesRelations, rel.Table, seen)
		}
	}
}

func (c *ClickHouseClient) getRelationsBack(sb *strings.Builder, tablesRelations []TableRelation, table string, seen *map[string]bool) {
	for _, rel := range tablesRelations {
		if rel.Table == table && rel.DependsOnTable != "" {
			depContent := c.generateTableNodeContent(rel.DependsOnTable)
			relContent := c.generateTableNodeContent(rel.Table)

			mermaidRow := fmt.Sprintf(
				"    %d[\"%s\"] --> %d[\"%s\"]\n",
				city.Hash32([]byte(rel.DependsOnTable)), depContent,
				city.Hash32([]byte(rel.Table)), relContent,
			)

			if !(*seen)[mermaidRow] {
				(*seen)[mermaidRow] = true
				sb.WriteString(mermaidRow)
			}
			c.getRelationsBack(sb, tablesRelations, rel.DependsOnTable, seen)
		}
	}
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

		var sourceCol, targetCol string

		// Check for AS alias: "expression AS alias"
		if strings.Contains(strings.ToUpper(colDef), " AS ") {
			parts := strings.SplitN(colDef, " AS ", 2)
			if len(parts) == 2 {
				targetCol = strings.TrimSpace(parts[1])
				sourceCol = c.extractBaseColumnName(parts[0], sourceColumns)
			}
		} else {
			// No alias - column name is the same
			sourceCol = c.extractBaseColumnName(colDef, sourceColumns)
			targetCol = sourceCol
		}

		if sourceCol != "" && targetCol != "" {
			relationships = append(relationships, ColumnRelationship{
				SourceColumn: sourceCol,
				TargetColumn: targetCol,
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

// buildColumnFlowchart generates a flowchart showing column-level data flow
func (c *ClickHouseClient) buildColumnFlowchart(dbName, tableName string) (string, error) {
	// Get the current table details
	currentTable, err := c.GetTableColumns(dbName, tableName)
	if err != nil {
		return "", fmt.Errorf("failed to get table details: %v", err)
	}

	// Get the table's CREATE query to check if it's a MaterializedView
	ctx := context.Background()
	fullTableName := dbName + "." + tableName

	var createQuery, engine string
	query := "SELECT create_table_query, engine FROM system.tables WHERE database = ? AND name = ?"
	row := c.conn.QueryRow(ctx, query, dbName, tableName)
	if err := row.Scan(&createQuery, &engine); err != nil {
		return "", fmt.Errorf("failed to get table info: %v", err)
	}

	var sb strings.Builder
	sb.WriteString("%%{init: {'flowchart': {'curve': 'basis', 'padding': 20}}}%%\n")
	sb.WriteString("flowchart LR\n")

	// If it's a Materialized View, parse the query to get column mappings
	if engine == "MaterializedView" {
		selectQuery, sourceTable, destTable := c.parseViewQuery(createQuery)
		log.Printf("MV %s: parsed sourceTable=%s, destTable=%s", fullTableName, sourceTable, destTable)

		// If destTable not found in CREATE query, look for it in table relations
		if destTable == "" {
			tablesRelations, err := c.getTablesRelations()
			if err == nil {
				for _, rel := range tablesRelations {
					if rel.DependsOnTable == fullTableName && rel.Table != "" {
						// Found a table that depends on this MV
						log.Printf("Found relation: %s depends on %s", rel.Table, rel.DependsOnTable)
						parts := strings.Split(rel.Table, ".")
						if len(parts) == 2 {
							relDbName, relTableName := parts[0], parts[1]
							if !c.isDistributedTable(relDbName, relTableName) {
								destTable = rel.Table
								log.Printf("Using destTable from relations: %s", destTable)
								break
							} else {
								log.Printf("Skipping distributed table: %s.%s", relDbName, relTableName)
							}
						}
					}
				}
			}
		}
		log.Printf("Final destTable for %s: %s", fullTableName, destTable)

		if sourceTable != "" {
			// Split database and table if source table is fully qualified
			srcDbName, srcTableName := dbName, sourceTable
			if strings.Contains(sourceTable, ".") {
				parts := strings.Split(sourceTable, ".")
				srcDbName, srcTableName = parts[0], parts[1]
			}

			// Skip if source table is distributed
			if c.isDistributedTable(srcDbName, srcTableName) {
				return "", fmt.Errorf("source table %s.%s is a Distributed table, skipping visualization", srcDbName, srcTableName)
			}

			// Get source table columns
			sourceTableDetails, err := c.GetTableColumns(srcDbName, srcTableName)
			if err == nil {
				// Extract column mappings
				columnMappings := c.extractColumnMappings(selectQuery, sourceTableDetails.Columns)

				// Generate source table subgraph
				sanitizedSourceTable := c.sanitizeTableName(srcTableName)
				sb.WriteString(fmt.Sprintf("    subgraph %s_graph[\"%s\"]\n", sanitizedSourceTable, srcTableName))
				sb.WriteString(fmt.Sprintf("        direction TB\n"))
				for _, col := range sourceTableDetails.Columns {
					nodeID := fmt.Sprintf("%s_%s", sanitizedSourceTable, col.Name)
					colType := c.simplifyColumnType(col.Type)
					sb.WriteString(fmt.Sprintf("        %s[\"%s: %s\"]\n", nodeID, col.Name, colType))
				}
				sb.WriteString("    end\n\n")

				// Generate current MV table subgraph (highlighted)
				sanitizedMVTable := c.sanitizeTableName(tableName)
				sb.WriteString(fmt.Sprintf("    subgraph %s_graph[\"%s (MV)\"]\n", sanitizedMVTable, tableName))
				sb.WriteString(fmt.Sprintf("        direction TB\n"))
				for _, col := range currentTable.Columns {
					nodeID := fmt.Sprintf("%s_%s", sanitizedMVTable, col.Name)
					colType := c.simplifyColumnType(col.Type)
					sb.WriteString(fmt.Sprintf("        %s[\"%s: %s\"]\n", nodeID, col.Name, colType))
				}
				sb.WriteString("    end\n\n")

				// Style the current MV table
				sb.WriteString(fmt.Sprintf("    style %s_graph fill:#FF6D00,stroke:#AA00FF,stroke-width:3px,color:#FFFFFF\n\n", sanitizedMVTable))

				// Generate destination table subgraph if exists
				if destTable != "" {
					destDbName, destTableName := dbName, destTable
					if strings.Contains(destTable, ".") {
						parts := strings.Split(destTable, ".")
						destDbName, destTableName = parts[0], parts[1]
					}

					// Skip if destination table is distributed
					if !c.isDistributedTable(destDbName, destTableName) {
						destTableDetails, err := c.GetTableColumns(destDbName, destTableName)
						if err == nil {
							sanitizedDestTable := c.sanitizeTableName(destTableName)
							sb.WriteString(fmt.Sprintf("    subgraph %s_graph[\"%s\"]\n", sanitizedDestTable, destTableName))
							sb.WriteString(fmt.Sprintf("        direction TB\n"))
							for _, col := range destTableDetails.Columns {
								nodeID := fmt.Sprintf("%s_%s", sanitizedDestTable, col.Name)
								colType := c.simplifyColumnType(col.Type)
								sb.WriteString(fmt.Sprintf("        %s[\"%s: %s\"]\n", nodeID, col.Name, colType))
							}
							sb.WriteString("    end\n\n")

							// Generate arrows from source -> MV -> destination
							for _, mapping := range columnMappings {
								srcNodeID := fmt.Sprintf("%s_%s", sanitizedSourceTable, mapping.SourceColumn)
								mvNodeID := fmt.Sprintf("%s_%s", sanitizedMVTable, mapping.TargetColumn)

								sb.WriteString(fmt.Sprintf("    %s --> %s\n", srcNodeID, mvNodeID))

								// Try to match MV column to destination column
								// First try exact name match
								matched := false
								for _, destCol := range destTableDetails.Columns {
									if strings.EqualFold(destCol.Name, mapping.TargetColumn) {
										destNodeID := fmt.Sprintf("%s_%s", sanitizedDestTable, destCol.Name)
										sb.WriteString(fmt.Sprintf("    %s --> %s\n", mvNodeID, destNodeID))
										matched = true
										break
									}
								}

								// If no exact match, try using areColumnsRelated for fuzzy matching
								if !matched {
									// Find the MV column info
									for _, mvCol := range currentTable.Columns {
										if strings.EqualFold(mvCol.Name, mapping.TargetColumn) {
											// Check if it's related to any destination column
											for _, destCol := range destTableDetails.Columns {
												if c.areColumnsRelated(mvCol, destCol) {
													destNodeID := fmt.Sprintf("%s_%s", sanitizedDestTable, destCol.Name)
													sb.WriteString(fmt.Sprintf("    %s --> %s\n", mvNodeID, destNodeID))
													matched = true
													break
												}
											}
											break
										}
									}
								}

								// Special case: if MV has only 1 column and dest has only 1 column, connect them
								// This handles cases like extracting distinct values to a lookup table
								if !matched && len(currentTable.Columns) == 1 && len(destTableDetails.Columns) == 1 {
									destCol := destTableDetails.Columns[0]
									destNodeID := fmt.Sprintf("%s_%s", sanitizedDestTable, destCol.Name)
									sb.WriteString(fmt.Sprintf("    %s --> %s\n", mvNodeID, destNodeID))
									log.Printf("Connected single MV column %s to single dest column %s", mapping.TargetColumn, destCol.Name)
								}
							}
						}
					}
				} else {
					// No destination table - just show source -> MV
					for _, mapping := range columnMappings {
						srcNodeID := fmt.Sprintf("%s_%s", sanitizedSourceTable, mapping.SourceColumn)
						mvNodeID := fmt.Sprintf("%s_%s", sanitizedMVTable, mapping.TargetColumn)
						sb.WriteString(fmt.Sprintf("    %s --> %s\n", srcNodeID, mvNodeID))
					}
				}
			}
		}
	} else {
		// For non-MV tables, show dependencies based on table relations
		tablesRelations, err := c.getTablesRelations()
		if err != nil {
			return "", fmt.Errorf("failed to get table relations: %v", err)
		}

		// Find tables that current table depends on (source tables)
		var sourceTables []string
		var destTables []string

		for _, rel := range tablesRelations {
			if rel.Table == fullTableName && rel.DependsOnTable != "" {
				// Check if the dependency table is distributed
				parts := strings.Split(rel.DependsOnTable, ".")
				if len(parts) == 2 {
					depDbName, depTableName := parts[0], parts[1]
					if !c.isDistributedTable(depDbName, depTableName) {
						sourceTables = append(sourceTables, rel.DependsOnTable)
					}
				}
			}
			if rel.DependsOnTable == fullTableName && rel.Table != "" {
				// Check if the dependent table is distributed
				parts := strings.Split(rel.Table, ".")
				if len(parts) == 2 {
					relDbName, relTableName := parts[0], parts[1]
					if !c.isDistributedTable(relDbName, relTableName) {
						destTables = append(destTables, rel.Table)
					}
				}
			}
		}

		// Generate subgraphs for source tables
		for _, srcTable := range sourceTables {
			parts := strings.Split(srcTable, ".")
			if len(parts) == 2 {
				srcDbName, srcTableName := parts[0], parts[1]
				srcDetails, err := c.GetTableColumns(srcDbName, srcTableName)
				if err == nil {
					sanitizedSrcTable := c.sanitizeTableName(srcTableName)
					sb.WriteString(fmt.Sprintf("    subgraph %s_graph[\"%s\"]\n", sanitizedSrcTable, srcTableName))
					sb.WriteString("        direction TB\n")
					for _, col := range srcDetails.Columns {
						nodeID := fmt.Sprintf("%s_%s", sanitizedSrcTable, col.Name)
						colType := c.simplifyColumnType(col.Type)
						sb.WriteString(fmt.Sprintf("        %s[\"%s: %s\"]\n", nodeID, col.Name, colType))
					}
					sb.WriteString("    end\n\n")
				}
			}
		}

		// Generate current table subgraph
		sanitizedCurrentTable := c.sanitizeTableName(tableName)
		sb.WriteString(fmt.Sprintf("    subgraph %s_graph[\"%s\"]\n", sanitizedCurrentTable, tableName))
		sb.WriteString("        direction TB\n")
		for _, col := range currentTable.Columns {
			nodeID := fmt.Sprintf("%s_%s", sanitizedCurrentTable, col.Name)
			colType := c.simplifyColumnType(col.Type)
			sb.WriteString(fmt.Sprintf("        %s[\"%s: %s\"]\n", nodeID, col.Name, colType))
		}
		sb.WriteString("    end\n\n")
		sb.WriteString(fmt.Sprintf("    style %s_graph fill:#FF6D00,stroke:#AA00FF,stroke-width:3px,color:#FFFFFF\n\n", sanitizedCurrentTable))

		// Generate subgraphs for destination tables
		for _, destTable := range destTables {
			parts := strings.Split(destTable, ".")
			if len(parts) == 2 {
				destDbName, destTableName := parts[0], parts[1]
				destDetails, err := c.GetTableColumns(destDbName, destTableName)
				if err == nil {
					sanitizedDestTable := c.sanitizeTableName(destTableName)
					sb.WriteString(fmt.Sprintf("    subgraph %s_graph[\"%s\"]\n", sanitizedDestTable, destTableName))
					sb.WriteString("        direction TB\n")
					for _, col := range destDetails.Columns {
						nodeID := fmt.Sprintf("%s_%s", sanitizedDestTable, col.Name)
						colType := c.simplifyColumnType(col.Type)
						sb.WriteString(fmt.Sprintf("        %s[\"%s: %s\"]\n", nodeID, col.Name, colType))
					}
					sb.WriteString("    end\n\n")
				}
			}
		}

		// Generate column-to-column arrows based on column name matching
		for _, srcTable := range sourceTables {
			parts := strings.Split(srcTable, ".")
			if len(parts) == 2 {
				srcDbName, srcTableName := parts[0], parts[1]
				srcDetails, err := c.GetTableColumns(srcDbName, srcTableName)
				if err == nil {
					sanitizedSrcTable := c.sanitizeTableName(srcTableName)

					// Track if any columns were matched
					anyMatched := false
					for _, srcCol := range srcDetails.Columns {
						for _, currCol := range currentTable.Columns {
							if c.areColumnsRelated(srcCol, currCol) {
								srcNodeID := fmt.Sprintf("%s_%s", sanitizedSrcTable, srcCol.Name)
								currNodeID := fmt.Sprintf("%s_%s", sanitizedCurrentTable, currCol.Name)
								sb.WriteString(fmt.Sprintf("    %s --> %s\n", srcNodeID, currNodeID))
								anyMatched = true
							}
						}
					}

					// Special case: if source has 1 column and current has 1 column, connect them
					if !anyMatched && len(srcDetails.Columns) == 1 && len(currentTable.Columns) == 1 {
						srcCol := srcDetails.Columns[0]
						currCol := currentTable.Columns[0]
						srcNodeID := fmt.Sprintf("%s_%s", sanitizedSrcTable, srcCol.Name)
						currNodeID := fmt.Sprintf("%s_%s", sanitizedCurrentTable, currCol.Name)
						sb.WriteString(fmt.Sprintf("    %s --> %s\n", srcNodeID, currNodeID))
						log.Printf("Connected single source column %s.%s to single current column %s", srcTableName, srcCol.Name, currCol.Name)
					}
				}
			}
		}

		for _, destTable := range destTables {
			parts := strings.Split(destTable, ".")
			if len(parts) == 2 {
				destDbName, destTableName := parts[0], parts[1]
				destDetails, err := c.GetTableColumns(destDbName, destTableName)
				if err == nil {
					sanitizedDestTable := c.sanitizeTableName(destTableName)

					// Track if any columns were matched
					anyMatched := false
					for _, currCol := range currentTable.Columns {
						for _, destCol := range destDetails.Columns {
							if c.areColumnsRelated(currCol, destCol) {
								currNodeID := fmt.Sprintf("%s_%s", sanitizedCurrentTable, currCol.Name)
								destNodeID := fmt.Sprintf("%s_%s", sanitizedDestTable, destCol.Name)
								sb.WriteString(fmt.Sprintf("    %s --> %s\n", currNodeID, destNodeID))
								anyMatched = true
							}
						}
					}

					// Special case: if current has 1 column and dest has 1 column, connect them
					if !anyMatched && len(currentTable.Columns) == 1 && len(destDetails.Columns) == 1 {
						currCol := currentTable.Columns[0]
						destCol := destDetails.Columns[0]
						currNodeID := fmt.Sprintf("%s_%s", sanitizedCurrentTable, currCol.Name)
						destNodeID := fmt.Sprintf("%s_%s", sanitizedDestTable, destCol.Name)
						sb.WriteString(fmt.Sprintf("    %s --> %s\n", currNodeID, destNodeID))
						log.Printf("Connected single current column %s to single dest column %s.%s", currCol.Name, destTableName, destCol.Name)
					}
				}
			}
		}
	}

	return sb.String(), nil
}

// Close closes the ClickHouse connection
func (c *ClickHouseClient) Close() error {
	return c.conn.Close()
}
