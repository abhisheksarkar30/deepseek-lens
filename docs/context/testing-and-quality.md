[← INDEX](INDEX.md)

# Testing & Quality

## Test frameworks in use

| Layer | Framework | Location | Evidence |
|---|---|---|---|
| Unit / integration | Go stdlib `testing` (no third-party test framework or assertion library) | one `_test.go` per package, all `internal/*` except `web` | e.g. [internal/proxy/proxy_test.go](../../internal/proxy/proxy_test.go), [internal/consumer/consumer_test.go](../../internal/consumer/consumer_test.go) |
| Store race coverage | Split race-on/race-off test files, run under `go test -race` | [internal/store/race_on_test.go](../../internal/store/race_on_test.go), [internal/store/race_off_test.go](../../internal/store/race_off_test.go) | — |
| Documentation-consistency | A test that diffs generated warning descriptions against README prose | [internal/analyze/readme_test.go](../../internal/analyze/readme_test.go) | — |

As of this doc's generation, `go test ./...` is green across every package with tests (`cmd/lens` and
`internal/web` have none, by design — no logic to test).

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
- Store's single-writer discipline under concurrency — race-on/race-off test pair
  ([internal/store/race_on_test.go](../../internal/store/race_on_test.go)).

**Known gaps:** none explicitly flagged in code (`TODO`/`FIXME`) as untested at review time; the
`security-and-permissions.md` file notes one open question (body-content secret scanning) that has no
corresponding test either way.

## CI gates

Two GitHub Actions workflows gate merges, but **neither runs `go build`, `go test`, or `go vet`** —
CI here enforces process (branch/commit hygiene), not code correctness
([CLAUDE.md](../../CLAUDE.md): "Neither runs tests — tests are not a CI gate").

| Workflow | Trigger | Enforces | Evidence |
|---|---|---|---|
| `branch-guard.yml` | PR opened/synchronized against `main` | PR must come from `develop`; title starts with a real `GI#<n>` issue; body has a closing keyword; every non-merge commit is prefixed with an issue the PR body closes | [.github/workflows/branch-guard.yml](../../.github/workflows/branch-guard.yml) |
| `main-guard.yml` | push to `main` | Verifies the pushed commit landed via a merged `develop` → `main` PR (checks the commit's associated PRs via the GitHub API); if not, force-resets `main` back to the prior commit and opens an issue tagging whoever pushed it | [.github/workflows/main-guard.yml](../../.github/workflows/main-guard.yml) |

Local, pre-push gates (opt-in via `git config core.hooksPath .githooks`):

- [.githooks/commit-msg](../../.githooks/commit-msg) — rejects a commit whose first line doesn't
  match `^GI#[0-9]+`.
- [.githooks/pre-commit](../../.githooks/pre-commit) — delegates to a machine-wide secret scan
  installed via a sibling `agentic-ai-artifacts` checkout; **refuses every commit** until that scan
  is installed, rather than silently skipping it.

Because correctness is never CI-gated, `go build ./..`, `go test ./...`, and `go vet ./...` (see
[build-and-run.md](build-and-run.md)) are the developer's own responsibility before every PR.
