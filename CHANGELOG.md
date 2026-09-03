# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Released versions are tagged `vX.Y.Z` in git, and both GitHub workflows fire only on
tags matching `v*` (`.github/workflows/release.yml`, `.github/workflows/docker-publish.yml`).

## [Unreleased]

### Fixed

- **A single request could kill the server process.** `simplifyColumnType`
  (`models/clickhouse.go`) matched `Nullable` with `strings.Contains` and then unwrapped
  it with `strings.TrimPrefix`. For a type that mentions `Nullable` without being one —
  `SimpleAggregateFunction(groupBitOr, Nullable(Bool))`, which ClickHouse writes for a
  nullable `Bool` under `SimpleAggregateFunction` — the prefix never matched, the string
  never shrank, and the function recursed on it forever. A stack overflow is not
  recoverable in Go, so one `GET /api/relationships/:database/:table` for a table holding
  such a column took the whole application down. The branch now requires the
  `Nullable(` prefix, so every recursion strips at least nine characters.
  Covered by `models/clickhouse_test.go`, the package's first non-Grafana test.

- **Column names and types overlapped in the Relationships diagram.** The two are
  anchored from opposite ends of a 240px card and were both written in full, so a real
  ClickHouse type — `SimpleAggregateFunction(sum, UInt64)`, 36 characters — printed
  straight through the column name beside it. Both are now fitted to the space with the
  full text on hover.

- **Exported Relationships diagrams rendered every column row as a black bar.**
  `commonDiagramCss()` in `static/js/app.js` carried `.rel-col-bg` but not
  `.rel-col-hit`, and an SVG `rect` with no `fill` paints black. The live stylesheet
  makes those hit targets transparent; the exported copy did not.

### Added

- **Grafana column usage.** Optional feature that maps Grafana dashboards to the
  ClickHouse columns they read, so unused columns can be found. Off unless configured:
  with no `GRAFANA_*` variable the application and UI are unchanged.
  - Dashboards are read from the Grafana HTTP API (`GRAFANA_URL` + `GRAFANA_TOKEN`) or
    from a directory of JSON files (`GRAFANA_DASHBOARDS_DIR`, no credentials). Set both
    and the API is primary with the directory as a fallback, reported as
    `mode: api-fallback-dir`.
  - Panel queries and templating variables are resolved with
    `github.com/AfterShip/clickhouse-sql-parser` (new direct dependency, pinned v0.5.6),
    falling back to name matching when a query will not parse. Grafana template variables
    are expanded first; a variable inside a table name becomes a pattern matching every
    table it can name.
  - Usage propagates along ClickHouse lineage: a dashboard on a `Distributed` wrapper
    marks the local table, and one reading a materialized view's destination marks every
    source column the view reads.
  - Columns get one of four verdicts — `used`, `unused`, `unknown`, `no-coverage` — of
    which only `unused` licenses a drop. Key columns and `Distributed` tables are never
    reported unused, and no column is reported unused unless a dashboard scan completed.
    See `docs/decisions/0002-four-state-column-verdicts.md`.
  - New endpoints: `GET /api/grafana/status`, `GET /api/grafana/usage/:database/:table`,
    `GET /api/grafana/unused`, and `POST /api/grafana/refresh` — the first non-`GET`
    route in the API, debounced by `GRAFANA_CACHE_TTL`. It writes nothing to ClickHouse.
  - New UI: a Usage column with per-column verdicts and dashboard evidence in the table
    inspector, an "Unused columns" report section, and a **Dashboards** diagram showing
    the selected table's columns wired to the Grafana panels that read them. All three
    are created only when Grafana is configured.
  - A line in the Dashboards view means the query *names* that column. Reads arriving
    through a `Distributed` wrapper keep their per-column line, because that propagation
    maps columns one-to-one; reads arriving through a materialized view are drawn as a
    single line from the table header, because that propagation is a table-level blanket
    (every column the view's `SELECT` touches) and drawing it per-column put 39 lines on
    a 40-column table for a panel naming one. The blanketed columns stay `used` and are
    counted in words above the diagram.
  - The dot beside each column carries two facts in two channels: **colour** is the
    verdict, so red means `unused` and nothing else, and **fill** is whether any query
    names the column, so a hollow dot means no line reaches it. They are separate because
    a table feeding a materialized view has every column legitimately in use with no
    dashboard naming one — colouring those red would invite dropping live data.
  - The Dashboards view draws one line per (column, panel) pair, from the same
    `/api/grafana/usage/:database/:table` payload the inspector uses — no new endpoint.
    Line style carries the strength of the claim: solid for a direct read, dashed for one
    reached through lineage (`via distributed`, `via mv`), and a faint dotted line from
    the table header for a panel that reads the table but named no column a parser could
    resolve. Clicking a column or a panel traces just its connections; the "read only"
    toggle hides columns nothing reads. Exportable through **Export HTML** like the other
    diagrams.
  - The view is built for real schemas: above a dozen panels it collapses to one row per
    dashboard, drops to the 30 most-connected dashboards with the remainder stated on the
    card, fades the resting edge state past 120 lines so a focused selection reads, and
    prints the totals in words above the diagram. A table read by 26 dashboards across 89
    panels renders 864px of content instead of 3852px; per-panel rows stay available
    behind the "panels" toggle.
  - Eight new environment variables, documented in `README.md`, `CLAUDE.md`,
    `.env.example` and `docs/deployment/`.
- **The repository's first tests.** Six `*_test.go` files covering `api/` and `models/`,
  with JSON dashboard fixtures under `models/testdata/dashboards/`.
  `models/grafana_corpus_test.go` is a measurement instrument: it skips unless
  `GRAFANA_DASHBOARDS_DIR` names a real dashboard tree, and fails if the SQL parse rate
  drops below 70%.
- ADRs `docs/decisions/0001-sql-parser-dependency.md` and
  `0002-four-state-column-verdicts.md`.
- Documentation baseline: `PROJECT.md`, `ARCHITECTURE.md`, `CONTRIBUTING.md`, this
  `CHANGELOG.md`, `.ai/rules.md`, and `docs/` with `api/`, `database/`, `deployment/`,
  `decisions/` (plus `adr-template.md`) and `diagrams/`.

### Changed

- Go 1.26 → **1.27** across `go.mod`, the `golang:1.27-alpine` Docker builder, the CI
  `setup-go` pin, and the version stated in `README.md`, `PROJECT.md`, `CONTRIBUTING.md`,
  `ARCHITECTURE.md` and `docs/deployment/`.
- `models.GrafanaIndex` is the first component in the repository that invalidates a
  cache: mutex-guarded, TTL'd, and rebuildable through `POST /api/grafana/refresh`.
  `.ai/rules.md` rules 1 and 8 and the `CLAUDE.md` caching section are amended to say so.
- `LoadSchemaSnapshot` warms `getTablesRelations` before building, because that cache is
  otherwise filled lazily by the first `/api/databases` request and a background scan can
  run before any request arrives.
- `switchSection()` and `exportHtml()` in `static/js/app.js` generalised from hardcoded
  two-way branches to N sections. Export HTML now declines on a non-diagram section
  instead of exporting the wrong diagram.
- Documentation corrected across `PROJECT.md`, `CONTRIBUTING.md`, `ARCHITECTURE.md`,
  `docs/deployment/`, `CLAUDE.md` and `.ai/rules.md`, which all stated the repository had
  zero test files.
- `.ai/rules.md` rule 3: the `EngineType` styling contract spans **eleven** sites, not
  ten — `exportHtml()`'s inline `:root` block redeclares the `--t-*-fg` variables
  independently, and missing it leaves exported HTML rendering with an undefined variable.
- `CLAUDE.md`: documented the cache lifecycle, the cross-file `EngineType` contract, the
  duplicated database-exclusion list, the `BuildID` asset requirement, and the verified
  build/vet/test/lint command set.

### Fixed

- `golangci-lint run` now reports `0 issues`; it previously could not run at all against a
  go1.27 toolchain, and once it could it found five unchecked `Close` calls
  (`main.go`, `models/clickhouse.go` ×2, `models/graph.go`, `models/usage.go`).

- `README.md`: corrected three inaccurate claims in *How It Works* — node IDs are the plain
  `database.table` string, not CityHash32; graph payloads are plain JSON rendered as SVG in
  the browser, with no sanitisation layer; and `Dictionary` dependencies come from the
  `loading_dependencies_*` columns, not the `SOURCE(...)` clause. Also corrected the sidebar
  refresh description (the cache is never invalidated) and the project-structure listing.

### Removed

- **`config/config.go`.** A second environment loader that nothing imported — `main.go`
  builds `models.Config` itself — carrying a `GetClickHouseDSN()` that interpolated the
  password into a connection URL. Dead code that formats a credential is a leak waiting
  for its first caller. `config/` now holds only `clickhouse-config.xml`, which the local
  test stack uses.

### Security

Findings from an audit of the whole codebase. No exploited vulnerability is known; these
close the gaps the audit found.

- **`POST /api/grafana/refresh` could be made to scan without limit.** The TTL debounce
  read `scannedAt`, released the lock, then scanned — and `scannedAt` is only written when
  a scan *finishes*. During the first scan every caller therefore read a zero timestamp,
  passed the check and started its own: twenty concurrent requests bought twenty full
  scans, each one HTTP request per dashboard against Grafana plus a full schema read
  against ClickHouse. The endpoint has no authentication and needs no CSRF token, so any
  page an operator visited could fire it cross-origin. A scan slot is now claimed under
  the write lock, so one runs at a time and everyone else is told they were debounced.
  Covered by `TestGrafanaIndexConcurrentRefreshScansOnce`.

- **The database allowlist did not hold on the URL.** `AllowedDatabase` (formerly
  `allowedDatabase`) was applied when the sidebar was built and nowhere else, so
  `GET /api/table/system/query_log` — and the `dataflow` and `relationships` equivalents —
  answered for the databases the sidebar hides. They now return 404 before touching
  ClickHouse. Covered by `TestTableEndpointsRefuseHiddenDatabases`.

- **500 responses carried the raw error.** A ClickHouse failure names the host, port and
  user it failed against, and the API is unauthenticated. The error is now logged and the
  response carries a fixed message.

- **The server had no timeouts.** `router.Run` leaves every `http.Server` timeout at zero,
  so a client that dribbles out a request header holds a goroutine indefinitely. The
  server is now built explicitly with read, read-header, write and idle timeouts.

- **Security headers on every response.** A Content-Security-Policy that allows scripts
  only from this origin, plus `X-Content-Type-Options`, `X-Frame-Options` and
  `Referrer-Policy`. Registered before the routes, because Gin applies middleware only to
  routes declared after the `Use` call — added later it would have covered the static
  files and missed the entire API.

- **No proxy is trusted by default.** Gin otherwise accepts `X-Forwarded-For` from any
  peer. The new `TRUSTED_PROXIES` variable names the proxy when there is one.

- **A panic in the dashboard scan no longer ends the process.** The scan runs on its own
  goroutine and feeds dashboard-authored SQL through a third-party parser; it now recovers
  and records the failure. (Probing the parser with pathological input — 100k-deep
  parentheses, 20k-deep nested selects, unclosed groups — did not crash it. This is
  defence in depth, not a fix for a known crash.)

- **`GRAFANA_URL` is validated.** It must be an absolute `http(s)` URL with a host;
  anything else is reported at startup instead of failing one request at a time. A plain
  `http://` URL is allowed — internal Grafanas run without TLS — but the server now warns
  that the token travels in cleartext.

- **The Docker image no longer runs as root**, and its base moved from `alpine:3.18`,
  which has been end-of-life since May 2025, to `alpine:3.22`. A new `.dockerignore` keeps
  `.env`, `.git` and logs out of the build context — `COPY . ./` had been putting a
  developer's real ClickHouse credentials into the builder layer.

- **`.env.bk` and `.env.local` are no longer tracked**, and `.gitignore` now covers
  `.env.*` with `.env.example` excepted. Every historical value in both files was checked:
  no live credential was ever committed. The pattern was the risk.

- **Dependencies bumped** past the advisories that reached imported packages:
  `golang.org/x/net` 0.47.0 → 0.57.0 (five `x/net/html` advisories, an `idna` one and an
  HTTP/2 `SETTINGS_MAX_FRAME_SIZE` infinite loop), `golang.org/x/text` 0.31.0 → 0.41.0,
  `golang.org/x/crypto` 0.45.0 → 0.56.0, `github.com/klauspost/compress` 1.18.0 → 1.18.7.
  `govulncheck` reported eight vulnerabilities in imported packages before and none after;
  none were ever reachable from this code.

- **`SECURITY.md` added** — reporting process, supported versions, and the threat model
  the API's lack of authentication implies. The README now states it too.

## Released versions

This file was introduced after v2.1.0. Entries for the releases below were never written
here — their contents are in the git history (`git log`), and for most of them also in the
GitHub releases at
<https://github.com/FulgerX2007/clickhouse-schemaflow-visualizer/releases> (remote from
`git remote -v`); `gh release list` shows 10 releases for the 14 tags, so v0.0.1, v0.0.2,
v0.0.3 and v1.1.0 exist as tags only. Release notes are generated by
GoReleaser from commit subjects, excluding `^docs:` and `^test:` commits
(`.goreleaser.yaml:80-85`).

TODO: backfill the Added / Changed / Fixed / Removed / Security entries for the versions
below from the GitHub releases and git history, or state explicitly that this file starts
at the next release.

Tag dates below come from `git tag --sort=-creatordate`:

- 2.1.0 — 2026-05-20
- 2.0.1 — 2026-05-13
- 2.0.0 — 2026-05-13
- 1.4.1 — 2025-07-08
- 1.4.0 — 2025-06-25
- 1.3.0 — 2025-05-26
- 1.2.0 — 2025-05-26
- 1.1.1 — 2025-05-20
- 1.1.0 — 2025-05-20
- 1.0.0 — 2025-04-24

Four pre-1.0 tags also exist, all dated 2025-04-24: `v0.0.1`, `v0.0.2`, `v0.0.3`,
`v0.0.4` (`git tag`).

<!--
Release flow: when cutting a release, rename [Unreleased] to [X.Y.Z] - YYYY-MM-DD,
then add a fresh empty [Unreleased] block above it.
Example:

## [1.0.0] - 2025-01-01
### Added
- Initial release.

Pushing the matching `vX.Y.Z` tag is what runs the release: .github/workflows/release.yml
(go test + go build, then GoReleaser `release --clean`) and
.github/workflows/docker-publish.yml (push ghcr.io/fulgerx2007/clickhouse-schemaflow-visualizer).
No workflow runs on push to master or on pull_request, so nothing is checked before the tag.
-->

[Unreleased]: https://github.com/FulgerX2007/clickhouse-schemaflow-visualizer/compare/v2.1.0...HEAD
