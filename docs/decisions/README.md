# Architecture Decision Records (ADRs)

This folder records **significant, hard-to-reverse decisions** — one file per
decision. ADRs capture the context and consequences of a choice so future readers
(and AI agents) understand *why* the system is the way it is.

## When to write an ADR

Write one when a decision is costly to reverse or shapes the architecture:
- choosing a datastore, framework, protocol, or major dependency
- a cross-cutting pattern (auth model, error handling, concurrency approach)
- a deliberate trade-off someone will later ask "why did we do it this way?"

Do **not** write one for routine, easily-reversible changes.

## How to add one

1. Copy `adr-template.md` to `NNNN-short-title.md` (zero-padded, next number).
   The template lives beside this file; it is rendered from the docs-governance
   docs-governance skill's ADR template and has the
   sections **Status / Date / Deciders**, **Context**, **Decision**,
   **Consequences** (Positive, Negative / trade-offs, Follow-ups) and
   **Alternatives considered**.
2. Fill it in. Set status to `Proposed`, then `Accepted` once agreed.
3. Link it from `ARCHITECTURE.md` §8 if it affects the architecture.
   TODO: there is no `ARCHITECTURE.md` at the repository root today — the only
   root-level Markdown files are `README.md` and `CLAUDE.md` (verified by listing
   the repository root). Fix this step or the file it points at.
4. Never rewrite history: to reverse a decision, add a new ADR that
   *supersedes* the old one and update the old one's status.

## Index

| ADR | Title | Status |
|-----|-------|--------|
| — | _No ADRs recorded yet — `docs/decisions/` contains no `NNNN-*.md` files._ | — |

## Candidates to backfill

The decisions below are **visibly embodied in the code**, but no ADR exists for any of
them. (The untracked, git-ignored `memory-bank/` directory holds older working notes that
touch the renderer change, but it is not part of the repository and predates the current
Dagre implementation.) Each line
is a TODO for a human: the citations state what the code *does*; the *why*, the
deciders, and the alternatives that were weighed are not recorded and must not be
invented when the ADR is written.

- TODO: **Mermaid replaced by a hand-written Dagre + SVG renderer.** Commit
  `a296d03` (2026-05-13) is titled "refactor: replace Mermaid with Dagre + custom
  SVG renderer". `static/js/diagram.js` is that renderer — its header comment
  documents `renderDataFlow` / `renderRelationships` and states "No
  string-templating, no parser sanitisation"; layout comes from the bundled
  `static/js/vendor/dagre.min.js` (`static/js/diagram.js:59-60`, `:241`, `:251`,
  `:271`, `:413`, `:430`). `README.md:44` describes the frontend as "diagrams laid
  out with Dagre and rendered as inline SVG by a custom renderer (no Mermaid
  runtime)". Note for whoever writes this up: `.goreleaser.yaml:48` and `:75` still
  advertise "Mermaid.js diagrams" in the package description and RPM summary —
  stale metadata, not a second renderer.

- TODO: **Whole-schema cache in package-level vars with no invalidation.**
  `models/clickhouse.go:34-36` declares `DatabasesData`, `TableRelations` and
  `TableMetadata` as package-level vars; `models/clickhouse.go:201-203` returns the
  cached value whenever all three are non-nil, and fills them on the first miss —
  `TableMetadata` at `:217`, `DatabasesData` at `:234`, `TableRelations` at `:326`. `api/handlers.go:51-52` documents the behaviour ("populates an in-memory
  cache … on first call and reuses it for every subsequent request"). Nothing in
  the repository clears them, so a process restart is the only way to pick up a
  schema change. Not covered by the cache: `GetTableColumns`
  (`models/clickhouse.go:429`), `BuildColumnIndex` (`models/graph.go:114`) and
  `isDistributedTable` (`models/clickhouse.go:642`) query
  ClickHouse on every request.

- TODO: **DDL parsed positionally instead of with a SQL parser.** Relationship
  discovery splits `create_table_query` / `engine_full` on separators and indexes
  into the result rather than parsing SQL: `strings.Split(res.createQuery, " ")` at
  `models/clickhouse.go:246`, `:255`, `:264`, `:277`, `:291`;
  `strings.Split(res.engineFull, "'")` at `:278`;
  `strings.Split(res.createQuery, "FROM ")` at `:292` (then `:298` splits the
  remainder on `" "`); the same shape recurs in the materialized-view helpers at
  `:497` (`"FROM "`), `:511` (`"TO "`), `:522` (`strings.Fields`), `:551` (`","`)
  and `:564` (a `strings.SplitN` on `" AS "`); note that `:551` and `:564` split a derived
  substring of the SELECT list rather than `create_table_query` directly. No SQL parsing dependency is declared in `go.mod`.

- TODO: **Distributed tables excluded from column-level graphs.**
  `models/graph.go:323-325` aborts the relationships graph when the source table is
  Distributed ("source table %s.%s is Distributed, skipping"); `:348` only adds a
  destination table when it is not Distributed; `:425` and `:431` filter Distributed
  tables out of the source and destination ID lists. The check is
  `isDistributedTable` (`models/clickhouse.go:642-651`), which runs
  `SELECT engine FROM system.tables WHERE database = ? AND name = ?` per call and
  returns `false` on any scan error. Note that `models/graph.go:15` and `:24-25`
  still classify `Distributed` as its own `EngineType` for the table-level diagram,
  so the exclusion is specific to the column-level view.
