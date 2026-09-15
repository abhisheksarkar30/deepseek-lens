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
| `LENS_BODY_CAP_BYTES` | Max bytes captured per body | optional |
| `LENS_ALLOW_REMOTE` | Allow non-loopback bind addresses | optional |
| `LENS_CAPTURE` | Enable/disable capture entirely | optional |
| `LENS_SESSION_GAP_MINUTES` | Inactivity gap before a new session | optional |
| `LENS_REPLAY_ENABLED` | Enable the replay endpoint | optional |
| `LENS_REPLAY_COST_THRESHOLD_USD` | Spend `lens replay` sends without `--yes` confirmation | optional |
| `LENS_MODEL_MAP` | Client-model → DeepSeek-model table | optional |
| `LENS_MODEL_MAX_TOKENS` | Per-model `max_tokens` ceiling table | optional |
| `HOME` (read, not `LENS_*`) | Resolves `~/.deepseek-lens/{lens.db,config.toml,prices.toml}` | optional, falls back to `os.UserHomeDir()` |
| `ANTHROPIC_BASE_URL` (client-side, not read by lens) | What the *client* (Claude Code/Cline) must set to point at the proxy | required by the client, not by `lens` |

Config file: `~/.deepseek-lens/config.toml` (flat `key = value` format, not real TOML — see
[conventions.md](conventions.md)). Price table: `~/.deepseek-lens/prices.toml`, same flat format,
editable via `lens prices --set` / `--edit`.

## Local dev setup

1. Clone, then run `git config core.hooksPath .githooks` once (arms the commit-message and
   secret-scan hooks — see [conventions.md](conventions.md)'s governance section).
2. `go build ./...` to verify the module builds (pulls `modernc.org/sqlite`, pure Go, no cgo/system
   SQLite required).
3. `go run ./cmd/lens serve` starts both listeners; it prints the `ANTHROPIC_BASE_URL` export line
   and the dashboard URL to copy-paste ([internal/cli/serve.go:169-176](../../internal/cli/serve.go)).
4. `go run ./cmd/lens doctor` at any time to check effective config and (if `serve` is running) live
   sink/consumer stats via `GET /api/health`.
