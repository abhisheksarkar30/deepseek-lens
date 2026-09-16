[← INDEX](INDEX.md)

# API Surface

Two HTTP surfaces run in one process (`lens serve`): the **proxy listener** (no routes — a
transparent reverse proxy, see [workflows.md](workflows.md)) and the **dashboard listener**, whose
JSON API is documented here. Registered in the `mux.Handle*` block at
[internal/api/api.go:82-97](../../internal/api/api.go).

## REST / RPC

| Method | Path | Auth | Summary | Response | Evidence |
|---|---|---|---|---|---|
| GET | `/api/requests` | none (loopback-only) | List captured requests; query params `limit`, `offset`, `since` (duration or RFC3339), `session`, `model`, `warn`, `errors` | `[]store.Request` + page headers | [internal/api/api.go:217-277](../../internal/api/api.go) |
| GET | `/api/requests/{id}` | none | One request + its attached warnings | `requestDetail{*store.Request, Warnings}` | [internal/api/api.go:278-308](../../internal/api/api.go) |
| POST | `/api/requests/{id}/replay` | **Origin/Host allowlist + opt-in flag** (see below) | Re-sends a captured request's body (optionally edited) through the live proxy | `replay.Result{ID, Captured, Status, Outcome}` | [internal/api/api.go:356-468](../../internal/api/api.go) |
| GET | `/api/stats` | none | Aggregate stats: summary, by-model, by-day, by-cost-source; query param `since` | `statsResponse` | [internal/api/api.go:652-700](../../internal/api/api.go) |
| GET | `/api/warnings/summary` | none | Warning counts grouped by `(kind, severity)` over the **whole** table; query params `since`, `kind`, `severity`. **No `limit`/`offset`** | `[]store.WarningGroup` | [internal/api/api.go:701-718](../../internal/api/api.go) |
| GET | `/api/warnings` | none | List warnings; query params `limit`, `offset`, `since`, `kind`, `severity` | `[]store.Warning` + page headers | [internal/api/api.go:719-754](../../internal/api/api.go) |
| GET | `/api/sessions` | none | List sessions with running totals; query params `limit`, `offset` | `[]store.Session` + page headers | [internal/api/api.go:755-803](../../internal/api/api.go) |
| GET | `/api/sessions/{id}` | none | One session + its calls (chronological) + union of its warnings | `sessionDetail{*store.Session, Calls, Warnings}` | [internal/api/api.go:804-851](../../internal/api/api.go) |
| GET | `/api/stream` | none | Server-Sent Events feed of `{type:"request", id}` / `{type:"warnings", id, warnings}` events | SSE `text/event-stream` | [internal/api/api.go:852-903](../../internal/api/api.go) |
| GET | `/api/health` | none | Sink accepted/dropped counts, consumer processed/failed/flushes, last-write age, `replay_enabled` | `healthResponse` | [internal/api/api.go:904-923](../../internal/api/api.go) |
| GET/* | `/` (catch-all) | none | Serves the embedded dashboard static assets (`internal/web`) | HTML/CSS/JS | [internal/api/api.go:97](../../internal/api/api.go) |

Every GET route but the catch-all is wrapped by `methodGet`, which rejects non-GET methods with a
JSON 405 ([internal/api/api.go:105-113](../../internal/api/api.go)). All routes except replay are
read-only and rely on loopback binding for their "no auth needed" rationale
([internal/api/api.go:96-104](../../internal/api/api.go), and see
[security-and-permissions.md](security-and-permissions.md)).

### Pagination contract (the three list routes)

`/api/requests`, `/api/warnings`, and `/api/sessions` are paginated. The metadata rides on
**response headers, not in the body** — deliberately, so every existing consumer that decodes these
routes as bare JSON arrays keeps working unchanged.

| Header | Meaning |
|---|---|
| `X-Total-Count` | Size of the **whole filtered set**, independent of the page window. Shrinking it to the page size is the failure that would make Next/Prev meaningless |
| `X-Limit` | The **applied** page size — see the rule below |
| `X-Offset` | The applied offset |

Request params: `?limit=<n>&offset=<n>`, both optional. Absent `?limit` parses to `0`, which
`store.Filter` treats as "use `store.DefaultLimit`" (**1000**, see
[internal/store/store.go:32](../../internal/store/store.go)) — never unbounded. A negative `limit` or
`offset` is rejected with **400** rather than clamped. On the two filtered routes — `/api/requests`
and `/api/warnings` — the handler passes the *same* `store.Filter` to its list call and its `Count*`
call, so the total and the page can never describe two different sets. `/api/sessions` has no
filterable column: its list call takes the window, but `CountSessions` takes no filter at all and
returns the whole table, so there is no filter for the total and the page to disagree on
([internal/api/api.go:254-261](../../internal/api/api.go),
[internal/api/api.go:741-746](../../internal/api/api.go),
[internal/api/api.go:770-777](../../internal/api/api.go)).

> **`X-Limit` is the applied page size, not the requested one.** When `?limit` is absent the request
> carries `0` but the store applies `DefaultLimit`, so the header reports `1000`, not `0`. A
> header-driven consumer computing `nextOffset = offset + X-Limit` depends on this: the raw `0` would
> leave it stuck on page 1 forever with no error to explain why
> ([internal/api/api.go:159-188](../../internal/api/api.go), and the regression test
> `TestPageHeaderLimitIsEffective` in [internal/api/pagination_test.go](../../internal/api/pagination_test.go)).

`/api/warnings/summary` is **deliberately unpaginated** and carries none of these headers. A
`GROUP BY kind, severity` result is bounded by the number of distinct pairs, not by row count, so it
cannot grow with the table, and paginating it would make a true group total unstatable — the exact
problem the summary exists to fix. Its *query* is unbounded (counting correctly means scanning every
warning); the covering index `idx_warnings_kind_severity_created_at` keeps that cheap — see
[data-model.md](data-model.md).

### Replay's guard (the one write route)

`POST /api/requests/{id}/replay` is the project's only billable, state-changing route. It is gated by:

1. `replayEnabled` — off by default; `lens serve --replay` turns it on
   ([internal/api/api.go:357-362](../../internal/api/api.go)).
2. An Origin/Host allowlist (`replayOriginReject`) applied before any bytes are sent —
   [internal/api/api.go:604-626](../../internal/api/api.go). Full rationale in
   [security-and-permissions.md](security-and-permissions.md).

Query params: `?set=<jsonpath>=<value>` (repeatable, body edits) and `?no_capture=true` (send without
recording).

## Async messaging

None — no queues or topics. SSE (`/api/stream`) is the closest analog: an in-process fan-out
`Broker` ([internal/api/broker.go](../../internal/api/broker.go)) that a `PublishingStore` decorator
feeds by wrapping every store write
([internal/api/publishing_store.go](../../internal/api/publishing_store.go)). A slow SSE subscriber
is dropped (buffer of 64) rather than allowed to block a publish.

## Scheduled / CLI triggers

No cron/scheduler. Every non-`serve` trigger is a `lens` CLI subcommand — see
[cli-and-tooling.md](cli-and-tooling.md) for the full table. `lens replay` is the one CLI command
that itself calls the dashboard's write route (`POST /api/requests/{id}/replay`) over HTTP rather than
touching the store directly — [internal/cli/replay.go](../../internal/cli/replay.go).

## Representative payloads

`healthResponse` (from `/api/health`, [internal/api/api.go:888-902](../../internal/api/api.go)):

```json
{
  "sink_accepted": 7,
  "sink_dropped": 2,
  "consumer_processed": 5,
  "consumer_failed": 0,
  "consumer_flushes": 1,
  "last_write_at": "2026-09-15T12:00:00Z",
  "last_write_age_ms": 1500,
  "replay_enabled": false
}
```

`replay.Result` (from `POST /api/requests/{id}/replay`,
[internal/replay/replay.go:59-64](../../internal/replay/replay.go)):

```json
{
  "id": 42,
  "captured": true,
  "status": 200,
  "outcome": {
    "id": 42, "status": 200, "model": "deepseek-flash",
    "input_tokens": 120, "output_tokens": 340,
    "cost_usd": 0.0021, "cost_source": "configured",
    "duration_ms": 812.4, "warnings": ["top_p_clamped: ..."]
  }
}
```
