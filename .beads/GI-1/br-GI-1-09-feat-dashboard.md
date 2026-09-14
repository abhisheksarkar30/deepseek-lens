# Bead br-GI-1-09: Dashboard and JSON API

**Plan Reference**: `docs/planning/GI-1-deepseek-lens-v1.md` §Bead sequence

- **Priority**: P1 (high)
- **Dependencies**: br-GI-1-06, br-GI-1-07, br-GI-1-08
- **Blocks**: br-GI-1-10, br-GI-1-11, br-GI-1-12, br-GI-1-13

## Description

A `go:embed`'d single-page dashboard plus the JSON API behind it, served on the dashboard listener
(`127.0.0.1:8788` by default). **No npm, no build step, no `node_modules`** — vanilla JS and CSS,
charts drawn as inline SVG.

**JSON API** (`internal/api`), all read-only except replay (br-GI-1-13):

- `GET /api/requests?limit&since&session&model&warn&errors` → list
- `GET /api/requests/{id}` → detail incl. warnings
- `GET /api/stats?since` → summary totals, percentiles, by-model, by-day
- `GET /api/warnings?kind&severity&since` → grouped and individual
- `GET /api/sessions`, `GET /api/sessions/{id}` → placeholders until br-GI-1-12 populates them
- `GET /api/stream` → **SSE push** of new-request and new-warning events
- `GET /api/health` → sink/consumer counters, last write age, drop count

Handlers are thin: parse query params into `store.Filter`, call the store, encode JSON. No business
logic here. Every list endpoint is capped by a `limit` — an unbounded query is never issued.

**SSE push** is fed by the consumer: when the consumer commits a batch it publishes
`{type:"request", id}` and `{type:"warnings", ...}` to an in-process broker
(`internal/api/broker.go`) that fans out to connected dashboard clients through buffered channels.
A slow browser client is **dropped from the broker**, never allowed to slow the publisher — the same
non-blocking discipline as the sink, applied to the UI.

**Dashboard views** (`internal/web/index.html`, `app.js`, `style.css`):

1. **Live feed** — rolling list of calls, newest first, over the SSE stream, with a pause control.
   Each row: time, duration, models, tokens, cost, status, and a warning badge when warnings exist.
2. **Request detail** — clicking a row shows full timing breakdown, both models, token breakdown,
   cost, redacted headers, and the warnings rendered prominently. Bodies pretty-printed with
   collapsible sections.
3. **Warning inbox** — grouped by kind with counts and severities; clicking a kind lists affected
   requests. This is the flagship feature's primary surface.
4. **Stats** — calls/tokens/cost over time as an inline SVG line chart, plus a by-model split.
5. **Sessions** — table view; the session drill-down lands in br-GI-1-12.

Live-total in the header: calls, tokens, cost for the current session window, updating on push.

The layout is responsive to ~400px (single column, no horizontal body scroll) and honours
`prefers-color-scheme`. Colours are defined as CSS custom properties so the theme is one block, not
scattered literals. Severity is never encoded by colour alone — each warning carries a text or
symbol label as well.

**Security posture**: the read-only dashboard binds loopback and has no auth. Combined with full
body storage this means prompts and code are visible to anything that can reach the port. `doctor`
states this plainly, and the README documents it. Dashboard auth is explicitly required before any
non-local deployment (noted in the spec's Deferred section).

**Replay is the exception** and is *not* covered by the read-only rationale: `POST
/api/requests/{id}/replay` (br-GI-1-13) is billable and state-changing, so it is gated by **two**
controls, neither a credential: a strict `Origin`/`Host` allowlist (reject when `Origin` is present
and is not the dashboard's own origin, and reject when `Host` is not loopback) and an explicit
opt-in — it stays **disabled unless replay is explicitly enabled** via `--replay`/`ReplayEnabled`
(off by default). A request failing either control is rejected before it can reach the upstream, so
a cross-origin page or a DNS-rebinding host cannot trigger replay spend. `doctor` and the README
surface this replay posture (off by default, enabled via `--replay`, guarded by `Origin`/`Host`).
"No auth is acceptable" applies only to the read-only observer views.

## Rationale

The dashboard is the difference between a log file and a tool. But it is also the largest
temptation to add a build toolchain, which would break the single-binary property that makes
`go build` the entire deploy story. Vanilla JS over ~2,000 lines of data is not a hardship.

The broker's drop-slow-clients policy is carried over from the sink deliberately: a dashboard with a
wedged browser tab must not be able to slow the proxy. Same invariant, second application.

## Outcome Definition

- `go test ./internal/api/...` passes.
- `lens serve` then `curl localhost:8788/api/requests` returns JSON.
- `curl localhost:8788/` returns the HTML; all assets are embedded (no file reads at runtime).
- A request through the proxy appears in the live feed within 1s without a page refresh.
- The dashboard renders correctly at 400px width with no horizontal body scroll.
- With 100 rapid requests, a deliberately stalled SSE client does not delay other clients or the
  consumer.

## Test Specifications

- Integration Tests (`internal/api/api_test.go`, `httptest` + temp store):
  - `GET /api/requests` returns the seeded rows; `limit` respected; `since`/`model`/`warn` filters work.
  - `GET /api/requests/{id}` returns the request with its warnings attached.
  - `GET /api/requests/999999` → 404 with a JSON error body, not an HTML error page.
  - `GET /api/stats` totals match the fixture.
  - `GET /api/warnings?kind=cache_control_ignored` returns only that kind.
  - `GET /api/health` reports sink/consumer counters.
  - `GET /api/stream` — connect, publish a new request via the broker, assert an SSE event arrives
    within 1s with the correct shape.
  - **Slow client isolation**: two `GET /api/stream` clients, one never reads. Publish 500 events.
    Assert the reading client receives them, the publisher never blocks (bounded time), and the
    stalled client is dropped rather than accumulating unboundedly.
  - **No unbounded queries**: `limit` absent → capped default, asserted against the store spy.
  - **Embedded assets**: `GET /` returns 200 HTML; `GET /app.js`, `/style.css` return 200.
  - **Method guard**: `POST /api/requests` → 405.
  - **Malformed query params**: `?limit=abc`, `?since=notatime` → 400 with a clear message, not a panic.
- E2E: start `lens serve`, issue a proxied request, fetch `/api/requests`, assert one row.

## Files to Touch

- `internal/api/api.go` (create)
- `internal/api/broker.go` (create)
- `internal/api/broker_test.go` (create)
- `internal/api/api_test.go` (create)
- `internal/web/index.html`, `app.js`, `style.css` (create)
- `internal/web/embed.go` (create — `//go:embed` FS export)
- `internal/cli/serve.go` (modify — start the dashboard listener)
