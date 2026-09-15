[← INDEX](INDEX.md)

# Conventions

## Naming & layout

- One `internal/<domain>` package per architectural concern (`proxy`, `sink`, `parse`, `consumer`,
  `store`, `session`, `pricing`, `analyze`, `api`, `web`, `cli`, `replay`, `config`) — see
  [architecture.md](architecture.md)'s component table. `cmd/lens/main.go` is a thin dispatch table
  only; it contains no logic of its own.
- Ticket references appear in code comments as `br-GI-1-NN` (bead id), e.g. "br-GI-1-07's grouped
  flush" — comments trace *why*, matching [CLAUDE.md](../../CLAUDE.md)'s ticket-prefix convention.
- Deliberate simplifications are marked inline with a `ponytail:` comment naming the ceiling and the
  upgrade path, e.g. the session heuristic
  ([internal/session/session.go:12-28](../../internal/session/session.go)), the flat (non-TOML)
  config/price file parser
  ([internal/config/config.go:140-142](../../internal/config/config.go)), and the replay row-poll
  ([internal/api/api.go:465-469](../../internal/api/api.go)).

## Error handling

- **"Fail open" is the project-wide rule** ([CLAUDE.md](../../CLAUDE.md)): the observer must never
  break the user's coding session. Applied concretely as: the proxy's hot path never imports `store`
  or `analyze`; the consumer recovers every panic per-call and per-analyzer rather than propagating
  ([internal/consumer/consumer.go:293-533](../../internal/consumer/consumer.go)); an unreadable price
  file degrades to "unpriced" instead of failing the call
  ([internal/consumer/consumer.go:374-388](../../internal/consumer/consumer.go)); a session-lookup
  store error degrades to "no session" instead of a wrong grouping or a failed call
  ([internal/session/session.go:88-92](../../internal/session/session.go)).
- **Errors wrap with `%w`** and a `"<package>: <verb>: "` prefix throughout (e.g.
  `"store: insert request: %w"`, [internal/store/store.go](../../internal/store/store.go);
  `"proxy: invalid upstream URL %q: %w"`, [internal/proxy/proxy.go:32](../../internal/proxy/proxy.go)).
- **Replay is the one exception to fail-open**: it is a billable write path, so it fails closed by
  default (`--replay` opt-in) and is rejected before any I/O when its Origin/Host guard fails — see
  [security-and-permissions.md](security-and-permissions.md).

## Dependency injection / composition

- **No DI framework.** Every dependency is a narrow, hand-written interface passed explicitly through
  constructors (`New(...)` functions), satisfied structurally (Go duck typing) rather than declared.
  Examples: `consumer.Store` (2 methods a full `*store.Store` satisfies automatically,
  [internal/consumer/consumer.go:64-70](../../internal/consumer/consumer.go)), `consumer.PriceTable`,
  `consumer.Analyzer`, `consumer.SessionResolver`/`SessionAggregator`
  ([internal/consumer/analyzer.go](../../internal/consumer/analyzer.go)), `api.Store`
  ([internal/api/api.go:29-39](../../internal/api/api.go)).
- **Optional capability interfaces**: a store gains batched writes by implementing `batchInserter`
  (`InsertRequests`) — a type assertion (`c.store.(batchInserter)`) is checked at call time rather
  than being part of the required `Store` interface, so a minimal test fake still compiles
  ([internal/consumer/consumer.go:72-80](../../internal/consumer/consumer.go)).
- **Decorators over inheritance**: `PublishingStore` wraps `*store.Store` to add SSE publication
  without changing `consumer.go` or `store.go`
  ([internal/api/publishing_store.go](../../internal/api/publishing_store.go)) — chosen explicitly
  because the wrapped files were outside that bead's file list.
- **`lens serve` is the composition root**: every concrete implementation is wired together once, in
  [internal/cli/serve.go:34-151](../../internal/cli/serve.go). No other file constructs the full
  graph.

## Logging & observability

- Stdlib `log` package only (`log.Printf`), no structured/leveled logging library. Runtime health is
  exposed instead through counters: `sink.Stats()` (accepted/dropped) and `consumer.Stats()`
  (processed/failed/flushes/last-write-at), both safe for concurrent reads
  ([internal/sink/sink.go:90-93](../../internal/sink/sink.go),
  [internal/consumer/consumer.go:82-145](../../internal/consumer/consumer.go)) — surfaced via
  `GET /api/health` and `lens doctor`'s `live_stats` check
  ([internal/cli/doctor.go](../../internal/cli/doctor.go)).
- `lens doctor` is the project's primary diagnostic surface: prints effective config plus a
  PASS/WARN/FAIL check list, exits non-zero only on FAIL
  ([internal/cli/doctor.go:44-77](../../internal/cli/doctor.go)).

## Testing conventions

- Every `internal/*` package (except `web`, which has no logic) has a `_test.go` sibling; see
  [testing-and-quality.md](testing-and-quality.md) for the full inventory and the hard-gate TTFB test.
- Tests assert against real behavior, not mocks, where practical: `internal/store` tests run against
  a real temp-file SQLite database; `internal/api` tests spin up real `httptest` servers.
- A few tests enforce **documentation stays true to code**: `internal/analyze/readme_test.go` asserts
  every `KindInfo.Description` string appears verbatim in the README's warning table — the single
  source of truth is the Go code, not the prose.
- Interface-based fakes (not mocking libraries) cover failure injection, e.g. a `Store` fake that
  always errors, to test the consumer's containment logic without a real database.

## Formatting / lint

- Standard `gofmt`/`go vet` discipline; no `.golangci.yml` or custom linter config found in the repo
  — `go vet ./...` is the documented check (see [build-and-run.md](build-and-run.md)).
- Comment wrap width ~76 columns per [CLAUDE.md](../../CLAUDE.md)'s commit-message convention,
  visibly followed in code comments throughout.

## Repository governance (commits, branches, PRs)

Enforced by tooling, not just documentation — see [CLAUDE.md](../../CLAUDE.md) for the full
convention and [testing-and-quality.md](testing-and-quality.md) for the CI gates:

- `git config core.hooksPath .githooks` (one-time per clone) arms
  [.githooks/commit-msg](../../.githooks/commit-msg) (rejects any commit not starting with
  `GI#<n>`) and [.githooks/pre-commit](../../.githooks/pre-commit) (delegates to a machine-wide
  secret scan; refuses every commit until that scan is installed).
- Commit subject format: `GI#<n> <type>: <lowercase summary> (br-GI-<n>-<NN>)`, `<type>` ∈
  `feat`/`fix`/`docs`/`chore`/`plan`/`beads`/`review`.
- Branches: `GI-<n>-<kebab-slug>` cut from `develop`. PRs into `main` must come from `develop` with a
  `GI#<n>`-prefixed title and a closing keyword in the body —
  [.github/workflows/branch-guard.yml](../../.github/workflows/branch-guard.yml).
