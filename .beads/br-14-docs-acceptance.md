# Bead 14: README and end-to-end acceptance against real DeepSeek

- **Priority**: P1 (high)
- **Dependencies**: 10, 11, 12, 13
- **Blocks**: none

## Description

Closes the project with the two artefacts that prove it works on real traffic and can be handed to
someone else: a README, and a recorded end-to-end acceptance run.

**README.md** — written for a developer who has never seen the repo:

- What it is in three sentences, leading with the dropped-parameter problem rather than the
  architecture.
- Quick start: `go install` / `go build`, `lens serve`, and the exact
  `export ANTHROPIC_BASE_URL=http://127.0.0.1:8787` line, for Claude Code and for Cline separately.
- The two ports and what each is for.
- **The warning table** — every `Kind` this tool can raise and what it means. This is the reference
  a user returns to, so it is generated from the same source as bead 10's rules rather than
  hand-copied.
- Cost setup: the price table is empty by default, `lens prices --set` fills it, costs are labelled
  `unpriced` until then. States the spec's open item plainly instead of burying it.
- Security posture, honestly: loopback-only by default, `--allow-remote` is a footgun, full body
  storage means prompts and code are in a local SQLite file, dashboard has no auth — and therefore
  dashboard auth is required before any non-local deployment.
- Session grouping's heuristic and its documented ceiling, pointing at `x-lens-session`.
- Replay's safety posture: it re-sends to the LLM and never executes anything.
- Troubleshooting, headed by "my coding session broke" → `lens doctor`, then `lens serve
  --no-capture`.

**End-to-end acceptance run**, recorded into `docs/acceptance.md` with real observed output, not
described behaviour:

1. `lens serve` starts; banner shows both addresses.
2. Point a real client at it — Claude Code with `ANTHROPIC_BASE_URL` set — and issue one prompt.
3. Confirm the request appears in `lens ls` with correct model mapping (`claude-*` →
   `deepseek-*`), token counts, and duration.
4. Confirm the response the client rendered matches what the client would have received directly
   (same content, no truncation).
5. Issue a request carrying `cache_control` → confirm `cache_control_ignored` appears in
   `lens warnings` naming the sites.
6. Issue several prompts in one session → confirm `lens sessions` shows one session with the right
   turn count.
7. Configure prices with `lens prices --set` → confirm costs populate without a restart.
8. Load the dashboard → confirm the live feed, warning inbox, and session drill-down all render,
   and that a new call appears within 1s without a refresh.
9. `lens replay <id>` with `--set max_tokens=1` → confirm the replay is stored with `replay_of` set
   and the comparison renders.
10. Measured latency: time a request direct vs through the proxy, both to first byte and total.
    Record the numbers. **If the added first-byte latency exceeds 50ms, that is a finding to report,
    not a number to omit.**

Step 10 is the point of the whole bead. Everything else verifies that the features exist; that one
verifies the product's founding constraint was actually met on real traffic rather than only in a
`httptest` fixture. A tool that observes everything but is slow is a tool that gets removed from the
`ANTHROPIC_BASE_URL` line within a week.

## Rationale

The README was originally assigned to bead 1, which was wrong: most of what it must document
(the warning kinds, the price setup, sessions, replay, troubleshooting) does not exist until bead
13. Moving it here means it is written once, accurately, against a finished product.

The acceptance run exists because every test up to this point uses a fake upstream. A canned SSE
fixture proves the parser works; it cannot prove that DeepSeek's real stream, real headers, and real
model mapping behave as assumed. Plan risk 6 (mapping drift) and the spec's token-accounting feature
both depend on assumptions that only real traffic can confirm.

Step 10 is included as a measurement requirement rather than a pass/fail, because a latency number
is only meaningful relative to the direct baseline, and because the honest outcome — whatever it is
— belongs in the repo. If the tool is slow, that finding arrives before the user relies on it.

## Outcome Definition

- `README.md` exists, and every command in it has been run as written.
- The warning table matches the kinds `analyze` can actually emit (checked by test or script).
- `docs/acceptance.md` records all ten steps with real observed output.
- Steps 1–9 complete successfully against the real endpoint.
- Step 10 records direct-vs-proxied first-byte and total latency, with the delta stated.
- Any step that fails is written up as a finding with its cause, not omitted.

## Test Specifications

- Integration Test (`internal/analyze/readme_test.go`):
  - Assert every `Kind` constant in `analyze` appears in `README.md`'s warning table, and that the
    table lists no kind the code cannot emit. Prevents the reference table drifting from the code.
- Manual acceptance: the ten-step run above, output pasted verbatim into `docs/acceptance.md`.
- Regression guard: step 10's measured delta recorded as a number, so a future change that adds
  buffering shows up as a changed figure in review.

## Files to Touch

- `README.md` (create)
- `docs/acceptance.md` (create)
- `internal/analyze/readme_test.go` (create)
- `docs/planning/0001-deepseek-lens.md` (modify — record the acceptance outcome)
