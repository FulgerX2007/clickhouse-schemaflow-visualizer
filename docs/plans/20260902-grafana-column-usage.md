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

- [ ] implement `apiSource`: paged `GET /api/search?type=dash-db&limit=…`, then `GET /api/dashboards/uid/<uid>`
- [ ] bearer auth, configurable timeout, `SkipVerify` TLS option
- [ ] build the deep link `<url>/d/<uid>`, capture `meta.folderTitle`
- [ ] skip v2-schema dashboards (`dashboard api version not supported`) with a counted, named warning — do not attempt conversion
- [ ] fall back to `dirSource` on API error when a directory is configured, and set mode `api-fallback-dir` so status reports it (decision 6 — a silent switch would misreport provenance)
- [ ] write `httptest.Server` tests: happy path, 401, 500, v2 refusal, empty result, timeout, fallback-fired
- [ ] run tests — must pass before task 4

### Task 4: Panel, target, and template-variable extraction

**Files:**
- Modify: `models/grafana.go`
- Create: `models/testdata/dashboards/panel_shapes.json`
- Modify: `models/grafana_test.go`

- [ ] extract panels recursively including collapsed `row.panels` — a flat `panels[]` walk silently misses them
- [ ] collect `rawSql` per target, filtering to `grafana-clickhouse-datasource`, inheriting the panel-level datasource when the target omits one
- [ ] resolve `-- Dashboard --` targets via `panelId`; count unresolvable ones so they become `unknown`, never "no usage"
- [ ] extract template variables: name, SQL (string form **and** `{query: …}` object form), values from `options`/`current`
- [ ] ignore `yesoreyeram-infinity-datasource` and other non-ClickHouse targets
- [ ] write tests for row nesting, dashboard-datasource refs, both variable query shapes, mixed-datasource dashboards, and a panel whose target omits its datasource
- [ ] run tests — must pass before task 5

### Task 5: `${var}` expansion and `$var` placeholder substitution

**Files:**
- Create: `models/grafana_sql.go`
- Create: `models/grafana_sql_test.go`

- [ ] implement `expandVariables(sql string, vars []TemplateVariable) (string, bool)` — the bool is `UnknownVar`
- [ ] expand `${var}` / `${var:raw}` in **table and column positions** (the probe shows both break the parser)
- [ ] substitute a literal placeholder for bare `$var`, justified as correctness — it would otherwise resolve as a phantom column identifier
- [ ] do **not** rewrite `$__timeFilter` / `$__interval*` / `$__conditionalAll` — verified to parse natively; add rules only if Task 7's heuristic path needs them
- [ ] write tests for var-in-table-name, var-in-column-position, unknown-variable flagging, `$` inside a string literal (`'%$x%'`), and a variable with multiple values
- [ ] run tests — must pass before task 6

### Task 6: AST resolver via clickhouse-sql-parser

**Files:**
- Modify: `models/grafana_sql.go`, `models/grafana_sql_test.go`, `go.mod`, `go.sum`

- [ ] add `github.com/AfterShip/clickhouse-sql-parser` **pinned at v0.5.6**, run `go mod tidy` (justification per `.ai/rules.md` dependency rule: the probe in Context; ADR in Task 17)
- [ ] parse with `parser.NewParser(...).ParseStmts()`; walk with `parser.Walk` / `parser.FindAll` — **do not** implement the 100-method `ASTVisitor`
- [ ] build an alias→qualified-table map covering CTEs, subqueries and JOINs; qualify unqualified tables with `GRAFANA_DEFAULT_DATABASE`
- [ ] **an unqualified column in a multi-table query is attributed to every candidate table whose schema has that name, at `heuristic` confidence** — the AST cannot disambiguate it, and calling it `exact` would falsely mark the other table's column `unused`
- [ ] set `SelectStar` for `SELECT *`, `SELECT t.*`, `SELECT * EXCEPT (…)` and `COLUMNS('regex')`
- [ ] populate `Tables` even when column resolution fails, and truncate snippets to `GRAFANA_SNIPPET_CHARS`
- [ ] write tests: single-table, aliased JOIN, CTE, nested subquery, all four star variants, unqualified-table, unqualified-column-in-JOIN downgrade
- [ ] write tests asserting a parse failure yields a populated `ParseError` and no panic
- [ ] run tests — must pass before task 7

### Task 7: Heuristic fallback resolver and corpus measurement

**Files:**
- Modify: `models/grafana_sql.go`, `models/grafana_sql_test.go`

- [ ] regex-extract `FROM`/`JOIN` table refs (backticks, quotes, `db.table`) when the AST parse failed
- [ ] word-boundary match candidate tables' known column names, skipping SQL keywords and function names
- [ ] emit `Confidence: heuristic`; attribute an ambiguous name to **all** candidates (conservative — over-reports `used`)
- [ ] wire `ResolveQuery` to try AST → fall back to heuristic → never return zero references *and* zero error silently
- [ ] add the **env-gated corpus test**: when `GRAFANA_DASHBOARDS_DIR` is set, parse every SQL string and report AST-success / heuristic-fallback / failure counts; `t.Skip` when unset
- [ ] run the corpus test against the real 172-file corpus (167 unique dashboards) and **record the three counts in this plan** — the probe predicts near-total AST success, so a low number means something is wrong with the integration, not with the parser choice
- [ ] write tests for deliberately unparseable SQL, ambiguous joined columns, keyword-named columns, a table with no known columns
- [ ] run tests — must pass before task 8

### Task 8: Pure usage domain and the ClickHouse seam

**Files:**
- Create: `models/usage.go`
- Create: `models/usage_test.go`
- Modify: `models/graph.go` (add the keys query only)

**Why this task exists:** `api.Handler` holds a concrete `*models.ClickHouseClient` with an unexported
`conn` field, and `NewClickHouseClient` pings on construction. There is no interface anywhere in the
repo. Without a seam, Tasks 9-10 and 12 cannot be tested at all.

- [ ] define `SchemaSnapshot`, `TableKeys`, `ColumnUsage`, `UsageRef`, `TableUsage` as **pure data** in `models/usage.go` — no client, no I/O
- [ ] add `LoadSchemaSnapshot(c *ClickHouseClient) (SchemaSnapshot, error)`: `BuildColumnIndex()` for columns (**one** query) plus **one** `SELECT database, name, engine, primary_key, sorting_key, partition_key FROM system.tables` for keys and engines — not `GetTableColumns` per table (that would be ~350 round trips across 175 tables)
- [ ] keep the new query's database filter identical to `allowedDatabase()` / `BuildColumnIndex`'s `NOT IN` list (rule 4 — they must change together)
- [ ] parse `sorting_key` / `primary_key` / `partition_key` expressions into a column-name set (they are expressions, e.g. `toYYYYMM(ts)`, not bare names — extract identifiers)
- [ ] write tests for `SchemaSnapshot` construction from literals and for key-expression parsing (bare name, function-wrapped, tuple, empty)
- [ ] run tests — must pass before task 9

### Task 9: Usage index with per-engine lineage propagation

**Files:**
- Modify: `models/usage.go`, `models/usage_test.go`

- [ ] `BuildUsageIndex(snap SchemaSnapshot, results []QueryParseResult, prov []UsageRef) UsageIndex` — a pure function over Task 8's types
- [ ] group references by `db.table`, attach dashboard/panel provenance, dedupe repeated refs
- [ ] **Distributed propagation**: edge is `{DependsOnTable: distributed, Table: local}` — mark the local table's columns used, `Via: "distributed:<t>"`
- [ ] **MV propagation, table-level**: if any destination column is used, parse the MV's stored SELECT with the Task 6 AST resolver and mark **every** source column it reads (projection, `WHERE`, `GROUP BY`, `JOIN`) as used, `Via: "mv:<t>"` — do **not** use `extractColumnMappings`, which leaves `SourceTable` empty and resolves at most one column per expression
- [ ] bound propagation by `maxRelationDepth` (50) with a `seen` map (rule 9); handle the cyclic case
- [ ] record which local tables had a Distributed wrapper that produced **no** edge (`len(queryParts2) < 6`), for Task 10 to mark `unknown`
- [ ] write tests: direct usage, distributed→local, MV dest→all source columns, `SELECT a + b AS c` marking both `a` and `b`, MV `WHERE`-only column, a cyclic relation set, depth capping, edgeless Distributed
- [ ] run tests — must pass before task 10

### Task 10: Four-state verdicts and the unused report

**Files:**
- Modify: `models/usage.go`, `models/usage_test.go`

- [ ] assign `used` / `unused` / `unknown` / `no-coverage` per the decision-3 table, with a `Reason` string on every non-`used` verdict
- [ ] require `exact` confidence for `unused`; a heuristic-resolved query downgrades that table's unmatched columns to `unknown`
- [ ] downgrade to `unknown` on `SelectStar` or `UnknownVar` or a non-empty `ParseError` touching the table
- [ ] exclude from `unused`: primary/sorting/partition-key columns (`Reason: "sorting-key"` etc.), every column of a `Distributed` table, and local tables behind an edgeless Distributed wrapper
- [ ] force `no-coverage` for every column when the scan state is `disabled`, `scanning` or `error` — never `unused`
- [ ] implement `BuildUnusedReport(idx, filters)` with database / verdict / min-confidence filters and stable ordering
- [ ] write tests for each of the four verdicts, the star and heuristic downgrades, every key-column exclusion, the Distributed exclusion, the disabled/scanning/errored cases, and the report filters
- [ ] run tests — must pass before task 11

### Task 11: Scan orchestration, cache warming, TTL and refresh debounce

**Files:**
- Modify: `models/grafana.go`, `main.go`, `models/grafana_test.go`

- [ ] add `GrafanaIndex` holding dashboards + usage index + scan stats + state behind a mutex
- [ ] **warm the ClickHouse caches before building the index**: `TableMetadata` and `TableRelations` are filled only by the lazy `getTablesRelations()`, which nothing in `main.go` calls — a startup scan would otherwise index against nil maps and report an empty set. Handle its error by moving to state `error`
- [ ] scan asynchronously so a slow or hung Grafana never delays the listen call; expose state `scanning` until the first scan completes
- [ ] honour `GRAFANA_CACHE_TTL`; make it the refresh debounce too (decision 5) — a refresh inside the window returns the current status with `debounced: true`
- [ ] a failed refresh leaves the previous good index intact and sets `error` alongside the stale `scannedAt`
- [ ] **document the caching-story change in `CLAUDE.md` and `.ai/rules.md` rule 8** — this plan is the first code that invalidates anything (rule 8 requires it be documented)
- [ ] write tests for concurrent read-during-refresh, TTL expiry, debounced refresh, failed refresh preserving the prior index, and the nil-cache warm path
- [ ] run tests — must pass before task 12

### Task 12: Usage, unused, and refresh endpoints

**Files:**
- Modify: `api/handlers.go`, `api/handlers_test.go`

- [ ] register `GET /api/grafana/usage/:database/:table`, `GET /api/grafana/unused`, `POST /api/grafana/refresh`
- [ ] extend `GET /api/grafana/status` with full scan stats and the four-state `state` field
- [ ] return the documented body for each of `disabled` / `scanning` / `error` / `ok` from **all four** endpoints
- [ ] validate `:database`/`:table` exactly as the existing handlers do — `400` on empty, per the rule-documented contract
- [ ] keep `api/` thin (`.ai/rules.md` coding conventions): handlers read params, call `models`, marshal JSON
- [ ] write handler tests for all four states, unknown-table, refresh-debounced, and refresh-error paths
- [ ] run tests — must pass before task 13

### Task 13: Inspector — per-column dashboard usage

**Files:**
- Modify: `static/js/app.js`, `static/css/styles.css`, `static/html/index.html`

- [ ] fetch `/api/grafana/status` once on load; when `state` is `disabled`, **create no Grafana DOM nodes at all and issue no further `/api/grafana/*` requests** (decision 7 — render-gated, not CSS-hidden, so there is no flash of content and no trace in the page source)
- [ ] extend `renderTableDetails` with a Usage column: verdict badge + dashboard count per column
- [ ] expand a column row to list dashboard → panel deep links with the SQL snippet as proof
- [ ] give `unknown` / `no-coverage` / `scanning` visually distinct treatments so none reads as `unused`; surface the `Reason` string
- [ ] show a banner for `state: error` — "usage unknown", explicitly not "unused"
- [ ] **build the anchors with `createElement`/`setAttribute`, not an `innerHTML` template** — `escapeHtml` (`app.js:2-6`) is `textContent`→`innerHTML` and does **not** escape `"`, so it is unsafe in `href="…"`; dashboard titles and URLs are Grafana-sourced
- [ ] add any new asset tag with `?v={{.BuildID}}` (rule 5)
- [ ] **manual verification**: point `GRAFANA_DASHBOARDS_DIR` at `models/testdata/dashboards`, open a fixture-covered table, and confirm — (a) a column referenced in `simple.json` shows `used` with the right dashboard/panel, (b) a column in no query shows `unused`, (c) the `SELECT *` fixture table shows all columns `unknown`, (d) a table in no fixture shows `no-coverage`, (e) with `GRAFANA_URL` pointed at a dead host, the banner appears and nothing reads `unused`. Record the result here.
- [ ] run tests — must pass before task 14

### Task 14: Unused-columns report view

**Files:**
- Modify: `static/html/index.html`, `static/js/app.js`, `static/css/styles.css`

- [ ] **generalise `switchSection()` (`app.js:312-327`) from its hardcoded two-way `if/else` to N sections**, including the `localStorage.activeSection` restore path
- [ ] **guard `exportHtml()` (`app.js:480`)**, whose `currentActiveSection === 'data-flow' ? dataflowDiagram : relationshipsDiagram` ternary would otherwise export the *relationships* diagram while the report is on screen
- [ ] add an "Unused columns" section to the nav, **not appended to the DOM at all** when `state` is `disabled` (decision 7); the section tab, its panel and its `switchSection` entry are all conditional
- [ ] render a sortable table: database, table, column, type, verdict, reason, dashboards-touching-table count
- [ ] add database / verdict / min-confidence filters wired to the `/api/grafana/unused` query params
- [ ] make each row click through to the table's inspector view
- [ ] add a legend stating plainly that **only `unused` licenses a drop**, and that it means unused *by Grafana*
- [ ] **manual verification**: with the fixture directory, confirm the report lists exactly the known-unused fixture column, that a sorting-key column is absent with reason shown, that switching to this section and back leaves Export HTML exporting the correct diagram, and that a reload restores the section. Record the result here.
- [ ] run tests — must pass before task 15

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

- [ ] verify both Overview deliverables are implemented: lineage browser (Task 13) and unused report (Task 14)
- [ ] verify the disabled path: unset every `GRAFANA_*` variable, confirm all six original endpoints are unchanged from `master`, and confirm via DevTools that the page contains **no** Grafana DOM node (search the rendered source) and issues **no** `/api/grafana/*` request beyond the single `status` call (decision 7)
- [ ] verify the directory-only path: with **only** `GRAFANA_DASHBOARDS_DIR` set — no URL, no token — the UI appears and works
- [ ] verify the errored path: point `GRAFANA_URL` at a dead host — the app starts, serves, reports `state: error`, and every column reads `no-coverage`, never `unused`
- [ ] verify the scanning path: no report is shown as empty before the first scan completes
- [ ] verify `${var}` expansion resolves `aggregated.newcust_${period2}_distributed` against the real corpus
- [ ] verify usage on `probe_raw.siplog_distributed` propagates to its local table
- [ ] verify a sorting-key column that no dashboard selects is **not** reported `unused`
- [ ] verify the verdict pass issues 2 ClickHouse queries, not ~350 (log or count them)
- [ ] run the full check sequence from `.ai/rules.md`: `go build -o clickhouse-schemaflow-visualizer .`, `go vet ./...`, `golangci-lint run`, `go test ./...`
- [ ] confirm `gofmt` was applied only to touched regions of `models/clickhouse.go`
- [ ] verify against the local stack (`docker compose -f docker-compose.clickhouse-test.yml up -d`) that relation discovery still behaves — `.ai/rules.md` requires DDL-parsing changes be checked against the seed, not reasoned about

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
