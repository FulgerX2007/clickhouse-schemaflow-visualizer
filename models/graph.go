package models

import (
	"context"
	"fmt"
	"strings"
)

// EngineType is the semantic family used by the frontend to colour nodes.
type EngineType string

const (
	EngineMergeTree   EngineType = "mergetree"
	EngineReplicated  EngineType = "replicated"
	EngineDistributed EngineType = "distributed"
	EngineMView       EngineType = "mview"
	EngineDictionary  EngineType = "dictionary"
)

// ClassifyEngine maps a raw engine name (e.g. "SummingMergeTree", "ReplicatedAggregatingMergeTree")
// to one of the five semantic families used for visual styling.
func ClassifyEngine(engine string) EngineType {
	switch {
	case engine == "Distributed":
		return EngineDistributed
	case engine == "MaterializedView":
		return EngineMView
	case strings.HasPrefix(engine, "Dictionary"):
		return EngineDictionary
	case strings.HasPrefix(engine, "Replicated"):
		return EngineReplicated
	default:
		return EngineMergeTree
	}
}

// GraphNode is a table-level node in a data-flow graph.
type GraphNode struct {
	ID         string     `json:"id"`
	Database   string     `json:"database"`
	Table      string     `json:"table"`
	Engine     string     `json:"engine,omitempty"`
	EngineType EngineType `json:"engine_type"`
	TotalRows  *uint64    `json:"total_rows,omitempty"`
	TotalBytes *uint64    `json:"total_bytes,omitempty"`
	Current    bool       `json:"current,omitempty"`
}

// GraphEdge is a directed edge between two GraphNodes.
type GraphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
}

// DataFlowGraph is the structured payload returned by /api/v2/dataflow/:db/:table.
type DataFlowGraph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// RelColumn is a column inside a table in a column-level relationships graph.
type RelColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// RelTableRole is one of "source", "current", "destination".
type RelTableRole string

const (
	RelRoleSource      RelTableRole = "source"
	RelRoleCurrent     RelTableRole = "current"
	RelRoleDestination RelTableRole = "destination"
)

// RelTable is a table participating in a relationships graph, with its columns.
type RelTable struct {
	ID         string       `json:"id"`
	Database   string       `json:"database"`
	Table      string       `json:"table"`
	Engine     string       `json:"engine,omitempty"`
	EngineType EngineType   `json:"engine_type"`
	Role       RelTableRole `json:"role"`
	Columns    []RelColumn  `json:"columns"`
}

// RelEdge is a column-to-column edge with an optional transformation expression.
type RelEdge struct {
	FromTable  string `json:"from_table"`
	FromColumn string `json:"from_column"`
	ToTable    string `json:"to_table"`
	ToColumn   string `json:"to_column"`
	Expression string `json:"expression,omitempty"`
}

// RelationshipsGraph is the structured payload returned by /api/v2/relationships/:db/:table.
type RelationshipsGraph struct {
	Tables []RelTable `json:"tables"`
	Edges  []RelEdge  `json:"edges"`
}

// BuildDataFlowGraph walks table relations forward and backward from the selected
// table and returns a structured DAG of nodes and edges. No string-templating,
// no sanitisation — the frontend renders this with a real layout engine.
func (c *ClickHouseClient) BuildDataFlowGraph(dbName, tableName string) (DataFlowGraph, error) {
	current := dbName + "." + tableName

	relations, err := c.getTablesRelations()
	if err != nil {
		return DataFlowGraph{}, fmt.Errorf("failed to get table relations: %w", err)
	}

	nodes := make(map[string]GraphNode)
	edges := make(map[string]GraphEdge)
	addNode(nodes, current, true)

	seen := make(map[string]bool)
	walkForward(nodes, edges, relations, current, seen, 0)
	walkBackward(nodes, edges, relations, current, seen, 0)

	g := DataFlowGraph{Nodes: make([]GraphNode, 0, len(nodes)), Edges: make([]GraphEdge, 0, len(edges))}
	for _, n := range nodes {
		g.Nodes = append(g.Nodes, n)
	}
	for _, e := range edges {
		g.Edges = append(g.Edges, e)
	}
	return g, nil
}

func addNode(nodes map[string]GraphNode, id string, current bool) {
	if _, ok := nodes[id]; ok {
		if current {
			n := nodes[id]
			n.Current = true
			nodes[id] = n
		}
		return
	}
	n := GraphNode{ID: id, Current: current}
	parts := strings.SplitN(id, ".", 2)
	if len(parts) == 2 {
		n.Database, n.Table = parts[0], parts[1]
	} else {
		n.Table = id
	}
	if meta, ok := TableMetadata[id]; ok {
		n.Engine = meta.Engine
		n.EngineType = ClassifyEngine(meta.Engine)
		n.TotalRows = meta.TotalRows
		n.TotalBytes = meta.TotalBytes
	} else {
		n.EngineType = EngineMergeTree
	}
	nodes[id] = n
}

func walkForward(nodes map[string]GraphNode, edges map[string]GraphEdge, relations []TableRelation, table string, seen map[string]bool, depth int) {
	if depth >= maxRelationDepth || table == "" {
		return
	}
	for _, rel := range relations {
		if rel.DependsOnTable != table {
			continue
		}
		addNode(nodes, rel.DependsOnTable, false)
		addNode(nodes, rel.Table, false)
		key := rel.DependsOnTable + "->" + rel.Table
		if seen[key] {
			continue
		}
		seen[key] = true
		edges[key] = GraphEdge{From: rel.DependsOnTable, To: rel.Table}
		walkForward(nodes, edges, relations, rel.Table, seen, depth+1)
	}
}

func walkBackward(nodes map[string]GraphNode, edges map[string]GraphEdge, relations []TableRelation, table string, seen map[string]bool, depth int) {
	if depth >= maxRelationDepth || table == "" {
		return
	}
	for _, rel := range relations {
		if rel.Table != table || rel.DependsOnTable == "" {
			continue
		}
		addNode(nodes, rel.DependsOnTable, false)
		addNode(nodes, rel.Table, false)
		key := rel.DependsOnTable + "->" + rel.Table
		if !seen[key] {
			seen[key] = true
			edges[key] = GraphEdge{From: rel.DependsOnTable, To: rel.Table}
		}
		walkBackward(nodes, edges, relations, rel.DependsOnTable, seen, depth+1)
	}
}

// BuildRelationshipsGraph returns a column-level graph for the selected table.
// For Materialized Views it parses the SELECT to extract source→target column
// mappings with transformation expressions. For regular tables it uses
// areColumnsRelated() heuristics to match columns against the table's direct
// dependencies.
func (c *ClickHouseClient) BuildRelationshipsGraph(dbName, tableName string) (RelationshipsGraph, error) {
	current, err := c.GetTableColumns(dbName, tableName)
	if err != nil {
		return RelationshipsGraph{}, fmt.Errorf("failed to get table details: %w", err)
	}

	ctx := context.Background()

	var createQuery, engine string
	if err := c.conn.QueryRow(ctx,
		"SELECT create_table_query, engine FROM system.tables WHERE database = ? AND name = ?",
		dbName, tableName,
	).Scan(&createQuery, &engine); err != nil {
		return RelationshipsGraph{}, fmt.Errorf("failed to get table info: %w", err)
	}

	g := RelationshipsGraph{}
	addTable := func(role RelTableRole, db, name string, cols []ColumnInfo, eng string) {
		id := db + "." + name
		t := RelTable{
			ID:         id,
			Database:   db,
			Table:      name,
			Engine:     eng,
			EngineType: ClassifyEngine(eng),
			Role:       role,
			Columns:    make([]RelColumn, 0, len(cols)),
		}
		for _, col := range cols {
			t.Columns = append(t.Columns, RelColumn{Name: col.Name, Type: c.simplifyColumnType(col.Type)})
		}
		g.Tables = append(g.Tables, t)
	}

	if engine == "MaterializedView" {
		return c.buildMVRelationshipsGraph(dbName, tableName, current, createQuery, addTable, &g)
	}
	return c.buildRegularRelationshipsGraph(dbName, tableName, current, engine, addTable, &g)
}

func (c *ClickHouseClient) buildMVRelationshipsGraph(
	dbName, tableName string,
	current *TableDetails,
	createQuery string,
	addTable func(role RelTableRole, db, name string, cols []ColumnInfo, eng string),
	g *RelationshipsGraph,
) (RelationshipsGraph, error) {
	selectQuery, sourceTable, destTable := c.parseViewQuery(createQuery)

	if destTable == "" {
		if relations, err := c.getTablesRelations(); err == nil {
			fullName := dbName + "." + tableName
			for _, rel := range relations {
				if rel.DependsOnTable != fullName || rel.Table == "" {
					continue
				}
				parts := strings.Split(rel.Table, ".")
				if len(parts) != 2 {
					continue
				}
				if c.isDistributedTable(parts[0], parts[1]) {
					continue
				}
				destTable = rel.Table
				break
			}
		}
	}

	if sourceTable == "" {
		return *g, nil
	}

	srcDb, srcName := dbName, sourceTable
	if strings.Contains(sourceTable, ".") {
		parts := strings.Split(sourceTable, ".")
		srcDb, srcName = parts[0], parts[1]
	}
	if c.isDistributedTable(srcDb, srcName) {
		return *g, fmt.Errorf("source table %s.%s is Distributed, skipping", srcDb, srcName)
	}

	srcDetails, err := c.GetTableColumns(srcDb, srcName)
	if err != nil {
		return *g, nil
	}

	mappings := c.extractColumnMappings(selectQuery, srcDetails.Columns)

	addTable(RelRoleSource, srcDb, srcName, srcDetails.Columns, srcDetails.Engine)
	addTable(RelRoleCurrent, dbName, tableName, current.Columns, current.Engine)

	srcID := srcDb + "." + srcName
	currID := dbName + "." + tableName

	var destDetails *TableDetails
	var destID string
	if destTable != "" {
		destDb, destName := dbName, destTable
		if strings.Contains(destTable, ".") {
			parts := strings.Split(destTable, ".")
			destDb, destName = parts[0], parts[1]
		}
		if !c.isDistributedTable(destDb, destName) {
			if dd, err := c.GetTableColumns(destDb, destName); err == nil {
				destDetails = dd
				destID = destDb + "." + destName
				addTable(RelRoleDestination, destDb, destName, dd.Columns, dd.Engine)
			}
		}
	}

	for _, m := range mappings {
		g.Edges = append(g.Edges, RelEdge{
			FromTable: srcID, FromColumn: m.SourceColumn,
			ToTable: currID, ToColumn: m.TargetColumn,
			Expression: m.Transformation,
		})

		if destDetails == nil {
			continue
		}

		matched := false
		for _, destCol := range destDetails.Columns {
			if strings.EqualFold(destCol.Name, m.TargetColumn) {
				g.Edges = append(g.Edges, RelEdge{
					FromTable: currID, FromColumn: m.TargetColumn,
					ToTable: destID, ToColumn: destCol.Name,
				})
				matched = true
				break
			}
		}
		if !matched {
			for _, mvCol := range current.Columns {
				if !strings.EqualFold(mvCol.Name, m.TargetColumn) {
					continue
				}
				for _, destCol := range destDetails.Columns {
					if c.areColumnsRelated(mvCol, destCol) {
						g.Edges = append(g.Edges, RelEdge{
							FromTable: currID, FromColumn: mvCol.Name,
							ToTable: destID, ToColumn: destCol.Name,
						})
						matched = true
						break
					}
				}
				break
			}
		}
		if !matched && len(current.Columns) == 1 && len(destDetails.Columns) == 1 {
			g.Edges = append(g.Edges, RelEdge{
				FromTable: currID, FromColumn: m.TargetColumn,
				ToTable: destID, ToColumn: destDetails.Columns[0].Name,
			})
		}
	}

	return *g, nil
}

func (c *ClickHouseClient) buildRegularRelationshipsGraph(
	dbName, tableName string,
	current *TableDetails,
	engine string,
	addTable func(role RelTableRole, db, name string, cols []ColumnInfo, eng string),
	g *RelationshipsGraph,
) (RelationshipsGraph, error) {
	relations, err := c.getTablesRelations()
	if err != nil {
		return *g, fmt.Errorf("failed to get table relations: %w", err)
	}

	fullName := dbName + "." + tableName
	var sourceIDs, destIDs []string
	for _, rel := range relations {
		if rel.Table == fullName && rel.DependsOnTable != "" {
			parts := strings.Split(rel.DependsOnTable, ".")
			if len(parts) == 2 && !c.isDistributedTable(parts[0], parts[1]) {
				sourceIDs = append(sourceIDs, rel.DependsOnTable)
			}
		}
		if rel.DependsOnTable == fullName && rel.Table != "" {
			parts := strings.Split(rel.Table, ".")
			if len(parts) == 2 && !c.isDistributedTable(parts[0], parts[1]) {
				destIDs = append(destIDs, rel.Table)
			}
		}
	}

	loadCols := func(id string) (*TableDetails, bool) {
		parts := strings.Split(id, ".")
		if len(parts) != 2 {
			return nil, false
		}
		td, err := c.GetTableColumns(parts[0], parts[1])
		if err != nil {
			return nil, false
		}
		return td, true
	}

	// Sources first → current → destinations to give the renderer a stable column order.
	srcDetailsMap := make(map[string]*TableDetails)
	for _, id := range sourceIDs {
		if td, ok := loadCols(id); ok {
			srcDetailsMap[id] = td
			parts := strings.Split(id, ".")
			addTable(RelRoleSource, parts[0], parts[1], td.Columns, td.Engine)
		}
	}
	addTable(RelRoleCurrent, dbName, tableName, current.Columns, engine)

	destDetailsMap := make(map[string]*TableDetails)
	for _, id := range destIDs {
		if td, ok := loadCols(id); ok {
			destDetailsMap[id] = td
			parts := strings.Split(id, ".")
			addTable(RelRoleDestination, parts[0], parts[1], td.Columns, td.Engine)
		}
	}

	currID := fullName
	for _, srcID := range sourceIDs {
		td, ok := srcDetailsMap[srcID]
		if !ok {
			continue
		}
		matched := false
		for _, srcCol := range td.Columns {
			for _, currCol := range current.Columns {
				if c.areColumnsRelated(srcCol, currCol) {
					g.Edges = append(g.Edges, RelEdge{
						FromTable: srcID, FromColumn: srcCol.Name,
						ToTable: currID, ToColumn: currCol.Name,
					})
					matched = true
				}
			}
		}
		if !matched && len(td.Columns) == 1 && len(current.Columns) == 1 {
			g.Edges = append(g.Edges, RelEdge{
				FromTable: srcID, FromColumn: td.Columns[0].Name,
				ToTable: currID, ToColumn: current.Columns[0].Name,
			})
		}
	}

	for _, destID := range destIDs {
		td, ok := destDetailsMap[destID]
		if !ok {
			continue
		}
		matched := false
		for _, currCol := range current.Columns {
			for _, destCol := range td.Columns {
				if c.areColumnsRelated(currCol, destCol) {
					g.Edges = append(g.Edges, RelEdge{
						FromTable: currID, FromColumn: currCol.Name,
						ToTable: destID, ToColumn: destCol.Name,
					})
					matched = true
				}
			}
		}
		if !matched && len(current.Columns) == 1 && len(td.Columns) == 1 {
			g.Edges = append(g.Edges, RelEdge{
				FromTable: currID, FromColumn: current.Columns[0].Name,
				ToTable: destID, ToColumn: td.Columns[0].Name,
			})
		}
	}

	return *g, nil
}
