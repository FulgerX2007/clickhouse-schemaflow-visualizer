# Deployment Documentation

How this project is built, configured, deployed, and operated.

> Keep in sync with the code: when pipeline, config, or infra changes, update this
> folder in the same change.

The deliverable is a single Go binary (`main.go`) that serves both the JSON API and
the static frontend from `static/` ([`main.go`](../../main.go) lines 52-89). It is
strictly read-only against ClickHouse — it queries only `system.tables` and
`system.columns` ([`models/clickhouse.go`](../../models/clickhouse.go),
[`models/graph.go`](../../models/graph.go)) — so a deployment owns no schema, no
migrations, and no write path.

## Environments

TODO: no environment definitions (staging/production hosts, URLs, ownership) exist
anywhere in the repository. The only compose targets and install scripts present are
listed below; classify them before filling this table in.

| Environment | Purpose | URL / target | Notes |
|-------------|---------|--------------|-------|
| Local dev (source) | Run from a checkout | `http://localhost:8080` (default `SERVER_ADDR=:8080`, [`main.go`](../../main.go) line 83) | `go run main.go` ([`README.md`](../../README.md)) |
| Local dev (compose) | Build image locally and run it | `http://localhost:8080` | [`docker-compose.yml`](../../docker-compose.yml) — `build: .`, `network_mode: "host"`, `env_file: ".env"` |
| Local test stack | ClickHouse + ZooKeeper + visualizer, seeded with demo schema | `http://localhost:8080`, ClickHouse `:9000`/`:8123` | [`docker-compose.clickhouse-test.yml`](../../docker-compose.clickhouse-test.yml) — for validating relationship detection, not for production |
| Published image | Run a released image without a checkout | `ghcr.io/fulgerx2007/clickhouse-schemaflow-visualizer:latest` ([`README.md`](../../README.md)) | Pushed by [`.github/workflows/docker-publish.yml`](../../.github/workflows/docker-publish.yml) |
| Distro package | systemd service from `.deb` / `.rpm` / `.apk` | `http://<host>:8080` | Built by [`.goreleaser.yaml`](../../.goreleaser.yaml) `nfpms`; see the known packaging defects below |

Neither compose file is a production deployment: `docker-compose.yml` builds the
image from the working tree rather than pulling a released tag
([`docker-compose.yml`](../../docker-compose.yml)), and
`docker-compose.clickhouse-test.yml` boots a throwaway ClickHouse with the hardcoded
credentials `default` / `default`
([`docker-compose.clickhouse-test.yml`](../../docker-compose.clickhouse-test.yml)).

## Build & release

- **CI/CD:** GitHub Actions — two workflows, both in `.github/workflows/`:
  - [`release.yml`](../../.github/workflows/release.yml) — trigger `push` on tags
    matching `v*`. Job `test`: `actions/checkout@v3` (`fetch-depth: 0`), `actions/setup-go@v4`
    with `go-version: '1.26'`, `go mod download`, `go test ./...`, `go build -o
    clickhouse-schemaflow-visualizer .`. Job `goreleaser` (`needs: test`):
    `goreleaser/goreleaser-action@v6`, `distribution: goreleaser`, `version: '~> v2'`,
    `args: release --clean`, with `GITHUB_TOKEN` from `secrets.GITHUB_TOKEN`.
    Workflow permission: `contents: write`.
  - [`docker-publish.yml`](../../.github/workflows/docker-publish.yml) — trigger
    `push` on tags matching `v*`. Steps: checkout,
    `docker/setup-buildx-action@v3`, `docker/login-action@v3` against
    `ghcr.io` with `github.actor` / `secrets.GITHUB_TOKEN`,
    `docker/metadata-action@v5` producing tags
    `type=semver,pattern={{version}}`, `type=semver,pattern={{major}}.{{minor}}`
    and `type=sha,prefix=`, then `docker/build-push-action@v5` with `push: true`
    and GitHub Actions build cache (`cache-from`/`cache-to: type=gha`).
    Permissions: `contents: read`, `packages: write`.
- **Nothing runs on `push` to `master` or on `pull_request`.** Both workflows are
  tag-only, so no build, test, or lint gate exists before a tag is created
  ([`.github/workflows/release.yml`](../../.github/workflows/release.yml),
  [`.github/workflows/docker-publish.yml`](../../.github/workflows/docker-publish.yml)).
  The `test` job's `go test ./...` also proves nothing today: the repository
  contains zero `*_test.go` files, so the run reports `[no test files]` for every
  package.
- **Artifacts produced:**
  - Container image `ghcr.io/fulgerx2007/clickhouse-schemaflow-visualizer`, tagged
    from the pushed semver tag plus the commit SHA
    ([`.github/workflows/docker-publish.yml`](../../.github/workflows/docker-publish.yml)).
    Built from the multi-stage [`Dockerfile`](../../Dockerfile): builder
    `golang:1.26-alpine` running `CGO_ENABLED=0 GOOS=linux go build -o
    clickhouse-schemaflow-visualizer .`, runtime `alpine:3.18` with
    `ca-certificates`, `WORKDIR /app`, the binary and `static/` copied in,
    `EXPOSE 8080`, `CMD ["./clickhouse-schemaflow-visualizer"]`.
  - Cross-compiled binaries — `CGO_ENABLED=0` for `linux`, `windows`, `darwin` ×
    `amd64`, `arm64` ([`.goreleaser.yaml`](../../.goreleaser.yaml) `builds`).
  - Archives — `tar.gz` (`zip` for Windows) containing `README.md`, `LICENSE*`,
    `static/**/*` and `.env.example` ([`.goreleaser.yaml`](../../.goreleaser.yaml)
    `archives`).
  - Packages — `deb`, `rpm`, `apk` named `clickhouse-schemaflow-visualizer`,
    license MIT, dependency `ca-certificates`, install scripts
    `scripts/{preinstall,postinstall,preremove,postremove}.sh`. Package contents:
    `static` installed as a tree at
    `/usr/share/clickhouse-schemaflow-visualizer/static` (mode 0644) and
    `.env.example` installed as
    `/etc/clickhouse-schemaflow-visualizer/config.env.example`
    (`config|noreplace`, mode 0644)
    ([`.goreleaser.yaml`](../../.goreleaser.yaml) `nfpms`).
- **Release process:** push a tag matching `v*`. That single event fans out to both
  workflows: `release.yml` runs the test/build job then GoReleaser (`release
  --clean`, which runs the `before` hooks `go mod tidy` and `go generate ./...`,
  publishes the archives, packages, and a GitHub Release whose changelog is sorted
  ascending and excludes commits matching `^docs:` and `^test:`), and
  `docker-publish.yml` builds and pushes the image to GHCR
  ([`.goreleaser.yaml`](../../.goreleaser.yaml),
  [`.github/workflows/release.yml`](../../.github/workflows/release.yml),
  [`.github/workflows/docker-publish.yml`](../../.github/workflows/docker-publish.yml)).
  TODO: no documented pre-tag checklist, approval, or sign-off exists in the repo.
- **Versioning/tagging:** git tags of the form `v<major>.<minor>.<patch>` — latest
  `v2.1.0` (2026-05-20), history running from `v0.0.1` (2025-04-24), 14 tags in total. The image tags
  are derived from the git tag by `docker/metadata-action` semver patterns; the
  binary itself carries no version string, and the frontend cache-buster is a
  per-process Unix timestamp, not a release version
  ([`main.go`](../../main.go) line 73).
- **Build/verify locally:** `go build -o clickhouse-schemaflow-visualizer .`,
  `go vet ./...` (clean), `go test ./...` (passes, but every package reports
  `[no test files]`). `golangci-lint` has no configuration in the repo — there is
  no `.golangci.yml`, so it runs with defaults. `gofmt -l .` currently flags
  `models/clickhouse.go` as unformatted (pre-existing).

### Stale release metadata

[`.goreleaser.yaml`](../../.goreleaser.yaml) `nfpms.description` and `rpm.summary`
still describe the app as using "Mermaid.js diagrams". The code no longer uses
Mermaid — [`static/js/diagram.js`](../../static/js/diagram.js) is a hand-written
Dagre + inline-SVG renderer. Package descriptions published to deb/rpm/apk carry
that stale text.

## Configuration

- **Mechanism:** environment variables. On startup `godotenv.Load()` reads a `.env`
  file **from the process working directory**, then every setting is read with
  `os.Getenv` and a hard-coded default ([`main.go`](../../main.go) lines 19-42,
  83). A missing `.env` is only a warning — the process continues on the ambient
  environment ([`main.go`](../../main.go) line 20). There is no secrets manager
  and no config-file parser in the code path.
  [`config/config.go`](../../config/config.go) exists but is dead code: nothing
  imports the `config` package; `main.go` builds `models.Config` inline.
- **Where the file lives per deployment path:**
  - Source / compose: `.env` next to the binary's working directory; both compose
    files pass it with `env_file: ".env"`
    ([`docker-compose.yml`](../../docker-compose.yml),
    [`docker-compose.clickhouse-test.yml`](../../docker-compose.clickhouse-test.yml)).
  - Container image: `WORKDIR /app`, so `.env` would have to be mounted at
    `/app/.env`; the documented image usage passes `--env-file .env` to
    `docker run` instead ([`Dockerfile`](../../Dockerfile),
    [`README.md`](../../README.md)).
  - Distro package: `/etc/clickhouse-schemaflow-visualizer/config.env`, created
    from `config.env.example` at 0640 `root:clickhouse-schemaflow-visualizer`
    ([`scripts/postinstall.sh`](../../scripts/postinstall.sh) lines 42-49).
    **This file is never read** — see the known issues below.
- **Required settings:** none are strictly required — every variable has a default
  ([`main.go`](../../main.go) lines 30-41, 83). In practice `CLICKHOUSE_HOST`,
  `CLICKHOUSE_PORT`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD` must point at a
  reachable server, because a failed `Ping` is fatal at startup
  (`log.Fatalf("Failed to connect to ClickHouse: %v", err)`,
  [`main.go`](../../main.go) lines 45-48). Never commit real values;
  [`.env.example`](../../.env.example) ships with blank credentials.

| Variable | Purpose | Required | Default |
|----------|---------|----------|---------|
| `CLICKHOUSE_HOST` | ClickHouse host | No (connect fails at startup if wrong) | `localhost` |
| `CLICKHOUSE_PORT` | Native-protocol port | No | `9000` |
| `CLICKHOUSE_USER` | ClickHouse user | No | `default` |
| `CLICKHOUSE_PASSWORD` | ClickHouse password; never returned by the API ([`api/handlers.go`](../../api/handlers.go) `GetConnection`) | No | (empty) |
| `CLICKHOUSE_DATABASE` | Default database for the connection | No | `default` |
| `CLICKHOUSE_SECURE` | Enable TLS | No | `false` |
| `CLICKHOUSE_SKIP_VERIFY` | Sets `InsecureSkipVerify` on the TLS config ([`models/clickhouse.go`](../../models/clickhouse.go) `NewClickHouseClient`) | No | `false` |
| `CLICKHOUSE_CERT_PATH` | Client certificate; loaded as a pair with the key | No | (empty) |
| `CLICKHOUSE_KEY_PATH` | Client key; loaded as a pair with the certificate | No | (empty) |
| `CLICKHOUSE_CA_PATH` | CA appended to a fresh `x509` pool | No | (empty) |
| `CLICKHOUSE_SERVER_NAME` | TLS `ServerName` (SNI) | No | (empty) |
| `SERVER_ADDR` | Listen address for the HTTP server | No | `:8080` |
| `GIN_MODE` | `release` switches Gin to release mode; any other value leaves debug ([`main.go`](../../main.go) lines 24-26) | No | `debug` |

Invalid integer or boolean values are not fatal: the process logs
`Warning: invalid value for <KEY>, using default` and falls back
([`main.go`](../../main.go) lines 102-127).

Note that [`.env.example`](../../.env.example) ships `CLICKHOUSE_SECURE=true` and
`CLICKHOUSE_SKIP_VERIFY=true`, i.e. TLS with certificate verification disabled. Any
deployment copying it verbatim inherits that. TODO: decide and document the intended
production TLS posture — the repo does not state one.

## Runbook

### Deploy — path 1: published container image

```bash
docker pull ghcr.io/fulgerx2007/clickhouse-schemaflow-visualizer:latest
docker run -p 8080:8080 --env-file .env \
  ghcr.io/fulgerx2007/clickhouse-schemaflow-visualizer:latest
```

Then open `http://localhost:8080` ([`README.md`](../../README.md)). The image
listens on 8080 (`EXPOSE 8080`) and runs `./clickhouse-schemaflow-visualizer` from
`/app`, where `static/` was copied at build time
([`Dockerfile`](../../Dockerfile)). The tag patterns declared are the release semver
(`{{version}}`, `{{major}}.{{minor}}`) and the commit SHA
([`.github/workflows/docker-publish.yml`](../../.github/workflows/docker-publish.yml)).
The workflow sets no `flavor:`, so `docker/metadata-action`'s default `latest=auto` also
publishes `:latest` for a semver tag — which is why the pull commands here and in
`README.md` use `:latest`. Pin an explicit semver tag for reproducible deployments.

### Deploy — path 2: docker compose (builds locally)

```bash
git clone https://github.com/fulgerX2007/clickhouse-schemaflow-visualizer.git
cd clickhouse-schemaflow-visualizer
# create .env first — docker-compose.yml declares env_file: ".env"
docker-compose up -d
```

([`README.md`](../../README.md), [`docker-compose.yml`](../../docker-compose.yml).)
The service is `container_name: clickhouse-schemaflow-visualizer`, `restart:
unless-stopped`, `ports: 8080:8080`, `env_file: ".env"` and
**`network_mode: "host"`** — with host networking the container shares the host
network namespace, so the published `ports:` mapping is redundant and the app binds
`SERVER_ADDR` directly on the host; a ClickHouse reachable on the host's `localhost`
is reachable from the app without further wiring. Because `build.context: .`, this
path ships whatever is in the working tree, not a released artifact.

Local test stack (ClickHouse + ZooKeeper + visualizer):

```bash
docker compose -f docker-compose.clickhouse-test.yml up -d
# re-seed from scratch:
docker compose -f docker-compose.clickhouse-test.yml down -v && \
docker compose -f docker-compose.clickhouse-test.yml up -d
```

This brings up `zookeeper:3.7` on `:2181`,
`clickhouse/clickhouse-server:latest` (container `clickhouse-test-engines`) on
`:9000`/`:8123` with `CLICKHOUSE_USER=default`, `CLICKHOUSE_PASSWORD=default`,
`CLICKHOUSE_DB=test_engines`, mounting
[`scripts/clickhouse_test_engines.sql`](../../scripts/clickhouse_test_engines.sql)
into `/docker-entrypoint-initdb.d` and
[`config/clickhouse-config.xml`](../../config/clickhouse-config.xml) as the
ZooKeeper config, plus the visualizer on a `clickhouse-network` bridge
([`docker-compose.clickhouse-test.yml`](../../docker-compose.clickhouse-test.yml)).
The seed creates `raw` (`airports_local` MergeTree, `flights_local`
ReplicatedMergeTree, each with a Distributed wrapper) and `aggregated`
(`flight_stats_daily_local` ReplicatedAggregatingMergeTree,
`airport_traffic_hourly_local` SummingMergeTree, each fed by a Materialized View
from `raw.flights_local` and exposed through a Distributed wrapper)
([`README.md`](../../README.md)).

### Deploy — path 3: distro package + systemd

Install the `.deb` / `.rpm` / `.apk` from a GitHub Release
([`.goreleaser.yaml`](../../.goreleaser.yaml) `nfpms`). The scriptlets do the
following:

`scripts/preinstall.sh` — creates the system user and group
`clickhouse-schemaflow-visualizer` (`useradd --system --home-dir
/var/lib/clickhouse-schemaflow-visualizer --shell /bin/false`); creates
`/var/lib/clickhouse-schemaflow-visualizer`,
`/var/log/clickhouse-schemaflow-visualizer` and
`/etc/clickhouse-schemaflow-visualizer`; chowns the first two to the service user
and `/etc/...` to `root:clickhouse-schemaflow-visualizer`; chmods them 755, 755 and
750 ([`scripts/preinstall.sh`](../../scripts/preinstall.sh)).

`scripts/postinstall.sh` — writes
`/etc/systemd/system/clickhouse-schemaflow-visualizer.service` (mode 644), copies
`config.env.example` to `config.env` if absent (0640,
`root:clickhouse-schemaflow-visualizer`), then `systemctl daemon-reload` and
`systemctl enable clickhouse-schemaflow-visualizer.service`
([`scripts/postinstall.sh`](../../scripts/postinstall.sh)). The unit it writes,
verbatim:

```ini
[Unit]
Description=ChView2 - ClickHouse Database Viewer
Documentation=https://github.com/FulgerX2007/clickhouse-schemaflow-visualizer
After=network.target
Wants=network.target

[Service]
Type=simple
User=clickhouse-schemaflow-visualizer
Group=clickhouse-schemaflow-visualizer
WorkingDirectory=/var/lib/clickhouse-schemaflow-visualizer
ExecStart=/usr/bin/clickhouse-schemaflow-visualizer
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

# Security settings
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/clickhouse-schemaflow-visualizer /var/log/clickhouse-schemaflow-visualizer
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
```

Operate it with:

```bash
sudo systemctl start clickhouse-schemaflow-visualizer
sudo systemctl status clickhouse-schemaflow-visualizer
sudo journalctl -u clickhouse-schemaflow-visualizer -f
```

Logs go to the journal only (`StandardOutput=journal`,
`StandardError=journal`) — despite `/var/log/clickhouse-schemaflow-visualizer`
being created and made writable, nothing in the code or the unit writes there
([`scripts/postinstall.sh`](../../scripts/postinstall.sh),
[`scripts/preinstall.sh`](../../scripts/preinstall.sh)).

File locations for this path:

| Path | Contents | Source |
|------|----------|--------|
| `/usr/bin/clickhouse-schemaflow-visualizer` | The binary. Only referenced by the unit's `ExecStart`; `.goreleaser.yaml` sets no `nfpms.bindir`, so the path comes from the nfpm default | [`scripts/postinstall.sh`](../../scripts/postinstall.sh) |
| `/usr/share/clickhouse-schemaflow-visualizer/static` | Frontend assets, mode 0644 | [`.goreleaser.yaml`](../../.goreleaser.yaml) |
| `/etc/clickhouse-schemaflow-visualizer/config.env.example` | Shipped example config (`config|noreplace`) | [`.goreleaser.yaml`](../../.goreleaser.yaml) |
| `/etc/clickhouse-schemaflow-visualizer/config.env` | Active config, 0640 `root:<service group>` — **never loaded**, see below | [`scripts/postinstall.sh`](../../scripts/postinstall.sh) |
| `/var/lib/clickhouse-schemaflow-visualizer` | `WorkingDirectory`, service-user owned | [`scripts/preinstall.sh`](../../scripts/preinstall.sh) |
| `/var/log/clickhouse-schemaflow-visualizer` | Created and writable, unused in practice | [`scripts/preinstall.sh`](../../scripts/preinstall.sh) |
| `/etc/systemd/system/clickhouse-schemaflow-visualizer.service` | The unit above | [`scripts/postinstall.sh`](../../scripts/postinstall.sh) |

Removal: `scripts/preremove.sh` stops and disables the unit if active/enabled;
`scripts/postremove.sh` deletes the unit file and reloads systemd, and on a purge
(`$1 = "purge"`, or `REMOVE_USER_DATA=true`) also removes `/var/lib/...`,
`/var/log/...`, `/etc/...` and the system user
([`scripts/preremove.sh`](../../scripts/preremove.sh),
[`scripts/postremove.sh`](../../scripts/postremove.sh)).

### Deploy — path 4: from source

```bash
go mod download
go run main.go        # or: go build -o clickhouse-schemaflow-visualizer .
```

Run it from the repository root — the process resolves `./static` relative to its
working directory ([`main.go`](../../main.go) lines 66, 72,
[`README.md`](../../README.md)).

### Known issues — packaged (deb/rpm/apk) deployments are broken as shipped

Both defects are derivable from the files; state them plainly rather than working
around them silently.

1. **The packaged service cannot find its static assets.** The binary resolves
   `./static` and `./static/html/index.html` relative to the working directory —
   `router.Static("/static", "./static")` and
   `template.Must(template.ParseFiles("./static/html/index.html"))`
   ([`main.go`](../../main.go) lines 66 and 72). The unit sets
   `WorkingDirectory=/var/lib/clickhouse-schemaflow-visualizer`
   ([`scripts/postinstall.sh`](../../scripts/postinstall.sh)), while the package
   installs the assets to `/usr/share/clickhouse-schemaflow-visualizer/static`
   ([`.goreleaser.yaml`](../../.goreleaser.yaml)). Since
   `template.ParseFiles` is wrapped in `template.Must`, the missing
   `index.html` panics the process at startup rather than degrading.
2. **`config.env` is never loaded.** Configuration is read by `godotenv.Load()`,
   which reads `.env` from the working directory
   ([`main.go`](../../main.go) line 19). The package writes
   `/etc/clickhouse-schemaflow-visualizer/config.env`
   ([`scripts/postinstall.sh`](../../scripts/postinstall.sh)) and the unit declares
   no `EnvironmentFile=`, so that file has no effect; the service starts on the
   ambient environment and therefore on the built-in defaults
   (`localhost:9000`, user `default`) unless something else injects the variables.

Also note the unit `Description` is `ChView2 - ClickHouse Database Viewer` and every
packaging script is commented "for ChView2" — an old name for this project
([`scripts/postinstall.sh`](../../scripts/postinstall.sh),
[`scripts/preinstall.sh`](../../scripts/preinstall.sh),
[`scripts/preremove.sh`](../../scripts/preremove.sh),
[`scripts/postremove.sh`](../../scripts/postremove.sh)).

### Rollback

TODO: no rollback procedure is defined in the repository. The material available:
container deployments can be re-pinned to an earlier published tag
(`{{version}}` / `{{major}}.{{minor}}` / commit SHA from
[`.github/workflows/docker-publish.yml`](../../.github/workflows/docker-publish.yml)),
and packages can be downgraded to an earlier GitHub Release artifact. Neither is
documented or tested anywhere in the repo, and the app holds no persistent state of
its own, so no data-side rollback exists to describe.

### Health checks / readiness

TODO: the application exposes **no** health, readiness, or liveness endpoint —
the only routes are `GET /`, `/static/*` ([`main.go`](../../main.go) lines 66-80)
and the six `GET /api/*` routes registered in
[`api/handlers.go`](../../api/handlers.go) (`/api/connection`, `/api/databases`,
`/api/columns`, `/api/dataflow/:database/:table`,
`/api/relationships/:database/:table`, `/api/table/:database/:table`). No compose
file defines a `healthcheck:`, and the systemd unit has no readiness notification
(`Type=simple`). What can be observed today: the process exits at startup if the
ClickHouse `Ping` fails ([`main.go`](../../main.go) lines 45-48), and
`GET /api/connection` returns the live connection parameters without the password
([`api/handlers.go`](../../api/handlers.go)), so it is the cheapest available
liveness probe. TODO: define real probes.

### Common operational issues & fixes

- **Service starts, then exits immediately.** `log.Fatalf("Failed to connect to
  ClickHouse: %v", err)` — the startup `Ping` failed
  ([`main.go`](../../main.go) lines 45-48). Check host/port/credentials and TLS
  settings; on the packaged path check defect 2 above first, because the service is
  almost certainly running on the built-in `localhost:9000` defaults.
- **Schema changes do not appear in the UI.** Discovery results are cached in
  package-level vars `DatabasesData`, `TableRelations`, `TableMetadata`
  ([`models/clickhouse.go`](../../models/clickhouse.go) lines 34-36), filled on
  first use and never invalidated. The sidebar **↻** button only re-renders from
  that cache ([`README.md`](../../README.md)). **Restart the process** to pick up
  new tables or relationships (`docker restart
  clickhouse-schemaflow-visualizer`, or `systemctl restart
  clickhouse-schemaflow-visualizer`).
- **Blank page / 500 on `/` after a package install.** See known issue 1 — the
  static assets are not under the unit's `WorkingDirectory`.
- **Stale CSS/JS after an upgrade.** `index.html` is rendered through
  `html/template` with a per-process `{{.BuildID}}` (a Unix timestamp) appended to
  every asset URL, and `/static/*` is served with `Cache-Control: no-cache`
  ([`main.go`](../../main.go) lines 62-79). A new asset tag added without
  `?v={{.BuildID}}` will be cached across upgrades.
- **`ProtectSystem=strict` write failures.** The packaged service may write only to
  `/var/lib/clickhouse-schemaflow-visualizer` and
  `/var/log/clickhouse-schemaflow-visualizer`
  ([`scripts/postinstall.sh`](../../scripts/postinstall.sh) `ReadWritePaths`).
- **Port already in use.** Change `SERVER_ADDR` ([`main.go`](../../main.go) line
  83). With `network_mode: "host"` in [`docker-compose.yml`](../../docker-compose.yml)
  the app binds the host port directly, so a conflict is with a host process.

### Quotas / limits

TODO: no quotas, rate limits, timeouts, or resource limits are configured anywhere
in the repository — the API has no authentication, no rate limiting, and no
pagination ([`api/handlers.go`](../../api/handlers.go)), no compose file sets
`deploy.resources` or `mem_limit`, and the systemd unit sets no `MemoryMax`/`CPUQuota`
([`docker-compose.yml`](../../docker-compose.yml),
[`scripts/postinstall.sh`](../../scripts/postinstall.sh)). The only limit-shaped
values in the code are the relationship-traversal depth bound
`maxRelationDepth = 50` ([`models/clickhouse.go`](../../models/clickhouse.go) line
16) and the `nofile` ulimits set on the *test* ClickHouse container
([`docker-compose.clickhouse-test.yml`](../../docker-compose.clickhouse-test.yml)).

### Monitoring & alerting

TODO: no metrics endpoint, structured logging, tracing, dashboards, or alert
definitions exist in the repository. Logging is Gin's default request logger plus
`log.Printf`/`log.Fatalf` from [`main.go`](../../main.go), reaching the journal on
the packaged path and container stdout/stderr otherwise.

### Secrets handling

TODO: not defined. What is verifiable: `CLICKHOUSE_PASSWORD` is supplied as an
environment variable and is never returned by the API
([`api/handlers.go`](../../api/handlers.go) `GetConnection`);
`/etc/clickhouse-schemaflow-visualizer/config.env` is created 0640
`root:clickhouse-schemaflow-visualizer`
([`scripts/postinstall.sh`](../../scripts/postinstall.sh)) — though that file is
never read, per known issue 2. The only CI secret in use is the automatic
`secrets.GITHUB_TOKEN`
([`.github/workflows/release.yml`](../../.github/workflows/release.yml),
[`.github/workflows/docker-publish.yml`](../../.github/workflows/docker-publish.yml)).
