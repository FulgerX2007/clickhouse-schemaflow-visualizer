package models

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"html"
	"log"
	"os"
	"regexp"
	"strings"
	"sync/atomic"

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
	// Kind is empty for an edge data flows along, and RelationLookup for a
	// table a materialized view reads without being triggered by it.
	Kind RelationKind
}

// RelationKind separates the two ways a view can read a table.
//
// An insert-triggered materialized view fires on inserts into one table only,
// the one ClickHouse records in dependencies_table. A table it JOINs, or a
// dictionary it calls dictGet on, is read on every insert but never causes
// one: rows inserted there flow nowhere until the trigger table gets a row.
// Drawn alike, the two would claim the view re-runs when a lookup changes.
type RelationKind string

const RelationLookup RelationKind = "lookup"

type TableInfo struct {
	Name       string
	Database   string
	TotalRows  *uint64
	TotalBytes *uint64
	Engine     string
	Icon       string
	// Refresh is a refreshable materialized view's schedule, e.g. "EVERY 1 MINUTE".
	Refresh string
}

type ColumnInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Position uint64 `json:"position"`
	Comment  string `json:"comment"`
	// DefaultKind is DEFAULT, MATERIALIZED, ALIAS or EPHEMERAL; empty for a
	// plain column.
	DefaultKind       string `json:"default_kind,omitempty"`
	DefaultExpression string `json:"default_expression,omitempty"`
	CompressionCodec  string `json:"compression_codec,omitempty"`
}

type TableDetails struct {
	Name         string       `json:"name"`
	Database     string       `json:"database"`
	Engine       string       `json:"engine"`
	EngineFull   string       `json:"engine_full,omitempty"`
	PrimaryKey   string       `json:"primary_key"`
	SortingKey   string       `json:"sorting_key"`
	PartitionKey string       `json:"partition_key"`
	Comment      string       `json:"comment,omitempty"`
	TotalRows    *uint64      `json:"total_rows"`
	TotalBytes   *uint64      `json:"total_bytes"`
	Columns      []ColumnInfo `json:"columns"`
	// CreateQuery is the table's DDL, pretty-printed when the server has
	// formatQueryOrNull and as stored otherwise. AsSelect is a view's SELECT.
	CreateQuery     string `json:"create_query"`
	CreateFormatted bool   `json:"create_formatted"`
	AsSelect        string `json:"as_select,omitempty"`
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
	asSelect    string
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
	query := "SELECT create_table_query, as_select, engine_full, engine, database, name, loading_dependencies_database, loading_dependencies_table, dependencies_database, dependencies_table, total_rows, total_bytes FROM system.tables ORDER BY name"
	rows, err := c.conn.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query tables: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var tables []TableRelation
	// A view's edges are built after the loop: its source is recorded on the
	// source table's row, which may come later, and both ends are checked
	// against TableMetadata, which is complete only once every row is read.
	var views []pendingView
	viewSources := make(map[string][]string)
	if TableMetadata == nil {
		TableMetadata = make(map[string]TableInfo)
	}

	for rows.Next() {
		res := result{}
		database, table := "", ""
		var loadingDependenciesTable []string
		var loadingDependenciesDatabase []string
		var dependenciesDatabase []string
		var dependenciesTable []string
		if err := rows.Scan(&res.createQuery, &res.asSelect, &res.engineFull, &res.engine, &database, &table, &loadingDependenciesDatabase, &loadingDependenciesTable, &dependenciesDatabase, &dependenciesTable, &res.totalRows, &res.totalBytes); err != nil {
			return nil, fmt.Errorf("failed to scan table data: %v", err)
		}

		if !AllowedDatabase(database) {
			continue
		}

		if DatabasesData == nil {
			DatabasesData = make(map[string]map[string]string)
		}

		if DatabasesData[database] == nil {
			DatabasesData[database] = make(map[string]string)
		}

		fullTableName := database + "." + table
		var icon, refresh string

		// ClickHouse lists, on a table's row, the views that read from it.
		for i, dependent := range dependenciesTable {
			if i < len(dependenciesDatabase) {
				key := dependenciesDatabase[i] + "." + dependent
				viewSources[key] = append(viewSources[key], fullTableName)
			}
		}

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
			icon = `<i class="fa-solid fa-eye"></i>`
			DatabasesData[database][table] = generateTableListContent(icon, table, res.totalRows, res.totalBytes)
			refresh = refreshSchedule(res.createQuery)
			views = append(views, pendingView{
				Name:    fullTableName,
				Target:  viewTarget(res.createQuery),
				Icon:    icon,
				Select:  res.asSelect,
				Refresh: refresh,
			})
		} else if res.engine == "View" { // Plain (non-materialized) view
			icon = `<i class="fa-solid fa-glasses"></i>`
			DatabasesData[database][table] = generateTableListContent(icon, table, res.totalRows, res.totalBytes)
			views = append(views, pendingView{Name: fullTableName, Icon: icon, Select: res.asSelect, Plain: true})
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
			Refresh:    refresh,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating table rows: %v", err)
	}

	tables = append(tables, viewRelations(views, viewSources, TableMetadata)...)

	TableRelations = tables

	return tables, nil
}

// pendingView is a materialized or plain view whose edges wait for the whole
// of system.tables to be read.
type pendingView struct {
	Name   string // db.view
	Target string // db.table from "TO db.table"; empty for a view with an inner table
	Icon   string
	Select string // as_select
	// Refresh is set for a refreshable materialized view, Plain for a View.
	// Neither has a trigger table: ClickHouse records no dependency for them,
	// so their sources come from the SELECT alone.
	Refresh string
	Plain   bool
}

// viewRelations builds the source → view and view → target edges.
//
// A trigger source comes from ClickHouse's dependencies columns, not from the
// SELECT: the first "FROM " of "SELECT ... FROM (SELECT ... FROM db.src JOIN
// ...)" is followed by "(SELECT", which used to become a node of that name.
// Every other table the SELECT reads is found by parsing it. For an
// insert-triggered view those are lookups; a refreshable view and a plain view
// have no trigger, and everything they read feeds them. Every end must be a
// table in known, so a name the DDL split misreads, or one in a hidden
// database, is dropped rather than drawn as a node nothing can open.
func viewRelations(views []pendingView, sources map[string][]string, known map[string]TableInfo) []TableRelation {
	var out []TableRelation
	for _, v := range views {
		edges := 0
		linked := map[string]bool{v.Name: true, v.Target: true}
		add := func(src string, kind RelationKind) {
			if linked[src] {
				return
			}
			if _, ok := known[src]; !ok {
				return
			}
			linked[src] = true
			out = append(out, TableRelation{DependsOnTable: src, Table: v.Name, Icon: v.Icon, Kind: kind})
			edges++
		}

		for _, src := range sources[v.Name] {
			add(src, "")
		}
		reads, dictionaries := selectReads(v.Select, v.Name)
		readKind := RelationLookup
		if v.Refresh != "" || v.Plain {
			readKind = ""
		}
		for _, src := range reads {
			add(src, readKind)
		}
		for _, dictionary := range dictionaries {
			add(dictionary, RelationLookup)
		}

		if v.Target != "" {
			if _, ok := known[v.Target]; ok {
				out = append(out, TableRelation{DependsOnTable: v.Name, Table: v.Target, Icon: v.Icon})
				edges++
			} else {
				log.Printf("materialized view %s: target %q is not a known table, edge skipped", v.Name, v.Target)
			}
		}
		if edges == 0 {
			out = append(out, TableRelation{Table: v.Name, Icon: v.Icon})
		}
	}
	return out
}

// viewSource returns the table a materialized view reads from, as recorded in
// relations by viewRelations, or "" when none is known. A Distributed wrapper
// over the view is also an edge ending at it, pointing the other way, so it
// is skipped, and so is a lookup, which is read alongside the source rather
// than instead of it.
func viewSource(relations []TableRelation, metadata map[string]TableInfo, view string) string {
	for _, rel := range relations {
		if rel.Table != view || rel.DependsOnTable == "" || rel.Kind == RelationLookup {
			continue
		}
		if metadata[rel.DependsOnTable].Engine == "Distributed" {
			continue
		}
		return rel.DependsOnTable
	}
	return ""
}

// qualifiedName matches db.table with either part optionally backticked.
const qualifiedName = "(?:`[^`]+`|[A-Za-z0-9_$]+)\\.(?:`[^`]+`|[A-Za-z0-9_$]+)"

var (
	// The target always precedes the column list, so only the text before the
	// first "(" is searched. A refreshable view puts REFRESH, SETTINGS and
	// APPEND between the name and TO, which the old positional split missed.
	viewTargetPattern = regexp.MustCompile(`(?i)\bTO\s+(` + qualifiedName + `)`)
	refreshPattern    = regexp.MustCompile(`(?i)\bREFRESH\s+((?:EVERY|AFTER)\s+\d+\s+[A-Z]+(?:\s+OFFSET\s+\d+\s+[A-Z]+)?)`)
	// dictGet('db.dict', ...) and the other dictGet*/dictHas forms. The
	// dictionary is named by a string, so the SQL parser does not see it.
	dictionaryPattern = regexp.MustCompile(`(?i)\bdict(?:Get\w*|Has|IsIn)\s*\(\s*'([^']+)'`)
)

// viewTarget returns the db.table a view writes to, or "" for a view with an
// inner table.
func viewTarget(createQuery string) string {
	header := createQuery
	if i := strings.Index(header, "("); i >= 0 {
		header = header[:i]
	}
	match := viewTargetPattern.FindStringSubmatch(header)
	if match == nil {
		return ""
	}
	return strings.ReplaceAll(match[1], "`", "")
}

// refreshSchedule returns "EVERY 1 MINUTE" for a refreshable view, or "".
func refreshSchedule(createQuery string) string {
	match := refreshPattern.FindStringSubmatch(createQuery)
	if match == nil {
		return ""
	}
	return strings.ToUpper(strings.Join(strings.Fields(match[1]), " "))
}

// selectReads lists the tables a view's SELECT reads, through the same parser
// the Grafana feature uses — FROM, every JOIN and every subquery — plus the
// dictionaries it calls dictGet on. Unqualified names take the view's
// database. The parser does not know every ClickHouse join form — it refuses
// "ANTI LEFT JOIN" — so a SELECT it rejects falls back to the word after each
// FROM and JOIN. That can name an alias or a CTE, which viewRelations drops
// because it is not a known table.
func selectReads(selectQuery, view string) (tables, dictionaries []string) {
	if strings.TrimSpace(selectQuery) == "" {
		return nil, nil
	}
	database, _ := splitQualified(view)
	result := resolveQueryAST(normalizeQuery(selectQuery, nil), database, "")
	tables = result.Tables
	if result.ParseError != "" {
		seen := map[string]bool{}
		for _, match := range heuristicTablePattern.FindAllStringSubmatch(selectQuery, -1) {
			table := qualifyName(strings.Trim(match[1], "`\"'"), database)
			if table != "" && !seen[table] {
				seen[table] = true
				tables = append(tables, table)
			}
		}
	}
	for _, match := range dictionaryPattern.FindAllStringSubmatch(selectQuery, -1) {
		dictionaries = append(dictionaries, qualifyName(match[1], database))
	}
	return tables, dictionaries
}

// AllowedDatabase reports whether a database is one this app will show.
//
// Exported because it is a boundary, not a display filter: the handlers that
// take a database from the URL have to refuse the hidden ones too, or
// /api/table/system/query_log answers for a database the sidebar denies.
func AllowedDatabase(database string) bool {
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
	case strings.HasPrefix(columnType, "Nullable("):
		// Unwrap, and only when this really is a Nullable(...) wrapper.
		//
		// Matching on Contains instead recursed forever on any type that merely
		// mentions Nullable inside another wrapper — ClickHouse writes
		// "SimpleAggregateFunction(groupBitOr, Nullable(Bool))", where TrimPrefix
		// finds no prefix to cut, TrimSuffix removes the final ")" once, and the
		// next call receives a string it can no longer shorten. That is an
		// unbounded self-call: it overflows the stack, and a stack overflow is
		// not recoverable, so one request for such a table killed the whole
		// process. Requiring the prefix guarantees each call strips at least
		// nine characters and therefore terminates.
		inner := strings.TrimSuffix(strings.TrimPrefix(columnType, "Nullable("), ")")
		return c.simplifyColumnType(inner)
	case strings.Contains(columnType, "Nullable"):
		return "other"
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
		SELECT engine, engine_full, total_rows, total_bytes, primary_key, sorting_key, partition_key,
		       comment, create_table_query, as_select
		FROM system.tables
		WHERE database = ? AND name = ?
	`

	var engine, engineFull, primaryKey, sortingKey, partitionKey, comment, createQuery, asSelect string
	var totalRows, totalBytes *uint64

	row := c.conn.QueryRow(ctx, tableQuery, database, table)
	if err := row.Scan(&engine, &engineFull, &totalRows, &totalBytes, &primaryKey, &sortingKey, &partitionKey,
		&comment, &createQuery, &asSelect); err != nil {
		return nil, fmt.Errorf("failed to get table info: %v", err)
	}

	formatted, ok := c.formatCreateQuery(ctx, database, table)
	if ok {
		createQuery = formatted
	}

	// Get column information
	columnsQuery := `
		SELECT name, type, position, comment, default_kind, default_expression, compression_codec
		FROM system.columns
		WHERE database = ? AND table = ?
		ORDER BY position
	`

	rows, err := c.conn.Query(ctx, columnsQuery, database, table)
	if err != nil {
		return nil, fmt.Errorf("failed to query columns: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var columns []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		if err := rows.Scan(&col.Name, &col.Type, &col.Position, &col.Comment,
			&col.DefaultKind, &col.DefaultExpression, &col.CompressionCodec); err != nil {
			return nil, fmt.Errorf("failed to scan column: %v", err)
		}
		columns = append(columns, col)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating columns: %v", err)
	}

	return &TableDetails{
		Name:            table,
		Database:        database,
		Engine:          engine,
		EngineFull:      redactSecrets(engineFull),
		PrimaryKey:      primaryKey,
		SortingKey:      sortingKey,
		PartitionKey:    partitionKey,
		Comment:         comment,
		TotalRows:       totalRows,
		TotalBytes:      totalBytes,
		Columns:         columns,
		CreateQuery:     redactSecrets(createQuery),
		CreateFormatted: ok,
		AsSelect:        redactSecrets(asSelect),
	}, nil
}

// formatUnsupported is set once the server has refused formatQueryOrNull, so a
// ClickHouse older than the function is asked once rather than on every click.
var formatUnsupported atomic.Bool

// formatCreateQuery pretty-prints a table's DDL on the server. system.tables
// stores it on one line, which for a materialized view puts the whole SELECT
// on that line too. It is a separate query so that a server without the
// function costs the formatting and nothing else.
func (c *ClickHouseClient) formatCreateQuery(ctx context.Context, database, table string) (string, bool) {
	if formatUnsupported.Load() {
		return "", false
	}
	var formatted *string
	row := c.conn.QueryRow(ctx,
		"SELECT formatQueryOrNull(create_table_query) FROM system.tables WHERE database = ? AND name = ?",
		database, table)
	if err := row.Scan(&formatted); err != nil {
		if strings.Contains(err.Error(), "UNKNOWN_FUNCTION") || strings.Contains(err.Error(), "Unknown function") {
			formatUnsupported.Store(true)
		}
		log.Printf("formatting the DDL of %s.%s: %v", database, table, err)
		return "", false
	}
	if formatted == nil || *formatted == "" {
		return "", false
	}
	return *formatted, true
}

// secretPattern matches a quoted password: dictionary sources write
// PASSWORD 'x', named collections and settings write password = 'x'.
var secretPattern = regexp.MustCompile(`(?i)(\bpassword\b\s*=?\s*)'(?:[^'\\]|\\.)*'`)

// redactSecrets hides quoted passwords in DDL. ClickHouse masks them itself in
// system.tables unless display_secrets_in_show_and_select is on, but older
// servers do not, and the API is unauthenticated. Credentials passed to an
// engine as plain positional arguments (MySQL('host', 'db', 'tbl', 'user',
// 'pass')) carry no keyword and cannot be found this way.
func redactSecrets(ddl string) string {
	return secretPattern.ReplaceAllString(ddl, "${1}'[HIDDEN]'")
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
