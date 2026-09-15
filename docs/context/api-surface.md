[← INDEX](INDEX.md)

# API Surface

Two HTTP surfaces run in one process (`lens serve`): the **proxy listener** (no routes — a
transparent reverse proxy, see [workflows.md](workflows.md)) and the **dashboard listener**, whose
JSON API is documented here. Registered in
[internal/api/api.go:66-81](../../internal/api/api.go).

## REST / RPC

| Method | Path | Auth | Summary | Response | Evidence |
|---|---|---|---|---|---|
| GET | `/api/requests` | none (loopback-only) | List captured requests; query params `limit`, `since` (duration or RFC3339), `session`, `model`, `warn`, `errors` | `[]store.Request` | [internal/api/api.go:151-188](../../internal/api/api.go) |
| GET | `/api/requests/{id}` | none | One request + its attached warnings | `requestDetail{*store.Request, Warnings}` | [internal/api/api.go:190-222](../../internal/api/api.go) |
| POST | `/api/requests/{id}/replay` | **Origin/Host allowlist + opt-in flag** (see below) | Re-sends a captured request's body (optionally edited) through the live proxy | `replay.Result{ID, Captured, Status, Outcome}` | [internal/api/api.go:254-362](../../internal/api/api.go) |
| GET | `/api/stats` | none | Aggregate stats: summary, by-model, by-day, by-cost-source; query param `since` | `statsResponse` | [internal/api/api.go:568-599](../../internal/api/api.go) |
| GET | `/api/warnings` | none | List warnings; query params `limit`, `since`, `kind`, `severity` | `[]store.Warning` | [internal/api/api.go:601-621](../../internal/api/api.go) |
| GET | `/api/sessions` | none | List all sessions with running totals | `[]store.Session` | [internal/api/api.go:623-634](../../internal/api/api.go) |
| GET | `/api/sessions/{id}` | none | One session + its calls (chronological) + union of its warnings | `sessionDetail{*store.Session, Calls, Warnings}` | [internal/api/api.go:655-698](../../internal/api/api.go) |
| GET | `/api/stream` | none | Server-Sent Events feed of `{type:"request", id}` / `{type:"warnings", id, warnings}` events | SSE `text/event-stream` | [internal/api/api.go:703-737](../../internal/api/api.go) |
| GET | `/api/health` | none | Sink accepted/dropped counts, consumer processed/failed/flushes, last-write age, `replay_enabled` | `healthResponse` | [internal/api/api.go:739-772](../../internal/api/api.go) |
| GET/* | `/` (catch-all) | none | Serves the embedded dashboard static assets (`internal/web`) | HTML/CSS/JS | [internal/api/api.go:79](../../internal/api/api.go) |

Every GET route but the catch-all is wrapped by `methodGet`, which rejects non-GET methods with a
JSON 405 ([internal/api/api.go:83-95](../../internal/api/api.go)). All routes except replay are
read-only and rely on loopback binding for their "no auth needed" rationale
([internal/api/api.go:83-86](../../internal/api/api.go), and see
[security-and-permissions.md](security-and-permissions.md)).

### Replay's guard (the one write route)

`POST /api/requests/{id}/replay` is the project's only billable, state-changing route. It is gated by:

1. `replayEnabled` — off by default; `lens serve --replay` turns it on
   ([internal/api/api.go:277-280](../../internal/api/api.go)).
2. An Origin/Host allowlist (`replayOriginReject`) applied before any bytes are sent —
   [internal/api/api.go:491-542](../../internal/api/api.go). Full rationale in
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

`healthResponse` (from `/api/health`, [internal/api/api.go:739-752](../../internal/api/api.go)):

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
