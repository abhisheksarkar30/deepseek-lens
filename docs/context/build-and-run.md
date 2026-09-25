[← INDEX](INDEX.md)

# Build & Run

Single buildable unit: the `lens` binary, `cmd/lens` (Go module
`github.com/abhisheksarkar30/deepseek-lens`, Go 1.24 — [go.mod](../../go.mod)).

### `lens` (Go binary)

| Task | Command | Source |
|---|---|---|
| Build | `go build ./...` (or `go build -o lens ./cmd/lens`) | [CLAUDE.md](../../CLAUDE.md), [README.md](../../README.md) |
| Test (all) | `go test ./...` | [CLAUDE.md](../../CLAUDE.md) |
| Test (one package) | `go test ./internal/proxy/` | [CLAUDE.md](../../CLAUDE.md) |
| Vet | `go vet ./...` | [CLAUDE.md](../../CLAUDE.md) |
| Run locally (proxy + dashboard) | `go run ./cmd/lens serve` | [CLAUDE.md](../../CLAUDE.md) |
| Diagnose config/health | `go run ./cmd/lens doctor` | [CLAUDE.md](../../CLAUDE.md) |
| Install to `$GOPATH/bin` | `go install ./cmd/lens` | [README.md:19](../../README.md) |
| Deploy | none — single local developer binary, no deploy pipeline | — |

## Environment variables / secrets

All `LENS_*` variables are optional overrides of `internal/config.Config` fields (env beats config
file, flags beat env) — [internal/config/config.go:109-124](../../internal/config/config.go). No
secret values are ever read from the environment; the proxy passes through whatever credential the
client itself sends.

| Name | Purpose | Required in |
|---|---|---|
| `LENS_PROXY_ADDR` | Proxy listen address (default `127.0.0.1:8787`) | optional |
| `LENS_DASHBOARD_ADDR` | Dashboard listen address (default `127.0.0.1:8788`) | optional |
| `LENS_UPSTREAM_URL` | DeepSeek upstream base URL | optional |
| `LENS_DB_PATH` | SQLite database file path | optional |
| `LENS_BODY_POLICY` | `full` \| `truncated` \| `off` | optional |
| `LENS_BODY_CAP_BYTES` | Max bytes captured per body. The shipped default is 262144; 8388608 is an operator opt-in | optional |
| `LENS_HOT_DAYS` | Days of bodies kept in the hot database. `0` leaves archival off | optional |
| `LENS_ALLOW_REMOTE` | Allow non-loopback bind addresses | optional |
| `LENS_CAPTURE` | Enable/disable capture entirely | optional |
| `LENS_SESSION_GAP_MINUTES` | Inactivity gap before a new session | optional |
| `LENS_REPLAY_ENABLED` | Enable the replay endpoint | optional |
| `LENS_REPLAY_COST_THRESHOLD_USD` | Spend `lens replay` sends without `--yes` confirmation | optional |
| `LENS_MODEL_MAP` | Client-model → DeepSeek-model table | optional |
| `LENS_MODEL_MAX_TOKENS` | Per-model `max_tokens` ceiling table | optional |
| `LENS_OFF_PEAK_DATES` | Peak calendar: dates billed off-peak all day (holidays), e.g. `2026-10-01..2026-10-07` | optional |
| `LENS_WORK_DATES` | Peak calendar: 调休 make-up work days (weekends the peak window still applies to), e.g. `2026-10-10` | optional |
| `HOME` (read, not `LENS_*`) | Resolves `~/.deepseek-lens/{lens.db,config.toml,prices.toml}` | optional, falls back to `os.UserHomeDir()` |
| `CLAUDE_CONFIG_DIR` (read, not `LENS_*`) | Locates a *different* home directory — Claude Code's, not lens's | optional |
| `ANTHROPIC_BASE_URL` (client-side, not read by lens) | What the *client* (Claude Code/Cline) must set to point at the proxy | required by the client, not by `lens` |

`HOME` and `CLAUDE_CONFIG_DIR` answer two unrelated questions and must not be conflated: `HOME`
locates *lens's own* files (`~/.deepseek-lens/*`), while `CLAUDE_CONFIG_DIR` locates *Claude
Code's* config directory (`settings.json`, `.deepseek-env.json`), read only by `lens doctor`'s
`provider_hooks` check to report whether the plugin hooks (a separate, optional repo) recognize
this route. The two also resolve with different precedence — `claudeConfigDir` tries
`CLAUDE_CONFIG_DIR`, then `USERPROFILE`, then `HOME`, then `os.UserHomeDir()`, matching Claude
Code's own documented Windows rule (`~/.claude` means `%USERPROFILE%\.claude`, not `$HOME/.claude`)
rather than lens's `$HOME`-first `config.userHomeDir` —
[internal/cli/doctor.go](../../internal/cli/doctor.go).

Config file: `~/.deepseek-lens/config.toml` (flat `key = value` format, not real TOML — see
[conventions.md](conventions.md)). Price table: `~/.deepseek-lens/prices.toml`, same flat format,
editable via `lens prices --set` / `--edit`.

## Local dev setup

1. Clone, then run `git config core.hooksPath .githooks` once (arms the commit-message and
   secret-scan hooks — see [conventions.md](conventions.md)'s governance section).
2. `go build ./...` to verify the module builds (pulls `modernc.org/sqlite`, pure Go, no cgo/system
   SQLite required).
3. `go run ./cmd/lens serve` starts both listeners; it prints the `ANTHROPIC_BASE_URL` export line.
   It writes `serve.state.json` beside the database before opening the store. `lens shutdown`,
   `lens restart`, and `lens reload` talk to that running process. `lens archive` moves or restores bodies.
   and the dashboard URL to copy-paste ([internal/cli/serve.go:139](../../internal/cli/serve.go)).
4. `go run ./cmd/lens doctor` at any time to check effective config and (if `serve` is running) live
   sink/consumer stats via `GET /api/health`.
