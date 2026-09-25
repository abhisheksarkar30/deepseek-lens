[← INDEX](INDEX.md)

# Testing & Quality

## Test frameworks in use

| Layer | Framework | Location | Evidence |
|---|---|---|---|
| Unit / integration | Go stdlib `testing` (no third-party test framework or assertion library) | one `_test.go` per package, all `internal/*` except `web` | e.g. [internal/proxy/proxy_test.go](../../internal/proxy/proxy_test.go), [internal/consumer/consumer_test.go](../../internal/consumer/consumer_test.go) |
| Store race coverage | Split race-on/race-off test files, run under `go test -race` | [internal/store/race_on_test.go](../../internal/store/race_on_test.go), [internal/store/race_off_test.go](../../internal/store/race_off_test.go) | — |
| Documentation-consistency | A test that diffs generated warning descriptions against README prose | [internal/analyze/readme_test.go](../../internal/analyze/readme_test.go) | — |

As of this doc's generation, `go test ./...` is green across every package with tests (`cmd/lens` has
none, by design; `internal/web` has none by the same convention, even though `app.js` does carry real
logic — formatting/rendering helpers such as `groupSessionWarnings`
([internal/web/app.js:567](../../internal/web/app.js)), `renderPager`
([internal/web/app.js:481](../../internal/web/app.js)) — that convention has not yet extended to a JS
test runner. The pager is shared by the sessions table and the warnings drill-down, so its
boundary arithmetic (edge disablement, the `X–Y of Z` label) is the one piece of `app.js` logic with
more than a couple of branches).

**Verifying a web change without a harness.** Verification is against the assets the *running server*
returns, not the files on disk: `internal/web` is `go:embed`-ed
([internal/web/embed.go:10](../../internal/web/embed.go)), so a binary built before the edit keeps
serving the old assets. The check is therefore three steps, in order — `node --check
internal/web/app.js` for syntax, a request for `/app.js` or `/style.css` asserting the new markup is
present in what is served, then a manual eyeball for anything interactive (hover a badge, click a
session row). No automated check covers the interaction; markup presence is not behaviour.

Because that third step is manual and the pager's logic is not markup, GI-15 also drove it from a
throwaway Node script that **extracts `renderPager` out of the shipped `app.js`** (rather than
restating it) and runs it against stub containers — catching, for instance, a page past the end
rendering a backwards `201-120 of 120` range. Such a script is deliberately not committed: the
no-JS-harness convention above still holds, and the script was scratch verification for one change,
not a suite. Under real timers the same script can also exercise the debounce and generation-counter
patterns, which is the only way to see a last-issued-wins guard actually discard a slow earlier
response.

## Coverage

**Explicitly covered, by design:**

- **The TTFB no-buffering guarantee** — `TestNoBufferingSSE`
  ([internal/proxy/proxy_test.go:139](../../internal/proxy/proxy_test.go)): a fake upstream slow-streams
  SSE and the test asserts the client sees the first event before upstream sends its last. Called out
  in [CLAUDE.md](../../CLAUDE.md) as "the hard gate" — a buffered stream still returns correct bytes,
  just late, so no other test would catch that regression.
- Byte-identity of proxied bodies (streaming and non-streaming) —
  `TestByteIdentityNonStreaming`/`TestByteIdentityStreaming`
  ([internal/proxy/proxy_test.go:57,102](../../internal/proxy/proxy_test.go)).
- Header redaction end-to-end — `TestRedactionIntegration`
  ([internal/proxy/proxy_test.go:331](../../internal/proxy/proxy_test.go)).
- Sink-full backpressure never delays the client — `TestFullSinkDoesNotDelayClient`
  ([internal/proxy/proxy_test.go:455](../../internal/proxy/proxy_test.go)).
- Batched-flush transactional insert + its SSE glue — `TestPublishingStoreInsertRequests`
  ([internal/api/api_test.go](../../internal/api/api_test.go)).
- Doctor's live-stats check, both server-up and server-down paths —
  `TestDoctorReportsLiveStatsFromRunningServer`, `TestDoctorWarnsWhenServerNotRunning`
  ([internal/cli/cli_test.go](../../internal/cli/cli_test.go)).
- Doctor's `provider_hooks` check (whether the plugin hooks recognize this route) — every
  predicate-clause combination, every no-plugin file state (missing/unreadable/malformed
  `settings.json` or `.deepseek-env.json`), and `claudeConfigDir`'s
  `CLAUDE_CONFIG_DIR` > `USERPROFILE` > `HOME` resolver precedence, isolated per test via
  `t.Setenv` rather than the real machine's config directory
  ([internal/cli/cli_test.go](../../internal/cli/cli_test.go)).
- Cost has a time dimension. `Calendar.IsPeak`
  ([internal/pricing/calendar.go](../../internal/pricing/calendar.go)) decides peak vs off-peak
  from a call's UTC timestamp and the two configured date sets; its precedence rules, its
  UTC-keying boundary, its malformed-input rejections, and the day it calls covered are in
  [internal/pricing/calendar_test.go](../../internal/pricing/calendar_test.go) —
  `TestZeroCalendarMatchesTheOldRule` is the load-bearing one, sweeping a week against a frozen
  copy of the pre-GI-24 window-and-weekend rule, which is what lets every `Calendar{}`-passing
  case elsewhere still mean what it says. `Compute`'s own behaviour is in
  [internal/pricing/pricing_test.go](../../internal/pricing/pricing_test.go): it multiplies each
  category's rate by two before summing/rounding rather than rounding the off-peak total and
  doubling it (`TestComputePeakRoundsSumNotTotal`), and a configured holiday prices at exactly 1x
  (`TestComputeHolidayIsOffPeak`). The `peak_pricing` warning that surfaces this on a request row
  is covered in [internal/analyze/analyze_test.go](../../internal/analyze/analyze_test.go), and
  the per-session rollup that counts those calls in
  [internal/api/api_test.go](../../internal/api/api_test.go).
- Store's single-writer discipline under concurrency —
  `TestConcurrentInsertsSerialize` ([internal/store/store_test.go](../../internal/store/store_test.go)),
  which runs unconditionally (no `//go:build race` tag). The race-on/race-off test pair
  ([internal/store/race_on_test.go](../../internal/store/race_on_test.go),
  [internal/store/race_off_test.go](../../internal/store/race_off_test.go)) is a smaller, differently-scoped
  thing: it only relaxes a performance test's timing budget under `go test -race`'s instrumentation
  overhead, not a concurrency-correctness test itself.

**Known gaps:** none explicitly flagged in code (`TODO`/`FIXME`) as untested at review time; the
`security-and-permissions.md` file notes one open question (body-content secret scanning) that has no
corresponding test either way.

## CI gates

Two GitHub Actions workflows gate merges, but **neither runs `go build`, `go test`, or `go vet`** —
CI here enforces process (branch/commit hygiene), not code correctness
([CLAUDE.md](../../CLAUDE.md): "Neither runs tests — tests are not a CI gate").

| Workflow | Trigger | Enforces | Evidence |
|---|---|---|---|
| `branch-guard.yml` | PR opened/synchronized against `main` | PR must come from a `GI-<n>-<slug>` branch whose issue the body closes; title starts with a real `GI#<n>` issue; body has a closing keyword; every non-merge commit is prefixed with an issue the PR body closes | [.github/workflows/branch-guard.yml](../../.github/workflows/branch-guard.yml) |
| `main-guard.yml` | push to `main` | Verifies the pushed commit landed via a merged `GI-<n>-…` → `main` PR (checks the commit's associated PRs via the GitHub API); if not, force-resets `main` back to the prior commit and opens an issue tagging whoever pushed it | [.github/workflows/main-guard.yml](../../.github/workflows/main-guard.yml) |

Local, pre-push gates (opt-in via `git config core.hooksPath .githooks`):

- [.githooks/commit-msg](../../.githooks/commit-msg) — rejects a commit whose first line doesn't
  match `^GI#[0-9]+`.
- [.githooks/pre-commit](../../.githooks/pre-commit) — delegates to a machine-wide secret scan
  installed via a sibling `agentic-ai-artifacts` checkout; **refuses every commit** until that scan
  is installed, rather than silently skipping it.

Because correctness is never CI-gated, `go build ./..`, `go test ./...`, and `go vet ./...` (see
[build-and-run.md](build-and-run.md)) are the developer's own responsibility before every PR.
