# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

ClickHouse Schema Flow Visualizer — a Go web app that connects to a ClickHouse instance, discovers table relationships by parsing `system.tables` metadata (CREATE queries, engine types, dependencies), and renders interactive flowchart diagrams (laid out with Dagre and drawn as inline SVG) showing data flow between tables, including column-level relationships with transformation expressions for materialized views.

The app is strictly read-only against ClickHouse: it only ever queries `system.tables` and `system.columns`.

## Development Commands

```bash
# Run locally (requires .env with ClickHouse connection)
go run main.go

# Build
go build -o clickhouse-schemaflow-visualizer .

# Checks before committing
go vet ./...
golangci-lint run
go test ./...           # api + models are covered; main + config have no tests
go test ./models -run TestName -v   # single test, once tests exist

# Run with test ClickHouse (creates ZooKeeper + ClickHouse + app)
docker-compose -f docker-compose.clickhouse-test.yml up -d

# Production Docker
docker-compose up -d
```

`models/clickhouse.go` is not gofmt-clean today — run `gofmt -w` only on the region you touched rather than `gofmt -w .`, to keep diffs reviewable.

## Architecture

**Single-binary Go server** (Gin) serving both the API and static frontend:

- `main.go` — entry point, loads `.env` config, creates ClickHouse client, registers routes, serves static files
- `api/handlers.go` — thin Gin wrappers; all logic lives in `models`. Six REST endpoints under `/api/`:
  - `GET /connection` — current ClickHouse connection info (host, port, user, database, secure flag); password is never exposed
  - `GET /databases` — all databases with their tables (cached after first query)
  - `GET /columns` — flat column index across all visible tables (used by the `Ctrl+K` / `⌘K` command palette)
  - `GET /dataflow/:database/:table` — table-level DAG (upstream sources + downstream materializations) for the selected table
  - `GET /relationships/:database/:table` — column-level DAG with transformation expressions on the edges
  - `GET /table/:database/:table` — column details for a single table (used by the inspector panel)

  Plus four routes for the optional Grafana column-usage feature (see below):
  - `GET /grafana/status` — is dashboard usage available, and if not why
  - `GET /grafana/usage/:database/:table` — per-column verdicts and the dashboards behind them
  - `GET /grafana/unused` — column verdicts across the schema; `?database=`, `?verdict=`
  - `POST /grafana/refresh` — **the only non-`GET` route**; rebuilds the in-process scan, debounced by `GRAFANA_CACHE_TTL`, and writes nothing to ClickHouse
- `models/clickhouse.go` — ClickHouse client + TLS setup, engine-aware relation discovery (`getTablesRelations`), MV SELECT parsing (`parseViewQuery`, `extractColumnMappings`, `extractBaseColumnName`), column-matching heuristics (`areColumnsRelated`)
- `models/graph.go` — the JSON graph payloads (`DataFlowGraph`, `RelationshipsGraph`, `ColumnIndexEntry`) and the builders behind the three graph endpoints
- `config/config.go` — **dead code**: a parallel env loader that nothing imports. `main.go` builds `models.Config` itself. Change `main.go` when adding a config value; either update or delete `config/` rather than assuming it's wired up.
- `static/` — frontend (vanilla HTML/CSS/JS); diagrams laid out with bundled Dagre (`static/js/vendor/dagre.min.js`) and rendered as inline SVG by `static/js/diagram.js`

### Relation discovery (`models/clickhouse.go`)

One query over `system.tables` builds three package-level caches in a single pass:

- `DatabasesData map[db]map[table]htmlLabel` — sidebar payload; the value is an **HTML fragment** (Font Awesome `<i>` icon + optional rows/size line) built by `generateTableListContent`. The table name is `html.EscapeString`d. `static/js/app.js` strips the leading `<i …></i>` back out (`addTableToList`), so the icon markup and that regex are coupled.
- `TableRelations []TableRelation` — flat `DependsOnTable → Table` edge list keyed by fully qualified `db.table` names
- `TableMetadata map[db.table]TableInfo` — engine, row/byte counts, icon

Per-engine parsing branches, all positional string splitting on `create_table_query` / `engine_full` (no SQL parser), so they are sensitive to ClickHouse's exact DDL formatting:

- `MergeTree`, `Replicated*` — leaf nodes; table name taken from `strings.Split(createQuery, " ")[2]`
- `Dictionary*` — edge comes from the `loading_dependencies_database` / `loading_dependencies_table` columns (not from parsing `SOURCE(...)`; the README says otherwise)
- `Distributed` — underlying table parsed out of `engine_full` by splitting on `'`, requiring ≥6 parts
- `MaterializedView` — two edges: `source → mv` (from `FROM `) and `mv → destination` (from `strings.Split(createQuery, " ")[5]`)
- anything else — a bare node with no edges

**Caching:** these three vars are populated on first use and never invalidated. The sidebar ↻ button only re-fetches `/api/databases`, which returns the same cached map — a **process restart is the only way to pick up ClickHouse schema changes**. `GetTableColumns`, `BuildColumnIndex`, and `isDistributedTable` are not cached and hit ClickHouse on every call.

The one component that *does* invalidate is `models.GrafanaIndex` (`models/grafana_index.go`), which owns the Grafana dashboard scan: mutex-guarded, TTL'd via `GRAFANA_CACHE_TTL`, and rebuildable through `POST /api/grafana/refresh` (the TTL doubles as that endpoint's debounce). A failed rescan keeps the previous good index rather than emptying it, because an empty usage index reads as "nothing uses anything". `LoadSchemaSnapshot` warms `getTablesRelations` before building, since that cache is otherwise filled lazily by the first `/api/databases` request and a background scan could run before any request arrives.

**Database filtering** is duplicated: `allowedDatabase()` in `clickhouse.go` and a hardcoded `NOT IN (...)` list in `BuildColumnIndex` in `graph.go`. Change both together or the command palette and sidebar disagree.

### Graph builders (`models/graph.go`)

- Node IDs are plain `database.table` strings — no hashing. `walkForward`/`walkBackward` traverse `TableRelations` from the selected table, deduping edges via a shared `seen` map and bounded by `maxRelationDepth` (50).
- `ClassifyEngine` collapses raw engine names into five `EngineType` values (`mergetree`, `replicated`, `distributed`, `mview`, `dictionary`). These strings are the styling contract with the frontend — they must stay in sync with `classifyEngine()` in `static/js/app.js`, the `glyphFor()` map, the legend in `static/html/index.html`, and the CSS classes in `static/css/styles.css`.
- `BuildRelationshipsGraph` branches on engine: MVs get real column mappings parsed from the stored SELECT (with the expression as the edge label); everything else falls back to `areColumnsRelated` name/type heuristics (exact match, `*_id` ↔ `id`, UUID and DateTime pairings), plus a single-column-to-single-column fallback.
- `Distributed` tables are deliberately **excluded** from relationship graphs (`isDistributedTable` guards), since they'd duplicate their local table's columns.
- Graph payloads are pure JSON and `diagram.js` builds SVG via `createElementNS` — no string templating and no sanitisation layer on this path. Keep it that way; don't reintroduce HTML-string assembly for diagram content.

### Frontend

- `static/html/index.html` is served through `html/template` (not `router.Static`) so asset URLs carry `?v={{.BuildID}}`, a per-process cache-buster. **Any new CSS/JS tag must include `?v={{.BuildID}}`** or it will be cached across upgrades. Everything under `/static` is served with `Cache-Control: no-cache`.
- `diagram.js` exposes `window.SchemaDiagram.renderDataFlow(container, graph, {onNodeClick})`, `.renderRelationships(container, graph, {onTableClick})` and `.renderDashboardUsage(container, usage, {engineType, hideUnconnected, onOpen})`; `app.js` owns sidebar, palette, inspector, and Export HTML.
- Export HTML inlines `commonDiagramCss()` from `app.js`, so diagram styling lives in two places — update both when changing node/edge appearance. An SVG `rect` with no `fill` paints **black**, so every transparent hit-target rule has to be in both copies; leaving `.rel-col-hit` out of the export copy turned every column row of an exported diagram into a black bar.

### Grafana column usage (optional)

Off unless `GRAFANA_URL`+`GRAFANA_TOKEN` or `GRAFANA_DASHBOARDS_DIR` is set. When off, the
UI **creates no DOM node for it at all** and issues no request beyond the single status
call — it is render-gated, not CSS-hidden. Two sections are built this way, by
`buildDashboardUsageSection()` and `buildUnusedSection()` in `app.js`, both called from
`loadGrafanaStatus()`; `sectionElements()` therefore tolerates their keys being absent.

Pipeline, in `models/`:

```
grafana.go       source (API or directory) → []Dashboard → panels + template variables
grafana_sql.go   expand ${vars} → AST parse (clickhouse-sql-parser) → heuristic fallback
usage.go         SchemaSnapshot + references → UsageIndex → four-state verdicts → report
grafana_index.go orchestration: async scan, TTL, refresh, state machine, mutex
```

Points that are easy to get wrong:

- **`parser.Walk` aborts the whole traversal** when the callback returns `false`, despite
  its doc comment saying it stops only that subtree. Never return `false` from it.
- **A template variable is never substituted with one of its values.** It becomes the
  marker `__gfvar__`; a table name carrying it is a *pattern* matching every table it
  could name. Picking one value would resolve `agg_${period}_distributed` to a single
  table and leave its siblings looking untouched.
- **The two lineage edge families point opposite ways.** `getTablesRelations` emits
  `{DependsOnTable: distributed, Table: local}` for a Distributed wrapper but
  `{DependsOnTable: source, Table: view}` for an MV, so propagation is per-engine.
- **MV propagation is table-level.** If anything downstream of a view is read, every
  column the view's SELECT touches is read. `extractColumnMappings` cannot be used for
  this: it resolves at most one source column per expression and leaves `SourceTable`
  empty, so `SELECT a + b AS c` would make `b` look droppable.
- **Verdicts require `state == ok`.** Any other state yields `no-coverage` for every
  column. An absent scan is indistinguishable from a Grafana where nothing is used. The
  Dashboards diagram makes the same distinction visually: a state other than `ok` renders
  a notice, never an empty diagram, because a picture with no lines in it reads as
  "nothing uses this table".
- Key columns and `Distributed` tables are never reported `unused`.
- The verdict pass costs **two** ClickHouse queries total (`BuildColumnIndex` plus one
  `system.tables` read), not one per table.
- **The Dashboards diagram is built for a schema read by hundreds of dashboards, not
  for the test fixture.** Drawn one row per panel with one card per dashboard, a table
  read by 26 dashboards across 89 panels is 3852px tall — the viewBox then scales it to
  a fifth of legible size, which reads as an empty tab. So above `DU_AUTO_PANEL_LIMIT`
  panels it collapses to one row per dashboard, caps at `DU_MAX_ROWS` (stating what was
  dropped), and fades edges past `DU_DENSE_EDGES`. `renderDashboardUsage` returns a
  `stats` object precisely because the picture at that size cannot be counted off, and
  `app.js` prints those totals above it.
- **`grafanaStatus` is fetched once at boot and a first scan outlives it.** 147
  dashboards take longer than page load, so anything gated on the cached state would
  say "scanning" forever. `syncGrafanaState` refreshes it from any endpoint that returns
  a state envelope, and `pollGrafanaStatus` watches a scan to completion.
- **The two lineage families differ in precision, and the diagram must not treat them
  alike.** `propagateDistributed` carries each column across a wrapper individually, so a
  `distributed:` reference names a real column and earns a per-column line.
  `propagateViews` is a cross product of every column a view's SELECT touches against
  every panel reading its destination — deliberately, so `SELECT a + b AS c` cannot make
  `b` look droppable — so an `mv:` reference is a claim about the *table*. Drawn
  per-column it produced 39 identical lines out of a 40-column table, burying the one
  column the panel actually named. `renderDashboardUsage` draws `mv:` claims as a single
  line off the table header and states the affected count in words; those columns are
  still `used`, and their dots still say so.
- **The Dashboards diagram adds no endpoint.** It is a second reading of the
  `/api/grafana/usage/:database/:table` payload the inspector already fetches into
  `currentUsage`: per-column `direct`/`derived` refs become the lines, and a ref present
  in the table-level `dashboards` list but in no column's refs becomes a line off the
  table header — a panel that reads the table without naming a resolvable column.
- **`renderDashboardUsage` never builds a URL.** `dashboard_url` comes from Grafana and is
  empty in directory mode, where `new URL("", origin)` would resolve to this app. The
  renderer hands the reference to `onOpen`, and `panelURL()` in `app.js` owns both the
  empty check and the `http`/`https` scheme check.

## Configuration

All via environment variables (or `.env` file), read in `main.go`:

| Variable | Default | Purpose |
|---|---|---|
| `CLICKHOUSE_HOST` | `localhost` | ClickHouse host |
| `CLICKHOUSE_PORT` | `9000` | Native protocol port |
| `CLICKHOUSE_USER` | `default` | |
| `CLICKHOUSE_PASSWORD` | (empty) | |
| `CLICKHOUSE_DATABASE` | `default` | |
| `CLICKHOUSE_SECURE` | `false` | Enable TLS |
| `CLICKHOUSE_SKIP_VERIFY` | `false` | Skip TLS cert verification |
| `CLICKHOUSE_CERT_PATH` | (empty) | Client certificate (with `CLICKHOUSE_KEY_PATH`) |
| `CLICKHOUSE_KEY_PATH` | (empty) | Client key |
| `CLICKHOUSE_CA_PATH` | (empty) | Custom CA bundle |
| `CLICKHOUSE_SERVER_NAME` | (empty) | TLS SNI / cert verification name |
| `SERVER_ADDR` | `:8080` | Listen address |
| `GIN_MODE` | `debug` | `debug` or `release` |
| `GRAFANA_URL` | (empty) | Grafana base URL; with `GRAFANA_TOKEN` enables API mode |
| `GRAFANA_TOKEN` | (empty) | Service-account token — never logged, never in a response |
| `GRAFANA_DASHBOARDS_DIR` | (empty) | Directory of dashboard JSON; enables directory mode with no credentials |
| `GRAFANA_SKIP_VERIFY` | `false` | Skip TLS verification for the Grafana API |
| `GRAFANA_TIMEOUT` | `30s` | Grafana HTTP timeout |
| `GRAFANA_CACHE_TTL` | `15m` | Scan reuse window; also the refresh debounce |
| `GRAFANA_DEFAULT_DATABASE` | `CLICKHOUSE_DATABASE` | Database assumed for unqualified table references |
| `GRAFANA_SNIPPET_CHARS` | `200` | Max SQL snippet length in responses; `0` omits them |

## Testing

There are no Go tests; the release workflow runs `go test ./...` (a no-op today) plus a build.

Manual verification uses `docker-compose.clickhouse-test.yml`, which boots ZooKeeper (`:2181`) and ClickHouse (`:9000` native, `:8123` HTTP, `default`/`default`) and seeds `scripts/clickhouse_test_engines.sql`. The seed creates `raw` (MergeTree + ReplicatedMergeTree with Distributed wrappers) and `aggregated` (ReplicatedAggregatingMergeTree + SummingMergeTree fed by Materialized Views), which between them exercise every parsing branch above. Re-seed with `docker compose -f docker-compose.clickhouse-test.yml down -v` then `up -d`.

`docker-compose.yml` reads the app's connection settings from `.env` (gitignored) and runs with `network_mode: host`.

**The test stack does not.** `docker-compose.clickhouse-test.yml` pins `CLICKHOUSE_*` in the service's `environment:` block, which overrides `env_file`, so `up` works whatever `.env` holds — a developer's `.env` normally points at a real ClickHouse, and aiming the test container at production would be both wrong and dangerous. `.env` is still read, marked `required: false`, for the settings the stack does not pin: `GRAFANA_*` and `GIN_MODE`. The app runs on a bridge network alongside `clickhouse-test-engines` and reaches it as host `clickhouse`.

## Release

Tagging `v*` triggers `.github/workflows/release.yml` (test + build, then GoReleaser per `.goreleaser.yaml`, including nfpm packages that use `scripts/pre*.sh` / `scripts/post*.sh`) and `docker-publish.yml` (image to `ghcr.io`).
