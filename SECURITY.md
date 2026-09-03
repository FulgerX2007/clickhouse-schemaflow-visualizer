# Security Policy

## Reporting a vulnerability

Report vulnerabilities privately through
[GitHub Security Advisories](https://github.com/FulgerX2007/clickhouse-schemaflow-visualizer/security/advisories/new).
Please do not open a public issue for anything exploitable.

Include what you need to reproduce it: the endpoint or input, the ClickHouse or Grafana
shape it needs, and what an attacker gets. A first response should arrive within a week.

## Supported versions

The latest tagged release only. Fixes go out as a new tag; there are no backports.

## Threat model

Read this before deciding where to run the visualizer.

**The HTTP API has no authentication, no authorization and no rate limiting.** Anyone who
can reach the port gets every endpoint, and the endpoints between them return the whole
schema metadata of the connected ClickHouse — database, table and column names, types,
comments, row and byte counts, and the stored `SELECT` of every materialized view. With
the Grafana integration configured they also return dashboard titles, dashboard URLs and
SQL snippets from panel queries.

This is by design: the deployment is expected to sit on a trusted network or behind an
authenticating reverse proxy. **The listening port is the security boundary.** See the
Security section of the [README](README.md) for how to bind and front it.

### What the application will not do

- **Write to ClickHouse.** It issues `SELECT` against `system.tables` and `system.columns`
  and nothing else. Point it at a read-only user.
- **Interpolate input into SQL.** Every query that takes a database or table name binds
  it as a parameter.
- **Expose credentials.** `CLICKHOUSE_PASSWORD` and `GRAFANA_TOKEN` are never logged and
  never appear in a response; `GET /api/connection` returns host, port, user, database and
  the TLS flag only.
- **Write to Grafana.** The integration reads dashboards, through a Viewer service-account
  token or from a directory of JSON files.

### In scope

Anything that breaks one of those four guarantees, plus: injection of any kind, a crash
reachable from a request or from dashboard content, credential disclosure, XSS through
schema or dashboard metadata, and unauthenticated amplification against ClickHouse or
Grafana.

### Out of scope

That the API is unauthenticated, and everything that follows from an operator exposing the
port to a network they do not trust. It is documented here and in the README rather than
fixed, because adding authentication is a feature decision, not a patch. If you want it,
open an issue.
