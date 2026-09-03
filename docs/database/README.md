# Database Documentation

**This application owns no schema and runs no migrations.** It has no database of its own, no ORM, no migration tool, and no write path of any kind. It is a strictly read-only *reader* of an external ClickHouse server's system tables.

Confirmed by inspection: a grep for `INSERT INTO`, `ALTER TABLE`, `DROP TABLE`, `TRUNCATE` and `.Exec(` across `main.go`, `api/`, `models/` and `config/` returns nothing, and the only ClickHouse connection methods used anywhere are `Query`, `QueryRow` and `Ping` (`models/clickhouse.go:139`, `:209`, `:442`, `:455`, `:646`; `models/graph.go:122`, `:254`). There is no migrations directory in the repository (a `find` for any path matching `*migration*` returns no results).

What follows therefore documents the **read contract** this app has with ClickHouse — the exact queries it issues and the system columns it depends on — plus the seed schema used by the local test stack. The schema of *your* ClickHouse instance is out of scope; see [Schema / data dictionary](#schema--data-dictionary).

> Keep in sync with the code: every schema change updates this folder + `CHANGELOG.md`
> in the same change (see `.ai/rules.md`).
>
> For this repository, "schema change" means a change to the queries or the system
> columns listed below, or to `scripts/clickhouse_test_engines.sql`.

## Engines & datastores

- **Application-owned datastore: none.** No embedded database, no local persistence, no file-backed state. All caching is in-process memory (see below).
- **External datastore: one ClickHouse server**, supplied by the operator and reached over the native protocol (default port `9000`) using `github.com/ClickHouse/clickhouse-go/v2` (`go.mod`; `models/clickhouse.go` `NewClickHouseClient`). Connection settings come from environment variables loaded out of `.env` by `godotenv` (`main.go:14`, `main.go:19`, `main.go:29-41`, `.env.example`). A failed `Ping` at startup is fatal (`models/clickhouse.go:139`; `main.go:47`).
- **Only two system tables are read:** `system.tables` and `system.columns`. No user table is ever read — the app reads *metadata about* user tables, never their rows.
- **Engine awareness.** The app does not create engines; it classifies the engine strings it finds. `ClassifyEngine` (`models/graph.go:22-35`) maps a raw `engine` value onto five `EngineType` values: `Distributed` → distributed, `MaterializedView` → mview, prefix `Dictionary` → dictionary, prefix `Replicated` → replicated, everything else → mergetree. Those five strings are the styling contract with the frontend (`static/js/app.js`, `static/css/styles.css`, the legend in `static/html/index.html`).

### Queries issued

Every statement the app can issue, in full. There are six, all `SELECT`.

| # | Query | Source | Cached? |
|---|-------|--------|---------|
| 1 | `SELECT create_table_query, engine_full, engine, database, name, loading_dependencies_database, loading_dependencies_table, total_rows, total_bytes FROM system.tables ORDER BY name` | `models/clickhouse.go:208` (`getTablesRelations`) | Yes — once per process |
| 2 | `SELECT engine, total_rows, total_bytes, primary_key, sorting_key, partition_key FROM system.tables WHERE database = ? AND name = ?` | `models/clickhouse.go:434-436` (`GetTableColumns`) | No |
| 3 | `SELECT name, type, position, comment FROM system.columns WHERE database = ? AND table = ? ORDER BY position` | `models/clickhouse.go:449-453` (`GetTableColumns`) | No |
| 4 | `SELECT engine FROM system.tables WHERE database = ? AND name = ?` | `models/clickhouse.go:645` (`isDistributedTable`) | No |
| 5 | `SELECT database, table, name, type FROM system.columns WHERE database NOT IN (…) ORDER BY database, table, position` | `models/graph.go:117-121` (`BuildColumnIndex`) | No |
| 6 | `SELECT create_table_query, engine FROM system.tables WHERE database = ? AND name = ?` | `models/graph.go:255` (`BuildRelationshipsGraph`) | No |

Queries 2, 3, 4 and 6 use bound parameters (`?`); queries 1 and 5 take no input. No query is built by string-concatenating a user-supplied value.

**Caching and staleness.** Query 1 populates three package-level variables — `DatabasesData`, `TableRelations`, `TableMetadata` (`models/clickhouse.go:34-36`) — on first use, and they are **never invalidated** (`getTablesRelations` returns early and logs "Using cached tables relations" whenever all three are non-nil, `models/clickhouse.go:201-204`). A process restart is the only way to pick up schema changes made on the ClickHouse side. Queries 2–6 are uncached and hit ClickHouse on every request.

### System columns this app depends on

Changing or removing any of these breaks the app. They are the real upstream contract.

**`system.tables`**

| Column | Used for | Source |
|--------|----------|--------|
| `create_table_query` | Positional string parsing to extract related table names; MV `SELECT` extraction for column-level edges | `models/clickhouse.go:208`, `:246`, `:487-533`; `models/graph.go:255` |
| `engine_full` | `Distributed` target resolution — split on `'`, needs ≥ 6 parts | `models/clickhouse.go:276-289` |
| `engine` | Branch selection in relation parsing; `ClassifyEngine` input; `isDistributedTable` | `models/clickhouse.go:245-311`, `:645`; `models/graph.go:22-35` |
| `database`, `name` | Table identity; node IDs are the plain `database.table` string (no hashing) | `models/clickhouse.go:208`, `:241`; `models/graph.go` |
| `loading_dependencies_database`, `loading_dependencies_table` | Sole source of `Dictionary*` dependencies — scanned as `[]string`, first element used | `models/clickhouse.go:263-275` |
| `total_rows`, `total_bytes` | Sidebar labels and node metadata; nullable, scanned into `*uint64` | `models/clickhouse.go:208`, `:434`; `models/clickhouse.go:67-68` |
| `primary_key`, `sorting_key`, `partition_key` | Table inspector panel (`GET /api/table/:database/:table`) | `models/clickhouse.go:434-436`, `:64-66` |

**`system.columns`**

| Column | Used for | Source |
|--------|----------|--------|
| `name`, `type` | Column rows in the inspector and in the `Ctrl+K` / `⌘K` column index | `models/clickhouse.go:449`; `models/graph.go:117` |
| `position` | `ORDER BY` for stable column ordering; carried into `ColumnInfo.Position` | `models/clickhouse.go:449-453`, `:53-58`; `models/graph.go:121` |
| `comment` | Column comment shown in the inspector | `models/clickhouse.go:449`, `:57` |
| `database`, `table` | Grouping for the flat column index | `models/graph.go:117` |

### Required ClickHouse privileges

Derived from the query list above, not from any grant statement in the repository: the configured `CLICKHOUSE_USER` needs **read access to `system.tables` and `system.columns`, and nothing else**. The app never reads a user table's data, never writes, and never issues DDL.

TODO: no file in this repository specifies a `GRANT` or role for the connecting user — the operator must define one. Confirm against your ClickHouse version how row visibility in `system.tables` is filtered per user, since a restricted user may simply see fewer tables (and therefore a smaller diagram) rather than receive an error.

### Excluded databases — list is duplicated in two places

Four databases are hidden from the UI, and the exclusion list exists **twice**, in two different forms. Both must be changed together.

| Where | Form | Excluded |
|-------|------|----------|
| `allowedDatabase()`, `models/clickhouse.go:331-346` | Go `switch`, filters rows after query 1 | empty string, `system`, `information_schema` (via `strings.ToLower`, so any casing), `performance_schema`, `mysql` |
| `BuildColumnIndex()`, `models/graph.go:119` | Hardcoded SQL `NOT IN` inside query 5 | `'system'`, `'information_schema'`, `'INFORMATION_SCHEMA'`, `'performance_schema'`, `'mysql'` |

Known drift between the two, derivable from the code above: the Go helper case-folds the name, so a database called `Information_Schema` is excluded from the diagram, while the SQL list matches only the two literal spellings and would still index its columns into the `Ctrl+K` palette.

## Migrations

- **Tool:** none. No migration library is present in `go.mod`, and no migration runner exists in the code.
- **Location:** none. There is no `migrations/` directory anywhere in the repository.
- **Naming convention:** not applicable.
- **Rules:**
  - The application must remain read-only. Any change that introduces a write path (`Exec`, DDL, `INSERT`) is a change of product category, not a refactor.
  - Treat the system-column list above as a versioned contract: adding or dropping a column from a query is a documented change — update this file and `CHANGELOG.md` in the same commit.
  - The excluded-database list is duplicated (`models/clickhouse.go:331-346` and `models/graph.go:119`); never edit one without the other.

The only versioned SQL in the repository is `scripts/clickhouse_test_engines.sql`. **It is not a migration.** It is the seed for the local test stack: `docker-compose.clickhouse-test.yml` mounts it read-only into `/docker-entrypoint-initdb.d/`, so ClickHouse applies it once, on a fresh data volume only. To re-apply it after editing:

```bash
docker compose -f docker-compose.clickhouse-test.yml down -v
docker compose -f docker-compose.clickhouse-test.yml up -d
```

The `-v` matters — it drops the named `clickhouse-data` volume declared at the bottom of `docker-compose.clickhouse-test.yml`; without it the init script is skipped.

The seed also depends on `config/clickhouse-config.xml`, mounted as `/etc/clickhouse-server/config.d/zookeeper.xml`. That file declares the ZooKeeper node (`zookeeper:2181`), the macros `cluster=test_cluster`, `shard=01`, `replica=replica_01`, and the `test_cluster` remote server — all of which the seed's `ReplicatedMergeTree` and `Distributed(test_cluster, …)` definitions require.

### Migration log

There are no migrations to log. The table below instead records the change history of the test-stack seed schema, taken from `git log -- scripts/clickhouse_test_engines.sql`.

| Change | Date | What changed | Reversible? |
|--------|------|--------------|-------------|
| `scripts/clickhouse_test_engines.sql` (`b1d9ca0`) | 2026-05-13 | Seeded the test ClickHouse with the airports/flights demo schema (current contents of the file) | Yes — seed is applied only to a throwaway container volume; `down -v` discards it |
| `scripts/clickhouse_test_engines.sql` (`b2394bd`) | 2025-06-02 | Added ClickHouse configuration and SQL script for test-engines setup | Yes — same |
| `scripts/clickhouse_test_engines.sql` (`9cbb13d`) | 2025-05-31 | Added a dictionary table definition for test engines (no `Dictionary` engine remains in the file today — see the coverage gap below) | Yes — same |
| `scripts/clickhouse_test_engines.sql` (`4457b74`) | 2025-05-31 | Added the Docker Compose configuration and the initial SQL script for ClickHouse test engines | Yes — same |

## Schema / data dictionary

**The schema of the ClickHouse instance you point this app at is out of scope for this repository.** The app discovers it at runtime; nothing here describes or constrains it.

TODO (operator): if your deployment targets a specific ClickHouse instance whose schema needs documenting, document it in your own runbook. The source of truth is the live server — `system.tables` and `system.columns` on that host, which this app itself renders at `GET /api/databases` and `GET /api/table/:database/:table`.

Everything below describes only `scripts/clickhouse_test_engines.sql`, the seed schema for the local verification stack. It is the fixture used to check that relationship detection works; it is not production data and no application depends on its shape.

**Engine coverage of the seed.** It exercises the `MergeTree`, `Replicated*`, `MaterializedView` and `Distributed` parsing branches (`models/clickhouse.go:245-311`). It contains **no `Dictionary` table** — a case-insensitive grep for "dictionary" over the seed file returns nothing — so the `Dictionary*` branch (`models/clickhouse.go:263-275`, which is the only consumer of `loading_dependencies_database` / `loading_dependencies_table`) and the `EngineDictionary` classification are not covered by the local stack.

### `raw.airports_local`

`ENGINE = MergeTree`, `ORDER BY code` (`scripts/clickhouse_test_engines.sql:20-30`). Seeded with 10 rows.

| Column | Type | Notes |
|--------|------|-------|
| `code` | `String` | Sort key |
| `name` | `String` | |
| `city` | `String` | |
| `country` | `String` | |
| `lat` | `Float64` | |
| `lon` | `Float64` | |

### `raw.flights_local`

`ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/raw/flights_local', '{replica}')`, `PARTITION BY toYYYYMM(scheduled_departure)`, `ORDER BY (toDate(scheduled_departure), origin, destination, flight_id)` (`scripts/clickhouse_test_engines.sql:32-46`). Seeded with 51 rows across 2026-05-10 … 2026-05-12; it is the source of both materialized views.

| Column | Type | Notes |
|--------|------|-------|
| `flight_id` | `UInt64` | Last element of the sort key |
| `flight_number` | `String` | |
| `airline_code` | `LowCardinality(String)` | |
| `origin` | `String` | Sort key; airport code, joins to `raw.airports_local.code` by convention only (no enforced constraint) |
| `destination` | `String` | Sort key |
| `scheduled_departure` | `DateTime` | Drives both the partition key and the sort key |
| `actual_departure` | `DateTime` | |
| `delay_minutes` | `Int32` | Aggregated by both MVs |
| `status` | `LowCardinality(String)` | Seed values observed: `on_time`, `delayed`, `cancelled` |

### `aggregated.flight_stats_daily_local`

`ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/aggregated/flight_stats_daily_local', '{replica}')`, `ORDER BY (day, origin, destination)` (`scripts/clickhouse_test_engines.sql:52-64`). Written only by `aggregated.flight_stats_daily_mv`.

| Column | Type | Notes |
|--------|------|-------|
| `day` | `Date` | Sort key |
| `origin` | `String` | Sort key |
| `destination` | `String` | Sort key |
| `flights_count` | `AggregateFunction(count, UInt64)` | Aggregate state |
| `avg_delay` | `AggregateFunction(avg, Int32)` | Aggregate state |
| `max_delay` | `AggregateFunction(max, Int32)` | Aggregate state |

The three `AggregateFunction` columns hold intermediate states, so a reader
merges them (`countMerge`, `avgMerge`, `maxMerge`) rather than selecting them
directly. That is standard ClickHouse semantics, not something this repository
states — no file in the repo reads these columns.

### `aggregated.airport_traffic_hourly_local`

`ENGINE = SummingMergeTree`, `ORDER BY (hour, airport)` (`scripts/clickhouse_test_engines.sql:66-74`). Written only by `aggregated.airport_traffic_hourly_mv`.

| Column | Type | Notes |
|--------|------|-------|
| `hour` | `DateTime` | Sort key |
| `airport` | `String` | Sort key |
| `flights` | `UInt64` | Summed |
| `total_delay` | `Int64` | Summed |

### Materialized views

Both read from `raw.flights_local` and write with `TO` into a local table (`scripts/clickhouse_test_engines.sql:80-99`). The seed file notes that they must exist before the seed `INSERT`s so they capture the seeded rows — hence the statement ordering in the file.

| View | Target | Transformation |
|------|--------|----------------|
| `aggregated.flight_stats_daily_mv` | `aggregated.flight_stats_daily_local` | `toDate(scheduled_departure) AS day`, `origin`, `destination`, `countState() AS flights_count`, `avgState(delay_minutes) AS avg_delay`, `maxState(delay_minutes) AS max_delay`, `GROUP BY day, origin, destination` |
| `aggregated.airport_traffic_hourly_mv` | `aggregated.airport_traffic_hourly_local` | `toStartOfHour(scheduled_departure) AS hour`, `origin AS airport`, `toUInt64(1) AS flights`, `toInt64(delay_minutes) AS total_delay` |

These expressions are what the app surfaces on the edges of `GET /api/relationships/:database/:table`, parsed out of `create_table_query` by `parseViewQuery` / `extractColumnMappings` (`models/clickhouse.go:487-533`, `:534+`).

### Distributed wrappers

Each local table gets one `Distributed` wrapper over the `test_cluster` cluster, declared with `AS <local table>` so it inherits that table's column list verbatim (`scripts/clickhouse_test_engines.sql:105-117`).

| Wrapper | Engine definition | Columns |
|---------|-------------------|---------|
| `raw.airports` | `Distributed(test_cluster, 'raw', 'airports_local', rand())` | As `raw.airports_local` |
| `raw.flights` | `Distributed(test_cluster, 'raw', 'flights_local', rand())` | As `raw.flights_local` |
| `aggregated.flight_stats_daily` | `Distributed(test_cluster, 'aggregated', 'flight_stats_daily_local')` | As `aggregated.flight_stats_daily_local` |
| `aggregated.airport_traffic_hourly` | `Distributed(test_cluster, 'aggregated', 'airport_traffic_hourly_local')` | As `aggregated.airport_traffic_hourly_local` |

The quoted database and table names in these definitions are exactly what the `Distributed` branch recovers by splitting `engine_full` on `'` and taking parts 3 and 5 (`models/clickhouse.go:276-289`) — which is why an `engine_full` with fewer than 6 quote-delimited parts yields no dependency edge.
