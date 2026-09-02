# Grafana Column Usage

> Revision 2 — incorporates an automated plan review plus direct verification of every claim it made.
> Changes from revision 1 are summarised in **Review Outcomes** at the end.

## Overview

Extend the visualizer with a second lineage source: **Grafana dashboards**. Today the app knows how
data flows *inside* ClickHouse (table → MV → table). It has no idea which of those columns anybody
actually reads. This adds the consumer side of the graph.

Two deliverables, equal weight:

1. **Dashboard-lineage browser** — pick a table, see every Grafana dashboard and panel that reads it,
   per column, with the SQL fragment that proves it.
2. **Unused-column report** — per table, the columns that no dashboard touches, so they can be
   dropped from the schema.

Problem it solves: schemas accumulate columns nobody queries. Right now the only way to find them is
to grep 172 dashboard JSON files by hand. The cost of a wrong answer is asymmetric — dropping a
column that *is* used breaks a production dashboard — so the report is built to be conservative and
to distinguish "provably unused" from "we could not tell".

Integration: the feature is **entirely optional**. With no Grafana configuration the app behaves
exactly as it does today; nothing about the six existing endpoints changes, and **the UI carries no
trace of the feature at all** — see decision 7.

## Context (from discovery)

**Codebase** (verified against the tree, not assumed)

- `main.go` (127 lines) — builds `models.Config` inline from env; `log.Fatalf`s if ClickHouse is
  unreachable; serves `index.html` through `html/template` with a `BuildID` cache-buster.
- `api/handlers.go` (139 lines) — six thin Gin wrappers, **all `GET`**, zero logic.
- `models/clickhouse.go` (657 lines) — `getTablesRelations()` fills three package-level caches in one
  pass; `GetTableColumns` (2 queries per call, uncached); `parseViewQuery`; `extractColumnMappings`;
  `isDistributedTable` (1 query per call, uncached).
- `models/graph.go` (521 lines) — graph payloads, `ClassifyEngine`, `walkForward`/`walkBackward`,
  and **`BuildColumnIndex()` (`:114`), which returns every column of every allowed database in one
  query** — the right input for a bulk verdict pass.
- `static/js/app.js` (973), `static/js/diagram.js` (663) — vanilla JS, no build step.
- `config/config.go` is **dead code**. Per `.ai/rules.md` rule 10, config goes in `main.go`.
- **Zero Go test files exist.** This plan introduces the first ones.
- **`.ai/rules.md` (281 lines) is an enforceable agent-rules file.** It is binding on this work; the
  rules it imposes are cited inline throughout the tasks below.

**Verified facts that shaped the design** (each was checked, not inferred)

| Claim | Verified how | Consequence |
|---|---|---|
| `TableDetails` already carries `PrimaryKey`, `SortingKey`, `PartitionKey` (`clickhouse.go:60-70`) | read the struct | key columns can and must be excluded from `unused` |
| `extractColumnMappings` returns `ColumnRelationship` with `SourceTable`/`TargetTable` **always empty** (`clickhouse.go:585-593`) | read the function | it cannot do dest→source propagation; design changed (see decision 4) |
| `extractBaseColumnName` returns **at most one** source column per expression, and the projection list is split on a bare `,` | read the function + its own "doesn't handle nested functions perfectly" comment | `SELECT a + b AS c` would attribute only `a`; column-level MV propagation is unsafe |
| Distributed edge is `{DependsOnTable: distributed, Table: local}` (`clickhouse.go:285`), MV edge is `{DependsOnTable: src, Table: mv}` (`:301`) | read both branches | the two edge families have **opposite** semantics; propagation must be per-engine, not "mirror `walkForward`" |
| A Distributed table with `len(queryParts2) < 6` emits `{Table: name}` with **no** `DependsOnTable` (`clickhouse.go:287`) | read the else branch | that local table gets no edge at all → must yield `unknown`, never `unused` |
| `escapeHtml` (`app.js:2-6`) is `textContent`→`innerHTML`, which does **not** escape `"` | read it | unsafe in `href="…"` attribute context |
| `switchSection` (`app.js:312-327`) is a two-way `if/else`; `exportHtml` (`app.js:480`) is a two-way ternary | read both | a third section silently exports the wrong diagram |
| `exportHtml`'s inline `:root` (`app.js:493-494`) redeclares the five `--t-*-fg` vars | read it | an **11th** styling site that `.ai/rules.md` rule 3 does not list |
| `github.com/AfterShip/clickhouse-sql-parser` latest is **v0.5.6** (2026-08-11), no runtime deps | module proxy `@latest` | pin v0.5.6, not v0.4.11 |

**Parser probe** — 19 Grafana-shaped queries run through v0.5.6 in a scratch module:

```
timeFilter ok   conditionalAll ok   select_star ok    array_join    ok   union     ok
bare_var   ok   cte_join       ok   star_except ok    string_dollar ok   subquery  ok
var_in_IN  ok   interval       ok   columns_regex ok  comment       ok   format    ok
dollar_alias ok  final_settings ok
braced_var_col  FAIL  line 1:9  <EOF> or ';' expected, got "{"   → SELECT ${col} FROM db.t
var_in_table    FAIL  line 1:35 <EOF> or ';' expected, got "{"   → FROM aggregated.newcust_${period2}_distributed
```

**2 of 19 fail, and only on `${…}` brace syntax — in column position as well as table position.**
The parser handles `$__timeFilter`, `$host`, `$__interval_ms`, `$__conditionalAll`, CTEs, aliased
JOINs, `SELECT *`, `* EXCEPT`, `COLUMNS('regex')`, `FINAL`, `SETTINGS`, `ARRAY JOIN`, `'%$x%'`,
comments, `FORMAT`, `UNION ALL` and subqueries natively. This retires the "normalisation is
mandatory" premise and shrinks Task 5 to brace expansion.

**Grafana corpus** (`/home/ailiev/Projects/otarie/GrafanaRepack/dashboards`, 172 files → **167 unique
dashboards** after uid dedup, 793 panels)

| Fact | Value | Consequence |
|---|---|---|
| targets with `rawSql` | 593 | raw SQL is the only shape worth parsing |
| `grafana-clickhouse-datasource` targets | 591 | reliable pre-filter |
| `-- Dashboard --` datasource targets | 180 | carry `panelId`, no SQL — resolve or mark `unknown`, never "no usage" |
| `yesoreyeram-infinity-datasource` | 23 | ignore |
| template variables | 821 (485 with SQL) | a second, easily-missed usage source |
| total SQL strings | 1012 | the parse corpus |
| subquery / CTE / JOIN | 15% / 11% / 7% | alias resolution matters |
| `SELECT *` | 1% | must yield `unknown` |
| fully-qualified `db.table` | 82% of 1337 refs, 175 distinct tables | unqualified refs need a default database |
| library panels | 0 | no `libraryPanel` handling needed |

Two corpus findings drive the architecture: **table names contain template variables**
(`aggregated.newcust_${period2}_distributed`) — the single highest-risk false positive; and
**dashboards query `Distributed` tables** (`probe_raw.siplog_distributed`, 85 refs — the most
referenced table), which `graph.go` deliberately excludes from relationship graphs.

## Development Approach

- **testing approach**: **Regular** (code first, then tests) — chosen by the user.
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests are not optional — they are a required part of the checklist
  - write unit tests for new functions/methods, and for modified ones
  - add new test cases for new code paths; update existing cases if behavior changes
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** — no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change; maintain backward compatibility — the six existing endpoints and their
  payloads are frozen

**Binding repository rules** (from `.ai/rules.md`; violating one is a blocker, not a preference):

- **Rule 1** — never add a ClickHouse write path. Everything here is `SELECT` against `system.tables`
  and `system.columns` only.
- **Rule 2** — request values reach SQL only as bound `?` parameters.
- **Rule 3** — the five `EngineType` strings are a five-file contract. See Task 15's real site list.
- **Rule 4** — `allowedDatabase()` and `BuildColumnIndex`'s inline `NOT IN` list change together.
- **Rule 5** — every new static asset tag carries `?v={{.BuildID}}`.
- **Rule 6** — no HTML-string assembly on the diagram path.
- **Rule 8** — the existing caches are never invalidated and `GetTableColumns`/`BuildColumnIndex`/
  `isDistributedTable` are uncached; changing the caching story is allowed but **must be documented**.
  This plan changes it (Task 11) and documents it (Task 17).
- **Rule 10** — new config goes in `main.go` + `models.Config` + `.env.example`, never `config/`.
- **Error contract** — `400` on empty path params, `500 {"error": …}` on failure, else `200`.
  This plan deviates once (disabled feature returns `200 {"enabled": false}`); the deviation is
  deliberate and recorded in an ADR (Task 17).
- **Dependencies** — "do not add dependencies without justification; prefer the standard library."
  The parser dependency's justification is the probe above; it gets an ADR.
- Go style: **no pointers** in new code where a value works; prefer `%w` wrapping in new code.
- `models/clickhouse.go` is not gofmt-clean — run `gofmt -w` only on touched regions.

## Testing Strategy

- **unit tests**: required for every Go task. Table-driven. New files: `models/grafana_test.go`,
  `models/grafana_sql_test.go`, `models/usage_test.go`, `api/handlers_test.go`.
- **the testability seam is Task 8 and it is load-bearing.** `api.Handler` holds a concrete
  `*models.ClickHouseClient` whose only field is unexported, and `NewClickHouseClient` pings on
  construction. Without Task 8 the usage/verdict tests in Tasks 9-10 and the handler tests in Task 12
  cannot be written at all. Task 8 must land before them.
- **fixtures**: `models/testdata/dashboards/*.json` — hand-written, modelled on the measured shapes.
  **Do not copy real dashboards from GrafanaRepack into this repo** — different project, internal
  topology.
- **corpus check**: an env-gated test walking `$GRAFANA_DASHBOARDS_DIR` when set, skipping when unset.
  Introduced in **Task 7** (not at the end) so the AST-vs-heuristic split is measured against reality
  while it can still change decisions.
- **e2e tests**: the repo has **no** e2e framework (`.playwright-mcp/` is an untracked MCP scratch
  dir, not a suite), and `.ai/rules.md` records verification as manual against
  `docker-compose.clickhouse-test.yml`. Frontend tasks therefore carry **named manual-verification
  checklists**, not a vacuous "run tests" box. Do not introduce Playwright as part of this plan.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope

## Solution Overview

```
  source          →   extract        →   resolve            →   index
  ──────────────      ─────────────      ─────────────────      ──────────────────
  Grafana API     |   panels          |  expand ${vars}     |   references per
   or JSON dir    |   + row.panels    |  AST parse ─────┐   |     (db, table, column)
                  |   + templating    |                 ↓   |   + lineage propagation
                  |     variables     |  heuristic fallback |   + four-state verdicts
```

**Key design decisions**

1. **`DashboardSource` interface** with two implementations (`apiSource`, `dirSource`). Everything
   downstream sees `[]Dashboard`. The whole pipeline is testable from fixture files, no network.

2. **AST-first with heuristic fallback, per query.** Expand `${vars}` → parse with
   `clickhouse-sql-parser` v0.5.6 → walk the AST for an alias→table map → resolve column refs. On
   parse failure, fall back to token matching for that query. Failure never silently drops a query; it
   downgrades confidence. Use `parser.Walk` / `parser.FindAll` — **not** the 100-method `ASTVisitor`
   interface.

3. **Four-state column verdict**, never a bare boolean:

   | Verdict | Meaning | Safe to drop? |
   |---|---|---|
   | `used` | ≥1 resolved reference | no |
   | `unused` | table is referenced, **every** referencing query resolved at `exact` confidence, column appeared in none | **yes** |
   | `unknown` | table is referenced, but ≥1 query was `SELECT *`, unresolved, heuristic-only, or used an unknown `${var}` | no |
   | `no-coverage` | no dashboard references this table at all | no |

   **`exact` confidence is required to reach `unused`.** A heuristic-resolved query downgrades that
   table's unmatched columns to `unknown` — heuristic matching cannot see a column referenced through
   an alias or a macro, so it cannot license a drop. Same for the Task 5 unknown-variable flag.

   Additional hard exclusions from `unused`:
   - columns named in `PrimaryKey`, `SortingKey` or `PartitionKey` (`ALTER TABLE … DROP COLUMN`
     fails on them; reporting them is actively harmful)
   - every column of a `Distributed` table (no independent storage — consistent with `graph.go`
     excluding them from relationship graphs)
   - every column of a local table whose Distributed wrapper produced no edge (the
     `len(queryParts2) < 6` case) → `unknown`
   - **if Grafana is disabled, errored, or still scanning, every column is `no-coverage`** — never
     `unused`.

4. **Lineage propagation is per-engine, because the edge families point opposite ways.**
   - **Distributed** (`{DependsOnTable: distributed, Table: local}`): a column used on the Distributed
     table is marked used on the local table, tagged `Via: "distributed:<table>"`.
   - **MV** (`{DependsOnTable: src, Table: mv}`, plus `{DependsOnTable: mv, Table: dst}`):
     **table-level, not column-level.** If *any* column of an MV's destination is used, then *every*
     source column the MV's SELECT reads — projection, `WHERE`, `GROUP BY`, `JOIN` — is marked `used`,
     tagged `Via: "mv:<table>"`. The source-column set comes from parsing the MV's stored SELECT with
     the **AST parser from Task 6**, not from `extractColumnMappings`.

     Rationale: `extractColumnMappings` leaves `SourceTable`/`TargetTable` empty and
     `extractBaseColumnName` returns at most one column per expression, so `SELECT a + b AS c` would
     silently make `b` droppable. Table-level propagation is less precise in the *safe* direction and
     needs no refactor of existing code — which `.ai/rules.md` would require to be a separate commit
     anyway ("refactors are behavior-preserving; do not mix a refactor with a feature").
   - Propagation is bounded by `maxRelationDepth` (50) with a `seen` map (rule 9).

5. **One cache mechanism serving two purposes.** A TTL'd Grafana scan; `POST /api/grafana/refresh`
   re-scans only if the last scan is older than `GRAFANA_CACHE_TTL`, which doubles as the debounce for
   an endpoint on an API that `.ai/rules.md` records as having "no authentication, no authorization,
   no rate limiting". The usage index is derived and rebuilt whenever the dashboard set is replaced.

6. **Failure is loud, not silent, and status is tri-state + scanning.** Grafana misconfiguration must
   never present as "everything is unused". `main.go` does **not** `log.Fatalf` on a Grafana error
   (unlike the ClickHouse path). `/api/grafana/status` reports
   `state: disabled | scanning | error | ok`, plus `mode` (`api` / `dir` / `api-fallback-dir`) so an
   automatic fallback is visible rather than silently misreporting provenance.

7. **When the feature is off, the UI is not rendered — not merely hidden.** (User requirement.)
   Nothing configured means no nav tab, no inspector Usage column, no badges, no banner, and **no
   request to any `/api/grafana/*` endpoint except the single `status` call** that decides the
   question. The elements are never created in the DOM rather than created and CSS-hidden, so there
   is no flash of content before `status` returns and nothing in the page source hints at the
   feature.

   "Off" means `state: disabled`, i.e. **no source of any kind** — neither `GRAFANA_URL`+
   `GRAFANA_TOKEN` nor `GRAFANA_DASHBOARDS_DIR`. A dashboards directory alone is a valid,
   credential-free source and **does** show the UI; that is how the feature runs against a
   provisioned dashboard repo.

   The `error` and `scanning` states are **not** "off": those render the feature with an explicit
   banner, because a Grafana that is configured but unreachable must never be indistinguishable from
   one where nothing is used.

8. **The verdict pass costs 2 ClickHouse queries, not ~350.** Built on `BuildColumnIndex()` (one
   query for all columns) plus one `SELECT database, name, engine, primary_key, sorting_key,
   partition_key FROM system.tables` for the key columns — instead of calling `GetTableColumns` per
   table across 175 tables. Both are `SELECT`s on system tables (rule 1). Note rule 4: `BuildColumnIndex`'s
   inline `NOT IN` list and `allowedDatabase()` must stay in step.

## Technical Details

### Configuration (`main.go` + `models.Config` + `.env.example`, per rule 10)

| Variable | Default | Purpose |
|---|---|---|
| `GRAFANA_URL` | (empty) | Grafana base URL. Enables API mode with a token. |
| `GRAFANA_TOKEN` | (empty) | Service-account token. **Never logged, never in any response.** |
| `GRAFANA_DASHBOARDS_DIR` | (empty) | Directory of dashboard JSON. Enables directory mode, no credentials. |
| `GRAFANA_SKIP_VERIFY` | `false` | Skip TLS verification for the Grafana API. |
| `GRAFANA_TIMEOUT` | `30s` | HTTP timeout. |
| `GRAFANA_CACHE_TTL` | `15m` | Scan reuse window; also the refresh debounce. |
| `GRAFANA_DEFAULT_DATABASE` | value of `CLICKHOUSE_DATABASE` | Database for unqualified table refs (18% of refs). |
| `GRAFANA_SNIPPET_CHARS` | `200` | Max SQL snippet length in responses; `0` disables snippets entirely. |

Mode: URL+token → `api` (falls back to `dir` on API error **and reports `api-fallback-dir`**);
directory alone → `dir`; neither → disabled.

### Types

```go
// models/grafana.go
type Dashboard struct {
    UID, Title, Folder, Source, URL string
    Panels    []Panel
    Variables []TemplateVariable
}
type Panel struct {
    ID int; Title, Type string
    Queries []string // rawSql, in target order
    RefPanelID int    // "-- Dashboard --" source panel, 0 if none
}
type TemplateVariable struct{ Name, Query string; Values []string }

// models/grafana_sql.go
type Confidence string // "exact" | "heuristic"   (no third value — nothing emits one)
type QueryReference struct {
    Database, Table, Column string
    Confidence Confidence
    Snippet    string
}
type QueryParseResult struct {
    References []QueryReference
    Tables     []string // tables found even when column resolution failed — needed to
                        // distinguish "referenced but unresolved" (unknown) from "no-coverage"
    SelectStar bool
    UnknownVar bool     // an unresolvable ${var} was substituted
    ParseError string
}

// models/usage.go — pure domain, no ClickHouse client (see Task 8)
type SchemaSnapshot struct {
    Columns   map[string][]ColumnInfo // "db.table" -> columns
    Keys      map[string]TableKeys    // primary / sorting / partition
    Engines   map[string]string       // "db.table" -> engine
    Relations []TableRelation
}
type ColumnVerdict string // "used" | "unused" | "unknown" | "no-coverage"
type ColumnUsage struct {
    Column, Type string
    Verdict ColumnVerdict
    Reason  string     // why not `unused`: "sorting-key", "select-star", "heuristic-only", …
    Direct, Derived []UsageRef
}
type UsageRef struct {
    DashboardUID, DashboardTitle, DashboardURL string
    PanelID int; PanelTitle string
    Via string // "" | "distributed:<t>" | "mv:<t>" | "variable:<name>"
    Confidence Confidence
    Snippet string
}
type TableUsage struct {
    Database, Table string
    Columns []ColumnUsage
    Dashboards []UsageRef
    UnusedCount, UnknownCount int
}
```

### Normalisation (Task 5 — deliberately small, per the parser probe)

1. **`${var}` / `${var:raw}` expansion** in both table and column positions, using known
   `TemplateVariable.Values`. This is the load-bearing rule: without it
   `aggregated.newcust_${period2}_distributed` fails to parse and every one of its columns is
   reported unused. Unknown variable → substitute a placeholder and set `UnknownVar`.
2. **`$var` → a literal placeholder.** Justification is **correctness, not parseability** (the probe
   shows `$host` parses fine): unsubstituted, `$host` resolves as a column identifier and produces a
   phantom reference.

Macros `$__timeFilter`, `$__timeFrom/To`, `$__interval*`, `$__conditionalAll` need **no** rewriting on
the AST path — verified. Add rewrite rules for them only if the Task 7 heuristic path needs them, and
say so there.

### Endpoints (`/api/grafana`)

| Endpoint | Returns |
|---|---|
| `GET /api/grafana/status` | `{state, mode, dashboards, panels, queries, parsed, heuristic, failed, scannedAt, error}` — never the token |
| `POST /api/grafana/refresh` | re-scans if older than TTL, else returns current status with `debounced: true` |
| `GET /api/grafana/usage/:database/:table` | `TableUsage` |
| `GET /api/grafana/unused` | report; `?database=`, `?verdict=`, `?minConfidence=` |

Payload per state, for all four (this is what makes the frontend branch unambiguous):

| `state` | body |
|---|---|
| `disabled` | `200 {"state":"disabled","reason":"not configured"}` |
| `scanning` | `200 {"state":"scanning"}` — the UI shows a spinner, **not** an empty report |
| `error` | `200 {"state":"error","error":"…"}` — the UI says "usage unknown", never "unused" |
| `ok` | `200` with the payload |

## What Goes Where

- **Implementation Steps** (`[ ]`): Go code, tests, frontend, docs — all achievable in this repo.
- **Post-Completion** (no checkboxes): verification against live Grafana and the real corpus, and the
  schema-pruning workflow the report feeds.

## Implementation Steps

### Task 1: Grafana configuration and tri-state status endpoint

**Files:**
- Create: `models/grafana.go`
- Modify: `main.go`, `api/handlers.go`, `.env.example`
- Create: `models/grafana_test.go`, `api/handlers_test.go`

- [x] add `GrafanaConfig` and `LoadGrafanaConfig(getenv func(string) string)` resolving mode api/dir/disabled — take the env getter as a parameter so mode resolution is testable without `os.Setenv`
- [x] read the eight `GRAFANA_*` variables in `main.go` next to the existing env block (rule 10 — **not** `config/`)
- [x] on Grafana config error, log a warning and continue serving — never `log.Fatalf` (contrast the ClickHouse path at `main.go:47`)
- [x] register `GET /api/grafana/status` returning `{"state":"disabled","reason":"not configured"}` when unset
- [x] write tests for mode resolution: api / dir / both / neither / url-without-token / token-without-url
- [x] write a test asserting the token never appears in the status payload
- [x] write a handler test for the disabled state, constructing `NewHandler(nil, …)` — no ClickHouse needed
- [x] run tests — must pass before task 2

**Task 1 notes**

- `NewHandler` gained a third parameter (`models.GrafanaStatus`). One caller, `main.go`. No HTTP
  payload changed.
- ➕ Added beyond the checklist: a route-registration test asserting the six original routes plus the
  new one are present and that the route count is exactly 7, and a test asserting
  `GET /api/connection` still omits the password. Both guard the "existing endpoints are frozen"
  constraint cheaply, without a ClickHouse connection.
- ➕ Added `.env.example` entries (rule 11 / rule 10 require it alongside `main.go`).
- Half-configured API (`GRAFANA_URL` without `GRAFANA_TOKEN`) degrades to `dir` mode when a directory
  is configured, and reports why in `status.reason`; with no directory it is `state: error`, not a
  silent `disabled`.
- ⚠️ **`golangci-lint` cannot run in this environment, and this is pre-existing, not caused by this
  work.** `golangci-lint 2.10.1` is built with go1.26.0 while the installed toolchain is go1.27.0;
  it panics with `file requires newer Go version go1.27 (application built with go1.26)`. Verified
  against an unmodified `master` worktree, where it fails the same way (`could not import fmt …
  export data version 4 is greater than maximum supported version 2`). `.ai/rules.md` requires
  `golangci-lint run` before every commit, so **the linter must be upgraded before that rule can be
  satisfied for any task in this plan**. `go build`, `go vet ./...`, `gofmt -l` and `go test ./...`
  all pass.

### Task 2: Directory dashboard source

**Files:**
- Modify: `models/grafana.go`
- Create: `models/testdata/dashboards/simple.json`, `models/testdata/dashboards/rows_and_vars.json`
- Modify: `models/grafana_test.go`

- [x] define `DashboardSource` interface and `dirSource`
- [x] walk the directory for `*.json`, tolerating both bare dashboards and `{"dashboard": …}` exports
- [x] validate uid against `^[A-Za-z0-9_-]{1,64}$` in **shared `Dashboard` construction** so both sources get it — a JSON file is untrusted input just as the API is
- [x] skip unparseable files with a counted warning rather than failing the whole scan
- [x] hand-write the two fixtures (do **not** copy GrafanaRepack files)
- [x] write tests for a clean directory, one malformed file, a missing directory, an empty directory, and a rejected uid
- [x] run tests — must pass before task 3

**Task 2 notes**

- Deviation from the planned signature: the interface is `Dashboards() (DashboardScan, error)`, not
  `([]Dashboard, error)`. `DashboardScan` carries `Skipped` and `Warnings` alongside the dashboards,
  which Task 11 needs for scan stats and which a bare slice would throw away. Warnings are capped at
  50 while the count keeps rising.
- ➕ **Discovered against the real corpus: 5 pairs of dashboards share a uid** — same uid, same title,
  provisioned into two folder trees (`mobile/OSN/…` and `mobile/common/…`). Grafana keys dashboards by
  uid, so these are one dashboard each. `dirSource` now dedupes by uid, first-wins in WalkDir's
  lexical order, counting and naming each collision. Without this every column those dashboards read
  would be counted twice and the lineage browser would list the dashboard twice. Test:
  `TestDirSourceDeduplicatesByUID`.
- ➕ Corrected the corpus figures: the tree holds **172** JSON files (the 173rd entry is a
  `.gitkeep`), yielding **167 unique dashboards**. Verified two ways — the scanner reports
  `dashboards=167 skipped=5`, and an independent Python pass counts 167 distinct uids.
- Walk is recursive: the real tree nests three levels (`oinis/common/…`), and a non-recursive glob
  would have found nothing. For directory mode the folder is derived from the relative path when
  `meta.folderTitle` is absent, which is the provisioned-repo case.
- A missing or non-directory path is a **hard error**, not an empty scan: reporting zero dashboards
  for a mistyped path would present as "no dashboard uses anything".
- Fixtures are hand-written and deliberately exercise Task 4's cases too: a bare dashboard, an
  API-export envelope, a collapsed row with a nested panel, a `-- Dashboard --` panel reference, an
  infinity-datasource panel to ignore, a template variable with SQL, and `${period}` inside a table
  name.

### Task 3: Grafana HTTP API dashboard source

**Files:**
- Modify: `models/grafana.go`
- Modify: `models/grafana_test.go`

- [x] implement `apiSource`: paged `GET /api/search?type=dash-db&limit=…`, then `GET /api/dashboards/uid/<uid>`
- [x] bearer auth, configurable timeout, `SkipVerify` TLS option
- [x] build the deep link `<url>/d/<uid>`, capture `meta.folderTitle`
- [x] skip v2-schema dashboards (`dashboard api version not supported`) with a counted, named warning — do not attempt conversion
- [x] fall back to `dirSource` on API error when a directory is configured, and set mode `api-fallback-dir` so status reports it (decision 6 — a silent switch would misreport provenance)
- [x] write `httptest.Server` tests: happy path, 401, 500, v2 refusal, empty result, timeout, fallback-fired
- [x] run tests — must pass before task 4

**Task 3 notes**

- `DashboardScan` gained a `Mode` field. The *configured* mode lives on `GrafanaConfig`; the
  *effective* mode is a property of the scan, because a fired fallback changes it. Status reports the
  scan's mode, so `api-fallback-dir` can never be passed off as a healthy API scan.
- **Failure policy, deliberately asymmetric.** A failed `/api/search` fails the whole scan — "search
  returned nothing" and "no column is used" are indistinguishable downstream. A single dashboard that
  fails to load is skipped and counted. But **if every dashboard fails while the search succeeded, the
  scan errors**: that is a systemic problem wearing the costume of an empty Grafana, and returning
  zero dashboards would make every column look droppable.
- Uid validation happens **before** the fetch request is built, so a hostile search result cannot
  steer the URL. Test asserts no request is issued for a traversal uid.
- ➕ Added beyond the checklist: paging test (a full page forces a second request), a token-leak test
  on the error path, an invalid-uid test, a search-metadata-fallback test (dashboard objects may omit
  their own uid/title/folder), and a `NewDashboardSource` selection test covering all four modes.
- ✔ **Live API validation completed** against `https://10.233.1.19/dna` with a real service-account
  token (the address in the grafana-skill config, `…1.17`, is unreachable; `…1.19` is the live one).
  `apiSource` read **147 dashboards / 636 panels in 4.0s with 0 skipped** — no v2-schema refusals, no
  failed fetches. Through the real binary: `state=ok mode=api dashboards=147 queries=1051 parsed=964
  failed=87`. The token appears in **no** response body and **no** log line.
- ✔ The *fallback* path was validated end-to-end against real configuration: with the real
  (unreachable) `GRAFANA_URL` plus the real dashboards directory, the scan reports
  `mode=api-fallback-dir dashboards=167 skipped=5`, carries a warning naming the connection failure,
  and still builds deep links from the configured Grafana URL — so links work even while the API does
  not. This is exactly the production scenario for a VPN-gated Grafana.

### Task 4: Panel, target, and template-variable extraction

**Files:**
- Modify: `models/grafana.go`
- Create: `models/testdata/dashboards/panel_shapes.json`
- Modify: `models/grafana_test.go`

- [x] extract panels recursively including collapsed `row.panels` — a flat `panels[]` walk silently misses them
- [x] collect `rawSql` per target, inheriting the panel-level datasource when the target omits one
- [x] resolve `-- Dashboard --` targets via `panelId`; count unresolvable ones so they become `unknown`, never "no usage"
- [x] extract template variables: name, SQL (string form **and** `{query: …}` object form), values from `options`/`current`
- [x] ignore `yesoreyeram-infinity-datasource` and other non-ClickHouse targets
- [x] write tests for row nesting, dashboard-datasource refs, both variable query shapes, mixed-datasource dashboards, and a panel whose target omits its datasource
- [x] run tests — must pass before task 5

**Task 4 notes**

- **Datasource filtering is a denylist, not an allowlist** — a deliberate change from the checklist's
  "filter to `grafana-clickhouse-datasource`". Wrongly *excluding* a query makes the columns it reads
  look unused, the one error that costs a schema; wrongly *including* one only invents usage that
  keeps a column alive. So anything carrying `rawSql` is kept unless its datasource type is known not
  to be this ClickHouse: infinity, prometheus, loki, elasticsearch, influx, graphite, jaeger, tempo,
  testdata, grafana — plus postgres/mysql/mssql, whose `rawSql` would otherwise resolve against
  same-named tables in an entirely different database. Legacy bare-string datasources and targets with
  no datasource at all are kept.
- **Hidden targets (`hide: true`) are kept**: a hidden query still names columns someone relies on.
- A `-- Dashboard --` panel copies the referenced panel's queries, because it really does display
  those columns. Chains do not recurse — only the referenced panel's *own* SQL is copied — and both a
  dangling reference and a chained one produce a named warning.
- Extraction warnings flow into `DashboardScan.Warnings` prefixed with the dashboard uid.
- ➕ Test-suite hygiene: three assertions hardcoded "2 fixtures" and broke when a third was added.
  They now derive the count from the fixture directory.

**Corpus measurements** (167 deduped dashboards)

| Measure | Value |
|---|---|
| panels extracted | 699 (661 carry SQL) |
| `-- Dashboard --` reuse panels | 174, **all resolved** — zero dangling references |
| panel queries | 727 |
| template variables | 775 (450 with SQL, 490 with values, 2068 values total) |
| **total SQL strings** | **1177** |

**⚠️ Findings that change Task 5** — measured, not estimated:

1. **889 of 1177 queries (76%) contain a `${var}`, and 433 have one in table position.** Every one of
   those fails AST parsing outright (the Task 5 probe showed `${` breaks the parser in both table and
   column position). Expansion is not an optimisation — without it roughly three quarters of the
   corpus falls to the heuristic path, which under decision 3 downgrades everything it touches to
   `unknown`, and the report becomes useless.
2. **2666 of 2778 unresolvable references are variables that ARE declared but carry no values.**
   Provisioned dashboards store `options: []` because Grafana populates them from the datasource at
   query time. Only 112 references across 9 names are undeclared, and those are dominated by
   `${__from}` / `${__to}`, which are built-in time macros rather than template variables.
3. Therefore Task 5 needs three paths, not one:
   - `${__from}` / `${__to}` and friends → substitute a literal; they are macros, not variables.
   - declared-but-valueless in **table** position → expansion is impossible, so emit a **table-name
     pattern** (`agg_${period}_distributed` → `agg_%_distributed`) and let Task 9 attribute usage to
     **every** known table matching it. Conservative in the safe direction, and it recovers exactly
     the 433 table-position cases that would otherwise report a whole table's columns as unused.
   - declared-but-valueless elsewhere → a literal placeholder is enough to parse.
4. 82 distinct variable names appear; the most common are `period`, `probe`, `probe_type`, `view`,
   `apn`, `link`, `period2`.

### Task 5: `${var}` expansion and `$var` placeholder substitution

**Files:**
- Create: `models/grafana_sql.go`
- Create: `models/grafana_sql_test.go`

- [x] implement variable expansion — `normalizeQuery(sql, vars) NormalizedQuery`
- [x] expand `${var}` / `${var:raw}` in **table and column positions** (the probe shows both break the parser)
- [x] substitute a literal placeholder for bare `$var`, justified as correctness — it would otherwise resolve as a phantom column identifier
- [x] do **not** rewrite `$__timeFilter` / `$__interval*` / `$__conditionalAll` — verified to parse natively; add rules only if Task 7's heuristic path needs them
- [x] write tests for var-in-table-name, var-in-column-position, unknown-variable flagging, `$` inside a string literal (`'%$x%'`), and a variable with multiple values
- [x] run tests — must pass before task 6

**Task 5 notes**

- **Deviation, and the most consequential one in this task: known values are deliberately NOT
  substituted.** The checklist said to expand `${var}` "using known variable values". Doing so is
  unsafe in both positions:
  - *table position* — `agg_${period}_distributed` with values `[6min, 1h]` would resolve to exactly
    one table and leave its siblings looking untouched. But the dashboard genuinely reads whichever
    value the viewer picks, so **all** of them are used.
  - *projection position* — `SELECT ${metric}` would resolve to one column and quietly make the other
    candidates droppable.

  Every variable therefore becomes the marker `__gfvar__`, a legal SQL identifier. A table name
  carrying it is a **pattern**: `agg___gfvar___distributed` matches every real `agg_<x>_distributed`.
  That is not over-attribution, it is the correct semantics. `TablePatternMatches` anchors both ends
  and never lets the marker cross the `.` between database and table, so a pattern cannot swallow a
  same-named table in another database. `TemplateVariable.Values` is still carried and can narrow
  pattern matches later if precision is ever wanted.
- `NormalizedQuery` reports `TablePattern` and `OpaqueVar` separately rather than one `UnknownVar`
  flag. Where an opaque variable landed decides whether it costs anything — one in a `WHERE` clause
  changes no columns, one in the projection does — and only the AST pass can see the marker's
  position. Task 6 makes that call; this pass just records that it happened.
- Grafana's built-ins (`${__from}`, `${__to}`, `${__interval_ms}`, …) become numeric literals and are
  **not** reported as template variables. An unrecognised `${__x}` becomes the marker.
- The `$__timeFilter` / `$__interval_ms` / `$__conditionalAll` macro family is left byte-identical, as
  the parser probe showed it should be.

**Corpus results** — 1177 queries, 889 of which carried a `${var}`:

| After normalization | Count |
|---|---|
| table names that became patterns | 431 |
| queries with an opaque variable elsewhere | 885 |
| `$__` macros preserved untouched | 750 |
| **leftover `${` (unparseable)** | **10** |
| leftover bare `$var` | **0** |

- ⚠️ The 10 leftovers are a **typo in the source dashboards**, not a gap in the expander: four files
  (`mobile/{OSN,common}/Special Events reporting/{Customer Experience,Mobile Data Usage}.json`)
  contain `${roaming)` — a missing closing brace — alongside correctly written `${roaming}` on the
  same line. Those panels render invalid SQL in Grafana too. The expander correctly refuses to match
  malformed syntax, so those queries fall to the heuristic path, which is the conservative outcome.
  Worth fixing in the dashboard repo, but out of scope here.

### Task 6: AST resolver via clickhouse-sql-parser

**Files:**
- Modify: `models/grafana_sql.go`, `models/grafana_sql_test.go`, `go.mod`, `go.sum`

- [x] add `github.com/AfterShip/clickhouse-sql-parser` **pinned at v0.5.6**, run `go mod tidy` (justification per `.ai/rules.md` dependency rule: the probe in Context; ADR in Task 17)
- [x] parse with `parser.NewParser(...).ParseStmts()`; walk with `parser.Walk` — **do not** implement the 100-method `ASTVisitor`
- [x] build an alias→qualified-table map covering CTEs, subqueries and JOINs; qualify unqualified tables with `GRAFANA_DEFAULT_DATABASE`
- [x] **an unqualified column in a multi-table query is attributed to every candidate table whose schema has that name, at `heuristic` confidence** — the AST cannot disambiguate it, and calling it `exact` would falsely mark the other table's column `unused`
- [x] set `SelectStar` for `SELECT *`, `SELECT t.*`, `SELECT * EXCEPT (…)` and `COLUMNS('regex')`
- [x] populate `Tables` even when column resolution fails, and truncate snippets to `GRAFANA_SNIPPET_CHARS`
- [x] write tests: single-table, aliased JOIN, CTE, nested subquery, all four star variants, unqualified-table, unqualified-column-in-JOIN downgrade
- [x] write tests asserting a parse failure yields a populated `ParseError` and no panic
- [x] run tests — must pass before task 7

**Task 6 notes**

- ⚠️ **`parser.Walk` aborts the entire traversal when the callback returns `false`, not just that
  subtree** — the doc comment says "stops for the current subtree", but each parent propagates a
  child's `false` upward. Returning `false` after handling a `Path` node truncated resolution to the
  first column of the query. Identifiers already accounted for are now suppressed by marking them in
  an exclusion set instead; `Walk` is pre-order, so a node's children are excluded before they are
  visited. Anything else in this repo that walks this AST must not return `false`.
- `CTEStmt`'s field names are inverted relative to their meaning: `Expr` holds the CTE's **name** and
  `Alias` holds its **body**. Confirmed by dumping the AST.
- A single-table query attributes its unqualified columns at `exact` confidence — there is nowhere
  else they could come from. Only multi-table queries downgrade to `heuristic`.
- A column reached through a CTE alias is attributed to nothing: the CTE is not a table, and inventing
  a table for it would corrupt the index. The real tables inside the CTE body are still read.
- `COLUMNS('…')` is detected on the `FunctionExpr` node, because the function's name identifier is
  already excluded by the time the identifier pass runs.
- ➕ **`INTERVAL <marker>` repair, added after measuring.** A variable in an interval operand
  (`INTERVAL ${period}`) leaves a bare identifier where ClickHouse demands `INTERVAL <number> <unit>`,
  which fails the *whole query* rather than one name. The operand names no column, so it is rewritten
  to a well-formed literal; plural units are singularised because the parser accepts only the singular
  spelling. **This alone took the corpus from 86.5% to 92.5% parsed.**

**Corpus results** — 1177 queries:

| Measure | Value |
|---|---|
| **parsed by the AST** | **1089 (92.5%)** |
| parse failures (→ Task 7 fallback) | 88 |
| column references | 6958 — **5994 exact (86%)**, 964 heuristic |
| concrete tables referenced | 69 |
| pattern tables (from `${var}` names) | 26 |
| `SELECT *` / `COLUMNS()` queries | 17 |
| queries with an opaque column | 656 |

- The **70% decision gate in Task 16 is met with room to spare**, so the parser dependency stays.
- Remaining failure signatures: 69 `<ident>`, 10 `<int>`, 4 `{` (the `${roaming)` source typo), 4
  bracket mismatches. These are the long tail Task 7's fallback exists for.

### Task 7: Heuristic fallback resolver and corpus measurement

**Files:**
- Modify: `models/grafana_sql.go`, `models/grafana_sql_test.go`

- [x] regex-extract `FROM`/`JOIN` table refs (backticks, quotes, `db.table`) when the AST parse failed
- [x] match candidate tables' known column names against the query's identifier tokens
- [x] emit `Confidence: heuristic`; attribute an ambiguous name to **all** candidates (conservative — over-reports `used`)
- [x] wire `ResolveQuery` to try AST → fall back to heuristic → never return zero references *and* zero error silently
- [x] add the **env-gated corpus test**: when `GRAFANA_DASHBOARDS_DIR` is set, parse every SQL string and report AST-success / heuristic-fallback / failure counts; `t.Skip` when unset
- [x] run the corpus test against the real 172-file corpus (167 unique dashboards) and **record the three counts in this plan**
- [x] write tests for deliberately unparseable SQL, ambiguous joined columns, keyword-named columns, a table with no known columns
- [x] run tests — must pass before task 8

**Task 7 notes**

- **Deviation: the fallback works from the schema inward, not from the SQL outward.** The checklist
  said to match column names "skipping SQL keywords and function names". That framing belongs to the
  opposite algorithm — pulling identifiers out of the SQL and deciding which are columns. Instead the
  resolver tokenises the query once and asks, for each column the table is *known* to have, whether
  that token appears. Two consequences, both good: a column genuinely named `count`, `status` or
  `select` is found rather than skipped as a keyword (skipping it would have risked a false `unused`),
  and the cost is one tokenisation instead of a regex per column.
- A table the schema does not know is **not** recorded. In a query the parser already rejected, an
  unrecognised `FROM` token is far more likely a CTE name or an alias than a real table, and recording
  it would invent a table for the report.
- `ResolveQuery` keeps the AST's `ParseError` even when the fallback produced references, so the
  verdict pass can hold back an `unused` claim about anything the query touched.
- `ColumnLookup` is the seam to the schema. Pattern names are handed to it whole, so the Task 8
  implementation unions the columns of every table a pattern matches.

**`TestCorpusResolution`** is now a permanent, env-gated measurement instrument: it skips unless
`GRAFANA_DASHBOARDS_DIR` is set, and fails if the AST parse rate drops below the plan's 70% gate.
Current reading against the reference corpus:

```
dashboards=167 queries=1177 parsed=1089 (92.5%) refs exact=5994 heuristic=964
```

### Task 8: Pure usage domain and the ClickHouse seam

**Files:**
- Create: `models/usage.go`
- Create: `models/usage_test.go`
- Modify: `models/graph.go` (add the keys query only)

**Why this task exists:** `api.Handler` holds a concrete `*models.ClickHouseClient` with an unexported
`conn` field, and `NewClickHouseClient` pings on construction. There is no interface anywhere in the
repo. Without a seam, Tasks 9-10 and 12 cannot be tested at all.

- [x] define `SchemaSnapshot`, `TableKeys` as **pure data** in `models/usage.go` — no client, no I/O
- [x] add `LoadSchemaSnapshot(c *ClickHouseClient) (SchemaSnapshot, error)`: `BuildColumnIndex()` for columns (**one** query) plus **one** `SELECT database, name, engine, primary_key, sorting_key, partition_key FROM system.tables` for keys and engines — not `GetTableColumns` per table (that would be ~350 round trips across 175 tables)
- [x] keep the new query's database filter identical to `allowedDatabase()` / `BuildColumnIndex`'s `NOT IN` list (rule 4 — they must change together)
- [x] parse `sorting_key` / `primary_key` / `partition_key` expressions into a column-name set (they are expressions, e.g. `toYYYYMM(ts)`, not bare names — extract identifiers)
- [x] write tests for `SchemaSnapshot` construction from literals and for key-expression parsing (bare name, function-wrapped, tuple, empty)
- [x] run tests — must pass before task 9

**Task 8 notes**

- `ColumnUsage` / `UsageRef` / `TableUsage` are deferred to Task 9, where the code that populates them
  lands. Defining them here with no producer would have been dead weight.
- `SchemaSnapshot` carries `Columns`, `Keys`, `Engines` and `Relations`, and exposes three seams:
  `Lookup()` adapts it to the resolver's `ColumnLookup`, `MatchTables()` resolves a reference —
  pattern or plain — to concrete tables, and `Tables()` iterates deterministically.
- `ParseKeyExpression` treats every identifier that is not a known wrapper function as a column, which
  errs toward *protecting* a column from an unused verdict. The wrapper list covers the usual
  `toYYYYMM`, `cityHash64`, `tuple`, `intDiv` and friends.
- The two queries are both `SELECT`s against `system.columns` and `system.tables`, so rule 1 holds.
  The database filter is copied verbatim from `BuildColumnIndex`; rule 4 binds the three together.

### Task 9: Usage index with per-engine lineage propagation

**Files:**
- Modify: `models/usage.go`, `models/usage_test.go`

- [x] `BuildUsageIndex(snapshot, []ResolvedQuery) UsageIndex` — a pure function over Task 8's types
- [x] group references by `db.table`, attach dashboard/panel provenance, dedupe repeated refs
- [x] **Distributed propagation**: edge is `{DependsOnTable: distributed, Table: local}` — mark the local table's columns used, `Via: "distributed:<t>"`
- [x] **MV propagation, table-level**: if any destination column is used, parse the MV's stored SELECT with the Task 6 AST resolver and mark **every** source column it reads (projection, `WHERE`, `GROUP BY`, `JOIN`) as used, `Via: "mv:<t>"` — do **not** use `extractColumnMappings`, which leaves `SourceTable` empty and resolves at most one column per expression
- [x] bound propagation by `maxRelationDepth` (50); handle the cyclic case
- [x] record Distributed wrappers that produced **no** edge (`len(queryParts2) < 6`)
- [x] write tests: direct usage, distributed→local, MV dest→all source columns, `SELECT a + b AS c` marking both `a` and `b`, MV `WHERE`-only column, a cyclic relation set, depth capping, edgeless Distributed
- [x] run tests — must pass before task 10

**Task 9 notes**

- `ResolvedQuery` pairs a `QueryParseResult` with the panel it came from, which is how provenance
  reaches the index without the resolver knowing anything about dashboards.
- **The two propagation passes are separate functions and both run every round.** Writing them as
  `a() || b()` looked natural and was wrong: `||` short-circuits, so the view pass would be skipped in
  any round where the distributed pass had changed something. Propagation loops to a fixed point,
  bounded by `maxRelationDepth`, so cycles terminate — covered by a test with a timeout.
- ⚠️ `findUnlinkedDistributed` had to become a free function. `UsageIndex` has a value receiver, and
  its maps mutate through it fine, but assigning a **slice** field would have been silently discarded.
- Table-level references dedupe on `dashboard|panel|via`, deliberately **excluding** confidence: at
  table level the question is only whether a panel reads the table at all, and including confidence
  listed the same panel once per confidence level.
- Opacity is recorded per table with a reason (`select-star`, `variable-column`, `unparsed-query`) and
  **propagates across a Distributed wrapper**, so a `SELECT *` against the wrapper leaves the local
  table unenumerable too rather than looking cleanly resolved.
- ➕ `SchemaSnapshot` gained `ViewQueries`, loaded from `create_table_query` in the same
  `system.tables` query as the keys — no extra round trip. It is what lets MV propagation read the
  view's actual SELECT.
- MV propagation is verified to mark **both** `a` and `b` from `SELECT a + b AS c`, and to mark a
  column read only in the view's `WHERE` clause. That is precisely what the old
  `extractColumnMappings` path could not do, and why the plan replaced it.

### Task 10: Four-state verdicts and the unused report

**Files:**
- Modify: `models/usage.go`, `models/usage_test.go`

- [x] assign `used` / `unused` / `unknown` / `no-coverage`, with a `Reason` on every verdict that is not plain `used`
- [x] a fallback-resolved query downgrades that table's unmatched columns to `unknown`
- [x] downgrade to `unknown` on `SelectStar`, a variable column, or a non-empty `ParseError` touching the table
- [x] exclude from `unused`: primary/sorting/partition-key columns, every column of a `Distributed` table, and tables behind an edgeless Distributed wrapper
- [x] force `no-coverage` for every column when the scan state is `disabled`, `scanning` or `error` — never `unused`
- [x] implement `BuildUnusedReport` with database and verdict filters, stable ordering, totals and caveats
- [x] write tests for each of the four verdicts, the star and fallback downgrades, key-column exclusion, the Distributed exclusion, the disabled/scanning/errored cases, and the report filters
- [x] run tests — must pass before task 11

**Task 10 notes**

- **A key column is `used`, with the key role as its reason — not excluded from the report.** The plan
  said to exclude primary/sorting/partition-key columns from `unused`, which left open what verdict
  they *do* get. `used` is the honest one: ClickHouse reads them for storage whatever the dashboards
  do, and `ALTER TABLE … DROP COLUMN` refuses to remove them. Calling such a column `unused` would be
  recommending an impossible change; hiding it would leave the reader wondering where it went.
- **The `exact`-confidence rule resolved into the opacity rule, and the distinction matters.** There
  are two sources of heuristic confidence and only one of them threatens a false `unused`:
  - the **fallback resolver**, which runs only after a parse failure and may miss a column reached
    through an alias or a macro. It always leaves a `ParseError`, which is already recorded as
    `unparsed-query` opacity, and opacity forces `unknown`. Covered.
  - an **unqualified column in a multi-table AST query**, which is attributed to *every* candidate.
    Here the whole query was read; only the owner is uncertain. It over-attributes usage and can never
    hide a column, so it does not downgrade anything.

  Applying the plan's literal wording would have poisoned every joined table's verdicts for no safety
  gain. The intent — "the fallback may not have seen everything" — is what is implemented.
- `Direct` and `Derived` references are separated on each column, so the inspector can say *how* a
  column is reached: a dashboard reading this table, or one reading a Distributed wrapper or a view
  downstream of it.
- `BuildUnusedReport` skips `Distributed` tables outright — they store nothing of their own, so a
  column of one is never the thing you drop — and returns `Caveats` alongside the rows: the
  Grafana-only scope always, plus the missing-scan and unlinked-wrapper conditions when they apply.
  Totals count every column, not just the filtered rows, so a filtered view still shows the shape of
  the whole schema.

### Task 11: Scan orchestration, cache warming, TTL and refresh debounce

**Files:**
- Modify: `models/grafana.go`, `main.go`, `models/grafana_test.go`

- [x] add `GrafanaIndex` holding dashboards + usage index + scan stats + state behind a mutex
- [x] **warm the ClickHouse caches before building the index** — done inside `LoadSchemaSnapshot`, which now calls `getTablesRelations()` itself and fails loudly if it errors
- [x] scan asynchronously so a slow or hung Grafana never delays the listen call; expose state `scanning` until the first scan completes
- [x] honour `GRAFANA_CACHE_TTL`; make it the refresh debounce too (decision 5)
- [x] a failed refresh leaves the previous good index intact and sets `error` alongside the stale `scannedAt`
- [x] **document the caching-story change in `CLAUDE.md` and `.ai/rules.md` rule 8**
- [x] write tests for concurrent read-during-refresh, TTL expiry, debounced refresh, failed refresh preserving the prior index, and the nil-cache warm path
- [x] run tests — must pass before task 12

**Task 11 notes**

- Cache warming lives in `LoadSchemaSnapshot` rather than in the index, so **any** future caller gets
  it. `getTablesRelations` caches internally, so the second call costs nothing.
- `GrafanaIndex` is used through a pointer because it carries a `sync.RWMutex`; everything it holds is
  still passed by value. `SchemaLoader` and `DashboardSource` are both injected, so the whole
  orchestration is tested with no ClickHouse and no network.
- **A nil `*GrafanaIndex` is a valid, fully safe value** — `Status()`, `Refresh()`, `Usage()`,
  `Report()`, `Start()` and `Enabled()` all handle it. That is what the handlers hold when the feature
  is off, and it removes a whole class of nil check from the API layer.
- A failed rescan keeps the previous good index and the previous `scannedAt`, and only flips `state`
  to `error`. Verdicts then read `no-coverage` because the state is not `ok`, so stale data can never
  be mistaken for a completed scan — but the dashboards list survives for the UI to show.
- `.ai/rules.md` rule 8 and rule 1 both amended, and `CLAUDE.md`'s caching paragraph rewritten: this
  is the first component in the repo that invalidates a cache and the first non-`GET` route.
- `main.go` now builds the source, constructs the index and calls `Start()`. A source-construction
  failure logs and disables the feature rather than stopping the process.

### Task 12: Usage, unused, and refresh endpoints

**Files:**
- Modify: `api/handlers.go`, `api/handlers_test.go`

- [x] register `GET /api/grafana/usage/:database/:table`, `GET /api/grafana/unused`, `POST /api/grafana/refresh`
- [x] extend `GET /api/grafana/status` with full scan stats and the four-state `state` field
- [x] return the documented body for each of `disabled` / `scanning` / `error` / `ok` from **all four** endpoints
- [x] validate `:database`/`:table` exactly as the existing handlers do — `400` on empty
- [x] keep `api/` thin: handlers read params, call `models`, marshal JSON
- [x] write handler tests for all four states, unknown-table, refresh-debounced, and refresh-error paths
- [x] run tests — must pass before task 13

**Task 12 notes**

- Every Grafana endpoint answers with a top-level `state`, and **a state other than `ok` carries no
  payload at all**. An empty usage payload and a real one showing nothing read are indistinguishable
  to a reader, so the envelope refuses to hand over a shape that could be misread.
- `POST /api/grafana/refresh` is the only non-`GET` route in the API. A test asserts that: it walks
  the router's route table and fails on any other non-`GET` method, so the next one has to be
  deliberate.
- ➕ Test seams (`NewGrafanaIndexForTest`, `NewGrafanaIndexFailingForTest`, `ScanForTest`) live in
  `models` because the `api` package cannot reach the index's unexported source and loader fields, and
  building a real one there would need a live ClickHouse and a live Grafana.
- The route-inventory test now pins all ten routes and separates the six frozen ones from the four new.

## End-to-end validation (after Task 12)

The backend was run for real against the local test stack
(`docker compose -f docker-compose.clickhouse-test.yml`, seeded from
`scripts/clickhouse_test_engines.sql`) with a hand-written dashboard directory, exercising every
engine family the seed provides. **Everything below is observed output, not expected output.**

**Lineage, two hops upstream.** A dashboard queries `aggregated.flight_stats_daily` — a `Distributed`
wrapper. Usage crosses to `aggregated.flight_stats_daily_local`, then back through the materialized
view feeding it, to the source columns in `raw.flights_local`:

```
raw.flights_local  engine=ReplicatedMergeTree
  flight_id            used         primary-key             via=-
  flight_number        unused                               via=-
  airline_code         unused                               via=-
  origin               used         read-through-lineage    via=mv:aggregated.flight_stats_daily_mv
  destination          used         read-through-lineage    via=mv:aggregated.flight_stats_daily_mv
  scheduled_departure  used         read-through-lineage    via=mv:aggregated.flight_stats_daily_mv
  actual_departure     unused                               via=-
  delay_minutes        used         read-through-lineage    via=mv:aggregated.flight_stats_daily_mv
  status               unused                               via=-
```

No dashboard mentions `raw.flights_local` at all. Without lineage propagation every one of those nine
columns would have read `no-coverage`, and the four that are genuinely load-bearing would have looked
as droppable as the four that are not. Note `delay_minutes` is reached through *two* aggregate
wrappers (`avgState`, `maxState`) and `scheduled_departure` through `toDate(...)`.

**The safety properties, each confirmed against live data:**

| Property | Observed |
|---|---|
| a key column is never droppable | `flight_id` → `used` / `primary-key`, though no dashboard names it |
| a table nothing reads is not "dead" | `aggregated.airport_traffic_hourly_local` → `no-coverage`, `unused` count 0 |
| a Distributed wrapper is never droppable | `raw.flights` → every column `unknown` / `distributed-wrapper-judge-the-local-table` |
| the report states its own limits | caveat returned: "Reflects Grafana dashboards only…" |
| refresh is debounced | `POST /api/grafana/refresh` → `debounced: true` inside the TTL |
| the six original endpoints are untouched | `/api/dataflow/raw/flights_local` → 6 nodes / 5 edges; `/api/table/...` → 9 columns |

**Disabled path.** Restarted with no `GRAFANA_*` variables at all: `status`, `usage` and `unused` all
answer `200 {"state":"disabled","reason":"not configured"}` **with no payload**, the six original
endpoints behave identically, and the process logs nothing about Grafana.

The report found **10 genuinely unused columns** across the seeded schema, with totals
`used=13 unused=10 unknown=0 no_coverage=12`.

### Live Grafana

Also run against the production instance (`https://10.233.1.19/dna`, service-account token):

| Measure | Live API | Provisioning repo |
|---|---|---|
| dashboards | **147** | 167 |
| panels | 636 | 699 |
| SQL strings | 1051 | 1177 |
| parsed by the AST | **964 (91.7%)** | 1089 (92.5%) |
| exact / heuristic references | 5383 / 888 | 5994 / 964 |
| skipped dashboards | **0** | 5 (duplicate uids) |
| scan time | 4.0s | 0.1s |

The two sources agree closely on parse rate, which is the number that matters. **They disagree on
population: 147 live against 167 in the repo.** The plan's Post-Completion asked for exactly this
comparison — 20 dashboards exist in the provisioning tree that the live instance does not serve (five
of them are the duplicate-uid pairs, so the real gap is ~15). Worth resolving before trusting either
source alone, since a dashboard that exists only in the repo still reads columns, while one that
exists only live is invisible to a file-mode scan.

⚠️ The production ClickHouse (`clickhouse:9000` from `.env`) is a cluster-internal name and is not
reachable from a developer machine, so a **real unused-column report against the production schema has
still not been produced**. That remains the single most valuable outstanding verification, and it
needs to run somewhere with access to both.

### Task 13: Inspector — per-column dashboard usage

**Files:**
- Modify: `static/js/app.js`, `static/css/styles.css`, `static/html/index.html`

- [x] fetch `/api/grafana/status` once on load; when `state` is `disabled`, **create no Grafana DOM nodes at all and issue no further `/api/grafana/*` requests**
- [x] extend `renderTableDetails` with a Usage column: verdict badge + dashboard count per column
- [x] expand a column row to list dashboard → panel deep links with the SQL snippet as proof
- [x] give `unknown` / `no-coverage` / `scanning` visually distinct treatments; surface the `Reason` string
- [x] show a banner for `state: error` — "usage unknown", explicitly not "unused"
- [x] **build the anchors with `createElement`/`setAttribute`, not an `innerHTML` template**
- [x] no new asset tag was needed (the code lives in the existing `app.js` / `styles.css`)
- [x] **manual verification** — recorded below
- [x] run tests — must pass before task 14

**Task 13 notes**

- Four new CSS tokens (`--v-used-*`, `--v-unused-*`, `--v-unknown-*`, `--v-nocoverage-*`) sit beside
  the engine tokens. `unused` is the only verdict that reads as an action; the other three are
  deliberately quieter, because three of the four states mean "do not touch this".
- Evidence nodes are built with `createElement`/`setAttribute`. `escapeHtml` (`app.js:2-6`) is a
  `textContent`→`innerHTML` round-trip, which per the HTML serialisation spec escapes `&`, `<`, `>`
  and NBSP but **not** `"` — safe for text, unsafe in `href="…"`, and dashboard titles and URLs come
  from Grafana.
- ➕ `panelURL()` parses the dashboard URL and **rejects any scheme that is not http/https**, so a
  hostile dashboard record cannot inject a `javascript:` href. It appends `?viewPanel=<id>` so a link
  opens the exact panel.
- A badge's tooltip states what the verdict licenses; a `heuristic` reference is tagged in the
  evidence list with "Attributed by matching names, not by parsing."

**Manual verification** (live browser against the test ClickHouse + a fixture dashboard directory):

*Feature on* — `raw.flights_local`, which **no dashboard mentions**, and whose usage therefore arrives
entirely through two lineage hops:

| column | badge | evidence |
|---|---|---|
| `flight_id` | `used` | reason `primary-key` — protected, though nothing reads it |
| `origin`, `destination`, `scheduled_departure`, `delay_minutes` | `used (1)` | dashboard "Flight Operations" › panel "Delays by route", `via mv:aggregated.flight_stats_daily_mv`, snippet `SELECT day, origin, avg_delay FROM aggregated.flight_stats_daily …` |
| `flight_number`, `airline_code`, `actual_departure`, `status` | `unused` | — |

The heading reads "4 unused". Note the proof snippet **never names `raw.flights_local`**: it queries
the Distributed wrapper of the view's destination, and the `via` tag is what connects them.

*Feature off* — restarted with no `GRAFANA_*` variables and re-checked in the browser:

```
grafanaRequestsAfterLoad: []        usageHeaderPresent: false
verdictBadges: 0                    evidenceRows: 0
banners: 0                          anyUsageNodeInDOM: 0
headerCells: ["Name","Type"]        columnsRendered: 9
pageSourceMentionsGrafana: false
```

Zero Grafana DOM nodes, zero further requests, and the word "grafana" appears nowhere in the rendered
page — while the columns table renders exactly as it did before this feature existed.

### Task 14: Unused-columns report view

**Files:**
- Modify: `static/html/index.html`, `static/js/app.js`, `static/css/styles.css`

- [x] **generalise `switchSection()` from its hardcoded two-way `if/else` to N sections**, including the `localStorage.activeSection` restore path
- [x] **guard `exportHtml()`**, whose two-way ternary would otherwise export the relationships diagram while the report is on screen
- [x] add an "Unused columns" section to the nav, **not appended to the DOM at all** when `state` is `disabled`
- [x] render a sortable table: database, table, column, type, verdict, reason, dashboards count
- [x] add database and verdict filters wired to the `/api/grafana/unused` query params
- [x] make each row click through to the table's inspector view
- [x] add a legend stating plainly that **only `unused` licenses a drop**, and that it means unused *by Grafana*
- [x] **manual verification** — recorded below
- [x] run tests — must pass before task 15

**Task 14 notes**

- The tab and the whole section are **created in JavaScript**, only when Grafana is configured. Nothing
  was added to `index.html`, so a disabled feature cannot leave markup behind even in the page source.
- ⚠️ **A real bug, found by the manual verification rather than by reasoning.** The section-restore
  path raced the asynchronous status fetch: on reload, `switchSection('unused-columns')` ran before
  the tab existed, fell back to `data-flow`, **and wrote that fallback to `localStorage`** — silently
  destroying the saved preference. Two fixes: `switchSection` now persists only a section the caller
  could actually reach, and `restoreActiveSection()` runs a second time once the status resolves.
  Verified both ways — the preference now survives a reload with Grafana on, and survives *unused*
  with Grafana off, so enabling Grafana later restores the user's choice.
- The totals bar, the legend and the caveats sit above the table. Three of the four verdicts mean "do
  not touch this", so the legend says so in words rather than relying on colour.

**Manual verification** (live browser, test ClickHouse + fixture dashboards):

| Check | Result |
|---|---|
| tabs present | `data-flow`, `relationships`, `unused-columns` |
| totals | `10 unused · 13 used · 0 unknown · 12 no data` — identical to the API |
| rows under the default `unused` filter | 10, each with database/table/column/type/verdict/reason/dashboards |
| sorting | clicking *Column* re-sorts and marks the header `▲` |
| verdict filter | `no-coverage` → 12 rows, all badged "no data" |
| row click-through | selects `flights_local` in the inspector |
| Export HTML **on the report** | refuses: "Export HTML applies to the Data flow and Relationships diagrams." |
| Export HTML **on a diagram** | exports normally, no warning |
| reload with the report active | restores the section and re-renders 10 rows |
| **Grafana disabled** | no `unused-columns` tab, no section element, 0 usage/verdict/unused nodes, page source has no "grafana", and the saved preference is left intact |

### Task 15: [OPTIONAL — recommended to defer] Dashboard lineage diagram

**Status: not required by either stated deliverable.** Task 13 already delivers the lineage browser
with SQL proof. This task adds a third rendering surface for a structurally 2-level fan-out
(table → panels → dashboards) that a list arguably renders more legibly than a DAG, and it costs a new
node-kind styling contract across **11 sites**. The plan review recommended cutting it. Decide before
starting Task 13, since it also adds a fifth endpoint.

**Files:**
- Modify: `static/js/diagram.js`, `static/js/app.js`, `static/html/index.html`, `static/css/styles.css`, `api/handlers.go`, `models/usage.go`

- [ ] add `GET /api/grafana/graph/:database/:table` returning a `DataFlowGraph`-shaped payload with `dashboard` and `panel` node kinds
- [ ] add `renderDashboardUsage(container, graph, {onNodeClick})` to `window.SchemaDiagram`, laid out with the bundled Dagre
- [ ] add the node kinds at **all 11 sites** — `.ai/rules.md` rule 3 lists ten and misses one:
  `styles.css:20-29` (`--t-*-fg/bg` vars), `:451-455` (legend glyph), `:766-770` (`.df-node` rail),
  `:827-831` (`.rel-table` rail), `:1066-1070` (engine chip), `:1294-1298` (palette glyph);
  `index.html:83-87` (legend); `app.js` `commonDiagramCss():540`; **and `exportHtml()`'s inline
  `:root` at `app.js:493-494`, which redeclares the five `--t-*-fg` vars independently** — miss it and
  exported HTML renders the new rails with an undefined variable
- [ ] do **not** touch `classifyEngine()` or `glyphFor()`: `diagram.js:163` derives the class straight from `node.engine_type`; `classifyEngine()` maps ClickHouse *engine name strings* for the breadcrumb chip and `glyphFor()` feeds the command palette — neither is on the diagram path
- [ ] build nodes with `createElementNS` only (rule 6)
- [ ] deep-link panel nodes to `<grafana>/d/<uid>?viewPanel=<id>`
- [ ] gate the section, its legend entries and its endpoint call on `state != disabled`, creating no DOM nodes when off (decision 7)
- [ ] **manual verification**: a table referenced by several dashboards lays out without overlap, and Export HTML renders the new node kinds identically to the live view. Record the result here.
- [ ] run tests — must pass before task 16

### Task 16: Verify acceptance criteria

- [x] verify both Overview deliverables: lineage browser (Task 13) and unused report (Task 14)
- [x] verify the disabled path — browser-checked: no Grafana DOM node, no `/api/grafana/*` request past the single `status` call, page source has no "grafana", six original endpoints unchanged
- [x] verify the directory-only path: with **only** `GRAFANA_DASHBOARDS_DIR` set the UI appears and works — the mode every UI check ran in
- [x] verify the errored path: an unreachable `GRAFANA_URL` yields `state: error`, and the verdict pass returns `no-coverage` for every column in any state but `ok` (unit-tested across `disabled`/`scanning`/`error`)
- [x] verify the scanning path: `state: scanning` withholds the payload entirely, so no empty report can be shown
- [x] verify `${var}` expansion against the real corpus — 889 brace queries reduced to 10 unparseable, all 10 a source typo
- [~] verify usage on `probe_raw.siplog_distributed` propagates — **equivalent verified** on the test stack (`raw.flights` → `raw.flights_local`, plus a second MV hop). The production table itself needs the production ClickHouse; see Post-Completion
- [x] verify a key column that no dashboard selects is **not** reported `unused` — `flight_id` reads `used` / `primary-key`
- [x] verify the verdict pass issues 2 ClickHouse queries, not ~350 — **measured** via `system.query_log` with sentinel boundaries: exactly 2 per refresh, `system.columns` and `system.tables`
- [x] run the check sequence: `go build`, `go vet ./...`, `go test ./...` all clean; **`golangci-lint` cannot run in this environment** (pre-existing — see the Task 1 note)
- [x] confirm `gofmt` touched only new files — `gofmt -l` reports only `models/clickhouse.go`, already unformatted before this work
- [x] verify against the local stack that relation discovery still behaves — the end-to-end section above ran against `docker-compose.clickhouse-test.yml`, exercising MergeTree, ReplicatedMergeTree, SummingMergeTree, ReplicatedAggregatingMergeTree, Distributed and MaterializedView

**Task 16 notes**

- The repository went from **0 test files to 6**, and `go test ./...` now reports `ok` for two
  packages instead of `[no test files]` for four.
- `TestCorpusResolution` currently reads `dashboards=167 queries=1177 parsed=1089 (92.5%) refs
  exact=5994 heuristic=964`, well clear of the 70% gate it enforces.
- Query-cost measurement used sentinel queries either side of a refresh rather than timestamps:
  `system.query_log.event_time` has second granularity, and the startup scan bled into the window
  otherwise.

### Task 17: Update documentation

Driven by `.ai/rules.md` *Documentation update requirements*, which is broader than a README pass.

- [ ] `README.md` — the feature, the four verdict states, and the new variables in the `.env` code block (README has no env table)
- [ ] `CLAUDE.md` — the new pipeline, the config table, the endpoint list, **and the corrected caching story** (rule 8 requires documenting the change)
- [ ] `ARCHITECTURE.md` — components, data flow, dependencies, diagram
- [ ] `docs/api/README.md` — the new routes and JSON fields
- [ ] `docs/deployment/README.md` — the eight new env vars (rule: "Deploy/config changed → `docs/deployment/`")
- [ ] `.env.example` — new variables, no real token (rule 11)
- [ ] `CHANGELOG.md` — entry under `[Unreleased]`
- [ ] **two ADRs in `docs/decisions/`** using `adr-template.md`: (a) adopting the SQL-parser dependency, with the probe results as evidence; (b) the four-state verdict model and the `exact`-confidence requirement
- [ ] record the deliberate deviations: `POST` on a previously all-`GET` API, `200 {"state":…}` instead of the documented 400/500/200 shape, and the widened disclosure surface (dashboard titles, Grafana URLs, **raw SQL snippets** — more sensitive than schema metadata) on an API that `.ai/rules.md` documents as unauthenticated
- [ ] **correct the now-stale "zero test files" claims** in `PROJECT.md:103,115,129`, `CONTRIBUTING.md:19,136,147,150`, `docs/deployment/README.md:60,103`, `.ai/rules.md:204-205`, `ARCHITECTURE.md:148` — this branch (`docs/governance-baseline`) exists to make the docs accurate; leaving six stale claims regresses it
- [ ] add the missing 11th site to `.ai/rules.md` rule 3 (`exportHtml`'s inline `:root`, `app.js:493-494`) — it is absent today whether or not Task 15 ships
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*Manual intervention or external systems — no checkboxes, informational only*

**Manual verification**
- Run against live Grafana (`https://10.233.1.17/dna/`) with a read-only service-account token and
  compare dashboard/panel counts to the 167 unique dashboards / 793 panels measured from the provisioning repo — a large gap means
  dashboards exist in only one of the two places.
- Spot-check five columns reported `unused` by grepping GrafanaRepack directly. Any hit is a bug, not
  a tuning issue.
- Measure refresh cost with the full corpus: 1012 SQL parses plus 2 ClickHouse queries.

**External system updates**
- A read-only Grafana service account (Viewer role) is needed; the token goes in the deployment's
  secret store, not `.env` in the repo.
- `docker-compose.yml` runs `network_mode: host` — confirm Grafana is reachable from there, and mount
  the dashboards directory if directory mode is used in production.
- **Before dropping anything**: `unused` means unused *by Grafana*. Ad-hoc queries, other BI tools,
  applications and scheduled jobs are invisible to it. Cross-check `system.query_log` (which records
  the exact columns each executed query read) first — that source was considered for this plan and
  deliberately left out of scope, partly because reading it would widen the app's ClickHouse surface
  beyond `system.tables` / `system.columns`.

## Review Outcomes

An automated plan review of revision 1 returned NEEDS REVISION with 6 critical and 12 important
findings. Every claim cited below was independently verified against the tree or the module proxy
before being accepted.

**Accepted and fixed** (revision 1 → revision 2):

| # | Finding | Fix |
|---|---|---|
| 1 | No test seam for `*ClickHouseClient` — Tasks 1/8/9/11 were unimplementable | new **Task 8**: pure `SchemaSnapshot` domain + `BuildUsageIndex` as free functions |
| 2 | `extractColumnMappings` cannot do dest→source propagation (`SourceTable` always empty) | decision 4 rewritten: **table-level MV propagation** via the AST parser; no refactor of existing code needed |
| 3 | `extractBaseColumnName` resolves ≤1 column per expression → `SELECT a + b AS c` made `b` droppable | same fix; explicit test case added in Task 9 |
| 4 | Key columns would be reported `unused` — `ALTER … DROP COLUMN` fails on them | Task 10 excludes primary/sorting/partition-key columns with a `Reason` |
| 5 | `TableMetadata` is nil at startup scan time (`getTablesRelations` is lazy, `main.go` never calls it) | Task 11 warms the caches first; `scanning` state added |
| 6 | `unused` was undefined for heuristic-confidence queries | decision 3: **`exact` required for `unused`**, heuristic downgrades to `unknown` |
| 7 | Wrong parser version; "normalisation mandatory" premise false | pinned **v0.5.6**; Task 5 cut to `${var}` expansion after a 19-case probe |
| 8 | AST can't attribute unqualified columns in JOINs, but they were marked `exact` | Task 6 attributes to all candidates at `heuristic` |
| 9 | Styling contract is 11 sites, not 4; `classifyEngine()` is the wrong hook | Task 15 rewritten with the real site list |
| 10 | `switchSection`/`exportHtml` are hardcoded two-way branches | explicit checkboxes in Task 14 |
| 11 | Verdict pass would issue ~350 ClickHouse queries | Task 8 uses `BuildColumnIndex` + one keys query = 2 |
| 12 | Frontend tasks had vacuous "run tests" boxes | named manual-verification checklists in Tasks 13/14/15 |
| 13 | Decision gate sat after the work it gated | resolved in advance by the probe; corpus test moved to Task 7 |
| 14 | Disabled vs errored vs scanning was under-specified | tri-state + `scanning`, with a per-state payload table |
| 15 | `escapeHtml` doesn't escape `"`; dirSource uids unvalidated | `createElement`/`setAttribute` in Task 13; uid validation moved to shared construction in Task 2 |
| 16 | `POST /refresh` amplification + SQL-snippet disclosure | TTL doubles as debounce; `GRAFANA_SNIPPET_CHARS` (0 disables); ADR |
| 17 | Docs list incomplete vs `.ai/rules.md`; 6 stale "zero tests" claims | Task 17 expanded, including two ADRs and the rules.md rule-3 correction |
| 18 | Distributed/MV edges point opposite ways; edgeless Distributed case | decision 4 is per-engine; edgeless case yields `unknown`; Distributed excluded from `unused` |

**Accepted with a different fix than suggested**: the review proposed extracting the dest↔MV-column
matching out of `buildMVRelationshipsGraph` as its own refactor task (finding 2). Table-level MV
propagation removes the need entirely — strictly safer for the never-falsely-unused constraint, and it
avoids touching working code, which `.ai/rules.md` would require as a separate commit anyway.

**Not accepted as a cut**: the review recommended deleting the lineage diagram outright. It is kept as
**Task 15, explicitly marked optional** with the cost stated, so the call stays the user's.

**Minor findings folded in**: `Confidence` reduced to two values; `QueryParseResult.Tables` justified
inline; `SELECT t.*` / `* EXCEPT` / `COLUMNS()` given the same downgrade; `%w` in new code; the
datasource-uid caveat (queries against a *different* ClickHouse are attributed here — errs toward
`used`, so safe, but can mask genuinely unused columns) noted for Task 4.
