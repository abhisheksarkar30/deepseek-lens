# Bead br-GI-1-12: Session and conversation grouping

**Plan Reference**: `docs/planning/GI-1-deepseek-lens-v1.md` §Bead sequence

- **Priority**: P2 (medium)
- **Dependencies**: br-GI-1-05, br-GI-1-06, br-GI-1-07, br-GI-1-09
- **Blocks**: none

## Description

Group individual calls into agentic sessions so the dashboard answers "this Claude Code run cost $X
across N turns" instead of showing an undifferentiated call log.

`internal/session` implements the `SessionResolver` interface that br-GI-1-07 already injects (nil until
now). It is its **own pre-insert seam**, distinct from the post-insert `Analyzer` seam: resolution
runs before `InsertRequest` and sets `req.SessionID` on the row (which is why `lens ls --session`
and the session drill-down can filter on it). No pipeline surgery is required.

**Resolution order**, first match wins:

1. **Explicit override** — if `meta.SessionHeader` (`x-lens-session`) is non-empty, that value is
   the session id verbatim. Always wins. Approximately five lines, and the escape hatch for every
   limitation below.
2. **Prefix + gap** — otherwise, look up the most recent session with the same `meta.PrefixHash`
   (br-GI-1-05's SHA-256 over system text plus the first two messages). If its `last_seen` is within
   `SessionGapMinutes` (default 30), attach to it and bump `last_seen`. Otherwise create a new
   session with a fresh id and the same prefix hash.
3. A call whose body was unparseable (empty `PrefixHash`) gets a session with a null prefix, keyed
   only by gap timing within a 5-minute window — so broken parses do not collapse into one
   ever-growing bucket.

`ponytail:` prefix-hash + time-window heuristic. It will mis-group two distinct runs that share an
identical opening prompt (two "fix the build" sessions started within 30 minutes, say). The ceiling
is documented in the code and the README; the upgrade path is `x-lens-session`, which is already
supported and is the answer for anyone who needs exactness. A content-aware correlation (matching
the growing conversation prefix rather than just its head) is the next step if this proves too
coarse.

Session id format: `s_<unix-ms>_<8 hex chars of prefix hash>` — sortable by time, greppable,
collision-resistant enough for a local tool.

`store.UpsertSession` maintains, incrementally: `first_seen`, `last_seen`, `request_count`,
`total_input_tokens`, `total_output_tokens`, `total_cost_usd`, `priced_count`, `unpriced_count`,
`model_set` (comma-joined distinct upstream models), `warning_count`. Incremental update rather
than aggregate-on-read keeps the session list O(1) per call instead of O(rows).

**CLI**: `lens sessions` lists them (`ID  STARTED  DURATION  TURNS  TOKENS  COST  ⚠`);
`lens ls --session <id>` filters; `lens show <id>` prints the session header when the id is a
session. `lens stats --by session` ranks by cost.

**Dashboard**: session table replacing the br-GI-1-09 placeholder, with a drill-down listing every call
in the session in order, a running token/cost total, and the union of all warnings raised across the
session — which is the view that makes the flagship feature legible, since a `cache_control_ignored`
firing on all forty turns of one run is one story, not forty rows.

Cost totals in a session reuse br-GI-1-11's mixed-pricing rule: `$1.23 + N unpriced`.

## Rationale

Individual calls are not how anyone reasons about agentic work — sessions are. A Claude Code run is
one activity with forty LLM calls in it, and the question a user actually has is "what did that run
cost and did it hit any compatibility problems?" Bead 11 alone cannot answer it.

The heuristic is accepted knowingly. Exact correlation would require either a client-supplied id
(which no current tool sends) or full conversation-state tracking (expensive and still ambiguous).
Prefix-plus-window gets the common case right at negligible cost, and the explicit header covers the
rest. This is the correct place to accept an approximation, and the wrong place to hide one — hence
the documented ceiling.

## Outcome Definition

- `go test ./internal/session/...` passes.
- Two calls sharing a prefix within the window resolve to one session.
- The same prefix beyond the window creates a new session.
- A differing prefix creates a new session.
- `x-lens-session` overrides both rules.
- Session aggregates (count, tokens, cost, warning count) are correct after a sequence of calls.
- `lens sessions` lists them with correct turns and totals.
- The dashboard session drill-down lists calls in order with the union of warnings.
- Null-prefix calls are grouped by a 5-minute window, not collapsed into one bucket.

## Test Specifications

- Unit Tests (`internal/session/session_test.go`):
  - **Same prefix, +2min** → same session id.
  - **Same prefix, +45min** (beyond 30) → new session id, same prefix hash.
  - **Different prefix, +1min** → different session id.
  - **Explicit header** present on both → grouped by header value regardless of prefix or gap.
  - **Explicit header on one, absent on the other** → not grouped (no fallback guessing).
  - **Boundary**: gap exactly equal to `SessionGapMinutes` → same session (inclusive).
  - **Directional**: a call with an *older* timestamp than `last_seen` does not create a new session.
  - **Null prefix** → 5-minute window grouping; two null-prefix calls 1min apart group; 10min apart
    do not.
  - **Id format** → matches `^s_\d+_[0-9a-f]{8}$`.
  - **Aggregates**: after 3 calls, `request_count == 3`, tokens summed correctly.
  - **Aggregates with mixed pricing**: `priced_count` and `unpriced_count` both correct.
  - **warning_count** accumulates across calls in the session.
  - **model_set** contains distinct models only, no duplicates.
- Integration Tests (`internal/consumer` extension):
  - Three captured calls sharing a prefix → one `sessions` row, three `requests` rows with the same
    `session_id`.
  - `lens sessions` shows turns=3; `lens ls --session` returns exactly those three.
  - Dashboard `/api/sessions/{id}` lists the three in chronological order.
- E2E: two successive real requests with an identical system prompt → one session.

## Files to Touch

- `internal/session/session.go` (create)
- `internal/session/session_test.go` (create)
- `internal/store/store.go` (modify — `UpsertSession` incremental aggregates)
- `internal/consumer/consumer.go` (modify — inject the resolver)
- `internal/cli/sessions.go` (create), `ls.go` (modify — `--session`), `stats.go` (modify — `--by session`)
- `internal/api/api.go` (modify — real session endpoints)
- `internal/web/app.js`, `index.html` (modify — session table and drill-down)
