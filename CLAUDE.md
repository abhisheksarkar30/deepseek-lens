# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

DeepSeek Lens: a local observability proxy for DeepSeek API traffic. One Go binary, `lens`, sits in
front of the DeepSeek endpoint and captures what was sent, what came back, what it cost, and which
request parameters the API silently dropped. It is a single-user developer tool — no hosted
deployment, no multi-user auth, loopback-only by default.

See `docs/planning/GI-1-deepseek-lens-v1.md` for the converged plan (v4, 4 review rounds) and
`docs/superpowers/specs/2026-09-14-deepseek-lens-design.md` for the design spec. The work items are
`.beads/GI-1/br-GI-1-01` … `-14`; each bead names its own file list and outcome definition.

For a categorized, cited map of the codebase (architecture, data model, API surface, workflows,
conventions, build/run, CLI, security, testing) meant for an AI agent picking this repo up cold, see
[docs/context/INDEX.md](docs/context/INDEX.md).

## Setup

One step per clone, with no build system to do it for you:

```
git config core.hooksPath .githooks
```

That arms `.githooks/commit-msg` (rejects any commit not starting with `GI#<n>`) and
`.githooks/pre-commit` (delegates to the machine-wide secret scan at
`$XDG_CONFIG_HOME/git/hooks/pre-commit`, and **refuses every commit** until that scan is installed —
`bash git-hooks/install.sh` from your `agentic-ai-artifacts` checkout).

## Commands

```
go build ./...                  # build
go test ./...                   # all tests
go test ./internal/proxy/       # one package
go vet ./...                    # vet
go run ./cmd/lens doctor        # print effective config, bind addresses, warnings
go run ./cmd/lens serve         # proxy + dashboard in one process
```

## Conventions

These are copied from `family-monitor` and are enforced, not advisory.

- **Ticket prefix `GI#<n>`** — `GI` is the GitHub issue number in this repo. The issue is the
  problem statement; the plan is the design. Everything keys off it.
- **Branches** — `GI-<n>-<kebab-slug>`, cut from `develop`. The plan doc, the branch, and the PR
  title all carry the same `GI-<n>-<slug>`.
- **Commit subject** — `GI#<n> <type>: <lowercase summary> (br-GI-<n>-<NN>)`, where `<type>` is one
  of `feat` / `fix` / `docs` / `chore` / `plan` / `beads` / `review`. The body explains *why* the
  change is correct, not what it does — the diff already says what. Wrap at ~76 columns.
- **PR** — title is the same `GI#<n> <type>: <summary>` as the branch's headline commit. Body is
  `## Summary`, `## Verification`, `## Beads`, then `Closes #<n>`. A feature branch PRs into
  `develop`; a separate `develop` → `main` PR promotes it.
- **Commits end with** `Co-Authored-By: Claude Code <noreply@anthropic.com>`; PR bodies end with
  `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.

### Enforcement

`branch-guard.yml` requires every PR into `main` to come from `develop`, with a `GI#<n>` title
naming a real issue, a closing keyword in the body, and every non-merge commit prefixed with an
issue the body closes. `main-guard.yml` force-reverts any commit that reaches `main` outside that
flow. Neither runs tests — tests are not a CI gate.

### Layout

| | |
|---|---|
| Plan | `docs/planning/GI-<n>-<slug>.md` |
| Beads | `.beads/GI-<n>/br-GI-<n>-<NN>-<type>-<slug>.md` |
| Cross-review artifacts | `planning/GI-<n>/review/round-N/` — **gitignored**, never committed |

## Architecture essentials

- **One process, two `http.Server`s on separate listeners.** The proxy listener holds the hot path
  and does no parsing; it tees the bytes into a bounded sink and returns. The consumer goroutine
  drains the sink, parses, analyzes, and writes to SQLite. The dashboard listener reads SQLite and
  pushes SSE.
- **Data flow is one-way for ingest, with one deliberate exception.** Live capture flows
  `client → proxy → (tee) → sink → consumer → {parse, analyze} → store → api → web`, and the proxy
  depends only on `sink` and `config` — if `proxy` ever imports `analyze` or `store`, the design has
  eroded, and that remains the signal to watch. A purge (the startup/scheduled purge, `POST
  /api/purge`, or `lens purge`) is the one exception: it runs `api → store` directly and reads no rows
  first, because deleting is not part of the ingest pipeline.
- **The hot path must never buffer the stream to count tokens.** The TTFB test (fake upstream,
  slow-streamed SSE, assert the client sees the first event before upstream sends its last) is the
  hard gate that keeps this true. A buffered stream still returns correct bytes — just late — so no
  other test would catch it.
- **SQLite has two writers, not one, serialized two different ways.** The consumer goroutine is still
  the only writer for row *ingest*: `lens replay` does not open a second writer for that, it calls the
  running server's `POST /api/requests/{id}/replay` and the server records the row through the same
  consumer writer. A purge is a second, additional writer, not a violation of that discipline —
  **in-process purge** (the scheduled/startup run and `POST /api/purge`) uses the store's own writer
  connection (`SetMaxOpenConns(1)`), so it queues behind ingest rather than racing it, while **`lens
  purge`** is a *separate process* with its own writer connection, serialized **cross-process** by WAL
  plus `busy_timeout(5000)` — the same protocol every other external opener of the file already relies
  on. Do not describe `lens purge` as going through "the same single connection" as the consumer; that
  claim is false for it specifically.
- **Cold-path pipeline order is fixed**: `ExtractMeta`/`ExtractUsage` → resolve session → compute
  cost → `InsertRequest` → run analyzers. Session resolution and costing populate columns on the
  row so they run *before* insert; warning analyzers attach by row id so they run *after*. Feature
  beads plug into these seams at fixed points rather than reordering them.
- **Fail open.** The proxy never makes the user's coding session depend on the observer. The
  dashboard listener carries three write routes — `POST /api/requests/{id}/replay`, `POST
  /api/prices`, and `POST /api/purge` (destructive) — each behind `replayOriginReject`'s
  `Origin`/`Host` allowlist, parameterized by action, with no shared secret. Replay is the only
  billable one and is off by default (`--replay`) on top of that guard — see the plan's security
  self-review for why the credentialless guard is sufficient and what the upgrade path is.
- **`x-api-key` is redacted before insert.** Full bodies *are* stored (256KB cap per body,
  configurable), so the content — every prompt and every file the agent read — is the asset this
  repo is protecting. Loopback binding and redaction are load-bearing defaults, not conveniences.
