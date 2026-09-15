# deepseek-lens — Design Specification

**Date:** 2026-09-14
**Status:** Approved (design) — pending implementation plan
**Repo:** `D:\github\deepseek-lens`

## Problem

Agentic coding tools (Claude Code, Cline, Cursor, and anything else speaking the Anthropic
Messages API) can be pointed at DeepSeek via `ANTHROPIC_BASE_URL=https://api.deepseek.com/anthropic`.
DeepSeek serves a genuinely Anthropic-native surface, so the tools work — but the compatibility
is *partial and silent*. Several client settings are accepted and then quietly ignored or rewritten.
The client has no way to know; there is no error, no field in the response, and no log on either side.

The result is that an agent can believe it is getting prompt caching, a thinking budget, and its
chosen sampling parameters, and be wrong on all three, with no signal anywhere.

## Goal

A local proxy that sits between the coding tools and `https://api.deepseek.com/anthropic`, passes
traffic through **byte-identically and without added latency**, and captures everything needed to
see what the calls actually were, what came back, what it cost, and — most importantly — **which
client parameters were silently dropped or rewritten**.

Non-goals for v1: translation between API dialects, multi-upstream routing, response caching,
multi-user auth, metrics export. See "Deferred" below.

## Key architectural decision

DeepSeek already speaks Anthropic natively. Therefore:

> **This is not a translation proxy. It is a transparent observing pass-through.**

Bytes arrive, bytes leave unchanged; a side-channel copy goes to an observer. This single fact is
the difference between roughly 800 lines of Go and roughly 5000, and it is the reason the chosen
approach is viable as a local single-binary tool rather than a service.

## Approaches considered

**A. Single Go binary, in-process observer — SELECTED.**
`httputil.ReverseProxy` with `FlushInterval: -1` (flush after every write) tees request and response
bodies into a bounded Go channel. One consumer goroutine owns every SQLite write. The dashboard is
`go:embed`'d vanilla HTML/JS — no npm, no build step. CLI dispatches subcommands via stdlib `flag`.

**B. Go sidecar proxy + separate dashboard service on Postgres — REJECTED.**
Hard-isolates the hot path from the UI. Also provisions a database server to store one developer's
proxy logs. The isolation it buys is not worth the operational surface at this scale. Revisit only
if lens ever becomes multi-user or team-hosted.

**C. Extend an existing tool (LiteLLM, claude-code-router, a mitmproxy addon) — REJECTED.**
The honest "buy vs build" check, and it fails on one specific point: none of these ship
DeepSeek-specific dropped-parameter detection, which is the entire reason this product exists.
They would also put a Python interpreter on the hot path.

## Topology

```
claude code / cline / cursor
        │  ANTHROPIC_BASE_URL=http://127.0.0.1:8787
        ▼
┌─────────────────────────────────────────────┐
│  deepseek-lens  (one process, two servers)  │
│                                             │
│  :8787  proxy ──tee──▶ bounded chan ──┐     │   hot path: zero parsing
│           │ FlushInterval:-1          │     │
│           ▼                           ▼     │
│     api.deepseek.com/anthropic   consumer ──▶ SQLite (WAL)
│                                       │     │
│  :8788  dashboard + JSON API ─────────┘     │   cold path: all parsing
└─────────────────────────────────────────────┘
```

Two listeners, deliberately. The dashboard never shares a listener with `/messages`: no path
collision with the upstream surface, and the two can be bound to different interfaces.

Default binds: proxy `127.0.0.1:8787`, dashboard `127.0.0.1:8788`. Both loopback.

## The latency contract

Stated as invariants, not intentions, because this is what makes the tool usable rather than
abandoned after a week:

| # | Invariant | Rationale |
|---|---|---|
| 1 | **Zero parsing on the hot path.** The proxy goroutine copies bytes and does nothing else. | Any per-chunk JSON work is latency added on every token. |
| 2 | **The observer can never block the client.** Capture goes to a bounded channel; on full, the capture is dropped and a drop counter incremented. Never backpressure. | A logging path able to stall a request is how you add seconds of latency to an agentic loop. |
| 3 | **No response buffering, ever.** | Counting tokens from SSE tempts you to buffer the stream. Instead parse incrementally with a tail carry-buffer for events split across chunk boundaries, while full bytes flow straight through. |
| 4 | **Fail open.** Capture failures never alter the response. Upstream errors pass through faithfully. | If lens wedges, the user's entire coding session dies with it. |
| 5 | **SQLite in WAL mode**, one writer connection and one read-only connection. | Readers never block the writer; the writer never blocks the proxy. |

Invariants 2 and 5 are the difference between a proxy that is used and one that is removed.

## Features

### F1 — Silently-dropped-parameter detection (flagship)

A pure function, `analyze(request, response) []Warning`, table-driven and unit-testable in isolation.
Verified against DeepSeek's published Anthropic-compatibility notes:

| Trigger | Warning kind | Severity | Why it matters |
|---|---|---|---|
| `cache_control` present anywhere | `cache_control_ignored` | warn | Client believes it is receiving prompt caching. It is not. |
| `thinking.budget_tokens` present | `budget_tokens_ignored` | warn | Thinking budget is silently unenforced. |
| `top_p` ≠ 1.0 outside thinking mode | `top_p_clamped` | warn | Sampling silently forced to 1.0. |
| `tool_choice.disable_parallel_tool_use` | `parallel_tool_use_ignored` | info | |
| model matches `opus*` | `model_remapped` | info | Resolves to `deepseek-v4-pro`. |
| model matches `sonnet*` / `haiku*` | `model_remapped` | info | Resolves to `deepseek-flash`. |
| model matches neither | `model_remapped` | warn | Falls back to `deepseek-flash` — a cost/quality surprise. |
| Unsupported content block type | `unsupported_content_block` | **error** | `document`, `search_result`, `redacted_thinking`, `mcp_tool_use`, `mcp_tool_result`, `container_upload`, `code_execution_tool_result`. May fail outright. |
| `top_k`, `service_tier`, `container`, `mcp_servers` present | `param_ignored` | info | |
| `anthropic-beta` header present | `header_ignored` | info | Ignored for `/messages`. |

Warnings are persisted as rows, not recomputed at read time. This makes the dashboard a single
query and makes a warning a historical fact rather than a function of current rules.

### F2 — Token and cost accounting

Incremental SSE parsing of only `message_start` (→ `input_tokens`) and `message_delta` (→ cumulative
`output_tokens`); plain JSON decode for non-streaming responses. Also capture `stop_reason`.

Cost = `usage × price_table[resolved_upstream_model]`.

**Known gap, stated plainly:** the model *names and mapping* were verified against DeepSeek's docs;
current per-token *pricing* was not. Prices will not be invented. The price table ships as a
user-editable config seeded with placeholder values, exposed via `lens prices`. Until it is
populated, cost figures are labelled with their provenance in both CLI and dashboard. This is a
hard prerequisite for the cost feature being meaningful, and it is the user's to resolve.

### F3 — Session / conversation grouping

Agentic loops resend a growing conversation, so the stable prefix identifies the run.
Key = `hash(messages[0..1])` combined with a 30-minute gap window.

`ponytail:` heuristic — it will mis-group two distinct runs that share an identical opening prompt.
Upgrade path: an explicit `x-lens-session` header, supported from day one as the manual override
(approximately five lines).

### F4 — Request replay

`lens replay <id> [--set <jsonpath>=<value>]`. Read path only: it re-hits the LLM, never the user's
infrastructure, so there is no side-effect surface. The response is captured as a new request with
`replay_of` set, giving before/after comparison for free.

## Storage

Tables: `requests`, `sessions`, `warnings`.

Driver: `modernc.org/sqlite` — pure Go, no CGO, so `GOOS=linux go build` works without a toolchain.

`requests` carries: id, timestamps, TTFB, total duration, method, path, status, client address,
`model_requested`, `model_upstream`, `stream`, `input_tokens`, `output_tokens`,
`cache_creation_tokens`, `cache_read_tokens`, `cost_usd`, `session_id`, `replay_of`, stop reason,
redacted headers, request body, response body, error.

**Cancellable columns created up front.** `session_id`, `cost_usd`, and the `warnings` table are
created by the base schema in bead 6 even though beads 10–12 are what populate them. This avoids
introducing a migration framework for v1.

`ponytail:` no migration system in v1 — `schema.sql` with `CREATE TABLE IF NOT EXISTS`. Add real
migrations when a v2 changes a shipped column.

### Body storage policy

Default **full, with a per-body size cap** (~256KB, configurable) applied to **both** the request
and the response body. Replay needs the real body, and detection of `cache_control` needs the
request body. Configurable to `truncated` or `off`.

### Secrets and network posture

- **`x-api-key` is redacted before storage.** Lens never persists a key and never injects one.
  Auth is pass-through by default.
- **Loopback-only bind.** `--allow-remote` is required to bind anything else.
- The SQLite file is gitignored. Enabling full body storage means prompts and codebase context
  live in a local file; this is a deliberate, documented trade, local-only.

## CLI

`serve`, `tail` (ANSI live table), `stats`, `ls`, `show <id>`, `replay <id>`, `warnings`, `prices`,
`export`, `doctor`. Stdlib `flag` with subcommand dispatch; no CLI dependency. `cobra` is added only
if flag ergonomics demonstrably hurt.

## Dashboard

`go:embed`'d static assets, vanilla JS, inline SVG charts, no build step and no `node_modules`.
Views: live feed (SSE push), request detail with dropped params highlighted, session view,
warning inbox, cost-over-time. JSON API on the dashboard listener.

A design that requires an npm toolchain to render a log table does not justify its build step.

## Test strategy

| Layer | What |
|---|---|
| Unit | Every `analyze` rule, positive **and** negative; model remap; cost arithmetic; session key. |
| Unit — the classic bug | SSE event split across two chunk boundaries. Most likely defect in the codebase; dedicated test. |
| Integration | `httptest` fake upstream emitting a canned SSE stream. Assert client body **byte-identical**, capture row written, tokens parsed. |
| **The money test** | Client receives the *first* SSE event **before** the fake upstream emits its last. This is what proves invariant 3 and prevents silent regression. |
| E2E (opt-in, needs a key) | One minimal real request against `api.deepseek.com/anthropic`. |

## Risks

1. **SSE events split across chunks** → carry buffer plus a dedicated test.
2. **Hot-path stall** → bounded channel, drop counter, TTFB assertion.
3. **Secret or prompt leakage** → key redaction, loopback binding, configurable body policy.
4. **The user's own coding session dies if the proxy wedges.** This is live dogfooding — the
   tool being built is what the user codes through. Mitigation: fail-open, `serve --no-capture`,
   and `doctor`.
5. **Price table drift** → config, never code.
6. **DeepSeek's model mapping changing** → mapping lives in config, not code.

## Deferred

Multi-upstream failover and routing; response caching (DeepSeek ignores `cache_control`, so there
is no upstream cache to hit, and a lens-side cache would be a semantic minefield); dashboard
authentication; Prometheus / OTel export; alerting; team or hosted deployment.

## Open item

DeepSeek per-token pricing for `deepseek-v4-pro` and `deepseek-flash` must be supplied by the user
before cost figures are meaningful. Tracked as its own bead deliverable, not assumed.
