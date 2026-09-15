[← INDEX](INDEX.md)

# Security & Permissions

This is the highest-stakes module in this codebase per [CLAUDE.md](../../CLAUDE.md): "Full bodies
*are* stored (256KB cap per body, configurable)... the content — every prompt and every file the
agent read — is the asset this repo is protecting."

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
requesting OS-level permissions. The closest analog is the replay endpoint's write authority:

| Capability | Why needed | Where gated | Evidence |
|---|---|---|---|
| Replay (re-send a captured request, billable) | Lets a user or the dashboard UI resend a call against the real upstream | Off by default (`--replay`); Origin/Host allowlist checked before any I/O; `lens replay` gates spend above `ReplayCostThresholdUSD` behind `--yes` | [internal/api/api.go:254-362](../../internal/api/api.go), [internal/cli/replay.go:155-172](../../internal/cli/replay.go) |

## Role / access model

No roles — any local process that can reach the loopback ports has full read access to captured data
and, if `--replay` is on, can trigger a replay (subject to the Origin/Host guard). This is a
documented, deliberate trust boundary: "a local process that could forge past this [guard] could
already read the SQLite file" ([internal/api/api.go:516-519](../../internal/api/api.go)).

### Replay's Origin/Host allowlist (the one credentialless write guard in the system)

`replayOriginReject` ([internal/api/api.go:491-542](../../internal/api/api.go)) runs before any store
or upstream access and rejects on two conditions:

- **`Host` must be loopback** — defends against DNS-rebinding pages that resolve their own hostname
  to `127.0.0.1` and then POST with a forged `Host`.
- **`Origin`, when present, must equal the request's own `Host`** — the browser same-origin rule,
  compared against the request's actual `Host` (not the configured `DashboardAddr`) because the
  dashboard answers on whichever loopback alias was browsed to.
- **A missing `Origin` passes** — browsers always send it, so its absence identifies a non-browser
  client (i.e., `lens replay` itself), deliberately, not a hole.

This is explicitly *not* a credential/secret-based guard — see the plan's security self-review
referenced in [CLAUDE.md](../../CLAUDE.md)'s "Fail open" section for the stated rationale and upgrade
path.

## Security rules (DB/storage)

No Firestore/RLS-style declarative rules — access control is entirely at the network layer
(loopback binding) plus the one application-layer guard above. The SQLite file itself has default
OS file permissions; `~/.deepseek-lens/prices.toml` is written with `0o600` and its directory with
`0o700` ([internal/pricing/table.go:154-155,179](../../internal/pricing/table.go)).

## Known gaps

> ⚠️ ASSUMPTION: no secret-scanning or TLS is applied to the SQLite file itself beyond directory/file
> permissions — the redaction self-test only checks for a reachable `x-api-key` pattern in header
> JSON, not for credentials that might appear inside a request/response *body* (e.g. a key
> accidentally included in a prompt). Confirm with the project owner whether this is an accepted
> scope boundary or a known future item; nothing in the code marks it either way.
