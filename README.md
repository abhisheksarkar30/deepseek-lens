# deepseek-lens

Point Claude Code, Cline, or any Anthropic-shaped client at DeepSeek's Anthropic-compatible endpoint
and the client keeps sending Anthropic parameters — prompt-cache breakpoints, `thinking.budget_tokens`,
`top_p`, `service_tier`, `tool_choice.disable_parallel_tool_use`, whole content-block types — that
DeepSeek accepts with a 200 and then silently ignores, rewrites, or drops. Nothing errors, nothing
warns you, and the answer you get back quietly differs from the one you asked for, at a cost you did
not choose. deepseek-lens is one Go binary that sits in front of that endpoint, proxies your traffic
through it without buffering or delaying the stream, and records what was sent, what came back, what
it cost, and which of those settings did nothing.

It is a single-user developer tool: loopback-only by default, one local SQLite file, no hosted
service.

## Quick start

```sh
# from a clone
go build -o lens ./cmd/lens     # or: go install ./cmd/lens   (installs into $GOPATH/bin)

./lens serve
```

`lens serve` prints the two things you need:

```
deepseek-lens is running.
  export ANTHROPIC_BASE_URL=http://127.0.0.1:8787
  dashboard:  http://127.0.0.1:8788
```

Then point your client at it.

**Claude Code** — in the shell you launch it from:

```sh
export ANTHROPIC_BASE_URL=http://127.0.0.1:8787
claude
```

**Cline** — Cline runs inside your editor, so it inherits the editor's environment, not your
terminal's. Either set the Anthropic provider's API base URL to `http://127.0.0.1:8787` in Cline's
settings, or launch the editor itself from a shell that has the variable exported:

```sh
export ANTHROPIC_BASE_URL=http://127.0.0.1:8787
code .
```

Watch it live in another terminal:

```sh
./lens tail
```

## The two ports

| Port | What it is | What talks to it |
|---|---|---|
| `8787` (proxy) | The hot path. A transparent reverse proxy that tees every request and response to a bounded in-memory queue and returns. It never parses, never buffers the stream to count tokens, and never makes your session depend on it. | Your coding client, via `ANTHROPIC_BASE_URL`. |
| `8788` (dashboard) | Read-only. Serves the embedded UI and the JSON/SSE API over the SQLite file. | Your browser, and the `lens` CLI. |

Both bind `127.0.0.1` unless you pass `--allow-remote` (see [Security](#security-posture)).
Override either with `--proxy-addr` / `--dashboard-addr`, or `LENS_PROXY_ADDR` / `LENS_DASHBOARD_ADDR`.

## What was silently dropped

`lens warnings` answers that. Every finding is one row with a `Kind`, a severity, the site inside the
request that caused it (`Path`), and a sentence naming what happened.

| Severity | Means |
|---|---|
| `error` | The request may have failed outright, or lost content. |
| `warn` | What you asked for and what ran are different, in a way that affects cost, quality, or safety. |
| `info` | A setting was dropped with no practical consequence. |

<!-- BEGIN warning kinds — checked against internal/analyze/kinds.go by internal/analyze/readme_test.go; do not hand-edit the kinds -->
| Kind | Severity | What it means |
|---|---|---|
| `cache_control_ignored` | warn | The client put a `cache_control` breakpoint in the prompt; DeepSeek ignores it, so nothing is cached and the whole prompt is billed at the input rate on every call. `Path` names the site when there is one, and the detail lists every site when there are several. |
| `budget_tokens_ignored` | warn | `thinking.budget_tokens` is disregarded. The thinking block itself is honored — only its budget is dropped. |
| `top_p_clamped` | warn | `top_p` below 1.0 outside thinking mode: DeepSeek forces sampling to 1.0, so the value constrains nothing that runs. |
| `top_p_below_floor` | warn | `top_p` below 0.95 inside thinking mode: DeepSeek raises it to its 0.95 floor, so the value that ran is not the one sent. |
| `parallel_tool_use_ignored` | info | `tool_choice.disable_parallel_tool_use` is ignored; DeepSeek decides tool-call parallelism itself, so serial tool calls are not guaranteed. |
| `model_remapped` | info | The model the client asked for is not one DeepSeek serves, so the model map's substitute serves it. `info` when the name was recognized (a known substitute is expected), `warn` when it was not — the substitute is then a guess. |
| `model_mapping_drift` | warn | The response reports a different resolved model than the model map predicted — the upstream mapping moved and the map is stale. |
| `unsupported_content_block` | error | A content block type DeepSeek's Anthropic endpoint does not support; the request may fail or the block may be dropped silently. One warning per offending block, so two unsupported blocks are two warnings. |
| `param_ignored` | info | A parameter DeepSeek accepts and then does nothing with. `Path` names which: `top_k`, `service_tier`, `container`, `mcp_servers`, or a `max_tokens` above the resolved model's known ceiling. |
| `header_ignored` | info | The `anthropic-beta` request header is ignored for `/messages`. |
| `upstream_error` | error | The upstream returned an error. Raised here when the error object arrives inside a 200 body (how the Anthropic wire reports overload and invalid requests once a stream has begun), and by the capture pipeline for a transport-level failure — the same kind, because it means the same thing to the reader. |
| `analyzer_panic` | error | A warning rule itself panicked. The call is still recorded; this row is the rule failing, not the request. It is the only kind raised by the pipeline rather than by a rule. |
<!-- END warning kinds -->

That table is checked against the code by `internal/analyze/readme_test.go`, which fails if a rule
can emit a kind the table does not list, if the table lists a kind nothing can emit, or if a row's
sentence drifts from the one beside the kind in `internal/analyze/kinds.go`. Both places edit
together or the build goes red.

```sh
./lens warnings                 # grouped by kind, worst first
./lens warnings --detail        # one row per occurrence, with request ids and sites
./lens ls --warn                # only the calls that raised something
```

What the model map resolves to is config, not code — `--model-map "opus:deepseek-v4-pro,sonnet:deepseek-flash,*:deepseek-flash"`.
A client model the map recognizes is remapped silently upstream; one it does not is served by the
`*` catch-all, and that is what `model_remapped` as a `warn` tells you. If DeepSeek ever changes its
side of that mapping, `model_mapping_drift` is how you find out without reading release notes.

## Costs

The shipped price table is **empty on purpose**: DeepSeek's model names were verified against real
traffic, its per-token prices were not, and an invented rate is worse than no rate — `$0.00` reads as
"this call was free", while `unpriced` tells you to go configure something.

```sh
./lens prices                                          # the effective table; "—" means no rate configured
./lens prices --set deepseek-flash.input=1.00 --set deepseek-flash.output=2.00
```

Those two rates are examples, not DeepSeek's prices — set your own. Rates are US dollars per
1,000,000 tokens, and `--unset <model>` reverts a model to unpriced. The running server re-reads
`~/.deepseek-lens/prices.toml` when it changes, so **`lens prices --set` takes effect without a
restart**; calls already recorded keep the cost they were recorded with. Until a rate is set, every
affected row reads `— (unpriced)`, and `lens stats` reports the unpriced count alongside the total
rather than folding it in as zero.

`lens prices --edit` opens the file in `$EDITOR` if you would rather edit it directly.

## Sessions

A session groups calls into the run they belong to, so `lens sessions` answers "this run cost $X
across N turns" instead of showing an undifferentiated call log.

The grouping is a heuristic: a call joins the most recently active session that has the same
*prefix hash* — SHA-256 over the request's `system` value plus its first two messages — if that
session was active within `--session-gap-minutes` (default 30). Two distinct runs that happen to open
with an identical prompt inside that window can be merged into one session, and one run that pauses
longer than the gap can be split in two. That ceiling is deliberate and documented in `internal/session`'s
package doc; the upgrade path is a client-supplied id, and it already works:

```
x-lens-session: my-run-42
```

That header overrides both heuristics verbatim. Use it whenever you need exactness — it is the only
exact correlation available, because no current client sends one on its own.

```sh
./lens sessions
./lens show s_1789455846977_dffa38ae     # one session's totals, or one call's detail
```

## Replay

Replay re-sends a captured request to the LLM API. It **never executes anything, never touches your
repository, and never re-runs tools.** A captured body may *describe* tool calls the original client
executed; replay re-sends that description to the model and records the reply — it does not act on
`tool_use` blocks. Replaying is billable.

It is **off by default**. Turn it on explicitly:

```sh
./lens serve --replay
./lens replay 6 --set max_tokens=1 --yes
```

The endpoint (`POST /api/requests/{id}/replay`) is guarded by an `Origin`/`Host` allowlist and no
shared secret: the `Host` must be loopback, and an `Origin` header, when present, must be the
dashboard's own origin. That kills the two browser-borne threats (DNS rebinding and cross-origin
POST) with nothing to distribute, and the CLI sends no `Origin` at all, which is why it needs no
credential. A local process that could forge past the guard can already read the SQLite file
directly, so the endpoint grants it nothing it did not have. If you ever want defense against local
processes, add a shared secret then.

`lens replay` prints the original call's cost before spending anything, and refuses without `--yes`
when that original cost is above `--replay-cost-threshold-usd` (default $0.25) or unknown. It is a
thin HTTP client for the running server — `lens serve` must be running, because the replay is
recorded by the same single writer that records live traffic rather than by the CLI opening its own.

One consequence worth knowing before you rely on it: lens never persists your credential and never
injects one, so a replayed request carries the *redacted* placeholder in `x-api-key`. Against an
upstream that requires the key, the replay is answered `401` and that `401` is recorded faithfully —
which is what the replay comparison is for. `lens replay <id> --dump out.json` writes the edited body
and sends nothing at all.

## Security posture

- **Loopback-only by default.** Both listeners bind `127.0.0.1`, and `config.Validate` refuses a
  non-loopback address unless `--allow-remote` is set. `lens doctor` prints the effective binds.
- **`--allow-remote` is a footgun.** It exists for a container or a VM you control. It does not add
  authentication, and the dashboard has none.
- **Full bodies are stored.** `--body-policy` defaults to `full` with a 256KB cap per body
  (`--body-cap-bytes`), so every prompt, every file the agent read, and every reply land in
  `~/.deepseek-lens/lens.db`. That content — not a credential — is the asset here.
- **Credentials are not stored.** `x-api-key`, `Authorization`, and `Cookie` are replaced with
  `[redacted]` before a request row is written, and lens never injects a key of its own.
- **The dashboard has no auth.** Acceptable for a read-only observer bound to loopback; **dashboard
  authentication is required before any non-local deployment.** Replay is the exception that proved
  the rule: it is a billable write path, so it is off by default and carries the `Origin`/`Host`
  guard described above.

`--body-policy truncated` keeps the metadata and caps bodies hard; `--body-policy off` stores none.

## Troubleshooting

**My coding session broke.**
1. `./lens doctor` — prints the resolved configuration and a PASS/WARN/FAIL list: whether the bind
   addresses are valid and loopback, whether the DB directory is writable, whether the upstream
   hostname resolves, how many rows are stored. It exits non-zero if any check fails. Note that it
   reports the configuration *for that command line*, so `lens doctor` run without `--replay` says
   `replay_enabled: false` even while a `lens serve --replay` process is running.
2. `./lens serve --no-capture` — the proxy still forwards your traffic, with no teeing, no sink, and
   no analysis. Nothing is recorded; the banner says so. If your session is fine under this and not
   without it, the problem is on the observation path and not in the proxy.
3. Take lens out entirely — unset `ANTHROPIC_BASE_URL` and point the client at
   `https://api.deepseek.com/anthropic` again.

lens is built to fail open: the proxy never makes your session depend on the observer. If it is
wedged, your client is not supposed to notice. Steps 2 and 3 are the proof of that, not a workaround.

**Calls are missing from `lens ls`.** Check `curl http://127.0.0.1:8788/api/health`:
`sink_dropped` non-zero means the observer queue overflowed and shed calls to keep the hot path
fast; `consumer_failed` non-zero means a call reached the pipeline and could not be written (the
server's stderr says why). `consumer_processed` vs `sink_accepted` is the gap between captured and
stored. A lone call lands within ~250ms of arriving, so an empty list a second after the call is
worth investigating.

**Costs read `— (unpriced)`.** That is the price table being empty, not a bug — see
[Costs](#costs).

**Everything is in one session.** Expected if your prompts share an opening; see
[Sessions](#sessions) and set `x-lens-session`.

## Commands

| Command | What it does |
|---|---|
| `lens serve` | Proxy + dashboard + consumer in one process. `--replay` enables the replay endpoint; `--no-capture` disables capture. |
| `lens doctor` | Resolved config and health checks. Exits non-zero on a failing check. |
| `lens ls` | The call log. `--limit`, `--since`, `--session`, `--model`, `--warn`, `--errors`, `--json`. |
| `lens show <id\|session>` | One call's detail and bodies, or one session's totals. |
| `lens tail` | Follow the call log as it is written. |
| `lens warnings` | What was dropped, grouped by kind. `--detail` for one row per occurrence. |
| `lens sessions` | Sessions with turns, tokens, cost, and warning counts. |
| `lens stats` | Window totals, per-model split, and the unpriced count. |
| `lens prices` | The effective price table. `--set`, `--unset`, `--edit`. |
| `lens replay <id>` | Re-issue a captured call. `--set path=value`, `--diff <id>`, `--dump <file>`, `--yes`. |
| `lens export` | Every stored row as JSON, one object per line. |

## How it fits together

```
client → proxy → (tee) → bounded sink → consumer → {parse, analyze} → SQLite → JSON API → web
```

One process, two `http.Server`s. The proxy listener does no parsing: it copies bytes and hands a
bounded copy to the sink, which never blocks the hot path. The consumer goroutine is the only
writer SQLite ever sees. The dashboard reads that file and pushes live updates over SSE.

Two invariants are worth stating because they are easy to break and hard to notice if you do: the
hot path never buffers the stream to count tokens (a buffered stream still returns the right bytes,
just late, so only a timing test catches it), and `internal/proxy` depends on nothing but `sink` and
`config` — if it ever imports `analyze` or `store`, the design has eroded.
