[← INDEX](INDEX.md)

# CLI & Tooling

`lens <command> [flags]`, dispatched from a fixed map in
[cmd/lens/main.go:14-32](../../cmd/lens/main.go). All thirteen names are implemented (no stubs remain).

| Command | Flags | What it does | Evidence |
|---|---|---|---|
| `doctor` | forwards config flags (`--proxy-addr`, `--db-path`, `--replay`, `--off-peak-dates`, `--work-dates`, ...) | Prints effective config (including `off_peak_dates` / `work_dates`) + PASS/WARN/FAIL checks (config validity, DB dir writable, upstream host resolves, redaction self-test, live sink/consumer stats from a running `serve`, whether the optional plugin hooks recognize this route as `provider_hooks`, and whether the configured peak-pricing date sets name the current year as `peak_calendar` — that one is PASS/WARN only, never FAIL, because the shipped date set is a dated fact the user may not have updated yet); exits non-zero on any FAIL | [internal/cli/doctor.go](../../internal/cli/doctor.go) |
| `serve` | config flags + `--replay`, `--no-capture` | Runs the proxy listener + dashboard listener + consumer in one process until `SIGINT` | [internal/cli/serve.go](../../internal/cli/serve.go) |
| `ls` | `--limit` (25), `--session`, `--model`, `--since`, `--warn`, `--errors`, `--json` | Lists recent captured requests as a table or JSON | [internal/cli/ls.go](../../internal/cli/ls.go) |
| `show` | `--full` | Prints one request's full detail (headers, body, warnings) | [internal/cli/show.go](../../internal/cli/show.go) |
| `tail` | `--session`, `--warn` | Follows new requests as they're captured | [internal/cli/tail.go](../../internal/cli/tail.go) |
| `stats` | `--since` (24h), `--by` (model\|day\|session), `--json` | Prints aggregate token/cost stats | [internal/cli/stats.go](../../internal/cli/stats.go) |
| `sessions` | — | Lists sessions with running totals | [internal/cli/sessions.go](../../internal/cli/sessions.go) |
| `warnings` | `--detail`, `--json` | Lists warnings, grouped by kind or per-occurrence | [internal/cli/warnings.go](../../internal/cli/warnings.go) |
| `export` | `--limit`, `--session`, `--model`, `--since` | Exports captured requests | [internal/cli/export.go](../../internal/cli/export.go) |
| `prices` | `--set model.field=rate` (repeatable), `--unset`, `--edit` | Views/edits the `~/.deepseek-lens/prices.toml` rate table | [internal/cli/prices.go](../../internal/cli/prices.go) |
| `replay` | `<id>` (positional), `--set path=value` (repeatable), `--dump`, `--no-capture`, `--diff id`, `--yes`, `--server` | Re-sends a captured request's body (optionally edited) via the dashboard's replay API and prints the outcome diff | [internal/cli/replay.go](../../internal/cli/replay.go) |
| `purge` | `--older-than <days>`, `--unpriced`, `--dry-run`, `--yes`, `--vacuum` | One-shot purge for when `lens serve` isn't running: opens the store directly (the one CLI subcommand that does), guarded against an unconfigured/whole-table delete before it ever opens the store | [internal/cli/purge.go](../../internal/cli/purge.go) |
| `archive` | `status`; `run [--dry-run] [--yes]`; `restore --since --until [--dry-run] [--yes]` | Prints archive health, moves bodies older than HotDays, or copies them back. A real run or restore requires `--yes`, the same gate as `purge` | [internal/cli/archive.go](../../internal/cli/archive.go) |

`replay` needs `--yes` to proceed when the original call's cost is above
`ReplayCostThresholdUSD` (default $0.25) or unknown — enforced by `costGate`
([internal/cli/replay.go:155-172](../../internal/cli/replay.go)).

Shared formatting helpers (table rendering, human-readable durations/tokens/cost, TTY detection) live
in [internal/cli/format.go](../../internal/cli/format.go) and are used by every read command.
