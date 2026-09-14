# Bead br-GI-1-01: Repo scaffold, module init, config, `doctor`

**Plan Reference**: `docs/planning/GI-1-deepseek-lens-v1.md` §Bead sequence

- **Priority**: P0 (critical)
- **Dependencies**: none
- **Blocks**: br-GI-1-02, br-GI-1-03, br-GI-1-06, br-GI-1-08

## Description

Create the Go module and the configuration layer everything else reads from.

`go.mod` declares module `github.com/abhisheksarkar30/deepseek-lens`, Go 1.24. The only dependency
added in this bead is `modernc.org/sqlite` (pure Go, no CGO) — pinned now so later beads do not each
perturb the module graph.

`internal/config` loads configuration from three sources with a fixed precedence:
**flags > environment (`LENS_*`) > config file > built-in defaults.**

Fields: `ProxyAddr` (default `127.0.0.1:8787`), `DashboardAddr` (default `127.0.0.1:8788`),
`UpstreamURL` (default `https://api.deepseek.com/anthropic`), `DBPath`
(default `~/.deepseek-lens/lens.db`), `BodyPolicy` (`full` | `truncated` | `off`, default `full`),
`BodyCapBytes` (default 262144 — the cap applied to **each** captured body, request *and* response),
`AllowRemote` (default false), `Capture` (default true),
`SessionGapMinutes` (default 30), `ReplayEnabled` (default false — the replay endpoint, br-GI-1-13,
stays off until this is explicitly set; see `--replay` on `lens serve`, br-GI-1-08).

Config file: `~/.deepseek-lens/config.toml`. Parse it with a minimal hand-rolled
`key = value` reader rather than pulling in a TOML library — the schema is flat, scalar, and
known, so a dependency is not earned. `ponytail:` hand-rolled flat TOML reader; swap in
`BurntSushi/toml` if nested config ever appears.

`Validate()` must reject: non-loopback addresses unless `AllowRemote`; `BodyPolicy` outside the
three allowed values; `BodyCapBytes` <= 0; `UpstreamURL` that is not `http`/`https` or has no host;
`SessionGapMinutes` <= 0. Errors name the offending field and the value.

`cmd/lens/main.go` dispatches subcommands via a `map[string]func([]string) error`. In this bead only
`doctor` is implemented; every other name returns a clear "not implemented yet" error. This keeps
`main.go` stable so later beads only add to the map.

`lens doctor` prints, as aligned `key: value` lines: effective config after precedence resolution,
whether the DB path's parent directory is writable, whether the upstream host resolves, the
effective bind addresses, and an explicit warning line when `AllowRemote` is set or `BodyPolicy` is
`full` (both of which widen exposure).

## Rationale

Every subsequent bead reads config. Building it first means no bead invents its own defaults, and
the precedence rules are settled before there are five call sites to reconcile. `doctor` exists
before the proxy does on purpose: risk 4 in the plan is that a wedged proxy kills the user's live
coding session, so the diagnostic ships before the thing it diagnoses.

## Outcome Definition

- `go build ./...` succeeds on a clean checkout with no Android/JDK/CGO toolchain.
- `go test ./internal/config/...` passes.
- `lens doctor` runs and prints effective config without needing the DB or upstream to exist.
- Every other subcommand prints a "not implemented yet" error and exits non-zero.
- Invalid config values are rejected with an error naming the field.

## Test Specifications

- Unit Tests (`internal/config/config_test.go`):
  - Defaults are applied when no flag, env, or file is present.
  - Flag overrides env; env overrides file; file overrides default — one case per edge.
  - `LENS_PROXY_ADDR` env var populates `ProxyAddr`.
  - A flat TOML file parses into the expected struct.
  - Malformed TOML (missing `=`, unterminated quote) returns an error, not a panic.
  - `Validate()` rejects `0.0.0.0:8787` without `AllowRemote`.
  - `Validate()` accepts `0.0.0.0:8787` with `AllowRemote` true.
  - `Validate()` rejects `BodyPolicy = "sometimes"`.
  - `Validate()` rejects `BodyCapBytes = 0` and negative.
  - `Validate()` rejects `UpstreamURL = "ftp://x"` and a URL with an empty host.
  - `Validate()` rejects `SessionGapMinutes = 0`.
- Integration Tests:
  - `lens doctor` invoked via `exec` in a temp `HOME` exits 0 and prints the effective proxy address.

## Files to Touch

- `go.mod`, `go.sum` (create)
- `internal/config/config.go` (create)
- `internal/config/config_test.go` (create)
- `cmd/lens/main.go` (create)
- `.gitignore` (create — `*.db`, `*.db-wal`, `*.db-shm`, `config.toml`, `~/.deepseek-lens/`)
