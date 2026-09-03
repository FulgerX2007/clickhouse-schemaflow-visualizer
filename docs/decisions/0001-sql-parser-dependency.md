# 1. Parse dashboard SQL with clickhouse-sql-parser

- **Status:** Accepted
- **Date:** 2026-09-02
- **Context:** Grafana column-usage feature (`docs/plans/20260902-grafana-column-usage.md`)

## Context

The column-usage feature has to decide which columns of which tables a Grafana dashboard
reads. The repository had four direct dependencies and a standing rule to "prefer the
standard library" (`.ai/rules.md`, Security restrictions), so adding a fifth needed
justification rather than preference.

Two options were measured against the reference corpus of 1177 SQL strings.

**Text matching only.** Regex the `FROM`/`JOIN` tables out, then look for each table's
known column names in the query text. No dependency, about 200 lines, and it always
produces an answer. But it cannot tell which side of a join owns an unqualified column,
and it cannot see a column reached through an alias or a CTE. 15% of the corpus has
subqueries, 11% CTEs, 7% joins.

**A real ClickHouse parser.** `github.com/AfterShip/clickhouse-sql-parser` — Go, no
runtime dependencies beyond the standard library, actively released.

## Decision

Use the parser, pinned at v0.5.6, with text matching as a per-query fallback.

The deciding evidence was a probe of 19 Grafana-shaped queries. The parser handles
`$__timeFilter`, `$host`, `$__interval_ms`, `$__conditionalAll`, CTEs, aliased joins,
`SELECT *`, `* EXCEPT`, `COLUMNS('regex')`, `FINAL`, `SETTINGS`, `ARRAY JOIN`,
`'%$x%'`, comments, `FORMAT` and `UNION ALL` natively. Only `${...}` brace syntax
defeats it, and that is handled by variable expansion before parsing.

Against the full corpus the parser reads **92.5%** of queries and produces 5994 exact
column references against 964 heuristic ones. The fallback covers the remaining 88.

## Consequences

- Column attribution is precise enough that an `unused` verdict is trustworthy. Text
  matching alone would have forced every joined table's columns to `unknown`.
- One new direct dependency, pinned. It pulls nothing else at runtime.
- `models/grafana_corpus_test.go` enforces the choice: it fails if the parse rate falls
  below 70%, which is the point at which the dependency stops earning its place.
- Use `parser.Walk`, never the 100-method `ASTVisitor`. Note that `Walk` aborts the
  **entire** traversal when the callback returns `false`, despite its doc comment.
