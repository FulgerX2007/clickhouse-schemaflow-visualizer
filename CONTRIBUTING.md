# Contributing to ClickHouse Schema Flow Visualizer

Thanks for contributing. This guide covers how to propose changes and the gates a
change must pass — including the documentation it must carry.

> Audience: contributors (human and AI).

Everything below is derived from files in this repository. Where the repository does
not document a rule, this guide says `TODO:` instead of inventing one.

## Getting set up

Prerequisites: Go 1.27 or newer (`go.mod` declares `go 1.27.0`; `README.md` states
"Go 1.27+"), and a reachable ClickHouse instance. Docker + Docker Compose are needed
only for the container workflows below.

```bash
go mod download                                    # install Go dependencies (README.md:118)
go test ./...                                      # api + models covered; main + config report "[no test files]"
```

Then create a `.env` in the repository root and run the server:

```bash
cp .env.example .env    # then fill in CLICKHOUSE_* values (.env.example)
go run main.go          # serves on SERVER_ADDR, default :8080 (main.go)
```

Local setup details, all verifiable in the cited files:

- Configuration is read from `.env` in the **current working directory** by
  `godotenv.Load()` (`main.go`), then from the process environment. A missing `.env`
  is only a warning — the server falls back to environment variables and defaults
  (`main.go`). The full variable list and defaults are in `.env.example` and in the
  config table in `CLAUDE.md`.
- `.env` is git-ignored (`.gitignore`); never commit it or any real credentials.
- **Run from the repository root.** `main.go` serves `./static` and parses
  `./static/html/index.html` by relative path, so starting the binary from another
  directory fails to find the frontend (`main.go`).
- A failed ClickHouse `Ping` is fatal at startup — `log.Fatalf("Failed to connect to
  ClickHouse: %v", ...)` in `main.go` — so the app will not boot without a reachable
  server.
- The app is strictly read-only against ClickHouse: it queries only `system.tables`
  and `system.columns` (`models/clickhouse.go`, `models/graph.go`). It owns no schema,
  no migrations, and no write path.

### Local test stack (ClickHouse + ZooKeeper + the app)

The repository ships a compose file that boots a single-node ClickHouse with ZooKeeper
(so `Replicated*` and `Distributed` engines work) and seeds a demo schema
(`docker-compose.clickhouse-test.yml`, `README.md:162-179`):

```bash
docker compose -f docker-compose.clickhouse-test.yml up -d
```

It brings up ZooKeeper on `:2181`, ClickHouse on `:9000` (native) and `:8123` (HTTP)
with credentials `default` / `default` and database `test_engines`, and the visualizer
on `:8080` (`docker-compose.clickhouse-test.yml`). The visualizer service uses
`env_file: ".env"`, so a `.env` must exist in the repository root before `up`.
The **test** stack does not require one: it pins the ClickHouse settings itself and
marks `.env` optional, so `docker compose -f docker-compose.clickhouse-test.yml up -d`
works even when `.env` points at a production instance.

The seed `scripts/clickhouse_test_engines.sql` creates database `raw`
(`airports_local` MergeTree, `flights_local` ReplicatedMergeTree, each with a
`Distributed` wrapper) and `aggregated` (`flight_stats_daily_local`
ReplicatedAggregatingMergeTree, `airport_traffic_hourly_local` SummingMergeTree, each
fed by a Materialized View from `raw.flights_local` and exposed through a `Distributed`
wrapper) — `README.md:175-177`. That covers four of the five branches in
`models/clickhouse.go`: MergeTree, `Replicated*`, `Distributed` and `MaterializedView`.
It creates **no `Dictionary`** object (`grep -ci dictionary scripts/clickhouse_test_engines.sql`
returns 0), so the `dictionary` branch has no local coverage — README.md:164's claim that the
seed exercises every branch is inaccurate.

TODO: extend the seed with a `Dictionary` so all five branches are covered locally.

Tear down and re-seed with:

```bash
docker compose -f docker-compose.clickhouse-test.yml down -v
docker compose -f docker-compose.clickhouse-test.yml up -d
```

When verifying a change by hand, remember that `DatabasesData`, `TableRelations` and
`TableMetadata` are package-level caches filled on first use and **never invalidated**
(`models/clickhouse.go:34-36`). Restart the process to pick up a changed ClickHouse
schema.

## Branching model

- The default branch is `master` (`git branch -a`; `remotes/origin/master`). There is no
  `develop` branch. Seven other remote branches exist (`origin/fix/docker-go-1.26`,
  `origin/testing-database`, and others); whether any is long-lived is not documented.
- One concern per branch; keep changes atomic and focused.
- TODO: confirm which branch to branch from and whether direct pushes to `master` are
  allowed — no branch policy is documented in the repository (no `CONTRIBUTING` guidance
  beyond `README.md:234`, no `CODEOWNERS`, no branch-protection file).
- TODO: branch naming convention. Existing branches mix styles
  (`fix/docker-go-1.26`, `visualize-relationships-between-fields-from-different-tables-`),
  so no convention can be asserted.
- TODO: review requirements (number of approvals, who reviews). Not documented anywhere
  in the repository — `.github/` contains only the two workflow files, with no PR/issue
  templates and no `CODEOWNERS`.

## Commit messages

TODO: confirm the commit-message policy — the repository documents none.

What is verifiable:

- Recent history predominantly uses Conventional-Commit prefixes such as `feat:`,
  `fix:`, `docs:`, `refactor:`, `build(deps):` (`git log --pretty=%s`), but this is an
  observed habit, not a written or enforced rule.
- `.goreleaser.yaml` filters release-note entries by commit prefix — it excludes
  `^docs:` and `^test:` from the generated changelog. Using those prefixes therefore
  changes what shows up in a release; a commit with a non-conventional subject is
  never filtered out.
- **No CI job checks commit messages.** Neither `.github/workflows/release.yml` nor
  `.github/workflows/docker-publish.yml` runs on pushes or pull requests (both trigger
  only on tags matching `v*`), and there is no commit-lint configuration in the
  repository.

Until a policy is written down, following the existing `type(scope): subject` style
(`feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `perf`, `ci`, `build`) keeps
release notes consistent.

## Quality gates (run before opening a PR)

**Local checks are the only gate.** No GitHub Actions workflow runs on pushes to
`master` or on pull requests — `release.yml` and `docker-publish.yml` both trigger
exclusively on `v*` tags (`.github/workflows/*.yml`). Nothing is verified automatically
until a release tag exists, so run these yourself:

```bash
gofmt -l .                                         # formatting: must print nothing new (models/clickhouse.go is already listed)
go vet ./...                                       # vet: clean today
golangci-lint run                                  # lint: no repo config, runs with defaults
go test ./...                                      # tests: api + models covered
go build -o clickhouse-schemaflow-visualizer .     # build (README.md, CLAUDE.md)
```

Verified state of each command in this repository at the time of writing:

| Command | Current result | Notes |
|---|---|---|
| `gofmt -l .` | prints `models/clickhouse.go` | Pre-existing, unrelated to your change. Do not reformat that file as a drive-by; if you edit it, format only what you touch or make the reformat its own commit. |
| `go vet ./...` | exit 0, no output | |
| `golangci-lint run` | not run here; with no config it would use the default linters | There is **no `.golangci.yml`** in the repository, so results depend on your locally installed version. TODO: decide whether to check in a `.golangci.yml` so lint output is reproducible. |
| `golangci-lint run` | passes, `0 issues` | Build it with the same Go release as your toolchain, or it panics on the standard library. Its default `max-same-issues: 3` hides a repeated finding — pass `--max-same-issues=0` when adding code that resembles existing code. |
| `go test ./...` | passes; `api` and `models` covered | Six `*_test.go` files, all from the Grafana column-usage feature. `main` and `config` still have none, so a green run does not mean the whole tree is exercised. |
| `go build -o clickhouse-schemaflow-visualizer .` | exit 0 | Same command CI runs inside the tag-triggered `test` job (`.github/workflows/release.yml`). |

Because coverage is partial and the diagram layer has none at all, **manual verification is part of the gate** for any
behavioural change: bring up the local test stack above, exercise the affected view in
the browser, and say in the PR what you checked.

- TODO: agree a testing convention (package layout, fixtures, whether ClickHouse-backed
  tests are integration-only) and add the first `*_test.go` files.
- TODO: decide whether to add a `pull_request` / `push` CI workflow so these checks run
  automatically rather than on trust.

### Contracts that are easy to break silently

These couplings are not covered by any test, so check them by hand when your change
touches them:

- **Engine styling contract.** `models/graph.go` `ClassifyEngine` emits exactly five
  strings — `mergetree`, `replicated`, `distributed`, `mview`, `dictionary`
  (`models/graph.go:13-17`). `static/js/app.js` (`classifyEngine`, `glyphFor`), the
  legend in `static/html/index.html`, and the CSS classes in `static/css/styles.css`
  all key off those strings. Change one side and you must change all four.
- **Database exclusion list is duplicated.** `allowedDatabase()` in
  `models/clickhouse.go` and the hardcoded SQL `NOT IN (...)` inside `BuildColumnIndex`
  in `models/graph.go` list the excluded databases separately. Both must change together.
- **Sidebar label coupling.** `generateTableListContent` in `models/clickhouse.go`
  emits an `<i class="fa-solid …"></i> name` fragment, and `addTableToList` in
  `static/js/app.js` strips that `<i>` tag with a regex.
- **Cache-buster on assets.** Every CSS/JS tag in `static/html/index.html` carries
  `?v={{.BuildID}}` (`static/html/index.html:10,31,171,172`), rendered per process by
  `main.go`. A new asset tag without `?v={{.BuildID}}` will be cached across upgrades.
- **Traversal depth.** Graph walks are bounded by `maxRelationDepth = 50`
  (`models/clickhouse.go:16`).

## Documentation duties

**Every change must keep docs in sync.** Before requesting review, run the matching
checklist and confirm:

- [ ] **New feature** → updated `README.md` (if user-facing), `ARCHITECTURE.md`
      (if structure changed), `docs/api/` (if API changed), and `CHANGELOG.md`.
- [ ] **Bug fix** → `CHANGELOG.md` entry under *Fixed*; doc corrected if it was wrong.
- [ ] **Database change** → `docs/database/` + a migration note; `CHANGELOG.md`.
- [ ] **Significant decision** → an ADR in `docs/decisions/`.

See the full lists in your docs-governance checklists, or follow `.ai/rules.md`.

Repository-specific notes on those duties:

- The HTTP surface is the six `GET` routes registered in `api/handlers.go:26-34`. Any
  route added, removed, or changed in shape must be reflected in `README.md`,
  `CLAUDE.md` and `docs/api/`.
- "Database change" here never means a migration — the app owns no schema. It means a
  change to the ClickHouse **queries** it issues (`models/clickhouse.go`,
  `models/graph.go`) or to the seed schema in `scripts/clickhouse_test_engines.sql`;
  document those under `docs/database/`.
- `CLAUDE.md` at the repository root is the guidance file for AI contributors. Keep it
  accurate when architecture, commands, or configuration change; `.ai/rules.md` should
  cross-link `../CLAUDE.md` rather than duplicate it.
- Add the `[Unreleased]` entry your change belongs in to `CHANGELOG.md` (Keep a Changelog
  format). TODO: entries for releases up to v2.1.0 are not backfilled — see the note in
  that file.
- Known stale metadata worth fixing if you touch it: `.goreleaser.yaml`'s nfpm
  `description` / `rpm.summary` still say the app renders "Mermaid.js diagrams", but
  `static/js/diagram.js` is a hand-written Dagre + SVG renderer; and the systemd unit
  written by `scripts/postinstall.sh` still describes the app as "ChView2", an old name.

## Pull request checklist

- [ ] Conventional commit messages
- [ ] Lint + tests pass locally
- [ ] Docs updated per the duties above
- [ ] `CHANGELOG.md` has an `[Unreleased]` entry
- [ ] No secrets, credentials, or generated artifacts committed
- [ ] `gofmt -l .` reports nothing new beyond the pre-existing `models/clickhouse.go`
- [ ] Behavioural changes verified by hand against the local test stack, with the steps
      stated in the PR description (automated coverage does not reach the frontend)
- [ ] `.env` and any real ClickHouse credentials stay out of the commit (`.gitignore`)

## Releasing

Releases are tag-driven and are the only time CI runs (`.github/workflows/release.yml`,
`.github/workflows/docker-publish.yml`):

- Pushing a tag matching `v*` triggers a `test` job (setup-go 1.27 → `go mod download`
  → `go test ./...` → `go build`) and then GoReleaser (`goreleaser-action@v6`,
  `release --clean`) per `.goreleaser.yaml`.
- The same tag triggers a Docker buildx build pushed to
  `ghcr.io/fulgerx2007/clickhouse-schemaflow-visualizer` with semver tags.

TODO: confirm who is allowed to cut a tag and how the version number is chosen — not
documented in the repository.

## License

By contributing you agree that your contributions are licensed under the MIT License
(`LICENSE`).
