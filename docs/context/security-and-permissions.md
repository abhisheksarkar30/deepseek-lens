[← INDEX](INDEX.md)

# Security & Permissions

This is the highest-stakes module in this codebase per [CLAUDE.md](../../CLAUDE.md): full bodies
are stored. The shipped cap is 256 KiB per body and is configurable (8 MiB is the operator opt-in).
A 16 KiB tail past that cap still carries `usage`. The content — every prompt and every file the
agent read — is the asset this repo is protecting.

## AuthN / AuthZ model

There is no user authentication anywhere — this is a single-user local tool. Safety instead rests on
three independent controls:

1. **Loopback-only binding by default.** `Config.Validate` → `validateLoopback` rejects any
   `ProxyAddr`/`DashboardAddr` that isn't `localhost`, a loopback IP, or explicitly allowed via
   `AllowRemote`/`--allow-remote`
   ([internal/config/config.go:266-322](../../internal/config/config.go)).
2. **Header redaction before anything is stored.** `X-Api-Key`, `Authorization`, and `Cookie` are
   replaced with `"[redacted]"` in a cloned header set before the tee ever reaches the sink — the
   original, unredacted headers are still what's sent to the client/upstream; only the *stored* copy
   is redacted ([internal/proxy/redact.go](../../internal/proxy/redact.go)).
3. **A startup self-test** (`store.RedactCheck`) scans every stored `req_headers`/`resp_headers` blob
   for a reachable `x-api-key` value and logs (does not block startup on) a failure — belt-and-braces
   for control #2, run automatically by `lens serve`
   ([internal/store/store.go:755-775](../../internal/store/store.go),
   [internal/cli/serve.go:49-52,153-164](../../internal/cli/serve.go)).

## Sensitive permissions & consent

Not applicable in the mobile/platform-permission sense — this is a server proxy, not a client app
requesting OS-level permissions. The closest analog is the five write routes' authority:

| Capability | Why needed | Where gated | Evidence |
|---|---|---|---|
| Replay (re-send a captured request, billable) | Lets a user or the dashboard UI resend a call against the real upstream | Off by default (`--replay`); Origin/Host allowlist checked before any I/O; `lens replay` gates spend above `ReplayCostThresholdUSD` behind `--yes` | [internal/api/api.go:512-625](../../internal/api/api.go), [internal/cli/replay.go:155-172](../../internal/cli/replay.go) |
| Set/unset a model's price rates | Lets the Settings tab or `lens prices --set` change what future calls cost | Always on; Origin/Host allowlist; invalid rate/model name rejected before write | [internal/api/prices.go](../../internal/api/prices.go) |
| Purge rows (delete, by age or the unpriced predicate) | Lets the Settings tab or `lens purge` reclaim disk; the one destructive capability in the system | Always on; Origin/Host allowlist; `days > 0` required for the age-based mode; `lens purge` additionally requires `--yes` for a non-dry-run delete | [internal/api/purge.go](../../internal/api/purge.go), [internal/cli/purge.go](../../internal/cli/purge.go) |
| Shutdown the running serve | Stops the proxy from the CLI without a console Ctrl-C | Origin/Host allowlist plus a loopback-caller check (`POST /api/shutdown`) | [internal/api/api.go](../../internal/api/api.go), [internal/cli/shutdown.go](../../internal/cli/shutdown.go) |
| Reload config | Applies `RetentionDays` and `HotDays` on a running serve; other changes need a restart | Origin/Host allowlist plus a loopback-caller check (`POST /api/reload`) | [internal/api/api.go](../../internal/api/api.go), [internal/cli/reload.go](../../internal/cli/reload.go) |

## Role / access model

No roles — any local process that can reach the loopback ports has full read access to captured data,
can always set prices and purge data (subject to the Origin/Host guard), and, if `--replay` is on,
can trigger a replay. This is a documented, deliberate trust boundary: "a local process that could
forge past this [guard] could already read the SQLite file"
([internal/api/api.go:767](../../internal/api/api.go)) — and, since GI-17, could already delete
rows from it via `lens purge` without going through the guard at all, which is why the guard's role
is to stop a *browser*, not a local process with its own access to the file.

### The Origin/Host allowlist shared by all five write routes

`replayOriginReject` ([internal/api/api.go:885-912](../../internal/api/api.go)) runs before any store
or upstream access and rejects on two conditions, for whichever of the five write routes calls it
(replay, prices, purge, shutdown, reload — each passes its own `action` string, used only in the
rejection message):

- **`Host` must be loopback** — defends against DNS-rebinding pages that resolve their own hostname
  to `127.0.0.1` and then POST with a forged `Host`.
- **`Origin`, when present, must equal the request's own `Host`** — the browser same-origin rule,
  compared against the request's actual `Host` (not the configured `DashboardAddr`) because the
  dashboard answers on whichever loopback alias was browsed to.
- **A missing `Origin` passes** — browsers always send it, so its absence identifies a non-browser
  client (i.e., a CLI caller), deliberately, not a hole.

This is explicitly *not* a credential/secret-based guard — see the plan's security self-review
referenced in [CLAUDE.md](../../CLAUDE.md)'s "Fail open" section for the stated rationale and upgrade
path.

## Security rules (DB/storage)

No Firestore/RLS-style declarative rules — access control is entirely at the network layer
(loopback binding) plus the shared application-layer guard above, applied identically to all three
write routes. The SQLite file itself has default OS file permissions;
`~/.deepseek-lens/prices.toml` is written with `0o600` and its directory with `0o700`
([internal/pricing/table.go:154-155,179](../../internal/pricing/table.go)).

## Known gaps

> ⚠️ ASSUMPTION: no secret-scanning or TLS is applied to the SQLite file itself beyond directory/file
> permissions — the redaction self-test only checks for a reachable `x-api-key` pattern in header
> JSON, not for credentials that might appear inside a request/response *body* (e.g. a key
> accidentally included in a prompt). Confirm with the project owner whether this is an accepted
> scope boundary or a known future item; nothing in the code marks it either way.
