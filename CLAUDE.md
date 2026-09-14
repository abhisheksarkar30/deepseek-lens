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
- **Data flow is strictly one-way**: `client → proxy → (tee) → sink → consumer → {parse, analyze}
  → store → api → web`. The proxy depends only on `sink` and `config`. If `proxy` ever imports
  `analyze` or `store`, the design has eroded — that is the signal, not a style nit.
- **The hot path must never buffer the stream to count tokens.** The TTFB test (fake upstream,
  slow-streamed SSE, assert the client sees the first event before upstream sends its last) is the
  hard gate that keeps this true. A buffered stream still returns correct bytes — just late — so no
  other test would catch it.
- **SQLite has exactly one writer: the consumer goroutine.** `lens replay` does not open a second
  writer; it calls the running server's `POST /api/requests/{id}/replay` and the server records the
  row through the same consumer writer. This is what keeps SQLite locking a non-issue.
- **Cold-path pipeline order is fixed**: `ExtractMeta`/`ExtractUsage` → resolve session → compute
  cost → `InsertRequest` → run analyzers. Session resolution and costing populate columns on the
  row so they run *before* insert; warning analyzers attach by row id so they run *after*. Feature
  beads plug into these seams at fixed points rather than reordering them.
- **Fail open.** The proxy never makes the user's coding session depend on the observer. Replay is
  the exception: it is a billable write path, so it is off by default (`--replay`), and guarded by a
  strict `Origin`/`Host` allowlist with no shared secret — see the plan's security self-review for
  why the credentialless guard is sufficient and what the upgrade path is.
- **`x-api-key` is redacted before insert.** Full bodies *are* stored (256KB cap per body,
  configurable), so the content — every prompt and every file the agent read — is the asset this
  repo is protecting. Loopback binding and redaction are load-bearing defaults, not conveniences.
