[← INDEX](INDEX.md)

# API Surface

Two HTTP surfaces run in one process (`lens serve`): the **proxy listener** (no routes — a
transparent reverse proxy, see [workflows.md](workflows.md)) and the **dashboard listener**, whose
JSON API is documented here. Registered in the `mux.Handle*` block at
[internal/api/api.go:153-171](../../internal/api/api.go).

## REST / RPC

| Method | Path | Auth | Summary | Response | Evidence |
|---|---|---|---|---|---|
| GET | `/api/requests` | none (loopback-only) | List captured requests; query params `limit`, `offset`, `since` (duration or RFC3339), `session`, `model`, `warn`, `errors` | `[]store.Request` + page headers | [internal/api/api.go:314-366](../../internal/api/api.go) |
| GET | `/api/requests/{id}` | none | One request + its attached warnings | `requestDetail{*store.Request, Warnings}` | [internal/api/api.go:375-400](../../internal/api/api.go) |
| POST | `/api/requests/{id}/replay` | **Origin/Host allowlist + opt-in flag** (see below) | Re-sends a captured request's body (optionally edited) through the live proxy | `replay.Result{ID, Captured, Status, Outcome}` | [internal/api/api.go:432-545](../../internal/api/api.go) |
| GET | `/api/stats` | none | Aggregate stats: summary, by-model, by-period, by-cost-source; query params `since`, `until`, and `granularity` (`hour`, `day`, `week`, or `month`; default `day`) — the two bounds are half-open `[since, until)`, and an unrecognized `granularity` is a 400 rather than a silent fallback | `statsResponse` | [internal/api/api.go:767-820](../../internal/api/api.go) |
| GET | `/api/warnings/summary` | none | Warning counts grouped by `(kind, severity)` over the **whole** table; query params `since`, `kind`, `severity`. **No `limit`/`offset`** | `[]store.WarningGroup` | [internal/api/api.go:822-854](../../internal/api/api.go) |
| GET | `/api/warnings` | none | List warnings; query params `limit`, `offset`, `since`, `kind`, `severity` | `[]store.Warning` + page headers | [internal/api/api.go:855-890](../../internal/api/api.go) |
| GET | `/api/sessions` | none | List sessions with running totals; query params `limit`, `offset` | `[]store.Session` + page headers | [internal/api/api.go:891-921](../../internal/api/api.go) |
| GET | `/api/sessions/{id}` | none | One session + its calls (chronological) + union of its warnings + a peak-priced rollup | `sessionDetail{*store.Session, Calls, Warnings, Peak}` — `Peak` is `{calls, cost_usd}`, computed **at read time** over the calls, so it uses the calendar installed **now**, not the one in force when each row was ingested; cost and warnings are frozen at ingest, so an old row's badge can disagree with this header (deliberately — see [workflows.md](workflows.md)) | [internal/api/api.go:960-1023](../../internal/api/api.go) |
| GET | `/api/stream` | none | Server-Sent Events feed of `{type:"request", id}` / `{type:"warnings", id, warnings}` events | SSE `text/event-stream` | [internal/api/api.go:1025-1062](../../internal/api/api.go) |
| GET | `/api/health` | none | Sink accepted/dropped counts, consumer processed/failed/flushes, last-write age, `replay_enabled` | `healthResponse` | [internal/api/api.go:1079-1099](../../internal/api/api.go) |
| GET | `/api/prices` | none | Effective price table, resolved fresh from `~/.deepseek-lens/prices.toml` on every call (no cache), plus the effective peak calendar's date sets echoed verbatim (`""`/`""` when no calendar is wired) | `pricesResponse{Path, PeakMultiplier, Models, OffPeakDates, WorkDates}` | [internal/api/prices.go:41-58](../../internal/api/prices.go) |
| POST | `/api/prices` | **Origin/Host allowlist** (`replayOriginReject`, action `"prices"`) | Replaces one model's rates wholesale (whole-row write, D3); an omitted field and an explicit `null` both unset it | `pricesResponse` (the post-write table, same shape — including the two calendar fields) | [internal/api/prices.go:80-126](../../internal/api/prices.go) |
| GET | `/api/retention` | none | Retention config plus both purge previews (age-based and unpriced) in one round trip, so a confirm dialog shows a real count | `retentionResponse` | [internal/api/purge.go:19-81](../../internal/api/purge.go) |
| POST | `/api/purge` | **Origin/Host allowlist** (`replayOriginReject`, action `"purge"`) | Deletes rows by `mode`: `older_than` (requires `days > 0`) or `unpriced` — the destructive route | `purgeResponse{Mode, Deleted, SessionsReconciled}` | [internal/api/purge.go:104-152](../../internal/api/purge.go) |
| GET/* | `/` (catch-all) | none | Serves the embedded dashboard static assets (`internal/web`) | HTML/CSS/JS | [internal/api/api.go:171](../../internal/api/api.go) |

Every GET route but the catch-all is wrapped by `methodGet`, which rejects non-GET methods with a
JSON 405 ([internal/api/api.go:176-189](../../internal/api/api.go)). All routes **except the three
POST write routes** (replay, prices, purge) are read-only and rely on loopback binding for their "no
auth needed" rationale ([internal/api/api.go:176-180](../../internal/api/api.go), and see
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
([internal/api/api.go:352-359](../../internal/api/api.go),
[internal/api/api.go:878-883](../../internal/api/api.go),
[internal/api/api.go:912-914](../../internal/api/api.go)).

> **`X-Limit` is the applied page size, not the requested one.** When `?limit` is absent the request
> carries `0` but the store applies `DefaultLimit`, so the header reports `1000`, not `0`. A
> header-driven consumer computing `nextOffset = offset + X-Limit` depends on this: the raw `0` would
> leave it stuck on page 1 forever with no error to explain why
> ([internal/api/api.go:230-248](../../internal/api/api.go), and the regression test
> `TestPageHeaderLimitIsEffective` in [internal/api/pagination_test.go](../../internal/api/pagination_test.go)).

`/api/warnings/summary` is **deliberately unpaginated** and carries none of these headers. A
`GROUP BY kind, severity` result is bounded by the number of distinct pairs, not by row count, so it
cannot grow with the table, and paginating it would make a true group total unstatable — the exact
problem the summary exists to fix. Its *query* is unbounded (counting correctly means scanning every
warning); the covering index `idx_warnings_kind_severity_created_at` keeps that cheap — see
[data-model.md](data-model.md).

### The three write routes' shared guard

`replayOriginReject` ([internal/api/api.go:675-733](../../internal/api/api.go)) is one Origin/Host
allowlist, parameterized by an `action` string for its error message, applied before any bytes are
sent or any row is touched. The write routes call it — nothing here is replay-specific
anymore:

1. `POST /api/requests/{id}/replay` — the project's **only billable** route, and the only one gated
   by an additional opt-in flag (`replayEnabled`, off by default; `lens serve --replay` turns it on,
   [internal/api/api.go:455-461](../../internal/api/api.go)). Query params: `?set=<jsonpath>=<value>`
   (repeatable, body edits) and `?no_capture=true` (send without recording).
2. `POST /api/prices` — always on; rejects malformed/unknown-field bodies and invalid rates before
   writing (see [internal/api/prices.go](../../internal/api/prices.go)).
3. `POST /api/purge` — always on; the **destructive** one. `mode: "older_than"` additionally requires
   `days > 0` (400 otherwise), and `mode: "unpriced"` ignores `days` entirely — see
   [internal/api/purge.go](../../internal/api/purge.go).
4. `POST /api/shutdown` — drives the running serve's stop. Same Origin/Host allowlist, plus a
   loopback-caller check because the route is disruptive. `lens shutdown` is the client.

Full guard rationale in [security-and-permissions.md](security-and-permissions.md).

## Async messaging

None — no queues or topics. SSE (`/api/stream`) is the closest analog: an in-process fan-out
`Broker` ([internal/api/broker.go](../../internal/api/broker.go)) that a `PublishingStore` decorator
feeds by wrapping every store write
([internal/api/publishing_store.go](../../internal/api/publishing_store.go)). A slow SSE subscriber
is dropped (buffer of 64) rather than allowed to block a publish.

## Scheduled / CLI triggers

No cron/scheduler, except the in-process purge: `lens serve` runs a 24-hour ticker that calls the same
`PurgeOlderThan` the `POST /api/purge` route calls, using `retention_days` — see
[internal/cli/serve.go](../../internal/cli/serve.go). Every non-`serve` trigger is a `lens` CLI
subcommand — see [cli-and-tooling.md](cli-and-tooling.md) for the full table. `lens replay` is the
one CLI command that calls a dashboard write route (`POST /api/requests/{id}/replay`) over HTTP
rather than touching the store directly — [internal/cli/replay.go](../../internal/cli/replay.go).
`lens purge` is the opposite case: the one CLI command that opens the store and writes to it
directly, deliberately not going through the API — see [internal/cli/purge.go](../../internal/cli/purge.go)
and CLAUDE.md's two-writers invariant.

## Representative payloads

`healthResponse` (from `/api/health`, [internal/api/api.go:1079-1099](../../internal/api/api.go)):

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
