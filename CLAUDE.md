# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

ClickHouse Schema Flow Visualizer — a Go web app that connects to a ClickHouse instance, discovers table relationships by parsing `system.tables` metadata (CREATE queries, engine types, dependencies), and renders interactive flowchart diagrams (laid out with Dagre and drawn as inline SVG) showing data flow between tables, including column-level relationships with transformation expressions for materialized views.

## Development Commands

```bash
# Run locally (requires .env with ClickHouse connection)
go run main.go

# Build
go build -o clickhouse-schemaflow-visualizer .

# Run with test ClickHouse (creates ZooKeeper + ClickHouse + app)
docker-compose -f docker-compose.clickhouse-test.yml up -d

# Production Docker
docker-compose up -d
```

## Architecture

**Single-binary Go server** (Gin) serving both the API and static frontend:

- `main.go` — entry point, loads `.env` config, creates ClickHouse client, registers routes, serves static files
- `api/handlers.go` — six REST endpoints under `/api/`:
  - `GET /connection` — current ClickHouse connection info (host, port, user, database, secure flag); password is never exposed
  - `GET /databases` — all databases with their tables (cached after first query)
  - `GET /columns` — flat column index across all visible tables (used by the `Ctrl+K` / `⌘K` command palette)
  - `GET /dataflow/:database/:table` — table-level DAG (upstream sources + downstream materializations) for the selected table; rendered by the frontend with Dagre + SVG
  - `GET /relationships/:database/:table` — column-level DAG with transformation expressions on the edges
  - `GET /table/:database/:table` — column details for a single table (used by the inspector panel)
- `models/clickhouse.go` — core logic: ClickHouse client, engine-aware relationship discovery, graph model returned to the frontend
- `config/config.go` — env-based config loader (currently unused — `main.go` loads config directly via `models.Config`)
- `static/` — frontend (vanilla HTML/CSS/JS); diagrams are laid out with bundled Dagre (`static/js/vendor/dagre.min.js`) and rendered as inline SVG by `static/js/diagram.js`

**Key design details in `models/clickhouse.go`:**
- Table relations are discovered by parsing `CREATE TABLE` queries and `engine_full` from `system.tables`
- Results are cached in package-level vars (`DatabasesData`, `TableRelations`, `TableMetadata`) — no cache invalidation
- Engine types determine relationship extraction: MergeTree, Replicated*, Dictionary, Distributed, MaterializedView each have distinct parsing logic
- Column-level relationships for MVs are detected by parsing the MV's SELECT query and matching source/target columns
- Node IDs in the returned graphs use CityHash32 of the fully qualified table name for deterministic, collision-resistant identifiers across reloads
- Column names and transformation expressions are sanitized before being emitted to the frontend so reserved characters still render in the SVG output

## Configuration

All via environment variables (or `.env` file):

| Variable | Default | Purpose |
|---|---|---|
| `CLICKHOUSE_HOST` | `localhost` | ClickHouse host |
| `CLICKHOUSE_PORT` | `9000` | Native protocol port |
| `CLICKHOUSE_USER` | `default` | |
| `CLICKHOUSE_PASSWORD` | (empty) | |
| `CLICKHOUSE_DATABASE` | `default` | |
| `CLICKHOUSE_SECURE` | `false` | Enable TLS |
| `CLICKHOUSE_SKIP_VERIFY` | `false` | Skip TLS cert verification |
| `SERVER_ADDR` | `:8080` | Listen address |
| `GIN_MODE` | `debug` | `debug` or `release` |

## Testing

A test ClickHouse environment is available via `docker-compose.clickhouse-test.yml` which seeds test data from `scripts/clickhouse_test_engines.sql`. This creates various engine types (MergeTree, Distributed, MaterializedView, etc.) for validating relationship detection.
