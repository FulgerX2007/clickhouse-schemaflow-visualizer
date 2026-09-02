# AI Agent Rules — ClickHouse Schema Flow Visualizer

Operating rules for AI coding agents working in this repository. These are
**enforceable constraints**, not suggestions. If a task conflicts with a rule here,
stop and ask.

> See also: [CLAUDE.md](../CLAUDE.md) — this file does not duplicate it.

`CLAUDE.md` carries the architecture walkthrough, the per-engine parsing detail and
the command list. This file carries only the rules you must not break. Read both.

## Operating procedure (run this loop for every task)

1. Read `PROJECT.md` — stack, standards, conventions, constraints.
2. Read this file — the hard rules below.
3. Read `ARCHITECTURE.md` — components, data flow, dependencies.
4. **Detect impacted modules** from the task and the diff; list them with paths.
5. Update code following existing conventions.
6. Update the impacted documentation (see *Documentation update requirements*).
7. Add a `CHANGELOG.md` entry under `[Unreleased]`.

If a document in this loop is not present in the tree, say so in your summary and
move on — never invent its contents. The Go code lives in four packages only
(`main.go`, `api/`, `models/`, `config/`, confirmed by `go.mod` and the tree), so
"impacted modules" is usually a short, checkable list.

## Repository-specific hard rules

These are the rules that break this repository when ignored. Every one is
verifiable in the cited file.

**1. Never add a write path to ClickHouse.** The app issues `SELECT` only, against
`system.tables` and `system.columns` (`models/clickhouse.go` `getTablesRelations`,
`GetTableColumns`, `isDistributedTable`; `models/graph.go` `BuildColumnIndex`;
`models/usage.go` `loadTableKeys`). Do not introduce `INSERT`, `CREATE`, `ALTER`,
`DROP`, `OPTIMIZE`, or any mutating ClickHouse statement, even behind a flag.

Every route was `GET` until `POST /api/grafana/refresh`, which mutates only the
in-process dashboard cache and never ClickHouse. Adding another non-`GET` route needs
the same justification and the same debounce.

**2. User input reaches SQL only as a bound parameter.** `database`/`table` from the
URL are passed as `?` arguments (`models/clickhouse.go:442,455,646`;
`models/graph.go:254`). Never build a query by interpolating a request value with
`fmt.Sprintf` or string concatenation.

**3. The five `EngineType` strings are a cross-file contract.**
`mergetree`, `replicated`, `distributed`, `mview`, `dictionary`
(`models/graph.go:13-17`, produced by `ClassifyEngine`). The same literals are
consumed by:

- `static/js/app.js` — the engine table (`:9-13`), `classifyEngine()` (`:16-23`),
  the search aliases (`:660-664`), `glyphFor()` (`:887-893`)
- `static/html/index.html` — the legend (`:83-87`)
- `static/css/styles.css` — the `--t-<engine>-fg/bg` variables (`:20-29`), the legend
  glyph rules (`:451-455`), the `.engine-<name>` rail rules (`:766-770` for `.df-node`,
  `:827-831` for `.rel-table`), the engine chip (`:1066-1070`) and the palette row glyph
  (`:1294-1298`)

- `static/js/app.js` — **`exportHtml()`'s inline `:root` block (`:493-494`) redeclares the
  five `--t-*-fg` variables independently of both `styles.css` and `commonDiagramCss()`.**
  It is the site most easily missed: omit it and the live view is correct while the
  exported HTML renders that family's rail with an undefined variable.

Adding, renaming or removing an engine family is a **five-file change in one commit**,
across eleven sites. A node whose engine key has no CSS class renders unstyled with no
error.

The column-usage verdicts (`used`, `unused`, `unknown`, `no-coverage`) are a smaller
contract of the same kind: `models/usage.go` produces them, `static/js/app.js` maps them
in `VERDICT_LABELS`/`VERDICT_TITLES`, and `static/css/styles.css` styles them via
`--v-*` tokens and `.verdict-<name>` classes.

**4. `allowedDatabase()` and `BuildColumnIndex`'s `NOT IN` list change together.**
`models/clickhouse.go:331-346` excludes `system`, `information_schema`,
`performance_schema`, `mysql`; `models/graph.go:119` repeats the same list inline in
SQL (plus the uppercase `INFORMATION_SCHEMA` spelling). Edit one without the other
and the `Ctrl+K` palette surfaces tables the sidebar hides, or vice versa.

**5. Every new static asset tag carries the build-id query string.**
`static/html/index.html` is rendered through `html/template` with a per-process
`BuildID` (`main.go:72-80`); the existing tags use `?v={{.BuildID}}`
(`index.html:10,31,171,172`). A tag without it is served with `Cache-Control:
no-cache` (`main.go:62-66`) but can still be reused from a stale defer-loaded cache
entry after an upgrade. The Font Awesome CDN tag additionally carries
`integrity` and `crossorigin` (`index.html:32`); the Google Fonts stylesheet
(`index.html:9`) carries neither.

**6. Do not reintroduce HTML-string assembly in the diagram path.**
`static/js/diagram.js` builds SVG exclusively with `createElementNS` and
`createTextNode` (`:19-31`); `innerHTML` appears only to clear a container
(`:243`, `:415`). Column names and transformation expressions arrive from ClickHouse
DDL and are written as text nodes — there is no sanitiser on that path, so switching
to string templating turns metadata into injected markup.

**7. The sidebar label is an HTML fragment with a matching regex on the other side.**
`generateTableListContent` emits `<i class="fa-solid …"></i> name` with the name
`html.EscapeString`d (`models/clickhouse.go:187-198`), and
`addTableToList` strips that `<i …></i>` back out with a regex before re-inserting it
(`static/js/app.js:233-241`). Change the icon markup and you must change the regex.

**8. Caches are never invalidated — with one documented exception.**
`DatabasesData`, `TableRelations` and `TableMetadata` are package-level vars
(`models/clickhouse.go:34-36`) filled on first use; a process restart is the only way
to pick up schema changes. Do not write code that assumes a later request sees fresh
data. `GetTableColumns`, `BuildColumnIndex` and `isDistributedTable` are uncached and
hit ClickHouse per request — keep it that way unless you are deliberately changing the
caching story, and document it if you do.

The exception is `models.GrafanaIndex` (`models/grafana_index.go`), which owns the
Grafana dashboard scan and everything derived from it. It **does** invalidate: it holds
its state behind a `sync.RWMutex`, honours `GRAFANA_CACHE_TTL`, and rebuilds on
`POST /api/grafana/refresh`. The TTL doubles as that endpoint's debounce, because the
HTTP API has no authentication or rate limiting and a refresh costs one request per
dashboard against Grafana. A failed rescan leaves the previous good index in place
rather than emptying it — an empty usage index reads as "nothing uses anything", which
is precisely the conclusion that would get a live column dropped. `LoadSchemaSnapshot`
warms `getTablesRelations` itself for the same reason: a scan that ran before any
request would otherwise index against nil relations.

**9. Keep the recursion guard.** `maxRelationDepth = 50`
(`models/clickhouse.go:16`) bounds `walkForward`/`walkBackward`
(`models/graph.go:202,222`). Do not remove or raise it without a stated reason;
relation graphs can cycle.

**10. Configuration is added in `main.go`, not in `config/`.** `config/config.go` is
dead code — nothing imports the package; `main.go:29-41` builds `models.Config`
directly from `os.Getenv`. A new setting means: `main.go`, `models.Config`
(`models/clickhouse.go:20-31`), and `.env.example`.

**11. Never commit `.env`.** It is gitignored (`.gitignore`); `.env.example` is the
tracked template and must stay in step with the variables `main.go` reads.

**12. Nothing is checked automatically before a tag exists.** Both workflows trigger
only on `v*` tags (`.github/workflows/release.yml:3-6`,
`.github/workflows/docker-publish.yml:3-6`) — there is no push or pull_request job.
Run the checks locally yourself (see *Testing standards*).

## Coding conventions

- Go is formatted with `gofmt` and must pass `go vet ./...` (clean today) and
  `golangci-lint run`. There is **no `.golangci.yml`** in the repo, so the linter runs
  with its default set.
- `models/clickhouse.go` is currently not `gofmt`-clean. Run `gofmt -w` on the region
  you touched, not `gofmt -w .`, so the diff stays reviewable
  (this matches the guidance in `CLAUDE.md`).
- Errors are wrapped with `fmt.Errorf`. `models/graph.go` uses `%w` throughout
  (7 occurrences); `models/clickhouse.go` uses `%v` throughout (12 occurrences) and does
  not wrap. `api/handlers.go` calls `fmt.Errorf` nowhere — it passes `err.Error()` straight
  into the `gin.H` response body. Prefer `%w` in new code. TODO: decide and record the
  canonical wrapping convention, then make `models/clickhouse.go` consistent.
- `api/` stays thin: handlers read params, call a `models` method, and marshal JSON
  (`api/handlers.go`). Business logic belongs in `models/`.
- Frontend is vanilla HTML/CSS/JS with **no build step and no package manager** — the
  only bundled library is `static/js/vendor/dagre.min.js`. The `node_modules/`
  directory in the tree is not referenced by any manifest and is not part of the
  build; do not start depending on it.
- Reuse existing utilities and patterns before introducing new ones — search first.
- Match the surrounding code's naming, structure, and idioms.

## Refactoring rules

- Refactors are behavior-preserving; do not mix a refactor with a feature/fix.
- Keep changes atomic and scoped to the task.
- Do not rename/move public symbols without updating all references and docs. The
  exported surface that the frontend depends on includes the JSON field names in
  `models/graph.go` (`engine_type`, `expression`, `nodes`/`edges`, `tables`/`edges`)
  and the `window.SchemaDiagram.renderDataFlow` / `.renderRelationships` signatures in
  `static/js/diagram.js`.
- Engine parsing in `models/clickhouse.go` is positional string splitting on
  `create_table_query` / `engine_full` — no SQL parser. It is sensitive to
  ClickHouse's exact DDL formatting, so any "cleanup" there must be verified against
  the test stack (see *Testing standards*), not reasoned about.
- Diagram CSS is duplicated: `static/css/styles.css` and `commonDiagramCss()` in
  `static/js/app.js:540` (inlined into the Export HTML output). Change both.

## Database modification rules

- **This application owns no schema.** There are no migrations, no ORM and no
  migration tool in the repository, because the app only reads ClickHouse system
  tables (rule 1 above). Do not add a migration framework to satisfy a template.
- The only DDL in the repo is `scripts/clickhouse_test_engines.sql`, the seed for the
  local test stack (`docker-compose.clickhouse-test.yml` mounts it into
  `/docker-entrypoint-initdb.d`). It creates the `raw` and `aggregated` databases used
  for manual verification.
- Treat that seed as append-mostly: it runs only on a fresh volume, so an edit takes
  effect only after `docker compose -f docker-compose.clickhouse-test.yml down -v`
  followed by `up -d`. Changing it changes what every manual verification run sees.
- Document every change to the seed schema in `docs/database/` and `CHANGELOG.md`, and
  say in the entry which engine families it exercises.
- If a change alters which ClickHouse system columns are queried, update the query
  list in the architecture docs in the same change.

## Logging standards

- The only logging is the **Go standard library `log` package** (`main.go:5`,
  `models/clickhouse.go:9`). There is no structured logger, no leveled logger and no
  logging dependency in `go.mod` — do not assume one exists, and do not add one as a
  side effect of another task.
- HTTP request logging comes from `gin.Default()` (`main.go:52`), which installs
  Gin's Logger and Recovery middleware.
- Fatal only at startup: `log.Fatalf` is used for a failed ClickHouse connect and a
  failed listen (`main.go:47,88`). Never call `log.Fatal*` from request-handling code.
- No secrets, credentials, tokens, or PII in logs. The connection password is never
  logged and never returned by `GET /api/connection` (`api/handlers.go:41-47`) — keep
  it that way.
- Log actionable context (ids, operation, outcome), not noise.
  `models/clickhouse.go:585` logs one line per discovered column mapping on every
  uncached relationship build; do not add more per-row logging in that style.
- TODO: define the level convention (what warrants a log line at all, and whether a
  leveled/structured logger should replace stdlib `log`) — the repo states no policy.

## Error handling standards

- Handle errors explicitly; never silently swallow them.
- Fail loudly in the right layer; surface actionable messages.
- The HTTP contract, as implemented in `api/handlers.go`: `400` with
  `{"error": "database and table parameters are required"}` when a path parameter is
  empty; `500` with `{"error": err.Error()}` for any ClickHouse failure; otherwise
  `200` with the JSON payload. Keep new endpoints to that shape.
- Note that `500` bodies today echo the raw driver error to the client
  (`c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})`). Do not
  widen that: never put connection strings, credentials or full DDL into an error
  returned to the browser.
- TODO: project-specific error type / wrapping convention (see *Coding conventions* —
  `%w` vs `%v` is currently split by file and no policy is recorded).

## Testing standards

- Test command: `go test ./...`.
- **Coverage is partial.** Six `*_test.go` files cover `api/` and `models/`, all added
  with the Grafana column-usage feature: table-driven tests, `httptest` for the Grafana
  API client, and JSON fixtures in `models/testdata/dashboards/`. `main.go` and
  `config/` have none, and the frontend has no automated coverage at all. Do not claim
  a suite reaches further than that.
- `models/grafana_corpus_test.go` is a measurement instrument rather than a fixture
  test: it skips unless `GRAFANA_DASHBOARDS_DIR` names a real dashboard tree, and fails
  if the SQL parse rate drops below 70%.
- Local check sequence before proposing a commit, all of which currently pass:
  `go build -o clickhouse-schemaflow-visualizer .`, `go vet ./...`,
  `golangci-lint run`, `go test ./...`.
- Verification is manual, against the local stack: `docker compose -f
  docker-compose.clickhouse-test.yml up -d` brings up ZooKeeper 3.7, ClickHouse
  (`clickhouse-test-engines`, ports 9000/8123, user `default`, password `default`,
  DB `test_engines`) and the visualizer, seeded from
  `scripts/clickhouse_test_engines.sql`. Re-seed with `down -v` then `up -d`.
- Because relation discovery is DDL-string parsing, any change to it must be checked
  against the engine families the seed provides (MergeTree, ReplicatedMergeTree,
  SummingMergeTree/AggregatingMergeTree, Distributed, MaterializedView) — reading the
  code is not sufficient evidence. Note the gap: the seed contains no `Dictionary`
  object, so the `dictionary` family from rule 3 has no local test coverage at all.
- New business logic must ship with tests.
- TODO: coverage expectation and an automated integration-test setup — neither exists
  today, and the release workflow's `go test ./...` step therefore asserts nothing.

## Security restrictions

- No hardcoded secrets; read config/secrets from the approved mechanism. Config comes
  from environment variables, loaded from `.env` by `godotenv` (`main.go:19-41`);
  `.env` is gitignored and `.env.example` is the tracked template.
- Validate and sanitize all external input. Concretely, in this repo: bind path
  parameters into SQL (rule 2), keep untrusted metadata on the `createElementNS` /
  text-node path (rule 6), and keep `html.EscapeString` on the one place that emits an
  HTML fragment from a table name (`models/clickhouse.go:189`).
- **The HTTP API has no authentication, no authorization, no rate limiting and no
  pagination** anywhere in the codebase. Every endpoint exposes the full schema
  metadata of the connected ClickHouse to anyone who can reach the port. Do not
  present the server as safe to expose publicly, and do not add an endpoint that
  assumes an authenticated caller.
- TLS to ClickHouse is configurable (`CLICKHOUSE_SECURE`, `CLICKHOUSE_SKIP_VERIFY`,
  cert/key/CA/server-name paths, wired in `models/clickhouse.go`
  `NewClickHouseClient`). `CLICKHOUSE_SKIP_VERIFY=true` sets `InsecureSkipVerify` —
  never make it the default or hardcode it.
- Do not add dependencies without justification; prefer the standard library. The
  direct set is four modules (`go.mod`): `clickhouse-go/v2`, `gin`, `godotenv`, and
  `go-faster/city` — the last of which is declared but imported by no Go file.
  TODO: decide whether `github.com/go-faster/city` is dropped from `go.mod` or put
  back to use.
- There is no `SECURITY.md`, no `CODEOWNERS` and no issue/PR template
  (`.github/` contains only the two workflow files). TODO: security contact and
  vulnerability-reporting process.
- TODO: branching, review and PR conventions. `README.md` says only "Contributions are
  welcome! Please feel free to submit a Pull Request." Commit subjects in history use
  conventional-commit prefixes and `.goreleaser.yaml:80-86` filters `^docs:` and
  `^test:` out of the release changelog, so prefixes are expected — but no written
  policy exists.

## Documentation update requirements

A change is **not complete** until its docs are updated in the same change:

- Structure/component/dependency changed → `ARCHITECTURE.md` (+ diagram).
- Public API changed → `docs/api/`.
- Schema/migration changed → `docs/database/` + migration note.
- Deploy/config changed → `docs/deployment/`.
- Significant, hard-to-reverse decision → new ADR in `docs/decisions/`.
- User-facing behavior changed → `README.md`.
- Always → a `CHANGELOG.md` entry under `[Unreleased]`.

Repo-specific triggers on top of the list above:

- New or changed environment variable → `main.go`, `.env.example`, the `.env` code block in
  `README.md` (it has no env-var table), the config table in `CLAUDE.md`, and
  `docs/deployment/`.
- New or changed `/api/` route or JSON field → `docs/api/`, plus the endpoint lists in
  `README.md` and `CLAUDE.md`.
- New engine family → all five files in rule 3, plus the legend description in
  `README.md`.
- Change to packaging (`.goreleaser.yaml`, `Dockerfile`, `scripts/*install.sh`,
  `scripts/*remove.sh`, the systemd unit) → `docs/deployment/`.

Cite file paths for every factual claim. Mark anything you cannot verify as `TODO:`.
Never invent configuration, contracts, SLAs, or owners.
