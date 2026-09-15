# DeepSeek Lens — Context Docs

deepseek-lens (`lens`) is a local observability proxy for DeepSeek API traffic: one Go binary that
sits in front of DeepSeek's Anthropic-compatible endpoint, streams every request/response through
unmodified, and records what was sent, what came back, what it cost, and which Anthropic-only
request parameters DeepSeek silently ignored, rewrote, or dropped. It is a single-user, loopback-only
developer tool — no hosted deployment, no multi-user auth, one local SQLite file. See
[README.md](../../README.md) for the user-facing quick start and
[CLAUDE.md](../../CLAUDE.md) for the architecture invariants that constrain every change to this
repo.

## Stack inventory

| Unit | Language/Runtime | Framework | Role | Evidence |
|---|---|---|---|---|
| `lens` | Go 1.24 | stdlib `net/http` + `modernc.org/sqlite` (pure Go SQLite) | The single buildable/deployable binary: proxy + dashboard + CLI | [go.mod](../../go.mod), [cmd/lens/main.go](../../cmd/lens/main.go) |

No other buildable units exist — no separate frontend build (the dashboard is vanilla embedded
HTML/CSS/JS with no `package.json`), no infra-as-code, no mobile app.

## Modules

| File | When to open it | Why it's here (core / trigger) |
|---|---|---|
| [architecture.md](architecture.md) | Understanding the system's shape: components, data flow, the hot-path/cold-path split | core |
| [conventions.md](conventions.md) | Before writing code: naming, error handling, DI style, dashboard/UI rules, testing, and commit/branch/PR governance actually used here | core |
| [build-and-run.md](build-and-run.md) | Building, testing, or running `lens` locally; env vars and config precedence | core |
| [glossary.md](glossary.md) | Decoding project-specific terms (session, sink, prefix hash, bead, fail open, ...) | core |
| [data-model.md](data-model.md) | Working with the SQLite schema (`requests`/`sessions`/`warnings`) or the Go types that mirror it | conditional: `internal/store/schema.sql` + Go structs found |
| [api-surface.md](api-surface.md) | Adding/changing a dashboard route or understanding what the CLI/UI can call | conditional: `net/http.ServeMux` routes found in `internal/api/api.go` |
| [workflows.md](workflows.md) | Tracing an end-to-end flow: live capture, the consumer pipeline, session resolution, or replay | conditional: multi-component flows spanning proxy → sink → consumer → store → api |
| [cli-and-tooling.md](cli-and-tooling.md) | Adding/changing a `lens` subcommand or looking up its flags | conditional: hand-rolled CLI dispatch table + per-command `flag.FlagSet`s found |
| [security-and-permissions.md](security-and-permissions.md) | Touching header redaction, loopback binding, or the replay write path | conditional: real security-relevant code (redaction, origin allowlist) explicitly called out as high-stakes in `CLAUDE.md` |
| [testing-and-quality.md](testing-and-quality.md) | Understanding what's tested (esp. the TTFB hard gate), and what actually gates a merge in CI | conditional: `_test.go` files + two GitHub Actions workflows found |

No `mobile-app.md`, `frontend-web.md`, `infra-and-deploy.md`, `ml-data-pipeline.md`,
`integrations-and-external-services.md`, `data-privacy-and-compliance.md`, or `decisions/` — no
trigger signal for any of them (no mobile project, no JS framework/`package.json`, no deploy
pipeline/IaC beyond CI merge-governance already covered in `conventions.md`, no regulated-data
domain, no external SaaS integrations beyond the DeepSeek upstream and embedded SQLite already
covered in `architecture.md`, and no genuine architectural fork found in code/history to warrant an
ADR).

## Grounding rules for agents

- **Discover, don't assume.** Every non-trivial claim in these docs is cited to a real file (and
  usually a line range). If you need to extend a claim, verify it against current code first — these
  docs can drift.
- **`⚠️ ASSUMPTION` / `❓ UNVERIFIED`** markers appear where evidence was ambiguous or a judgment call
  was made without code confirming it either way. Treat those as lower-confidence than everything
  else here.
- **No duplication**: each fact lives in exactly one module file; other files link to it rather than
  restating it.
- **This doc set can go stale.** Prefer `git log`/reading the code directly over this index for
  anything about *recent* changes; refresh this doc set (same skill, refresh mode) after a
  significant architectural change.

## Last generated / refreshed

2026-09-16, REFRESH mode, scoped to the GI-9 dashboard-affordance fix in
[PR #10](https://github.com/abhisheksarkar30/deepseek-lens/pull/10) — the accessible-name rule for
warning badges and how a web change is verified without a harness (`conventions.md`,
`testing-and-quality.md`). Documentation was written against the `GI-9-fix-warning-badge-affordance`
branch, which was unmerged at refresh time: on `develop` the `⚠` spans are still inlined and
`warnBadge` does not exist.

Previously: 2026-09-15, scoped to the GI-4 peak-pricing/hook-integration story (peak-aware
`pricing.Compute`, the `peak_pricing` warning kind, and doctor's `provider_hooks` check).
Originally generated 2026-09-15, FRESH mode, whole-repository scope.
