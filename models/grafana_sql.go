package models

import (
	"regexp"
	"strings"
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

	result.SQL = sql
	return result
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
