# API Documentation

Reference for this project's external interface(s).

> Keep in sync with the code: when an endpoint, message, or contract changes, update
> this folder in the same change (see `.ai/rules.md`).

Scope: the ClickHouse Schema Flow Visualizer exposes one HTTP surface — a small
read-only JSON API under `/api/` plus the two routes that serve the single-page
frontend. Every endpoint is a `GET`; the server has no write path and issues only
`SELECT` statements against `system.tables` and `system.columns`
(`models/clickhouse.go`, `models/graph.go`).

## Contract source of truth

- **Style:** REST-ish JSON over HTTP, served by Gin. Six `GET` endpoints are registered
  in the `/api` route group (`api/handlers.go:25-35`), plus `GET /` and `GET /static/*`
  for the frontend (`main.go:66-79`). There is no gRPC service, no GraphQL schema, and
  the binary accepts no CLI flags or subcommands (`main.go`).
- **Spec file:** TODO: no OpenAPI/Swagger document and no `.proto` file exist in the
  repository (searched for `*openapi*`, `*swagger*`, `*.proto`). The Go structs in
  `models/graph.go` and `models/clickhouse.go` are the only machine-readable contract —
  decide whether a spec should be added and generated from them.
- **Generated docs / UI:** TODO: none is served. The router registers only the six
  `/api/` routes (`api/handlers.go:26-34`), `GET /` and `router.Static("/static", "./static")`
  (`main.go:66-79`) — no Swagger UI, Redoc, or equivalent.

## Conventions

- **Versioning:** none. All routes are mounted flat under `/api/` with no version
  segment (`api/handlers.go:26`). Note a stale comment: `models/graph.go:56` and
  `models/graph.go:97` describe `DataFlowGraph` and `RelationshipsGraph` as the payloads
  of `/api/v2/dataflow/:db/:table` and `/api/v2/relationships/:db/:table`. Those `v2`
  paths **do not exist** — the registered paths are `/api/dataflow/:database/:table` and
  `/api/relationships/:database/:table` (`api/handlers.go:31-32`). TODO: no versioning or
  deprecation policy is documented anywhere in the repo.
- **Authentication:** none. No auth middleware, no `Authorization` header handling, and
  no API-key or token check appears anywhere in `main.go`, `api/`, `models/` or `config/`
  (verified by grep for `cors|middleware|authorization|rate.?limit` — zero hits). Every
  endpoint is anonymous, and the only middleware in the chain is what `gin.Default()`
  installs (logger + recovery) plus a `Cache-Control` setter (`main.go:51-66`).
  TODO: the repository does not state whether a reverse proxy, VPN, or network policy is
  expected to supply access control in front of the service.
- **Error format:** a single JSON object, `{"error": "<message>"}` — no error code, type,
  or trace id. Two statuses are produced:
  - `400 Bad Request` — `{"error": "database and table parameters are required"}` when a
    path parameter resolves to an empty string on the three `:database/:table` routes
    (`api/handlers.go:86-89`, `106-109`, `127-130`).
  - `500 Internal Server Error` — `{"error": err.Error()}` for any ClickHouse or parsing
    failure, with the underlying Go error string passed through verbatim
    (`api/handlers.go:57-62`, `74`, `93`, `113`, `134`). A table that does not exist also
    lands here, not on a `404`: `GetTableColumns` fails its `row.Scan` and returns an
    error (`models/clickhouse.go:429-446`).
  - There is no `404` handler for unknown API paths and no `405`; Gin's defaults apply.
- **Pagination / filtering:** none anywhere in the codebase. `GET /api/databases` and
  `GET /api/columns` each return their complete result set in one response
  (`models/clickhouse.go:349-359`, `models/graph.go:114-140`), and no handler reads a
  query string parameter (`api/handlers.go`). Filtering is done client-side by
  `static/js/app.js`. There is no rate limiting.
- **CORS:** no CORS middleware is registered; the API is consumed same-origin by the
  bundled frontend (`static/js/app.js:160,172,333,334,388,746`).
- **Caching (response headers):** the `Cache-Control: no-cache` middleware is registered
  *after* the API routes and *before* the static routes (`main.go:56` vs `main.go:65-66`,
  `74`), so it applies to `GET /` and `/static/*` only — `/api/*` responses carry no
  `Cache-Control` header. This follows from Gin's model: `RouterGroup.handle` fixes a
  route's handler chain via `combineHandlers` at registration time (gin v1.10.0
  `routergroup.go:88`), so middleware added by a later `router.Use` cannot reach routes
  already registered.
- **Server-side caching (data freshness):** only the relation scan is cached.
  `getTablesRelations` fills the process-level caches `DatabasesData`, `TableRelations`
  and `TableMetadata` (`models/clickhouse.go:34-36`) on first use and never invalidates
  them, so `GET /api/databases` and the topology of `GET /api/dataflow/...` need a process
  restart to reflect a schema change.
  `GET /api/relationships/...` is **not** served from those caches: `BuildRelationshipsGraph`
  issues live queries on every request — `GetTableColumns` for the current table and for each
  source/destination (`models/graph.go:246`, `:327`, `:349`, `:442`), a live
  `SELECT create_table_query, engine FROM system.tables` (`models/graph.go:254-259`), and a
  live `isDistributedTable` per candidate — so column lists and engines on that route do
  reflect schema changes without a restart, while the set of related tables it walks comes
  from the cached relation list. `GET /api/columns` and `GET /api/table/...` are likewise
  uncached (`models/graph.go:114`, `models/clickhouse.go:429`, `:642`).

## Endpoints / operations

All paths are relative to the listen address, `SERVER_ADDR` (default `:8080`, `main.go:83`).

| Operation | Purpose | Auth |
|-----------|---------|------|
| `GET /api/connection` | Current ClickHouse connection info for the header chip; password never included (`api/handlers.go:40-48`) | None |
| `GET /api/databases` | Every visible database mapped to its tables, each value an HTML label fragment (`api/handlers.go:54-66`) | None |
| `GET /api/columns` | Flat column index across all allowed databases, backing the `Ctrl+K` / `⌘K` palette (`api/handlers.go:71-78`) | None |
| `GET /api/dataflow/:database/:table` | Table-level DAG of upstream sources and downstream materializations (`api/handlers.go:82-98`) | None |
| `GET /api/relationships/:database/:table` | Column-level DAG with transformation expressions on the edges (`api/handlers.go:102-118`) | None |
| `GET /api/table/:database/:table` | Table details and column list for the inspector panel | None |
| `GET /api/grafana/status` | Whether Grafana column usage is available, and why not when it is not | None |
| `GET /api/grafana/usage/:database/:table` | Per-column verdicts and the dashboards behind them | None |
| `GET /api/grafana/unused` | Column verdicts across the schema, with totals and caveats | `database`, `verdict` |
| `POST /api/grafana/refresh` | Re-scan the dashboard source; debounced by `GRAFANA_CACHE_TTL` | None |
| `GET /` | Renders `static/html/index.html` through `html/template` with a per-process `{{.BuildID}}` cache-buster (`main.go:74-80`) | None |
| `GET /static/*` | Frontend assets from `./static` on disk, served with `Cache-Control: no-cache` (`main.go:60-66`) | None |

### `GET /api/connection`

Returns the configured connection, straight from the in-process `models.Config`
(`api/handlers.go:40-48`). No ClickHouse round-trip, so it never returns `500`.

| Field | JSON type | Source |
|---|---|---|
| `host` | string | `CLICKHOUSE_HOST` (`main.go:30`) |
| `port` | number | `CLICKHOUSE_PORT` (`main.go:31`) |
| `user` | string | `CLICKHOUSE_USER` (`main.go:32`) |
| `database` | string | `CLICKHOUSE_DATABASE` (`main.go:34`) |
| `secure` | boolean | `CLICKHOUSE_SECURE` (`main.go:36`) |

`CLICKHOUSE_PASSWORD` is deliberately absent from the payload (`api/handlers.go:38-47`).

### `GET /api/databases`

Returns `map[string]map[string]string` — database name → table name → **an HTML label
fragment**, not a plain table name (`models/clickhouse.go:349-359`). The fragment is
built by `generateTableListContent` (`models/clickhouse.go:188-199`) as an engine icon
element followed by the HTML-escaped table name, and, when row counts are known, a
`<br><small>…Rows: <b>…</b> | Size: <b>…</b></small>` suffix:

```
<i class="fa-solid fa-database"></i> my_table<br><small style="color: #000; font-size: 0.8em;">Rows: <b>1.2M</b> | Size: <b>…</b></small>
```

Consumers other than the bundled frontend must strip the markup: `addTableToList` in
`static/js/app.js` removes the leading `<i>` tag with a regex. Icons are chosen per
engine (`fa-database`, `fa-circle-nodes`, `fa-book`, `fa-diagram-project`, `fa-eye`,
`fa-table` — `models/clickhouse.go:247,256,265,279,293,306`).

Databases `system`, `information_schema`, `performance_schema` and `mysql` are excluded
by `allowedDatabase()` (`models/clickhouse.go:331-347`).

Errors: `500` with `{"error": …}` if the underlying `system.tables` query fails
(`api/handlers.go:56-63`).

### `GET /api/columns`

Returns a JSON array of `ColumnIndexEntry` (`models/graph.go:104-110`), built by one
`SELECT database, table, name, type FROM system.columns` ordered by
`database, table, position` (`models/graph.go:114-140`). Uncached — the query runs per
request.

| Field | JSON type |
|---|---|
| `database` | string |
| `table` | string |
| `name` | string |
| `type` | string (raw ClickHouse type) |

The exclusion list is repeated here as a hardcoded SQL `NOT IN` clause carrying five
literals — it spells both `'information_schema'` and `'INFORMATION_SCHEMA'`, because
`allowedDatabase()` matches that name case-insensitively via `strings.ToLower`
(`models/clickhouse.go:337`) and SQL cannot — (`models/graph.go:119`) and must be kept in step with `allowedDatabase()`
(`models/clickhouse.go:331-347`).

Errors: `500` with `{"error": …}` (`api/handlers.go:72-76`).

### `GET /api/dataflow/:database/:table`

Returns a `DataFlowGraph` (`models/graph.go:57-60`) — `{"nodes": [...], "edges": [...]}`.
Built by `BuildDataFlowGraph` (`models/graph.go:145-172`), which seeds the requested
table as the current node and walks the cached relation list forward and backward,
depth-bounded by `maxRelationDepth = 50` (`models/clickhouse.go:16`,
`models/graph.go:201-238`).

`nodes[]` — `GraphNode` (`models/graph.go:38-48`):

| Field | JSON type | Notes |
|---|---|---|
| `id` | string | Plain `database.table`; no hashing (`models/graph.go:183-188`) |
| `database` | string | Split from `id` |
| `table` | string | Split from `id` |
| `engine` | string | Omitted when empty; raw ClickHouse engine from `TableMetadata` |
| `engine_type` | string | One of `mergetree`, `replicated`, `distributed`, `mview`, `dictionary` (`models/graph.go:12-18`, `ClassifyEngine` at `:22-35`); defaults to `mergetree` when the table is not in `TableMetadata` (`models/graph.go:196`) |
| `total_rows` | number \| absent | Nullable, omitted when unknown |
| `total_bytes` | number \| absent | Nullable, omitted when unknown |
| `current` | boolean \| absent | `true` only on the requested table |

`edges[]` — `GraphEdge` (`models/graph.go:50-54`): `from`, `to` (both node `id`s) and an
optional `label`. Node and edge order is not stable — both are collected from Go maps
(`models/graph.go:164-171`).

The `engine_type` strings are a styling contract shared with `classifyEngine()` and
`glyphFor()` in `static/js/app.js`, the legend in `static/html/index.html`, and the CSS
classes in `static/css/styles.css`; changing them breaks the rendering.

Errors: `400` when either path parameter is empty; `500` on relation-discovery failure
(`api/handlers.go:86-95`). A table with no known relations is **not** an error — it comes
back as a one-node graph (`models/graph.go:154-172`).

### `GET /api/relationships/:database/:table`

Returns a `RelationshipsGraph` (`models/graph.go:98-101`) — `{"tables": [...], "edges": [...]}`.
`BuildRelationshipsGraph` (`models/graph.go:245-282`) branches on engine: a
`MaterializedView` is handled by parsing its `SELECT` for source→target column mappings
with transformation expressions (`models/graph.go:285`), any other engine falls back to
column-name heuristics against its direct dependencies (`models/graph.go:408`).

`tables[]` — `RelTable` (`models/graph.go:78-87`):

| Field | JSON type | Notes |
|---|---|---|
| `id` | string | `database.table` |
| `database`, `table` | string | |
| `engine` | string | Omitted when empty |
| `engine_type` | string | Same five values as above |
| `role` | string | `source`, `current` or `destination` (`models/graph.go:71-75`) |
| `columns` | array | `RelColumn` = `{"name": string, "type": string}` (`models/graph.go:63-67`); `type` is passed through `simplifyColumnType` (`models/clickhouse.go:361`), so it is a simplified label, not the raw ClickHouse type |

`edges[]` — `RelEdge` (`models/graph.go:89-96`): `from_table`, `from_column`, `to_table`,
`to_column`, and `expression` (omitted when empty) carrying the transformation the
backend could infer.

Errors: `400` on an empty path parameter; `500` when the table details or
`system.tables` lookup fails — including for a non-existent table
(`api/handlers.go:106-115`, `models/graph.go:246-259`).

### `GET /api/table/:database/:table`

Returns a single `TableDetails` object (`models/clickhouse.go:60-71`), assembled from one
`system.tables` row plus a `system.columns` query ordered by position
(`models/clickhouse.go:429-486`). Uncached.

| Field | JSON type | Notes |
|---|---|---|
| `name`, `database` | string | Echoed from the path parameters |
| `engine` | string | Raw ClickHouse engine |
| `primary_key` | string | |
| `sorting_key` | string | |
| `partition_key` | string | |
| `total_rows` | number \| null | Nullable; the field is always present |
| `total_bytes` | number \| null | Nullable; the field is always present |
| `columns` | array \| null | `ColumnInfo` = `{"name", "type", "position", "comment"}` (`models/clickhouse.go:53-58`); `type` here is the **raw** ClickHouse type, and the array is `null` rather than `[]` for a table with no rows returned |

Errors: `400` on an empty path parameter; `500` on any query failure, which is also what
an unknown database/table produces (`api/handlers.go:127-136`,
`models/clickhouse.go:443-445`).

### Path parameter notes

Both `:database` and `:table` are single Gin path segments and are passed to ClickHouse
as bound query parameters (`models/clickhouse.go:429-455`, `models/graph.go:254-257`), so
identifiers containing `/` cannot be addressed through these routes. The `400` guard only
rejects the empty string (`api/handlers.go:86`, `106`, `127`).

### Frontend routes

- `GET /` renders `static/html/index.html` as a Go template with `{{.BuildID}}` set to the
  process start timestamp, so every CSS/JS URL in that page carries a `?v=` cache-buster
  that changes on restart (`main.go:74-80`). Any newly added asset tag must include it.
- `GET /static/*` serves the `./static` directory from disk with `Cache-Control: no-cache`
  (`main.go:60-66`), which permits caching but forces revalidation; combined with Gin's
  `Last-Modified`, unchanged files round-trip as `304`s. Both routes resolve paths relative
  to the process working directory.


## Grafana column usage

Four routes added by the column-usage feature. They are optional: with no `GRAFANA_*`
variable set the feature is off and every one of them answers
`200 {"state":"disabled","reason":"not configured"}`.

### The state envelope

Every Grafana route answers with a top-level `state`, and **only `ok` carries a payload**:

| `state` | Meaning | Body |
|---|---|---|
| `disabled` | no dashboard source configured | `{"state":"disabled","reason":"not configured"}` |
| `scanning` | the first scan has not completed | `{"state":"scanning", …}` |
| `error` | configured but the scan failed | `{"state":"error","error":"…", …}` |
| `ok` | usage is available | the payload, plus `"state":"ok"` |

The payload is withheld outside `ok` on purpose. An empty usage payload and a real one
showing nothing read are indistinguishable to a reader, and acting on that difference is
what drops a live column.

This deviates from the repository's `400`/`500`/`200` convention: a switched-off feature
answers `200` with a state rather than an error status, so the frontend has one branch
instead of four. The deviation is deliberate — see `docs/decisions/0002-four-state-column-verdicts.md`.

### `GET /api/grafana/status`

```json
{
  "state": "ok",
  "mode": "dir",
  "scanned_at": "2026-09-02T16:53:09Z",
  "stats": { "dashboards": 1, "panels": 2, "queries": 2,
             "parsed": 2, "heuristic": 0, "failed": 0, "skipped": 0 },
  "warnings": ["…"]
}
```

`mode` is the source that produced the scan: `api`, `dir`, or `api-fallback-dir` when an
API scan failed and the directory took over. The service-account token never appears.

### `GET /api/grafana/usage/:database/:table`

```json
{
  "state": "ok",
  "usage": {
    "database": "raw", "table": "flights_local", "engine": "ReplicatedMergeTree",
    "columns": [
      { "column": "origin", "type": "String", "verdict": "used",
        "reason": "read-through-lineage",
        "derived": [ { "dashboard_uid": "flights-e2e", "dashboard_title": "Flight Operations",
                       "dashboard_url": "https://grafana/d/flights-e2e", "panel_id": 1,
                       "panel_title": "Delays by route",
                       "via": "mv:aggregated.flight_stats_daily_mv",
                       "confidence": "exact", "snippet": "SELECT day, origin, …" } ] }
    ],
    "dashboards": [ … ], "used_count": 5, "unused_count": 4, "unknown_count": 0
  }
}
```

`direct` references read this table; `derived` ones arrived through a `Distributed`
wrapper or a materialized view, named by `via`. `confidence` is `exact` (the AST tied the
column to one table) or `heuristic` (a guess that cannot license an `unused` verdict).

### `GET /api/grafana/unused`

`?database=` and `?verdict=` narrow the rows. Totals always count the whole schema, not
the filtered subset, and `caveats` states what the report cannot see.

```json
{ "state": "ok",
  "report": {
    "rows": [ { "database": "raw", "table": "flights_local", "engine": "ReplicatedMergeTree",
                "column": "status", "type": "LowCardinality(String)",
                "verdict": "unused", "dashboards": 1 } ],
    "caveats": ["Reflects Grafana dashboards only. …"],
    "totals": { "used": 13, "unused": 10, "unknown": 0, "no_coverage": 12 } } }
```

Verdicts are defined in `docs/decisions/0002-four-state-column-verdicts.md`. **Only
`unused` licenses dropping a column.**

### `POST /api/grafana/refresh`

The only non-`GET` route in the API. It writes nothing to ClickHouse — it rebuilds an
in-process cache — and is debounced by `GRAFANA_CACHE_TTL`; a call inside that window
returns the current status with `"debounced": true`.
