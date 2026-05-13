# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

ClickHouse Schema Flow Visualizer — a Go web app that connects to a ClickHouse instance, discovers table relationships by parsing `system.tables` metadata (CREATE queries, engine types, dependencies), and renders interactive Mermaid.js flowchart diagrams showing data flow between tables.

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
- `api/handlers.go` — four REST endpoints under `/api/`:
  - `GET /databases` — all databases with their tables (cached after first query)
  - `GET /schema/:database/:table` — Mermaid flowchart of table-level relationships
  - `GET /relationships/:database/:table` — Mermaid flowchart of column-level relationships with transformations
  - `GET /table/:database/:table` — column details for a table
- `models/clickhouse.go` — core logic: ClickHouse client, relationship discovery, Mermaid diagram generation
- `config/config.go` — env-based config loader (currently unused — `main.go` loads config directly via `models.Config`)
- `static/` — frontend (HTML/CSS/JS with Mermaid.js rendering)

**Key design details in `models/clickhouse.go`:**
- Table relations are discovered by parsing `CREATE TABLE` queries and `engine_full` from `system.tables`
- Results are cached in package-level vars (`DatabasesData`, `TableRelations`, `TableMetadata`) — no cache invalidation
- Engine types determine relationship extraction: MergeTree, Replicated*, Dictionary, Distributed, MaterializedView each have distinct parsing logic
- Column-level relationships for MVs are detected by parsing the MV's SELECT query and matching source/target columns
- Node IDs in Mermaid diagrams use CityHash32 for deterministic, collision-resistant identifiers
- Column names and expressions are sanitized for Mermaid compatibility (special chars replaced)

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
