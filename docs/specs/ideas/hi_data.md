# `hi data` specification

Status: Draft

Dependencies: `hi server` (keys kept on the server, devices, groups,
`policy.json`, audit, Slack), `hi ask` (approvals for expensive queries),
the private `data-access` skill planned in v0.17.0, and the decisions on
data access tracked in `hifinab/templates`.

## Goal

Let agents and people query the team's data with guardrails that hold even
when the agent makes a mistake: read-only, bounded in time, rows, and cost,
recorded, and without a database password on any laptop.

For a quant team this is probably the most used tool an agent could have.
Today an agent that needs prices or fills either uses the person's own
database credentials from `.env` (which it can read, copy, and use for
anything the person can), or the person runs queries by hand and pastes
results. Neither scales, and neither leaves a record of which data a
backtest used.

Which databases the team uses is not decided yet; that decision is tracked
in `hifinab/templates`. This spec is written to fit any of the common ones.

## Commands

On a device:

```text
hi data ls                                sources this device may query
hi data schema <source> [<table>]         tables, columns, types, row counts, and their descriptions
hi data sample <source> <table> [-n 20]   a few rows, to see what the data looks like
hi data sql <source> "<query>"            run a query
    [--dry-run] [--limit <n>] [--format table|csv|jsonl] [--out <file>]
    [--reason "<why>"] [--record]
hi data explain <source> "<query>"        what the query would read and cost, without running it
```

On the server box:

```text
hi server data add <source> --kind postgres|clickhouse|bigquery|snowflake|duckdb
                                          store the connection (read without echo)
hi server data remove|list|test [<source>]
```

`-` as the query reads it from stdin, so an agent can pass a file.

## Guardrails

Each limit is enforced by the database itself where it can be, and by hi as
well, so one mistake doesn't remove both.

| Guardrail | In the database | In hi |
|---|---|---|
| Read-only | A read-only role. Postgres: `default_transaction_read_only`. ClickHouse: `readonly=1`. BigQuery and Snowflake: a role with only read grants | Refuses statements that aren't a query, after parsing the first keyword and rejecting several statements in one call |
| Time | Postgres `statement_timeout` (and `transaction_timeout` from 17), ClickHouse `max_execution_time`, BigQuery job timeout, Snowflake `STATEMENT_TIMEOUT_IN_SECONDS` | Cancels after the same limit, default 60 s |
| Rows | ClickHouse `max_result_rows` | Returns at most `--limit` rows (default 1,000; at most 100,000 with `--out`) and says how many were cut |
| Cost | BigQuery `maximumBytesBilled`, so an oversized query fails without a charge | Estimates first (below) and asks for approval over a threshold |
| Size | | Results over 50 MB are refused with a hint to aggregate or filter |

### Estimating before running

`hi data sql` first asks the database what the query will read, where the
database can tell:

- **BigQuery**: a dry run gives the bytes processed, an upper bound. `LIMIT`
  doesn't reduce it on tables that aren't clustered, and hi says so when an
  agent relies on one.
- **Snowflake**: `EXPLAIN` gives partitions and bytes assigned without a
  running warehouse.
- **ClickHouse**: `EXPLAIN ESTIMATE` gives parts, rows, and marks for
  MergeTree tables.
- **Postgres**: `EXPLAIN` gives the planner's row and cost estimates, which
  are rough but catch a missing filter on a large table.

Policy turns the estimate into a decision: run, ask, or refuse. `--dry-run`
shows the estimate and stops, which is what the skill tells agents to do
first for any query on a table they haven't used.

## Policy

`policy.json` gains a `data` section per group. Without it, a group may use
no source.

```json
"staff": {
  "data": {
    "prices": { "tables": ["market.*", "ref.*"], "approve_over_gb": 50, "refuse_over_gb": 500 },
    "fills":  { "tables": ["trading.fills_daily"], "per_hour": 120 }
  }
},
"students": {
  "data": { "prices": { "tables": ["market.daily_bars"], "refuse_over_gb": 5 } }
}
```

- `tables` lists the tables or schemas a group may read. hi checks the
  tables a query names, and the database role is the real limit: each
  group maps to its own role on the server where the database supports it.
- Over `approve_over_gb`, the query goes to the caller's owner through
  `hi ask --approve`, with the query and the estimate.
- `per_hour` limits queries per user per source.

## Made for agents

- **Schema with meaning.** `hi data schema` shows column descriptions from
  the database's own comments, plus a catalog file the private templates
  repository can provide (`data/catalog.yaml`, served by the server like
  the templates): what a table is for, its time zone, whether it is
  point-in-time, known gaps. This is where the `data-access` skill's
  knowledge becomes something an agent can look up instead of guess.
- **Sample first.** The skill's rule: `schema`, then `sample`, then
  `explain`, then `sql`. Most bad queries are bad because the agent guessed
  a column.
- **Compact output.** The default table output is narrow, numbers are
  rounded for display (never in `csv` or `jsonl`), and cut rows are counted.
- **`--record`** writes the query, source, time, row count, and a hash of the
  result to `.hifin/data/queries.jsonl` in the project, so a backtest's
  inputs can be traced to the exact query and day. Research templates can
  turn it on by default.

## Where queries run

Queries run **on the server**, which holds the connection details and
streams results back over the signed API. The alternative, giving devices
short-lived database credentials, was considered and left for later: it
would let large extracts go direct, but most databases can't scope
temporary credentials as tightly as a server-side role, and a laptop would
hold a working password for the credential's lifetime.

## Records

Each query is audited with the user, device, agent, source, tables, the
estimate, bytes and rows returned, duration, and status. The query text is
kept for 30 days in `data_queries.jsonl` on the server, for reviewing what
agents ran; results are never stored. The weekly report gets a line per
source: queries, bytes scanned, and the top users.

## Protocol

`POST /v1/data/query` with `{source, sql, limit, dry_run, reason}`, signed
like every `/v1` route; results come back as JSON Lines in chunks.
`GET /v1/data` lists sources and rules for this device's group;
`GET /v1/data/{source}/schema` returns the schema and catalog.

## Releases

1. Postgres and ClickHouse (or whichever the team decides first): `ls`,
   `schema`, `sample`, `sql`, `explain`; read-only roles, time, row, and
   size limits; `tables` and `per_hour` in policy; audit.
2. Cost estimates and approvals through `hi ask`; BigQuery and Snowflake;
   the catalog from the private templates; `--record`.
3. Per-group database roles managed by `hi server data`, and DuckDB over
   Parquet files on the server for datasets that aren't in a database.

## Risks

- **Data leaves through the agent.** An agent allowed to read prices can
  send them anywhere it can reach. Inside a box, the egress allowlist
  limits where; outside one, nothing does. Sensitive sources should be
  limited to groups whose agents run in boxes.
- **Wrong answers that look right.** The biggest risk in research is not a
  leaked table but a query with look-ahead bias or the wrong time zone. The
  catalog and the private skills address it; hi can't check it.
- **The server becomes a data path.** Large results through the server use
  its bandwidth and memory. The size limit keeps that bounded; bulk extracts
  stay a person's job until direct credentials are decided.
- **Estimates are rough** on Postgres and missing on some engines. Time and
  row limits still apply.

## Open questions

1. Which databases the team uses, and where the data lives (tracked in
   `hifinab/templates`).
2. Whether queries should run as one role per group or as each person's own
   database identity, which matters if the database has row-level security.
3. Whether results should ever go direct to devices for large extracts.
4. Whether `hi data` should also offer Parquet or Arrow output for research
   notebooks, which would add a dependency.

## Findings

Checked on 2026-10-02.

- **Cost limits.** BigQuery dry runs give an upper bound on bytes, and a
  query over `maximumBytesBilled` fails without a charge; `LIMIT` doesn't
  reduce bytes read on unclustered tables. Snowflake's `EXPLAIN` reports
  `partitionsAssigned` and `bytesAssigned` without a running warehouse.
  ClickHouse's `EXPLAIN ESTIMATE` works for MergeTree tables, and `readonly`,
  `max_execution_time`, and `max_result_rows` bound a session. Postgres has
  `default_transaction_read_only`, `statement_timeout`, and, from version
  17, `transaction_timeout`.
- **Database MCP servers** converge on the same guardrails. Snowflake's
  managed MCP runs SQL with `read_only`, a timeout, and a chosen warehouse,
  truncates results at 250 KB, and advises a separate least-privilege role.
  Postgres MCP Pro's restricted mode uses read-only transactions, parses SQL
  to reject `COMMIT` and `ROLLBACK`, and limits run time. MotherDuck's MCP is
  read-only by default with 1,024 rows and 50,000 characters at most.
- What none of them do, and hi adds: approvals in Slack over a cost
  threshold, per-group table rules in the team's policy, an audit in the
  same place as compute, and credentials that never reach the device.

## Sources

- BigQuery: https://docs.cloud.google.com/bigquery/docs/best-practices-costs
- Snowflake: https://docs.snowflake.com/en/sql-reference/sql/explain,
  https://docs.snowflake.com/en/user-guide/snowflake-cortex/cortex-agents-mcp
- ClickHouse: https://clickhouse.com/docs/sql-reference/statements/explain,
  https://clickhouse.com/docs/operations/settings/query-complexity
- Postgres: https://www.postgresql.org/docs/current/runtime-config-client.html
- Postgres MCP Pro: https://github.com/crystaldba/postgres-mcp
- MotherDuck MCP: https://glama.ai/mcp/servers/15mdwrzibz
