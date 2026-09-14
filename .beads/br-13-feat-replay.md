# Bead 13: Request replay and re-issue

- **Priority**: P2 (medium)
- **Dependencies**: 3, 6, 8, 9
- **Blocks**: none

## Description

Re-issue any captured request, optionally with edits, and record the result as a first-class
comparable call.

**`lens replay <id>`** reads the stored request body, POSTs it to the upstream through the same
proxy transport, and prints the new request id plus a compact diff of the outcome
(status, model, tokens, cost, duration, warnings). Flags:

- `--set <jsonpath>=<value>` — repeatable. Path syntax: dot-separated keys with array indices,
  e.g. `messages.0.content`, `temperature`, `thinking.budget_tokens`, `tools.2.name`. Values are
  parsed as JSON when they parse as JSON, otherwise treated as a string — so `--set temperature=0.7`
  and `--set messages.0.content='"hi"'` both work, and `--set stream=true` yields a boolean.
- `--dump <path>` — write the edited body to a file and exit without sending. This is the safe way
  to inspect exactly what would be sent.
- `--no-capture` — send without recording.
- `--diff <other-id>` — compare this replay against another request id instead of its original.

**Safety posture — stated plainly because it is the only write path in the project.** Replay sends
a request to an LLM API. It never executes anything, never touches the user's repository, never
re-runs tools. A captured body may *describe* tool calls that the original client executed; replay
re-sends the description to the model and stores the reply. It does not act on `tool_use` blocks.
This is worth stating in `--help` and the README so nobody assumes replay re-runs an agent.

**Cost warning**: `lens replay` prints the estimated cost of the original call before sending and
requires `--yes` when the original's `cost_usd` exceeds a configurable threshold, or when the price
table is unpriced (so the cost is unknown). Replaying is billable and should never be a surprise.

**Storage linkage**: the replay is captured as a normal request with `replay_of = <original id>`
(column exists from bead 6) and `--set` edits recorded in a `replay_edits` column (JSON array of
`{path, old, new}`), so an edited replay is self-documenting rather than an unexplained variant.

**Dashboard**: the request detail view gains a **Replay** button opening a small editor seeded with
the original body, a JSON-path editor for the same `--set` operations, a clear cost confirmation,
and a side-by-side result comparison against the original.

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
- `lens replay <id>` re-sends and prints the new id with an outcome diff.
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
- E2E (opt-in, needs a key): replay a real captured request with `--set max_tokens=1` (a cheap edit
  that cannot alter behaviour) → new row with `replay_of` set.

## Files to Touch

- `internal/replay/edit.go` (create)
- `internal/replay/replay.go` (create)
- `internal/replay/edit_test.go` (create)
- `internal/replay/replay_test.go` (create)
- `internal/cli/replay.go` (create)
- `internal/api/api.go` (modify — `POST /api/requests/{id}/replay`)
- `internal/store/store.go` (modify — `replay_edits` column handling; column already exists from bead 6)
- `internal/web/app.js`, `index.html` (modify — replay editor and comparison)
