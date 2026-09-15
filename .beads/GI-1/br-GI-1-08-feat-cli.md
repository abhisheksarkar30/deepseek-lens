# Bead br-GI-1-08: CLI subcommands

**Plan Reference**: `docs/planning/GI-1-deepseek-lens-v1.md` §Bead sequence

- **Priority**: P1 (high)
- **Dependencies**: br-GI-1-01, br-GI-1-06, br-GI-1-07
- **Blocks**: br-GI-1-11, br-GI-1-13

## Description

The command surface, dispatched from the map established in br-GI-1-01. Stdlib `flag` only.

**`lens serve`** — starts the proxy listener (br-GI-1-03) and the dashboard listener (br-GI-1-09) and the
consumer (br-GI-1-07) in one process. Flags: `--proxy-addr`, `--dashboard-addr`, `--no-capture`,
`--allow-remote`, `--db`, `--replay`. `--replay` sets config `ReplayEnabled` and enables the replay
endpoint (`POST /api/requests/{id}/replay`, br-GI-1-13), which is **off by default** and guarded by an
`Origin`/`Host` allowlist. Prints a startup banner with the exact
`export ANTHROPIC_BASE_URL=http://127.0.0.1:8787` line for the user to copy, plus the dashboard URL.
Blocks until SIGINT; graceful shutdown with a bounded drain. `--no-capture` prints a standing
warning that nothing is being recorded.

**`lens ls`** — table of recent requests: `ID  TIME  DUR  MODEL  IN/OUT TOK  COST  STATUS  ⚠`.
Flags: `--limit` (default 25), `--session`, `--model`, `--since`, `--warn` (only requests with
warnings), `--errors`. Human-readable relative times (`4m ago`).

**`lens show <id>`** — full detail for one request: all timings, both models, token breakdown,
cost, the warning list with severities, request headers (redacted), and request/response bodies
pretty-printed, **truncated to terminal height** with a `--full` flag to dump everything. Bodies are
JSON-pretty-printed when parseable, raw otherwise.

**`lens tail`** — live feed. Polls the store every 500ms and redraws a rolling table of the last N
calls using ANSI cursor movement (no TUI library). `--session` and `--warn` filters apply. Exits on
Ctrl-C. Falls back to plain append-only line output when stdout is not a TTY, so it can be piped.

**`lens stats`** — aggregate over a window (`--since`, default 24h): total calls, input/output
tokens, cost, error count, warning count by kind, mean/p50/p95 duration, and a per-model breakdown.
Prints a simple in-terminal bar for the model split using block characters.

**`lens warnings`** — list warnings across requests: `KIND  SEVERITY  COUNT  LAST SEEN`, plus
`--detail` to list individual occurrences with their request ids. This is the CLI face of the
flagship feature and the command a user will actually run to answer "what is being dropped?".

**`lens export`** — JSONL to stdout, one request per line, for external analysis. Real JSON
encoding, not the truncated terminal formatting.

**`lens doctor`** — extended from br-GI-1-01: now also reports schema version, row counts, sink
accepted/dropped, consumer last-write age, WAL mode, the replay posture (off by default; on when
config `ReplayEnabled` is set — `--replay`, `LENS_REPLAY_ENABLED`, or `replay_enabled` in the config
file; endpoint guarded by the `Origin`/`Host` allowlist), and a PASS/WARN line per check. Exits
non-zero if any check fails.

**`lens prices`, `lens replay`** — registered in the map but returning "not implemented" until
br-GI-1-11 and br-GI-1-13.

Shared output helpers in `internal/cli`: `table()` (column-aligned, width-aware), `humanDuration`,
`humanTokens` (`1.2k`, `3.4M`), `humanCost`, and `relTime`. `--json` on `ls`/`stats`/`warnings`
emits machine-readable output for scripting.

## Rationale

Two of these commands carry more weight than their size suggests. `lens warnings` is the primary
user-facing surface of the flagship feature — if it is not quick to read, the detection work is
wasted. `lens doctor` is the mitigation for plan risk 4: when the user's coding session is broken
because the proxy is wedged, the first thing they need is one command that says what is wrong.

Everything routes through a shared `output` package so column formatting and human units are not
reimplemented seven times.

## Outcome Definition

- `go test ./internal/cli/...` passes.
- Every listed subcommand runs, renders to a non-TTY without ANSI escapes, and exits 0.
- `lens ls --json` output parses as JSON.
- `lens show` on a nonexistent id exits non-zero with a clear message.
- `lens tail` with stdout piped emits plain lines, not cursor-movement sequences.
- `lens doctor` exits non-zero when a check fails.
- `lens serve --no-capture` prints the standing warning and records nothing.
- The replay endpoint is enabled when config `ReplayEnabled` is set — via `--replay`,
  `LENS_REPLAY_ENABLED`, or `replay_enabled` in the config file; it is off by default.
- Startup banner contains a copy-pasteable `ANTHROPIC_BASE_URL` line with the effective address.

## Test Specifications

- Unit Tests (`internal/cli/format_test.go`):
  - `humanTokens`: 999 → `999`; 1500 → `1.5k`; 1_500_000 → `1.5M`; 0 → `0`.
  - `humanDuration`: sub-ms, ms, s boundaries; `1.4s`, `12ms`.
  - `humanCost`: nil → `—` (not `$0.00`, which would imply a real zero cost); small values to 4dp.
  - `relTime`: `just now`, `4m ago`, `3h ago`, `2d ago`.
  - `table()`: aligns ragged input; handles a value wider than its column; truncates with `…` at a
    width budget.
  - ANSI is suppressed when the writer is not a TTY.
- Integration Tests (`internal/cli/cli_test.go`, temp DB seeded with fixtures, capturing stdout):
  - `ls` renders the expected row count and respects `--limit`.
  - `ls --warn` returns only warned requests.
  - `ls --json` parses and has the expected field set.
  - `show <id>` includes the warning list and both bodies.
  - `show <missing>` exits non-zero.
  - `show` truncates a large body without `--full`; dumps fully with it.
  - `stats` totals match the fixture.
  - `warnings` groups by kind with correct counts; `--detail` lists occurrences.
  - `export` emits valid JSONL with one object per request.
  - `doctor` reports PASS on a healthy store and non-zero exit on an injected failure.
  - `doctor` reports the replay posture (off by default; on when `ReplayEnabled` is set via
    `--replay`, `LENS_REPLAY_ENABLED`, or `replay_enabled` in the config file; the `Origin`/`Host`
    guard named).
  - `serve --help` lists all flags.
- E2E: `lens serve` started as a subprocess, a request sent through it, `lens ls` shows it.

## Files to Touch

- `internal/cli/format.go` (create)
- `internal/cli/format_test.go` (create)
- `internal/cli/ls.go`, `show.go`, `tail.go`, `stats.go`, `warnings.go`, `export.go`, `doctor.go`,
  `serve.go` (create)
- `internal/cli/cli_test.go` (create)
- `cmd/lens/main.go` (modify — register the new subcommands)

## Review Notes (Phase 5.5, 2026-09-15)

- **OK** on all subcommands: 31 cli tests, the testable-function pattern held, and
  `prices`/`replay` were registered as stubs with specific errors as the bead required.
- **WARNING — the "`lens serve` as a subprocess, request, `lens ls`" integration test was not
  written.** Flagged by the implementer at the time. The real end-to-end path is recorded instead in
  `docs/acceptance.md`, so the behaviour is verified somewhere, just not as an automated test.
- `prices.go` and `replay.go` were created here but were not in the bead's Files-to-Touch list; the
  bead's own prose requires them, so the change is correct.
