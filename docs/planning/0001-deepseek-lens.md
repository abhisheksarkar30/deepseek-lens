# Plan: deepseek-lens v1

**Ticket:** none (greenfield — no tracker id; beads use sequential ids, no `GI#`/ADO prefix)
**Spec:** `docs/superpowers/specs/2026-09-14-deepseek-lens-design.md`
**Date:** 2026-09-14

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

Dependency direction is strictly one-way (web → api → store → consumer → {parse, analyze, session,
pricing}); the proxy depends only on `sink` and `config`. This keeps the hot path's dependency
graph tiny and independently testable.

## Infrastructure

- No CI, no branch guards, no containers in v1. A new repo does not need a pipeline to prove itself.
- `.gitignore`: the SQLite file, `*.db`, `*.db-wal`, `*.db-shm`, and the config file.
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
dashboard has no auth (acceptable while loopback-only, and explicitly the reason dashboard auth is
a prerequisite for any hosted deployment); full body storage deliberately retains sensitive content
locally under a documented policy with a configurable switch.

---

## Bead sequence

Core first (beads 1–9), producing a working, dogfoodable tool. Features after (beads 10–13), each
independently verifiable. Bead 14 closes with the README and a recorded end-to-end acceptance run
against real DeepSeek traffic, including a measured latency delta. See `.beads/` for the full text
of each.

| # | Bead | Category |
|---|---|---|
| 1 | Repo scaffold, module init, config, `doctor` | chore |
| 2 | Non-blocking capture sink | feat |
| 3 | Transparent proxy core | feat |
| 4 | SSE and response parsing | feat |
| 5 | Request metadata extraction | feat |
| 6 | SQLite store and schema | feat |
| 7 | Consumer wiring (sink → parse → store) | feat |
| 8 | CLI subcommands | feat |
| 9 | Dashboard and JSON API | feat |
| 10 | Dropped-parameter detection | feat |
| 11 | Token and cost accounting | feat |
| 12 | Session grouping | feat |
| 13 | Request replay | feat |
| 14 | README and end-to-end acceptance | docs |

Bead count: 14. Dependency graph is a DAG with a single long spine
(`1 → 2 → 3 → 7 → 8 → 9`) and four feature beads hanging off `9`, converging on `14`.
Beads 10–13 are strictly additive: each registers into interfaces that bead 7 already defines
(`Analyzer`, `SessionResolver`), so none rewrites the pipeline.

## Decision log

| Decision | Choice | Alternative rejected |
|---|---|---|
| Stack | Go | TS/Bun (runtime dep, fiddlier non-blocking SSE), Rust (build cost), Python (hot path) |
| Shape | Single binary, embedded dashboard | Sidecar + Postgres (operational surface for one user) |
| Build vs buy | Build | LiteLLM / claude-code-router (no DeepSeek-specific detection; Python hot path) |
| Sequencing | Core first, then features | Vertical slices; detection-first |
| Body storage | Full, 256KB cap | Truncated; metadata-only |
| Migrations | None in v1 | Framework from day one |
| CLI | stdlib `flag` | `cobra` unless ergonomics demand it |
