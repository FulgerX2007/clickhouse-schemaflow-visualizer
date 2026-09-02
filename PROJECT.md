# PROJECT.md — ClickHouse Schema Flow Visualizer

The governing specification for this repository. It is the **single source of truth**
for how this project is built, what standards apply, and how AI agents must behave
here. Read this before changing code. Keep it current (see CONTRIBUTING.md).

> Audience: maintainers and AI coding agents.
> Rule: only assert what the repo can prove. Unknowns stay as `TODO:`.

---

## 1. Business context

- **What this project does:** A single-binary Go web application that connects to a ClickHouse instance, parses `system.tables` metadata (engine types, dependency columns, materialized-view `SELECT` bodies) and renders two interactive diagrams: a table-level data-flow DAG and a column-level relationship graph with transformation expressions on the edges (`README.md:7`). Layout is computed with the bundled Dagre build (`static/js/vendor/dagre.min.js`) and drawn as inline SVG (`static/js/diagram.js`).
- **Who uses it / who depends on it:** TODO: no file in the repository names a consumer, downstream system, or user group.
- **Why it exists (the problem it solves):** ClickHouse does not expose table lineage directly; the app derives it from `system.tables` and `system.columns` and presents it visually (`README.md`, `models/clickhouse.go`, `models/graph.go`). It is **strictly read-only** against ClickHouse — no `INSERT`, `ALTER`, `CREATE` or `DROP` is issued anywhere in the codebase (verified across `models/clickhouse.go` and `models/graph.go`; see §6).
- **Owners / points of contact:** The only owner-like fact in the repository is the package metadata in `.goreleaser.yaml`: `vendor: Fulgerx2007`, `maintainer: Fulgerx2007 <fulgerx2007@gmail.com>`, `homepage: https://github.com/FulgerX2007/clickhouse-schemaflow-visualizer`. TODO: no `CODEOWNERS`, no `SECURITY.md`, no support channel, no escalation path exists (`find .github -type f` returns only the two workflow files).
- **Status / lifecycle:** TODO: no file declares active / maintenance / deprecated. Observable release facts only: the latest tag is `v2.1.0` (2026-05-20), the tag history runs `v0.0.1` (2025-04-24) → `v2.1.0` across 14 tags, and `git log v2.1.0..HEAD` is empty — there are no unreleased code commits.

## 2. Technology stack

- **Language(s) & version:** Go — module `github.com/fulgerX2007/clickhouse-schemaflow-visualizer` (`go.mod:1`), toolchain directive `go 1.26.3` (`go.mod:3`). CI pins `go-version: '1.26'` (`.github/workflows/release.yml`), the Docker builder is `golang:1.26-alpine` (`Dockerfile`). Frontend is vanilla HTML/CSS/JS with **no build step and no package manifest** the app uses (`static/`).
- **Frameworks / key libraries:** Direct requires in `go.mod`:
  - `github.com/gin-gonic/gin v1.10.0` — HTTP router and JSON responses (`main.go:52`, `api/handlers.go`).
  - `github.com/ClickHouse/clickhouse-go/v2 v2.34.0` — native-protocol client (`models/clickhouse.go` `NewClickHouseClient`).
  - `github.com/joho/godotenv v1.5.1` — loads `.env` from the working directory (`main.go:19`).
  - `github.com/go-faster/city v1.0.1` — **declared but unused**: grep over `main.go api models config` finds no import (`go.mod:8`). Do not describe hashing behaviour based on it.
  - Frontend: bundled `static/js/vendor/dagre.min.js`; Font Awesome 6.4.0 CSS and Google Fonts are pulled from CDNs by `static/html/index.html`.
- **Datastores:** ClickHouse only, and **read-only**. The application owns no schema, no tables, and no write path (§6).
- **Messaging / queues:** None. `go.mod` declares no broker, queue, or streaming client, and no such code exists in `main.go`, `api/`, or `models/`.
- **Build / package tooling:** `go build` (`Dockerfile`, `.github/workflows/release.yml`); GoReleaser v2 for archives and `deb`/`rpm`/`apk` packages (`.goreleaser.yaml`); Docker multi-stage build (`Dockerfile`); Docker Compose for local runs and the local test stack (`docker-compose.yml`, `docker-compose.clickhouse-test.yml`).
- **Runtime / deploy target:** Container image `ghcr.io/fulgerx2007/clickhouse-schemaflow-visualizer` built from `alpine:3.18`, `EXPOSE 8080`, `WORKDIR /app`, `CMD ["./clickhouse-schemaflow-visualizer"]` (`Dockerfile`, `.github/workflows/docker-publish.yml`); or a systemd service installed by the OS packages (`scripts/postinstall.sh` — see §8 for two known packaging defects). GoReleaser cross-builds `CGO_ENABLED=0` for linux/windows/darwin × amd64/arm64 (`.goreleaser.yaml`).

## 3. Coding standards

- **Style / formatter:** `gofmt`. Not enforced by any workflow — `.github/workflows/` contains no format check, and `gofmt -l .` currently reports `models/clickhouse.go` as unformatted (pre-existing). Run `gofmt -w` on files you touch; do not reformat `models/clickhouse.go` wholesale in an unrelated change.
- **Linter & config:** `golangci-lint` is run locally by the maintainer as a pre-commit habit (not recorded in any repository file), but **there is no `.golangci.yml` in the repository**, so it runs with upstream defaults and is not reproducible or enforced. `go vet ./...` is clean (exit 0). TODO: no committed linter configuration and no CI gate for either tool.
- **Naming conventions:** TODO: nothing in the repository documents naming rules beyond standard Go conventions observable in `main.go`, `api/handlers.go`, `models/clickhouse.go`, and `models/graph.go`.
- **Project-specific patterns to follow:** (representative files cited)
  - **Prefer values over pointers in new Go code** where a value works — a maintainer preference, not recorded in any repository file. The existing code uses pointers where they carry meaning or are idiomatic: `*uint64` row/byte counters that model ClickHouse `NULL` (`models/clickhouse.go` `TableInfo`), the `*ClickHouseClient` / `*Handler` receivers (`api/handlers.go:11-22`), `GetTableColumns(...) (*TableDetails, error)` (`models/clickhouse.go:429`), and `&clickhouse.Options{}` / `*tls.Config` in `NewClickHouseClient`.
  - **Engine classification is a cross-language contract.** `models/graph.go` `ClassifyEngine` maps raw engine names to exactly five `EngineType` string values — `mergetree`, `replicated`, `distributed`, `mview`, `dictionary` (constants at `models/graph.go:12-18`, `ClassifyEngine` at `:22-35`). Those literal strings are consumed by `classifyEngine()` (`static/js/app.js:16-24`, keys at `:8-14`) and `glyphFor()` (`static/js/app.js:887`), the legend in `static/html/index.html`, and the `.engine-*` CSS classes in `static/css/styles.css` at `:766-770`, `:827-831`, `:1066-1070` and `:1294-1298`. Adding or renaming a value requires a matching change in all four files.
  - **Every asset tag must carry the cache-buster.** `static/html/index.html` is rendered through `html/template` with a per-process `{{.BuildID}}` (`main.go:72-80`); every CSS/JS URL appends `?v={{.BuildID}}`. A new asset tag without it will be served stale across upgrades, since `/static` is only sent `Cache-Control: no-cache` (`main.go:62-66`).
  - **The database exclusion list is duplicated and must change in both places.** `allowedDatabase()` excludes `system`, `information_schema`, `performance_schema`, `mysql` (`models/clickhouse.go:331-346`); `BuildColumnIndex` carries the same intent as a hardcoded SQL `NOT IN` (`models/graph.go:119`). The two do not match exactly: the SQL lists five literals because it spells both `'information_schema'` and `'INFORMATION_SCHEMA'`, where `allowedDatabase()` compares with `strings.ToLower`. Keep both in step, and mind the different matching semantics.
  - **Sidebar labels are an HTML fragment with a matching parser.** `generateTableListContent` emits `<i class="fa-solid …"></i> name` with the name `html.EscapeString`'d (`models/clickhouse.go:189`); `addTableToList` in `static/js/app.js` strips that `<i>` tag with a regex. Changing either side breaks the other.
  - **Diagram CSS is duplicated by design.** `commonDiagramCss()` in `static/js/app.js` is inlined into the Export HTML output, so diagram styling exists both there and in `static/css/styles.css`. Style changes to nodes/edges must be applied to both or exports diverge from the live view.
- **Patterns to avoid:**
  - **Do not assume caches invalidate.** `DatabasesData`, `TableRelations`, and `TableMetadata` are package-level vars filled on first use and **never invalidated** (`models/clickhouse.go:34-36`, `models/clickhouse.go:201-203`). A process restart is the only way to pick up schema changes. Do not add a feature that presents cached data as live without addressing this.
  - **Do not deepen the positional string parsing.** Relation discovery splits `create_table_query`/`engine_full` on spaces and quotes rather than using a SQL parser: MergeTree and `Replicated*` take `strings.Split(createQuery, " ")[2]`; `Distributed` splits `engine_full` on `'` and requires ≥6 parts; `MaterializedView` reads the source from `"FROM "` and the destination from index `[5]` (`models/clickhouse.go:246-307`, `models/clickhouse.go:497-522`). This is brittle against DDL formatting. Extend it only where the existing shape already holds, and prefer the `loading_dependencies_database`/`loading_dependencies_table` columns (used for `Dictionary*`) where they are available.
  - **Do not treat `config/config.go` as live code.** Nothing imports package `config` (grep over `main.go api models`); `main.go:29-41` builds `models.Config` inline from `os.Getenv`. It is dead code (86 lines).
  - **Do not introduce a write path.** See §6.
  - **Do not build SVG from HTML strings.** `static/js/diagram.js` constructs nodes with `createElementNS` and exposes `window.SchemaDiagram.renderDataFlow(container, graph, {onNodeClick})` / `.renderRelationships(container, graph, {onTableClick})`. There is no sanitisation layer on that path, so string templating would introduce injection risk from schema-derived text.
  - **Do not repeat `.goreleaser.yaml`'s stale description as truth.** Its nfpm `description`/`rpm.summary` still claim the app uses "Mermaid.js diagrams"; the renderer is hand-written Dagre + SVG (`static/js/diagram.js`).

## 4. Security requirements

- **Secrets handling:** Configuration, including `CLICKHOUSE_PASSWORD`, comes from environment variables or a `.env` file loaded by godotenv (`main.go:19`, `main.go:29-41`). `.env` is gitignored (`.gitignore`); `.env.example` ships placeholders only and contains no credentials. `GET /api/connection` deliberately returns host, port, user, database, and the secure flag but **never the password** (`api/handlers.go:41-47`). Do not add the password to any response, log line, or template value.
- **AuthN / AuthZ model:** **None.** There is no authentication, authorization, session, or API-key middleware anywhere — `main.go` registers only `gin.Default()` plus a `Cache-Control` handler, and `api/handlers.go` registers six unguarded `GET` routes (`api/handlers.go:26-34`). Every endpoint is open to anyone who can reach `SERVER_ADDR`, and the exposed metadata includes full table lists, column names and types, and row/byte counts. TODO: no file states a required deployment posture (network isolation, reverse-proxy auth, or otherwise) — treat exposure as a deployment decision that is currently undocumented.
- **Input validation expectations:** Handlers reject empty `:database`/`:table` path params with `400` and return `500 {"error": msg}` on any ClickHouse failure (`api/handlers.go`). Parameterised queries are used for user-supplied identifiers — `WHERE database = ? AND name = ?` in `GetTableColumns` and `isDistributedTable`, `WHERE database = ? AND table = ?` in the column query (`models/clickhouse.go`, `models/graph.go`). Keep new queries parameterised; never interpolate a path param into SQL.
- **TLS to ClickHouse:** `NewClickHouseClient` builds a `tls.Config` when `CLICKHOUSE_SECURE` is set: a client cert+key pair is loaded together, a CA file is appended to a fresh `x509.CertPool`, `ServerName` is set for SNI, and `InsecureSkipVerify` comes straight from `CLICKHOUSE_SKIP_VERIFY` (`models/clickhouse.go`). Note that the shipped `.env.example` sets both `CLICKHOUSE_SECURE=true` **and** `CLICKHOUSE_SKIP_VERIFY=true`, which disables certificate verification — do not copy that pair into a real deployment.
- **Dependencies / supply chain policy:** TODO: no policy file, no Dependabot/Renovate config, no vulnerability scan in either workflow, and no pinned-dependency rule is documented. Observable facts only: `go.sum` is committed, direct dependencies are pinned to exact versions in `go.mod`, `go mod tidy` runs as a GoReleaser `before` hook (`.goreleaser.yaml`), and the frontend pulls Font Awesome and Google Fonts from third-party CDNs at runtime (`static/html/index.html`).
- **Data classification & handling:** TODO: no file classifies the metadata this app reads and serves. Factually, the app persists nothing — it holds ClickHouse metadata in process memory only (`models/clickhouse.go:34-36`) and writes no files at runtime.

## 5. API conventions

- **Style:** REST-ish JSON over HTTP. Six routes, all `GET`, all under the `/api` group (`api/handlers.go:26-34`):
  | Route | Returns | Source |
  |---|---|---|
  | `GET /api/connection` | host, port, user, database, secure — never the password | `api/handlers.go:41-47` |
  | `GET /api/databases` | `map[database][table] -> HTML label fragment`, served from the in-memory cache | `api/handlers.go`, `models/clickhouse.go` |
  | `GET /api/columns` | flat column index across allowed databases; powers the `Ctrl+K` / `⌘K` palette | `models/graph.go` `BuildColumnIndex` |
  | `GET /api/dataflow/:database/:table` | table-level DAG `{nodes, edges}` | `models/graph.go` `DataFlowGraph` |
  | `GET /api/relationships/:database/:table` | column-level DAG `{tables, edges}` with `expression` on edges | `models/graph.go` `RelationshipsGraph` |
  | `GET /api/table/:database/:table` | `TableDetails`: engine, primary_key, sorting_key, partition_key, total_rows, total_bytes, columns[] | `models/clickhouse.go` `GetTableColumns` |

  Outside `/api`: `GET /` renders `static/html/index.html` through `html/template` with a per-process `BuildID`, and `/static/*` is served by `router.Static` behind a `Cache-Control: no-cache` middleware (`main.go:57-80`).
- **Versioning:** None. There is no version segment, header, or negotiation — the group is a bare `/api` (`api/handlers.go:26`). Release versioning is the `v*` git tag convention that both workflows trigger on (`.github/workflows/release.yml`, `.github/workflows/docker-publish.yml`). TODO: no documented policy for what constitutes a major/minor/patch bump, and no rule for evolving the JSON shapes compatibly.
- **Error format:** `{"error": "<message>"}` — `400` when `:database` or `:table` is empty, `500` on any ClickHouse failure, with the raw `err.Error()` as the message (`api/handlers.go`). Note this propagates backend error text to unauthenticated clients.
- **Auth scheme:** None — see §4.
- **Source of truth for the contract:** `api/handlers.go` and the response structs in `models/graph.go` / `models/clickhouse.go`. TODO: there is no OpenAPI/proto schema; `docs/api/` exists but is empty.
- **Absent by design or by omission (state, do not assume otherwise):** no pagination, no rate limiting, and no request-size or result-size limits anywhere in the codebase. `GET /api/columns` returns every column of every allowed database in one response.

## 6. Database conventions

- **Engine(s):** ClickHouse, over the native protocol (default port `9000`, `main.go:31`), via `clickhouse-go/v2` (`models/clickhouse.go`).
- **Migration tool & location:** None, and none is appropriate. **This application owns no schema, has no migrations, no ORM, and no write path.** It reads only `system.tables` and `system.columns`:
  - `SELECT create_table_query, engine_full, engine, database, name, loading_dependencies_database, loading_dependencies_table, total_rows, total_bytes FROM system.tables ORDER BY name` (`models/clickhouse.go` `getTablesRelations`)
  - `SELECT engine, total_rows, total_bytes, primary_key, sorting_key, partition_key FROM system.tables WHERE database = ? AND name = ?` and `SELECT name, type, position, comment FROM system.columns WHERE database = ? AND table = ? ORDER BY position` (`models/clickhouse.go` `GetTableColumns`)
  - `SELECT database, table, name, type FROM system.columns WHERE database NOT IN (…)` (`models/graph.go:119` `BuildColumnIndex`)
  - `SELECT engine FROM system.tables WHERE database = ? AND name = ?` (`models/clickhouse.go` `isDistributedTable`)

  `scripts/clickhouse_test_engines.sql` is **seed data for the local test stack, not a migration** — it is mounted into `/docker-entrypoint-initdb.d` by `docker-compose.clickhouse-test.yml`.
- **Migration naming:** N/A — no migrations exist.
- **Rules:**
  - **Read-only is a hard constraint.** Never add a statement that mutates the connected ClickHouse instance. The app is expected to be safe to point at production.
  - **Keep queries parameterised** for anything derived from a request path (§4).
  - **Caching:** `DatabasesData`, `TableRelations`, `TableMetadata` are filled once and never invalidated (`models/clickhouse.go:34-36`, `:201-203`). `GetTableColumns`, `BuildColumnIndex`, and `isDistributedTable` are **uncached** and hit ClickHouse on every request — account for that before adding a caller in a hot path.
  - **Graph identity:** node IDs are the plain `database.table` string, split back apart with `strings.SplitN(id, ".", 2)` (`models/graph.go:184-189`) — there is no hashing. A database or table name containing a `.` will split wrong.
  - **Traversal depth** is bounded by `maxRelationDepth = 50` in `walkForward`/`walkBackward` (`models/clickhouse.go:16`, `models/graph.go:202`, `:222`).
- **Schema docs:** see [docs/database/](docs/database/) — TODO: the directory exists but is currently empty.

## 7. Testing requirements

- **Test command:** `go test ./...` — it passes, but reports `[no test files]` for all four packages.
- **Frameworks:** None in use. **There are zero test files in the repository** (`find . -name '*_test.go'` outside `node_modules/` returns 0). Do not describe a test suite, helpers, or fixtures that do not exist.
- **Coverage expectation:** TODO: none is documented, and current coverage is 0%.
- **What must have tests:** TODO: no policy exists. The highest-value untested surfaces, if a policy is written: the positional DDL parsing in `models/clickhouse.go:246-307` and `:497-522`, `ClassifyEngine` (`models/graph.go:22-33`), and the graph traversal in `models/graph.go`.
- **Integration / e2e setup:** Manual only — there is no automated integration test. `docker-compose.clickhouse-test.yml` starts `zookeeper:3.7` on 2181 and `clickhouse/clickhouse-server:latest` as container `clickhouse-test-engines` on 9000/8123 (`CLICKHOUSE_USER=default`, `CLICKHOUSE_PASSWORD=default`, `CLICKHOUSE_DB=test_engines`), mounting `scripts/clickhouse_test_engines.sql` as seed data and `config/clickhouse-config.xml` as the ZooKeeper config, with the visualizer container attached to a `clickhouse-network` bridge and reading `.env`. The seed covers the engine families the parser branches on: database `raw` (`airports_local` MergeTree, `flights_local` ReplicatedMergeTree, each with a `Distributed` wrapper) and `aggregated` (`flight_stats_daily_local` ReplicatedAggregatingMergeTree, `airport_traffic_hourly_local` SummingMergeTree, each fed by a Materialized View from `raw.flights_local` and exposed through a `Distributed` wrapper) — `README.md`, `scripts/clickhouse_test_engines.sql`. Re-seed with `docker compose -f docker-compose.clickhouse-test.yml down -v` then `up -d`.

**Local command set.** The `build` / `vet` / `test` / `gofmt` lines below were run against this repository and their stated results reproduced; `go run` and the compose commands need a reachable ClickHouse and were not executed here.

```bash
go run main.go                                     # run (requires a reachable ClickHouse; a failed Ping is fatal)
go build -o clickhouse-schemaflow-visualizer .     # build — exit 0
go vet ./...                                       # clean — exit 0
go test ./...                                      # passes; "[no test files]" for all four packages
gofmt -l .                                         # currently lists models/clickhouse.go (pre-existing)
golangci-lint run                                  # runs with upstream defaults — no .golangci.yml in the repo
docker-compose -f docker-compose.clickhouse-test.yml up -d   # local ClickHouse + seed + app
docker-compose up -d                               # local build, host network, reads .env
```

## 8. Deployment constraints

- **Environments:** TODO: no environment definitions, no per-environment config, and no promotion path exist in the repository. The only compose files are a local build (`docker-compose.yml`: `container_name: clickhouse-schemaflow-visualizer`, `ports 8080:8080`, `env_file: .env`, `restart: unless-stopped`, `network_mode: "host"`) and the local test stack (§7).
- **Pipeline:** GitHub Actions — two workflows, **both triggered only by tags matching `v*`** (details in `docs/deployment/`, currently empty):
  - `.github/workflows/release.yml` — job `test` (setup-go 1.26, `go mod download`, `go test ./...`, `go build`), then job `goreleaser` (goreleaser-action v6, `release --clean`).
  - `.github/workflows/docker-publish.yml` — buildx, login to `ghcr.io`, metadata-action semver tags, push `ghcr.io/fulgerx2007/clickhouse-schemaflow-visualizer`.

  **Critical gap:** no workflow runs on push to `master` or on `pull_request`. Nothing — not build, not vet, not `go test` (which has no tests anyway), not formatting — is checked automatically before a tag is cut. Verify changes locally with the command set in §7.
- **Release process:** Tag `vX.Y.Z` and push the tag; both workflows fire. GoReleaser (`.goreleaser.yaml`, version 2) runs `go mod tidy` and `go generate ./...` as `before` hooks, builds `CGO_ENABLED=0` for linux/windows/darwin × amd64/arm64, produces `tar.gz` archives (`zip` on Windows) containing `README.md`, `LICENSE*`, `static/**/*`, `.env.example`, and builds `deb`/`rpm`/`apk` packages named `clickhouse-schemaflow-visualizer` (license MIT, dependency `ca-certificates`) with `scripts/{pre,post}install.sh` and `scripts/{pre,post}remove.sh`. Packages install `static/` to `/usr/share/clickhouse-schemaflow-visualizer/static` and `.env.example` to `/etc/clickhouse-schemaflow-visualizer/config.env.example`. The changelog excludes `^docs:` and `^test:` commits. TODO: no documented rule for who may tag, what a release must contain, or how the version number is chosen beyond the `v*` tag convention the workflows match.
- **Package install behaviour** (`scripts/preinstall.sh`, `scripts/postinstall.sh`): creates the system user `clickhouse-schemaflow-visualizer` (`useradd --system`; the matching group is created by `useradd` on most distributions, but no explicit `groupadd` is run) and the directories `/var/lib/`, `/var/log/`, `/etc/clickhouse-schemaflow-visualizer`; writes `/etc/systemd/system/clickhouse-schemaflow-visualizer.service` (`Type=simple`, that user/group, `WorkingDirectory=/var/lib/clickhouse-schemaflow-visualizer`, `ExecStart=/usr/bin/clickhouse-schemaflow-visualizer`, `Restart=always`/10s, journal logging, `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=strict`, `ProtectHome`, `ReadWritePaths` for `/var/lib` and `/var/log`, `CAP_NET_BIND_SERVICE`); copies `config.env.example` to `/etc/clickhouse-schemaflow-visualizer/config.env` (0640, root:service group); then `daemon-reload` and `enable`. Logs: `journalctl -u clickhouse-schemaflow-visualizer -f`.
- **Known packaging defects (do not soften; all three are derivable from the shipped files):**
  1. **The packaged service cannot find its assets.** The binary resolves `./static` and `./static/html/index.html` relative to the working directory (`main.go:66`, `main.go:72`), but the unit sets `WorkingDirectory=/var/lib/clickhouse-schemaflow-visualizer` while the package installs static files to `/usr/share/clickhouse-schemaflow-visualizer/static` (`.goreleaser.yaml`, `scripts/postinstall.sh`).
  2. **The packaged config is never loaded.** Config is read by godotenv from `.env` in the working directory (`main.go:19`), while the package writes `/etc/clickhouse-schemaflow-visualizer/config.env` and the unit declares no `EnvironmentFile` (`scripts/postinstall.sh`).
  3. Cosmetic: the systemd unit `Description` and the install scripts still call the app **"ChView2"**, an old name.
- **Rollback:** TODO: no rollback procedure is documented. Mechanically, prior versions remain available as GitHub release artefacts and as semver-tagged images in `ghcr.io` (`.github/workflows/docker-publish.yml`), but no file states a supported rollback path.
- **Operational limits / quotas:** TODO: none documented. Factually: a failed ClickHouse `Ping` at startup is fatal (`main.go:47`); the process serves from never-invalidated in-memory caches, so a restart is required to reflect schema changes (§6); and there is no rate limiting, pagination, or result-size cap on any endpoint (§5).

## 9. AI-specific instructions

Rules an AI agent must follow when working in this repo (the enforcement detail
lives in [.ai/rules.md](.ai/rules.md)):

- Read this file, `.ai/rules.md`, and `ARCHITECTURE.md` before editing. Repo-specific architecture and gotcha notes also live in `CLAUDE.md` at the repository root — read it, and cross-link it rather than duplicating it.
- Cite file paths for every factual claim; never invent config, contracts, or owners.
- Reuse existing patterns and utilities before writing new ones.
- Keep changes atomic and update the impacted docs + CHANGELOG in the same change.
- Mark anything you cannot verify as `TODO:` rather than guessing.
- Stop and ask if a requested change conflicts with a rule here.

Repository-specific rules that bind an agent here:

- **Never introduce a ClickHouse write.** Read-only against `system.tables` and `system.columns` is the project's core safety property (§6).
- **Never claim tests pass as evidence of correctness** — there are none (§7). Verify with `go build`, `go vet`, and a manual run against `docker-compose.clickhouse-test.yml`.
- **Changing an `EngineType` value means changing four files** — `models/graph.go`, `static/js/app.js`, `static/html/index.html`, `static/css/styles.css` (§3).
- **Changing the database exclusion list means changing two places** — `models/clickhouse.go` `allowedDatabase()` and `models/graph.go:119` (§3).
- **Any new CSS/JS tag must carry `?v={{.BuildID}}`** (§3).
- **Do not commit automatically.** Prompt first; use conventional commit messages; never `git commit --amend` or `git reset`; run `gofmt`, `go vet`, `go build`, and `golangci-lint` before proposing a commit (maintainer's standing workflow rules).
- TODO: the branching model, PR review policy, and required approvals are undocumented. `README.md:234` says only "Contributions are welcome! Please feel free to submit a Pull Request." Do not invent a branch naming scheme or review gate — ask.
