# GI-1 — deepseek-lens v1

<!-- version=4 status=converged -->

Ticket: [issue #1](../../../issues/1) · Branch: `GI-1-deepseek-lens-v1` (off `origin/develop`) · Spec: `docs/superpowers/specs/2026-09-14-deepseek-lens-design.md`

_Converged 2026-09-14 — round-4 review clean, no further findings._

---

## What changes

A new Go module at `D:\github\deepseek-lens`. Nothing pre-exists; every file below is created.

```
deepseek-lens/
├── go.mod, README.md, .gitignore
├── cmd/lens/main.go                     subcommand dispatch
├── internal/
│   ├── config/                          flags + file + env, defaults, validation
│   ├── sink/                            bounded channel, drop counter — the non-blocking guarantee
│   ├── proxy/                           reverse proxy, tee, fail-open
│   ├── parse/                           incremental SSE + non-stream JSON, request metadata
│   ├── store/                           SQLite schema, single writer, read connection
│   ├── consumer/                        sink → parse → analyze → store wiring
│   ├── analyze/                         dropped-parameter rules (flagship)
│   ├── pricing/                         price table, cost arithmetic
│   ├── session/                          conversation grouping
│   ├── api/                             dashboard JSON API + SSE push
│   ├── cli/                             subcommand implementations (br-GI-1-08)
│   ├── replay/                          body-edit + re-issue logic (br-GI-1-13)
│   └── web/                             go:embed'd index.html, app.js, style.css
└── docs/, .beads/
```

## Why — mapping to requirements

| Requirement | Delivered by |
|---|---|
| Least latency | `internal/proxy` + `internal/sink` (invariants 1–3), proven by the TTFB test |
| See calls made | `internal/store` `requests` table + `lens ls` / `lens show` |
| See responses received | response body capture (full, capped) + request detail view |
| Tokens used | `internal/parse` incremental SSE extraction |
| Whatever else possible | `internal/analyze` dropped-param detection, cost accounting, session grouping, replay |
| CLI | `cmd/lens` subcommands |
| Dashboard | `internal/api` + `internal/web` |

## Architecture

One process, two `http.Server`s on separate listeners. The proxy listener holds the hot path and
does no parsing; it tees into `sink`. The consumer goroutine drains `sink`, parses, analyzes, and
writes to SQLite. The dashboard listener reads SQLite and pushes live updates over SSE.

Data flow: `client → proxy → (tee) → sink → consumer → {parse, analyze} → store → api → web`.

**Cold-path pipeline order (fixed, per call).** The consumer processes a captured call in this
order; the feature beads plug into named seams at fixed points rather than reordering the pipeline:

1. `ExtractMeta` / `ExtractUsage` → `Meta`, `Usage`
2. **Resolve session** — the `SessionResolver` sets `req.SessionID`
3. **Compute cost** — the cost step sets `req.CostUSD` / `req.CostSource`
4. `InsertRequest(ctx, req)` → `id`
5. **Run analyzers** — `Analyzer`-registered rules return `[]store.Warning`, written via
   `InsertWarnings(ctx, id, warnings)`

Session resolution and costing populate columns on the row itself, so they must run **before**
insert; warning analyzers attach to the row by id, so they run **after** insert. Cost is a separate
pre-insert step, not a `[]Warning`-returning analyzer. The settled `Analyzer` seam is
`Analyze(meta parse.Meta, usage parse.Usage, req *store.Request) []store.Warning` — `req` carries
the response body the rules need.

**Single writer, always.** The SQLite writer connection is owned by the consumer goroutine and
nothing else. Replay does not open a second writer: `lens replay <id>` replays by calling the
running server's `POST /api/requests/{id}/replay`, and the server records the replay row through
the same consumer writer. `lens replay` therefore requires `lens serve` to be running (the CLI is a
thin client for the send path; `--dump` is local and sends nothing). This is what keeps SQLite
locking a non-issue.

Dependency direction is strictly one-way (importer → imported): both the dashboard API and the
consumer import `store` — never the reverse; the consumer additionally imports `{sink, parse,
analyze, session, pricing}`; the CLI layer imports `api`, `consumer`, and `replay`; the proxy
depends only on `sink` and `config`. This keeps the hot path's dependency graph tiny and
independently testable.

## Infrastructure

- No test CI and no containers in v1. A new repo does not need a test pipeline to prove itself.
  The branch-flow guards (`branch-guard.yml`, `main-guard.yml`) and the local `commit-msg` hook do
  exist — they enforce the `develop` → `main` PR flow and the `GI#<n>` prefix, not test execution.
- `.gitignore`: the SQLite file, `*.db`, `*.db-wal`, `*.db-shm`, the config file, and `/**/*review*/`
  (cross-review artifacts are working state and are never committed).
- No network egress besides the configured upstream.

## Test strategy

Ordered by how much they actually protect the product:

1. **TTFB / no-buffering test** (proxy). Fake upstream, slow-streamed SSE; assert the client sees the
   first event before upstream sends its last. *This is the test that keeps invariant 3 true.*
2. **Byte-identity test** (proxy). Client body is unchanged end to end, including with a tee active.
3. **Split-chunk SSE test** (parse). An event split across two chunk boundaries parses correctly.
4. **Rule table tests** (analyze). Every rule, positive and negative.
5. **Non-blocking sink test** (sink). With the consumer stopped, N writes complete without blocking
   and the drop counter reaches N.
6. **Store round-trip test** (store). Write, read back, WAL mode confirmed, key redaction verified.
7. **Session key tests** — same prefix within window groups; beyond window splits; header override wins.
8. **Cost arithmetic test** — with a seeded price table; asserts the unpopulated-table case is
   labelled, not silently zero.
9. **E2E (opt-in)** — one real minimal request.

## Risk areas

| Risk | Mitigation |
|---|---|
| Buffering the stream to count tokens | Invariant 3 + the TTFB test as a hard gate |
| Hot-path stall on a full observer queue | Bounded channel, drop counter, sink test 5 |
| `x-api-key` persisted | Redaction before insert; asserted in store test 6 |
| Binding non-loopback by default | `--allow-remote` required; `doctor` prints the effective bind |
| User's coding session dies with the proxy | Fail-open, `serve --no-capture`, `doctor` |
| Price table unpopulated → misleading costs | Provenance labelled; separate bead deliverable |
| Session heuristic mis-grouping | Documented `ponytail:` ceiling; `x-lens-session` override |

## Self-review

**As a senior engineer.** The architecture is small on purpose. The one thing I would normally push
back on is the absence of a migration framework — accepted for v1 because the schema is created
whole (including cancellable columns), and rejected the moment a shipped column changes. The
strictly one-way dependency graph is what makes the hot path auditable; if `proxy` ever imports
`analyze` or `store`, that is the signal the design has eroded.

**As a QA engineer.** The failure modes that matter are all *silent*: a buffered stream still returns
correct bytes, just late; a stalled sink still returns correct bytes, just late; a wrong token count
still renders. Hence tests 1, 3, and 5 assert timing and intermediate state, not just final output.
Gaps accepted: no test for DeepSeek changing its model map (config-driven, so recoverable without a
code change), and no concurrency stress test in v1.

**As a security engineer.** The highest-value asset here is not a credential — it is the *content*:
every prompt, every file the agent read, every line it wrote, concentrated into one file. Loopback
binding and redaction are therefore load-bearing defaults, not conveniences. Accepted risks, stated:
`--allow-remote` is a footgun (mitigated by being off by default and surfaced by `doctor`); the
dashboard has no auth — acceptable while loopback-only for a read-only observer, but replay is the
exception: it is a billable write path, so `POST /api/requests/{id}/replay` is off by default (turned
on explicitly with `--replay`) and guarded by a strict `Origin`/`Host` allowlist — reject when
`Origin` is present and is not the dashboard's own origin, and reject when `Host` is not loopback.
The guard is deliberately credentialless: it kills the original threats (DNS rebinding and
cross-origin POST) with no secret, a CLI request sends no `Origin` at all so the CLI-authentication
question disappears, and any local process that could forge past the guard can already read the
SQLite file directly — so the endpoint grants no privilege over what is already on disk. If defense
against local (non-browser) processes is ever wanted, add a shared secret then; that is the
documented upgrade path. The replay posture — off by default, enabled via `--replay`, guarded by
`Origin`/`Host` — is surfaced by `doctor` (br-GI-1-08) and the README (br-GI-1-14); dashboard auth remains
a prerequisite for any hosted deployment. Full body storage deliberately retains sensitive content locally under a
documented policy with a configurable switch.

---

## Bead sequence

Core first (br-GI-1-01–br-GI-1-09), producing a working, dogfoodable tool. Features after (br-GI-1-10–br-GI-1-13), each
independently verifiable. Bead br-GI-1-14 closes with the README and a recorded end-to-end acceptance run
against real DeepSeek traffic, including a measured latency delta. See `.beads/GI-1/` for the full text
of each.

| Bead | What it delivers | Category |
|---|---|---|
| br-GI-1-01 | Repo scaffold, module init, config, `doctor` | chore |
| br-GI-1-02 | Non-blocking capture sink | feat |
| br-GI-1-03 | Transparent proxy core | feat |
| br-GI-1-04 | SSE and response parsing | feat |
| br-GI-1-05 | Request metadata extraction | feat |
| br-GI-1-06 | SQLite store and schema | feat |
| br-GI-1-07 | Consumer wiring (sink → parse → store) | feat |
| br-GI-1-08 | CLI subcommands | feat |
| br-GI-1-09 | Dashboard and JSON API | feat |
| br-GI-1-10 | Dropped-parameter detection | feat |
| br-GI-1-11 | Token and cost accounting | feat |
| br-GI-1-12 | Session grouping | feat |
| br-GI-1-13 | Request replay | feat |
| br-GI-1-14 | README and end-to-end acceptance | docs |

Bead count: 14. Dependency graph is a DAG with a single long spine
(`br-GI-1-01 → br-GI-1-02 → br-GI-1-03 → br-GI-1-07 → br-GI-1-08 → br-GI-1-09`) and four feature
beads hanging off `br-GI-1-09`, converging on `br-GI-1-14`.
Bead br-GI-1-07 defines the pipeline and its seams; br-GI-1-10–br-GI-1-13 plug into them at fixed points rather than
rewriting it — but they are not uniformly "additive", because the seams differ in kind and timing.
Bead br-GI-1-10 registers a post-insert `Analyzer`; br-GI-1-12 injects a pre-insert `SessionResolver`; br-GI-1-11
supplies a pre-insert cost step (a `Coster`-style step, separate from the warning analyzers — it
writes `cost_usd` / `cost_source` on the row, it does not return `[]Warning`); br-GI-1-13 adds the
replay path and its API endpoint. These seams must be settled before br-GI-1-07 lands — br-GI-1-10's entry
point conforms to `Analyze(meta parse.Meta, usage parse.Usage, req *store.Request) []store.Warning`.

## Decision log

| Decision | Choice | Alternative rejected |
|---|---|---|
| Stack | Go | TS/Bun (runtime dep, fiddlier non-blocking SSE), Rust (build cost), Python (hot path) |
| Shape | Single binary, embedded dashboard | Sidecar + Postgres (operational surface for one user) |
| Build vs buy | Build | LiteLLM / claude-code-router (no DeepSeek-specific detection; Python hot path) |
| Sequencing | Core first, then features | Vertical slices; detection-first |
| Body storage | Full, 256KB cap | Truncated; metadata-only |
| Migrations | None in v1 | Framework from day one |
| Replay write path | `lens replay` calls the running server's replay API — single writer preserved | CLI opens its own DB writer (breaks the single-writer discipline) |
| CLI | stdlib `flag` | `cobra` unless ergonomics demand it |

## Acceptance (br-GI-1-14, 2026-09-15)

The ten-step run in `.beads/GI-1/br-GI-1-14-docs-acceptance.md` was executed against the real
`https://api.deepseek.com/anthropic` endpoint and recorded verbatim in `docs/acceptance.md`.
Everything below is measured, not projected.

- **Steps 1–9 pass.** A real Claude Code 2.1.268 client and `curl` were proxied, captured, priced,
  grouped, warned on, and replayed through the run.
- **Model mapping is confirmed, not assumed.** `claude-sonnet-4-5` was served as `deepseek-flash`
  and the response's own `usage.model` agreed with the shipped model map — no `model_mapping_drift`,
  so risk 6 did not materialise on this traffic.
- **The dropped-parameter engine fired on unmodified client traffic.** The CLI's own calls raised
  `cache_control_ignored` (three sites), `budget_tokens_ignored` (31999), and `header_ignored`,
  each on a `200`.
- **Step 10 measured delta: +12.5 ms added first-byte latency** (warm medians, five paired runs,
  direct 192.1 ms vs proxied 204.6 ms), against a 50 ms budget. The cold comparison shows the proxy
  *faster* by 78.1 ms, which is connection pooling rather than proxy speed and is written up as such.
  Upstream jitter (~±30 ms) is larger than the delta, so the figure is a recorded number to regress
  against, not a precise instrument reading.
- **Three open findings, all written up with causes**: a replay against a key-protected upstream is
  answered `401` because lens stores `[redacted]` rather than a credential; `export
  ANTHROPIC_BASE_URL=...` alone does not move a Claude Code install whose `settings.json` carries its
  own `env` block; and graceful SIGINT shutdown cannot be raised against a native Windows process
  from Git Bash, so the shutdown drain was not exercised. A fourth, cosmetic: Claude Code's
  `HEAD /api/hello` probe is proxied and stored as a row.
- **Not verified**: browser rendering of the dashboard (no browser here — step 8 verified HTTP, the
  JSON API, and a live SSE event at 0.267 s, and claims nothing about pixels), the Cline path, and
  DeepSeek's actual per-token prices (which is why the shipped table remains empty).

## Change History

### v2 — round 1 review (2026-09-14)
- **F1.1** — settled the plugin seam instead of hand-waving "strictly additive". Named the
  `Analyzer` signature and split cost out as a separate pre-insert `Coster`-style step. Corrected
  the bead-sequence note and the Architecture section.
- **F1.2** — stated the cold-path pipeline order explicitly: session resolution and cost run
  **before** `InsertRequest`; warning analyzers run **after** (they need the row id).
- **F1.5** — the security self-review no longer blesses a blanket "no auth" now that replay is a
  billable write path; the replay endpoint required a per-process secret and an `Origin`/`Host`
  allowlist and is opt-in. **(Superseded — the per-process secret was dropped in v3; see the v3
  F2.3 entry. The `Origin`/`Host` allowlist and the opt-in posture stand.)**
- **F1.6** — stated the replay DB write path: `lens replay` calls the running server's API, so the
  consumer stays the single writer; recorded the decision and its rejected alternative.
- **F1.10** — added `internal/cli` and `internal/replay` to the "What changes" tree; corrected the
  dependency-direction sentence to the real import direction (consumer imports store).

### v3 — round 2 review (2026-09-14)
- **F2.3 (plan-text)** — dropped the per-process secret from the replay guard (conductor override);
  the endpoint is guarded by `Origin`/`Host` alone. Recorded the deliberate rationale in the plan
  (DNS-rebinding / cross-origin POST killed with no credential; a CLI request sends no `Origin`, so
  the CLI-auth question disappears; a local process able to forge past the guard can already read the
  SQLite file) and the documented upgrade path (add a shared secret only if local-process defense is
  ever wanted). The matching bead text (`br-GI-1-13`, `br-GI-1-09`) was de-secreted in the same round — both are
  now `Origin`/`Host`-only, agreeing with the plan.
- **F2.4 (plan-text)** — made the surface claim precise: named the delivering artifacts (`doctor`,
  owned by br-GI-1-08; the README, owned by br-GI-1-14) and aligned the surfaced wording with the
  no-secret guard ("off by default, enabled via `--replay`, guarded by `Origin`/`Host`", not "enter a
  secret"). The bead edits that make it true landed in the same round (`br-GI-1-08`, `br-GI-1-14`).
- **F2.1 / F2.2 / F2.5 / F2.6 / F2.7** — no plan-text component in this round's scope; each fix
  lands in a bead (`br-GI-1-01`/`br-GI-1-08`, `br-GI-1-02`/`br-GI-1-03`, `br-GI-1-07`, `br-GI-1-10`/spec, `br-GI-1-11`) handled by the
  separate bead/spec writer.

### v4 — round 3 review (2026-09-14)
- **F3.2 (plan-text)** — removed the stale Change History statements the round-2 edit left behind.
  The v3 F2.3 bullet no longer claims the beads "still carry the secret" (they were de-secreted in
  round 2 — `br-GI-1-13`, `br-GI-1-09` are `Origin`/`Host`-only); the v3 F2.4 bullet no longer calls the bead
  edits a future job of "a separate writer" (they landed in `br-GI-1-08`, `br-GI-1-14`). The v2 F1.5 bullet is
  marked superseded rather than rewritten: it now reads in the past tense with a pointer to the v3
  F2.3 entry that dropped the per-process secret, so the plan no longer contains a live "requires a
  per-process secret" requirement while history stays honest.

### v4 — converged (2026-09-14)
- **Round-4 review returned `VERDICT: NO_FURTHER_FINDINGS`.** The round-4 reviewer re-read the plan
  (v4), all 14 beads, and the spec, re-verified both round-3 findings as landed, and found no new
  inconsistency. The cross-review loop converged; no content change was made — this entry records the
  status marker only. The version number stays 4 (a status marker, not a content revision).

### v5 — acceptance recorded (2026-09-15)
- Added the `## Acceptance` section: the measured outcome of br-GI-1-14's ten-step run against the
  real DeepSeek endpoint, with the step-10 latency delta recorded as a number and the run's findings
  named. No plan content changed; the run confirmed the plan's assumptions (risk 6 in particular)
  rather than revising them.
