# 2. Report column usage as four states, not a boolean

- **Status:** Accepted
- **Date:** 2026-09-02
- **Context:** Grafana column-usage feature (`docs/plans/20260902-grafana-column-usage.md`)

## Context

The feature exists to answer "which columns can I drop?". The obvious shape is a
boolean: used or unused.

That shape is unsafe, because the cost of the two errors is not symmetric. A column
wrongly called *used* costs nothing but a missed cleanup. A column wrongly called
*unused* gets dropped, and a production dashboard breaks.

And the evidence has several ways of being absent that a boolean cannot tell apart:

- no dashboard reads the table at all
- a dashboard reads it, but through `SELECT *`, so the columns are not enumerable
- a query could not be parsed
- Grafana is switched off, still scanning, or unreachable

Under a boolean every one of those reads "unused".

## Decision

Four verdicts, of which exactly one licenses a drop.

| Verdict | Meaning | Safe to drop |
|---|---|---|
| `used` | something reads it: a dashboard, lineage, or ClickHouse itself | no |
| `unused` | the table is read, every query reading it was fully understood, and none name this column | **yes** |
| `unknown` | the table is read, but a query touching it could not be enumerated | no |
| `no-coverage` | nothing observed reads the table at all | no |

Reaching `unused` additionally requires that the column is not part of a primary,
sorting or partition key — `ALTER TABLE … DROP COLUMN` refuses those, so reporting one
would be recommending an impossible change — and that the table is not a `Distributed`
wrapper, which stores nothing of its own.

**When the dashboard scan has not completed, every column is `no-coverage`.** A Grafana
that is off, scanning or unreachable produces exactly the evidence of a Grafana in which
nothing is used.

## Consequences

- The report finds fewer droppable columns than a boolean would, and each one is
  defensible. That is the intended trade.
- Every verdict but plain `used` carries a `Reason`, so a reader can see *why* no claim
  is being made rather than guessing.
- The API withholds the payload entirely in any state but `ok`, and the UI states in
  words that only `unused` means safe to drop.
- `unused` still means *unused by Grafana*. Ad-hoc queries, applications and scheduled
  jobs are invisible to it, which the report says in its own caveats.
