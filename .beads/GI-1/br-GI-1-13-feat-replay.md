# Bead br-GI-1-13: Request replay and re-issue

**Plan Reference**: `docs/planning/GI-1-deepseek-lens-v1.md` §Bead sequence

- **Priority**: P2 (medium)
- **Dependencies**: br-GI-1-03, br-GI-1-06, br-GI-1-08, br-GI-1-09
- **Blocks**: none

## Description

Re-issue any captured request, optionally with edits, and record the result as a first-class
comparable call.

**`lens replay <id>`** requires a running `lens serve` and is a thin client for the send path: it
calls the server's `POST /api/requests/{id}/replay` rather than opening its own DB writer, so the
consumer stays the **sole writer** (br-GI-1-06's single-writer discipline). The server reads the stored
request body, POSTs it to the upstream through the same proxy transport, and records the replay row
through the same consumer writer; the CLI prints the new request id plus a compact diff of the
outcome (status, model, tokens, cost, duration, warnings). Flags:

- `--set <jsonpath>=<value>` — repeatable. Path syntax: dot-separated keys with array indices,
  e.g. `messages.0.content`, `temperature`, `thinking.budget_tokens`, `tools.2.name`. Values are
  parsed as JSON when they parse as JSON, otherwise treated as a string — so `--set temperature=0.7`
  and `--set messages.0.content='"hi"'` both work, and `--set stream=true` yields a boolean.
- `--dump <path>` — write the edited body to a file and exit without sending. This is the safe,
  purely local path: no upstream send, and no running server required. It is how you inspect exactly
  what would be sent.
- `--no-capture` — send without recording.
- `--diff <other-id>` — compare this replay against another request id instead of its original.

**Safety posture — stated plainly because it is the only write path in the project.** Replay sends
a request to an LLM API. It never executes anything, never touches the user's repository, never
re-runs tools. A captured body may *describe* tool calls that the original client executed; replay
re-sends the description to the model and stores the reply. It does not act on `tool_use` blocks.
This is worth stating in `--help` and the README so nobody assumes replay re-runs an agent.

**Endpoint guard** (settled posture; also noted in br-GI-1-09, since the endpoint lives on the otherwise
unauthenticated loopback dashboard). Because `POST /api/requests/{id}/replay` is the project's only
billable, state-changing route, it is **not** covered by the read-only dashboard's "no auth on
loopback" rationale. It is gated by **two** controls, neither a credential:

1. **Explicit opt-in** — the endpoint is **disabled unless replay is explicitly enabled**: config
   `ReplayEnabled` (default false), set by `lens serve --replay`. Off by default.
2. **Strict `Origin`/`Host` allowlist** — reject when `Origin` is present and is not the dashboard's
   own origin, and reject when `Host` is not loopback. A cross-origin page or a DNS-rebinding host
   therefore cannot trigger replay spend.

A request failing either control is rejected (403) before any upstream send.

**Why no secret (deliberate).** An earlier draft required a per-process secret printed in the
`serve` banner. That is dropped: the `Origin`/`Host` guard already kills the threat that mattered —
DNS rebinding and cross-origin POST — with no credential to distribute. A CLI request sends no
`Origin` at all, so `lens replay` needs nothing extra and the "how does the CLI obtain the secret"
question disappears rather than needing an answer. Any local process able to forge past this guard
can already read the SQLite file directly, so the endpoint grants no privilege beyond what is
already on disk. If defense against local (non-browser) processes is ever wanted, add a shared
secret then — that is the documented upgrade path.

`doctor` and the README surface this replay posture (off by default, enabled via `--replay`,
guarded by `Origin`/`Host`).

**Cost warning**: `lens replay` prints the estimated cost of the original call before sending and
requires `--yes` when the original's `cost_usd` exceeds a configurable threshold, or when the price
table is unpriced (so the cost is unknown). Replaying is billable and should never be a surprise.

**Storage linkage**: the replay is captured as a normal request with `replay_of = <original id>`
(column exists from br-GI-1-06) and `--set` edits recorded in a `replay_edits` column (JSON array of
`{path, old, new}`), so an edited replay is self-documenting rather than an unexplained variant.

**Dashboard**: the request detail view gains a **Replay** button opening a small editor seeded with
the original body, a JSON-path editor for the same `--set` operations, a clear cost confirmation,
and a side-by-side result comparison against the original. The embedded page issues a same-origin
request to the guarded replay endpoint (the `Origin`/`Host` guard passes for the dashboard's own
origin); the button is inert when replay is not enabled.

`internal/replay` holds the body-editing logic as a pure function —
`ApplyEdits(body []byte, edits []Edit) ([]byte, error)` — with no I/O, so the JSON-path walker is
testable in isolation. Path traversal rejects edits that would require creating missing intermediate
objects, returning a clear error rather than inventing structure.

## Rationale

Replay turns the captured corpus from a museum into a workbench. Prompt tuning against real traffic,
A/B-comparing a model or parameter change, and reproducing a bad response all need it, and none of
them are possible from a read-only log.

It is sequenced last because it is the only bead that adds risk: every other bead is a pure
observer, and this one makes the tool capable of *causing* API calls and spend. Keeping it at the
end means the observer is complete, tested, and dogfooded before anything can modify traffic, and it
means the cost-confirmation gate is designed by someone who already knows what a session costs.

The `ApplyEdits` pure-function split mirrors `analyze`: the trickiest part (path traversal into
nested JSON) is isolated from I/O so it can be tested exhaustively without a network.

## Outcome Definition

- `go test ./internal/replay/...` passes.
- `lens replay <id>` (with `lens serve` running) re-sends via the server's replay API and prints the
  new id with an outcome diff; the CLI opens no DB writer of its own.
- The replay endpoint rejects a request with a disallowed `Origin`/`Host`, or when replay is
  disabled — before any upstream send.
- `--set temperature=0.7` changes the value in the sent body (asserted against a capture).
- `--set` into a nested array path works; a path into a missing key errors clearly without sending.
- `--set` with a JSON value and with a plain string both behave as documented.
- `--dump` writes the edited body and sends nothing.
- The replayed request is stored with `replay_of` set and edits recorded.
- Replay never sends when the cost gate is unmet and `--yes` is absent.
- The dashboard Replay button round-trips: edit, send, see the comparison.

## Test Specifications

- Unit Tests (`internal/replay/edit_test.go`):
  - `--set temperature=0.7` on a body with `temperature` → value replaced, other fields untouched.
  - `--set` a string value → stored as a JSON string, not a bare token.
  - `--set` a nested path `messages.0.content` → correct element changed.
  - `--set` an array index `tools.2.name` → correct tool changed.
  - **Missing intermediate key** `a.b.c` where `a` does not exist → error, no mutation, no send.
  - **Out-of-range index** `messages.99.content` → error naming the index and the length.
  - **Non-numeric index** `messages.x.content` on an array → clear error.
  - **Invalid JSON value** for a JSON-typed field → error, no partial mutation.
  - **Empty edits** → body returned byte-identical.
  - **Multiple edits** applied in order; a later edit to the same path wins.
  - **Body byte-preservation**: editing one field does not reformat unrelated parts beyond
    JSON round-trip semantics (documented behaviour, asserted explicitly).
  - Edits recorded as `{path, old, new}` with correct old values.
- Integration Tests (`internal/replay`, `httptest` upstream):
  - Replay of a captured request hits the upstream with the edited body (asserted server-side).
  - The new request is stored with `replay_of` pointing at the original.
  - `--dump` makes no HTTP request.
  - **Cost gate**: unpriced original without `--yes` → refuses and sends nothing; with `--yes` →
    sends.
  - `--no-capture` → sends but writes no row.
  - `--diff` produces a comparison of the two requests' tokens/status/warnings.
  - **Endpoint guard**: `POST /api/requests/{id}/replay` with a disallowed `Origin` (or `Host`) →
    403; when replay is disabled → rejected. In every rejected case a recording fake upstream sees
    **zero** hits.
  - **Enabled → accepted (happy path)**: with replay enabled (`--replay`), a same-origin/loopback
    request is accepted and reaches the recording fake upstream exactly once.
  - **CLI needs the server**: `lens replay` against no running server fails clearly (no send, no
    row) rather than opening its own DB writer.
- E2E (opt-in, needs a key): replay a real captured request with `--set max_tokens=1` (a cheap edit
  that cannot alter behaviour) → new row with `replay_of` set.

## Files to Touch

- `internal/replay/edit.go` (create)
- `internal/replay/replay.go` (create)
- `internal/replay/edit_test.go` (create)
- `internal/replay/replay_test.go` (create)
- `internal/cli/replay.go` (create)
- `internal/api/api.go` (modify — `POST /api/requests/{id}/replay`)
- `internal/store/store.go` (modify — `replay_edits` column handling; column already exists from br-GI-1-06)
- `internal/web/app.js`, `index.html` (modify — replay editor and comparison)
