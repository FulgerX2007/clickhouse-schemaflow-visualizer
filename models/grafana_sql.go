package models

import (
	"regexp"
	"strings"

	"github.com/AfterShip/clickhouse-sql-parser/parser"
)

// varMarker replaces a Grafana template variable that could not be resolved to a
// concrete value. It is a legal SQL identifier, so it parses wherever a name,
// a column or a table-name fragment would.
//
// A table name carrying the marker is a *pattern*: agg_${period}_distributed
// becomes agg___gfvar___distributed, which matches every real agg_<x>_distributed
// table. That is not over-attribution — the dashboard genuinely reads whichever
// value the viewer picks, so every matching table is genuinely used.
const varMarker = "__gfvar__"

// Grafana's built-in variables are not template variables and have no declared
// values; they render as times, durations or names at query time.
var builtinVarValues = map[string]string{
	"__from":          "0",
	"__to":            "0",
	"__interval":      "60",
	"__interval_ms":   "60000",
	"__range_ms":      "60000",
	"__range_s":       "60",
	"__rate_interval": "60",
}

var (
	// ${name} and ${name:format}.
	braceVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::[A-Za-z0-9_]+)?\}`)
	// $name, excluding the $__ macro family, which the parser reads natively.
	bareVarPattern = regexp.MustCompile(`\$([A-Za-z][A-Za-z0-9_]*)`)
	// The table token after FROM or JOIN. A subquery starts with "(" and so is
	// deliberately not matched.
	tableRefPattern = regexp.MustCompile(`(?i)\b(from|join)(\s+)([A-Za-z0-9_$.{}:]*\$\{?[A-Za-z0-9_$.{}:]*)`)
	// A marker standing where an INTERVAL's operand belongs. ClickHouse requires
	// INTERVAL <number> <unit>, so the identifier the marker leaves behind is a
	// syntax error rather than an unknown name.
	intervalMarkerPattern = regexp.MustCompile(`(?i)\bINTERVAL\s+` + regexp.QuoteMeta(varMarker) +
		`(\s+(?:second|minute|hour|day|week|month|quarter|year)s?\b)?`)
)

// NormalizedQuery is a query rewritten so a SQL parser can read it.
type NormalizedQuery struct {
	SQL string
	// TablePattern is set when a table name became a pattern and must be matched
	// against the real schema rather than looked up directly.
	TablePattern bool
	// OpaqueVar is set when a variable was substituted somewhere other than a
	// table name. Whether that actually costs us anything depends on where it
	// landed: a variable in a WHERE clause changes no columns, while one in the
	// projection does. The AST pass makes that distinction, because it can see
	// the marker's position; this pass only records that it happened.
	OpaqueVar bool
	// Variables names every template variable that was substituted.
	Variables []string
}

// normalizeQuery substitutes Grafana template variables so the SQL parses.
//
// Variables are replaced by a marker rather than by one of their declared
// values, even when values are known. Picking a single value would resolve
// agg_${period}_distributed to exactly one table and leave that table's siblings
// looking untouched, and would resolve a variable column to one name while the
// others silently became droppable. The marker keeps every candidate in play.
//
// The $__timeFilter / $__interval_ms / $__conditionalAll macro family is left
// alone: the ClickHouse parser reads those natively, and rewriting them would
// risk losing the column names they carry as arguments.
func normalizeQuery(sql string, variables []TemplateVariable) NormalizedQuery {
	declared := make(map[string]bool, len(variables))
	for _, variable := range variables {
		declared[variable.Name] = true
	}

	result := NormalizedQuery{}
	seen := map[string]bool{}

	noteVariable := func(name string) {
		if !seen[name] {
			seen[name] = true
			result.Variables = append(result.Variables, name)
		}
	}

	// Table names first, so their substitutions can be recognised as patterns
	// rather than as opaque values.
	sql = tableRefPattern.ReplaceAllStringFunc(sql, func(match string) string {
		parts := tableRefPattern.FindStringSubmatch(match)
		keyword, gap, table := parts[1], parts[2], parts[3]

		rewritten, names, substituted := substituteVars(table, declared, true)
		if substituted {
			result.TablePattern = true
			for _, name := range names {
				noteVariable(name)
			}
		}
		return keyword + gap + rewritten
	})

	// Everything else: projections, filters, GROUP BY and the rest.
	sql, names, substituted := substituteVars(sql, declared, false)
	if substituted {
		result.OpaqueVar = true
		for _, name := range names {
			noteVariable(name)
		}
	}

	result.SQL = repairIntervalOperands(sql)
	return result
}

// repairIntervalOperands rewrites INTERVAL <marker> into a well-formed interval.
//
// A Grafana variable in that position holds a duration — "5" beside a literal
// unit, or "5 MINUTE" on its own — and the marker that replaces it is a bare
// identifier, which ClickHouse rejects outright. The concrete duration is
// irrelevant here: an interval operand names no column, and the only cost of
// leaving it malformed is that the whole query fails to parse and every column
// it reads goes unattributed.
func repairIntervalOperands(sql string) string {
	return intervalMarkerPattern.ReplaceAllStringFunc(sql, func(match string) string {
		parts := intervalMarkerPattern.FindStringSubmatch(match)
		if unit := parts[1]; unit != "" {
			// The unit is spelled out, so only the count was a variable. The
			// parser accepts only the singular spelling, so "days" becomes "day".
			return "INTERVAL 60 " + strings.TrimSuffix(strings.TrimSpace(unit), "s")
		}
		return "INTERVAL 60 SECOND"
	})
}

// substituteVars replaces template variables in one fragment. inTable suppresses
// the built-in macro values, which are never part of a table name.
func substituteVars(fragment string, declared map[string]bool, inTable bool) (string, []string, bool) {
	names := []string{}
	substituted := false

	record := func(name string) {
		names = append(names, name)
		substituted = true
	}

	fragment = braceVarPattern.ReplaceAllStringFunc(fragment, func(match string) string {
		name := braceVarPattern.FindStringSubmatch(match)[1]

		if !inTable {
			if value, builtin := builtinVarValues[name]; builtin {
				return value
			}
			if strings.HasPrefix(name, "__") {
				// An unrecognised built-in: a name is the safest rendering.
				return varMarker
			}
		}

		record(name)
		return varMarker
	})

	fragment = bareVarPattern.ReplaceAllStringFunc(fragment, func(match string) string {
		name := bareVarPattern.FindStringSubmatch(match)[1]

		// $__timeFilter and friends parse natively; leave them exactly as they are.
		if strings.HasPrefix(name, "__") || !declared[name] {
			return match
		}

		record(name)
		return varMarker
	})

	return fragment, names, substituted
}

// IsTablePattern reports whether a table name came out of variable expansion and
// must be matched against the schema rather than looked up.
func IsTablePattern(name string) bool {
	return strings.Contains(name, varMarker)
}

// TablePatternMatches reports whether a concrete table name is covered by a
// pattern. The marker stands for one name segment, so it never crosses the dot
// that separates a database from a table.
func TablePatternMatches(pattern, name string) bool {
	if !IsTablePattern(pattern) {
		return strings.EqualFold(pattern, name)
	}

	expression := "^"
	for i, part := range strings.Split(pattern, varMarker) {
		if i > 0 {
			expression += `[^.]*`
		}
		expression += regexp.QuoteMeta(part)
	}
	expression += "$"

	matcher, err := regexp.Compile("(?i)" + expression)
	if err != nil {
		return false
	}
	return matcher.MatchString(name)
}

// ─── AST resolution ──────────────────────────────────────────────────────────

// Confidence records how firmly a reference was attributed to a table. Only an
// exact reference can license an "unused" verdict downstream.
type Confidence string

const (
	// ConfidenceExact means the AST tied the column to exactly one table.
	ConfidenceExact Confidence = "exact"
	// ConfidenceHeuristic means the attribution is a guess: an unqualified
	// column in a multi-table query, or a query that had to be read by the
	// fallback resolver.
	ConfidenceHeuristic Confidence = "heuristic"
)

// QueryReference is one column of one table, as read by one query.
type QueryReference struct {
	Database   string
	Table      string
	Column     string
	Confidence Confidence
	Snippet    string
}

// QueryParseResult is everything one query yields.
type QueryParseResult struct {
	References []QueryReference
	// Tables lists every table the query reads, qualified, even when no column
	// could be resolved. It is what separates "this table was referenced but we
	// could not read the columns" (unknown) from "nothing references this table"
	// (no coverage).
	Tables []string
	// SelectStar records a projection this analysis cannot enumerate: *, t.*,
	// * EXCEPT (…) or COLUMNS('…'). Every column of the tables involved must
	// then be treated as possibly-read.
	SelectStar bool
	// OpaqueColumn records a template variable standing where a column name
	// belongs, which has the same consequence as SelectStar for that table.
	OpaqueColumn bool
	ParseError   string
}

// resolveQueryAST parses a normalised query and attributes its columns.
func resolveQueryAST(normalized NormalizedQuery, defaultDatabase string, snippet string) QueryParseResult {
	result := QueryParseResult{}

	statements, err := parser.NewParser(normalized.SQL).ParseStmts()
	if err != nil {
		result.ParseError = err.Error()
		return result
	}

	scope := newQueryScope(defaultDatabase)
	for _, statement := range statements {
		scope.collectSources(statement)
	}
	for _, statement := range statements {
		scope.collectColumns(statement)
	}

	result.Tables = scope.tables()
	result.SelectStar = scope.selectStar
	result.OpaqueColumn = scope.opaqueColumn
	result.References = scope.references(snippet)
	return result
}

// queryScope accumulates the tables a query reads and the columns it names.
type queryScope struct {
	defaultDatabase string

	// qualified maps an alias or table name to its qualified db.table.
	qualified map[string]string
	// real is the set of qualified tables that are actual tables rather than
	// CTEs. Order is preserved for stable output.
	real      []string
	realSeen  map[string]bool
	cteNames  map[string]bool
	excluded  map[*parser.Ident]bool
	columnRef []columnRef

	selectStar   bool
	opaqueColumn bool
}

type columnRef struct {
	qualifier string // alias or table, empty when the column was unqualified
	column    string
}

func newQueryScope(defaultDatabase string) *queryScope {
	return &queryScope{
		defaultDatabase: defaultDatabase,
		qualified:       map[string]string{},
		realSeen:        map[string]bool{},
		cteNames:        map[string]bool{},
		excluded:        map[*parser.Ident]bool{},
	}
}

// collectSources records CTE names, table references and aliases, and marks the
// identifiers that name them so the column pass does not mistake them for
// columns.
func (s *queryScope) collectSources(node parser.Expr) {
	parser.Walk(node, func(current parser.Expr) bool {
		switch typed := current.(type) {
		case *parser.CTEStmt:
			// The parser stores the CTE's name in Expr and its body in Alias.
			if name, ok := typed.Expr.(*parser.Ident); ok {
				s.cteNames[strings.ToLower(name.Name)] = true
				s.excluded[name] = true
			}

		case *parser.TableIdentifier:
			s.addTable(typed)

		case *parser.AliasExpr:
			alias := identName(typed.Alias)
			if alias == "" {
				return true
			}
			if identifier, ok := typed.Alias.(*parser.Ident); ok {
				s.excluded[identifier] = true
			}
			if table, ok := typed.Expr.(*parser.TableIdentifier); ok {
				s.qualified[strings.ToLower(alias)] = s.qualify(table)
			}

		case *parser.SelectItem:
			// A projection alias is a new name, not a column that was read.
			if typed.Alias != nil {
				s.excluded[typed.Alias] = true
			}

		case *parser.FunctionExpr:
			if typed.Name != nil {
				s.excluded[typed.Name] = true
				// COLUMNS('regex') selects a set this analysis cannot enumerate.
				if strings.EqualFold(typed.Name.Name, "COLUMNS") {
					s.selectStar = true
				}
			}
		}
		return true
	})
}

func (s *queryScope) addTable(table *parser.TableIdentifier) {
	if table.Table == nil {
		return
	}
	s.excluded[table.Table] = true
	if table.Database != nil {
		s.excluded[table.Database] = true
	}

	name := strings.ToLower(table.Table.Name)
	// A reference to a CTE is not a table read.
	if table.Database == nil && s.cteNames[name] {
		return
	}

	full := s.qualify(table)
	s.qualified[name] = full
	if !s.realSeen[full] {
		s.realSeen[full] = true
		s.real = append(s.real, full)
	}
}

// qualify names a table, supplying the configured default database when the
// query left it out.
func (s *queryScope) qualify(table *parser.TableIdentifier) string {
	database := s.defaultDatabase
	if table.Database != nil && table.Database.Name != "" {
		database = table.Database.Name
	}
	if database == "" {
		return table.Table.Name
	}
	return database + "." + table.Table.Name
}

// collectColumns records every identifier that stands where a column belongs.
func (s *queryScope) collectColumns(node parser.Expr) {
	parser.Walk(node, func(current parser.Expr) bool {
		switch typed := current.(type) {
		case *parser.Path:
			s.addPath(typed)
			// Returning false from a WalkFunc aborts the entire traversal in this
			// library, not just this subtree, so the path's own identifiers are
			// suppressed by marking them instead. Walk is pre-order, so they are
			// excluded before they are visited.
			for _, field := range typed.Fields {
				s.excluded[field] = true
			}

		case *parser.Ident:
			s.addIdent(typed)
		}
		return true
	})
}

func (s *queryScope) addPath(path *parser.Path) {
	if len(path.Fields) == 0 {
		return
	}

	column := path.Fields[len(path.Fields)-1].Name
	qualifier := ""
	if len(path.Fields) > 1 {
		qualifier = path.Fields[len(path.Fields)-2].Name
	}

	if column == "*" {
		// t.* enumerates the whole table just as * does.
		s.selectStar = true
		return
	}
	if column == varMarker {
		s.opaqueColumn = true
		return
	}
	s.columnRef = append(s.columnRef, columnRef{qualifier: qualifier, column: column})
}

func (s *queryScope) addIdent(identifier *parser.Ident) {
	if s.excluded[identifier] {
		return
	}

	switch identifier.Name {
	case "":
		return
	case "*":
		s.selectStar = true
		return
	case varMarker:
		s.opaqueColumn = true
		return
	}

	s.columnRef = append(s.columnRef, columnRef{column: identifier.Name})
}

func (s *queryScope) tables() []string {
	return s.real
}

// references attributes each column to a table.
//
// A qualified column resolves through the alias map and is exact. An unqualified
// column in a single-table query is exact too, because there is nowhere else it
// could come from. In a multi-table query the AST cannot say which table owns it,
// so it is attributed to every candidate at heuristic confidence — over-reporting
// usage, which keeps a column alive, rather than under-reporting it, which would
// mark a live column droppable.
func (s *queryScope) references(snippet string) []QueryReference {
	if len(s.real) == 0 {
		return nil
	}

	references := []QueryReference{}
	seen := map[string]bool{}

	add := func(table, column string, confidence Confidence) {
		database, name := splitQualified(table)
		key := database + "\x00" + name + "\x00" + strings.ToLower(column)
		if seen[key] {
			return
		}
		seen[key] = true
		references = append(references, QueryReference{
			Database:   database,
			Table:      name,
			Column:     column,
			Confidence: confidence,
			Snippet:    snippet,
		})
	}

	for _, reference := range s.columnRef {
		if reference.qualifier != "" {
			if table, ok := s.qualified[strings.ToLower(reference.qualifier)]; ok {
				if s.realSeen[table] {
					add(table, reference.column, ConfidenceExact)
				}
				continue
			}
			// An unknown qualifier is most likely a CTE column; it cannot be
			// attributed to a real table.
			continue
		}

		if len(s.real) == 1 {
			add(s.real[0], reference.column, ConfidenceExact)
			continue
		}
		for _, table := range s.real {
			add(table, reference.column, ConfidenceHeuristic)
		}
	}

	return references
}

func splitQualified(name string) (string, string) {
	if index := strings.LastIndex(name, "."); index >= 0 {
		return name[:index], name[index+1:]
	}
	return "", name
}

func identName(node parser.Expr) string {
	if identifier, ok := node.(*parser.Ident); ok {
		return identifier.Name
	}
	return ""
}

// truncateSnippet trims a query to the configured evidence length.
func truncateSnippet(sql string, limit int) string {
	if limit <= 0 {
		return ""
	}
	compact := strings.Join(strings.Fields(sql), " ")
	if len(compact) <= limit {
		return compact
	}
	return compact[:limit] + "…"
}
