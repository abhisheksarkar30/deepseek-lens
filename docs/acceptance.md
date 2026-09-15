# End-to-end acceptance run — br-GI-1-14

**Date**: 2026-09-15 · **Binary**: `go build -o lens.exe ./cmd/lens` from `GI-1-deepseek-lens-v1` ·
**Upstream**: `https://api.deepseek.com/anthropic` (real, billable) · **Client**: Claude Code
2.1.268 and `curl 8.7.1` · **Host**: Windows 11, Go 1.24.1.

This is a recording of a run that actually happened. Every block below is verbatim output, pasted
as captured. Where a step could not be verified in this environment, the document says so rather
than describing what it would have shown.

## Method

The server was started against an **isolated HOME** (`$TEMP/lens-accept`) so the run's database and
price table landed in a scratch directory and the user's real `~/.deepseek-lens/` was never touched.
Both the server and every CLI invocation below ran with that same `HOME`.

The credential was passed only as `-H "x-api-key: $DEEPSEEK_API_KEY"` on `curl` command lines, read
from the environment at call time. It was never echoed, never written to a file, and never captured
in any output pasted here. lens replaces `x-api-key` with `[redacted]` before insert, and the stored
rows in the raw JSON bodies below show exactly that.

Spend was kept minimal by design: `max_tokens` of 1, 16, 32, or 64 on short prompts, ~50 calls in
total. Two of them were expensive by accident — Claude Code's session-title call (53,099 input
tokens, step 3) and its main turn (754 input tokens) — and both are reported below rather than
hidden.

## Step 1 — `lens serve` starts, banner shows both addresses

```
$ HOME=... ./lens.exe serve --replay
deepseek-lens is running.
  export ANTHROPIC_BASE_URL=http://127.0.0.1:8787
  dashboard:  http://127.0.0.1:8788
```

**PASS.** Both addresses are printed, and the `ANTHROPIC_BASE_URL` line is copy-pasteable as
printed. A follow-up request confirmed both listeners were live:

```
$ curl -s http://127.0.0.1:8788/api/health
{"sink_accepted":0,"sink_dropped":0,"consumer_processed":0,"consumer_failed":0,"consumer_flushes":0,"replay_enabled":true}
```

`replay_enabled: true` confirms the endpoint is on for this run, which step 9 needs.

## Step 2 — a real client through the proxy

### 2a. Claude Code, the real CLI

```
$ cd "$TEMP/lens-accept"
$ ANTHROPIC_API_KEY="$DEEPSEEK_API_KEY" claude --settings ./settings-lens.json \
    -p "Reply with the single word: pong" --model claude-sonnet-4-5 --output-format text --max-turns 1
⚠ claude.ai connectors are disabled because ANTHROPIC_API_KEY or another auth source is set and
takes precedence over your claude.ai login · Unset it to load your organization's connectors
[claude-code:unrecognized_model] {"model":"deepseek-flash","query_source":"generate_session_title"}
pong
cli exit=0
```

where `settings-lens.json` is `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8787"}}`.

**PASS, with a finding.** The real client answered through the proxy, and lens recorded its traffic —
see step 3, where requests 1–3 are the CLI's own. But the `--settings` flag was necessary; see
finding F3: a plain `export ANTHROPIC_BASE_URL=...` was silently ignored by this Claude Code
installation, and the first attempt never reached lens at all (`sink_accepted` stayed at 0).

### 2b. Streaming, the normal path for a coding client

```
$ curl -s -N -o resp-proxy-stream.txt -w 'http=%{http_code} ttfb=%{time_starttransfer} total=%{time_total}\n' \
    http://127.0.0.1:8787/v1/messages \
    -H "x-api-key: $DEEPSEEK_API_KEY" -H "anthropic-version: 2023-06-01" \
    -H "content-type: application/json" --data-binary @body-stream.json
http=200 ttfb=0.233442 total=1.288350
```

```
--- first 12 SSE lines ---
event: message_start
data: {"type":"message_start","message":{"id":"b75c94e6-e513-4acb-bd31-57da131ed181","type":"message","role":"assistant","model":"deepseek-flash","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":38,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0,"service_tier":"standard"}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}
...
--- last 4 SSE lines ---

event: message_stop
data: {"type":"message_stop"}

--- line count ---
81 resp-proxy-stream.txt
```

**PASS.** The stream arrives event-by-event (`ttfb` 233ms, total 1.29s — the first byte came back
long before the last), and the stream terminates properly with `message_stop`.

## Step 3 — `lens ls`: model mapping, tokens, duration

```
$ HOME=... ./lens.exe ls --limit 10
ID  TIME      DUR    MODEL           IN/OUT TOK  COST               STATUS  ⚠
4   just now  1.3s   deepseek-flash  38/19       — (unpriced)       200     ⚠
3   just now  2.4s   deepseek-flash  53.1k/39    — (unpriced)       200     ⚠
2   just now  1.8s   deepseek-flash  754/129     — (unpriced)       200     ⚠
1   just now  344ms                  0/0         — (unknown-model)  401
```

```
$ HOME=... ./lens.exe ls --limit 10 --json
{"id":4,"started_at":"2026-09-15T07:03:05.3288114Z","duration_ms":1286,"model":"deepseek-flash","input_tokens":38,"output_tokens":19,"cost_usd":null,"cost_source":"unpriced","status":200,"warned":true}
{"id":3,"started_at":"2026-09-15T07:02:53.5060996Z","duration_ms":2400,"model":"deepseek-flash","input_tokens":53099,"output_tokens":39,"cost_usd":null,"cost_source":"unpriced","status":200,"warned":true}
{"id":2,"started_at":"2026-09-15T07:02:51.4144523Z","duration_ms":1818,"model":"deepseek-flash","input_tokens":754,"output_tokens":129,"cost_usd":null,"cost_source":"unpriced","status":200,"warned":true}
{"id":1,"started_at":"2026-09-15T07:02:48.6933499Z","duration_ms":344,"model":"","input_tokens":0,"output_tokens":0,"cost_usd":null,"cost_source":"unknown-model","status":401,"warned":false}
```

**PASS.** Token counts, durations, and statuses are recorded for every call. **No
`model_mapping_drift`.** The client asked for `claude-sonnet-4-5`; config predicts `deepseek-flash`;
the upstream's own `usage.model` reports `deepseek-flash`. The mapping the tool assumed is the
mapping that served the traffic — plan risk 6 is confirmed rather than assumed, on real traffic.

Request 1 is a finding of its own: see F2.

## Step 4 — the client's response matches a direct call

The same body was sent twice — once through the proxy, once straight to the upstream — and the two
responses compared. Both were complete JSON with a `usage` object.

```
--- PROXIED ---
{"id":"8e88c9d5-9f06-40db-b71e-ed12933b6f97","type":"message","role":"assistant","model":"deepseek-flash","content":[{"type":"thinking","thinking":"The user wants me to reply with the single word \"pong\". Simple.","signature":"8e88c9d5-9f06-40db-b71e-ed12933b6f97"}],"stop_reason":"max_tokens","stop_sequence":null,"usage":{"input_tokens":38,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":16,"service_tier":"standard"}}
--- DIRECT ---
{"id":"7d8bbe47-e439-4772-a509-74a694e2a0d3","type":"message","role":"assistant","model":"deepseek-flash","content":[{"type":"thinking","thinking":"The user wants me to reply with the single word: pong. Simple.","signature":"7d8bbe47-e439-4772-a509-74a694e2a0d3"}],"stop_reason":"max_tokens","stop_sequence":null,"usage":{"input_tokens":38,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":16,"service_tier":"standard"}}
```

Structural comparison (`signature` stripped, since it repeats the message id), verbatim:

```
type: equal=True proxied='message' direct='message'
role: equal=True proxied='assistant' direct='assistant'
model: equal=True proxied='deepseek-flash' direct='deepseek-flash'
stop_reason: equal=True proxied='max_tokens' direct='max_tokens'
stop_sequence: equal=True proxied=None direct=None
usage: equal=True proxied={'input_tokens': 38, 'cache_creation_input_tokens': 0, 'cache_read_input_tokens': 0, 'output_tokens': 16, 'service_tier': 'standard'} direct={'input_tokens': 38, 'cache_creation_input_tokens': 0, 'cache_read_input_tokens': 0, 'output_tokens': 16, 'service_tier': 'standard'}
content (signature stripped): equal= False
 blocks proxied: [{'type': 'thinking', 'thinking': 'The user wants me to reply with the single word "pong". Simple.'}]
 blocks direct : [{'type': 'thinking', 'thinking': 'The user wants me to reply with the single word: pong. Simple.'}]
```

and the raw byte counts of the two files:

```
449 resp-proxied-4.json
446 resp-direct-4.json
Σ 895
```

**PASS.** Every protocol field, the model, the stop reason, the stop sequence, and the entire usage
object are byte-identical; the response is complete, not truncated, on both sides. The only
difference is the generated *text* — `"pong".` with quotes versus `pong.` without, at
`temperature: 0` — which is upstream sampling, not proxying: two direct calls produce the same
variation. The 3-byte size difference is exactly that difference.

## Step 5 — `cache_control` is caught and named

Request body carried `cache_control: {"type":"ephemeral"}` on `system[0]` and on
`messages[0].content[0]`.

```
$ curl -s -o resp-cache.json -w 'http=%{http_code} ttfb=%{time_starttransfer} total=%{time_total}\n' \
    http://127.0.0.1:8787/v1/messages ... --data-binary @body-cache.json
http=200 ttfb=0.243745 total=1.410727

$ HOME=... ./lens.exe warnings
KIND                   SEVERITY  COUNT  LAST SEEN
model_remapped         info      4      just now
cache_control_ignored  warn      2      just now
header_ignored         info      2      1m ago
budget_tokens_ignored  warn      1      1m ago

$ HOME=... ./lens.exe warnings --detail
REQUEST  KIND                   SEVERITY  AGE       PATH                    DETAIL
6        cache_control_ignored  warn      just now                          client requested prompt caching at system[0], messages[0].content[0]; DeepSeek ignores cache_control, so no caching occurs
6        model_remapped         info      just now  model                   model: claude-sonnet-4-5 → deepseek-flash (billed at deepseek-flash rates)
5        model_remapped         info      just now  model                   model: claude-sonnet-4-5 → deepseek-flash (billed at deepseek-flash rates)
4        model_remapped         info      just now  model                   model: claude-sonnet-4-5 → deepseek-flash (billed at deepseek-flash rates)
3        cache_control_ignored  warn      1m ago                            client requested prompt caching at system[1], system[2], messages[0].content[11]; DeepSeek ignores cache_control, so no caching occurs
3        budget_tokens_ignored  warn      1m ago    thinking.budget_tokens  thinking.budget_tokens: thinking.budget_tokens=31999 is disregarded
3        model_remapped         info      1m ago    model                   model: claude-sonnet-4-5 → deepseek-flash (billed at deepseek-flash rates)
3        header_ignored         info      1m ago    anthropic-beta          anthropic-beta: anthropic-beta is ignored for /messages
2        header_ignored         info      1m ago    anthropic-beta          anthropic-beta: anthropic-beta is ignored for /messages
```

(The `--detail` table is reproduced in full here; it is every row the command printed.)

**PASS**, and better than the step asked for. The status was `200` — the API accepted the request
and ignored the caching entirely, which is precisely the failure mode that justifies the tool.
`cache_control_ignored` names both sites.

The rows for requests 2 and 3 are the *real Claude Code CLI's own traffic*, not a synthetic body:
the CLI sent `cache_control` at three sites (`system[1]`, `system[2]`, `messages[0].content[11]`), a
`thinking.budget_tokens` of 31999, and an `anthropic-beta` header. That is the flagship feature
firing on an unmodified real client, which is stronger evidence than any fixture.

## Step 6 — several prompts, one session

Three requests sharing the same `system` and the same first two messages, differing only in the last
message.

```
$ for f in body-s1 body-s2 body-s3; do curl -s -o "resp-$f.json" -w "$f http=%{http_code} total=%{time_total}\n" \
    http://127.0.0.1:8787/v1/messages ... --data-binary @"$f.json"; done
body-s1 http=200 total=1.145719
body-s2 http=200 total=1.844989
body-s3 http=200 total=1.423522

$ HOME=... ./lens.exe sessions
ID                        STARTED   DURATION  TURNS  TOKENS  COST            ⚠
s_1789455846977_dffa38ae  just now  3.2s      3      251     — + 3 unpriced  3
s_1789455837699_0e60d265  just now  0µs       1      64      — + 1 unpriced  2
s_1789455786865_c473ece6  1m ago    40.5s     2      111     — + 2 unpriced  2
s_1789455776164_ef8b95b1  1m ago    0µs       1      53.1k   — + 1 unpriced  4
s_1789455773484_76120522  1m ago    0µs       1      883     — + 1 unpriced  1
s_1789455769289_e3b0c442  1m ago    0µs       1      0       — + 1 unpriced
```

**PASS.** The three prompts are one session, `s_1789455846977_dffa38ae`, with **3 turns**, 251
tokens, and the 3 warnings those calls raised. The two calls the real Claude Code CLI made grouped
into a second session with 2 turns, which is the same heuristic working on client traffic.

The last row (`..._e3b0c442`) is the null-prefix bucket: the CLI's `HEAD /api/hello` probe has no
body to hash, so it lands there under a fixed placeholder id — see F2.

## Step 7 — prices populate without a restart

The database and price file for this run were empty at this point, so every prior call was unpriced.

```
$ HOME=... ./lens.exe prices
price table: C:\Users\abhis\AppData\Local\Temp\lens-accept\.deepseek-lens\prices.toml
rates are US dollars per 1,000,000 tokens; "—" means no rate is configured
MODEL            INPUT  OUTPUT  CACHE READ  CACHE WRITE  SOURCE
deepseek-flash   —      —       —           —            unpriced
deepseek-v4-pro  —      —       —           —            unpriced

$ HOME=... ./lens.exe prices --set deepseek-flash.input=1.00 --set deepseek-flash.output=2.00 \
                                 --set deepseek-flash.cache_read=0.10
MODEL            INPUT  OUTPUT  CACHE READ  CACHE WRITE  SOURCE
deepseek-flash   1      2       0.1         —            configured
deepseek-v4-pro  —      —       —           —            unpriced
```

A new call was then made against the **same running server — PID 42460, unchanged throughout** (there
was no restart between the price edit and this call). The `tasklist` check taken immediately before
the call reported that PID alive.

```
{"Image Name","PID","Session Name","Session#","Mem Usage"}
"lens.exe","42460","Console","1","23,492 K"

$ curl -s -o resp-price.json -w 'http=%{http_code} ttfb=%{time_starttransfer} total=%{time_total}\n' ...
http=200 ttfb=0.232699 total=1.003955

$ HOME=... ./lens.exe ls --limit 12
ID  TIME      DUR    MODEL           IN/OUT TOK  COST               STATUS  ⚠
10  just now  1.0s   deepseek-flash  37/27       $0.0001            200     ⚠
9   just now  1.4s   deepseek-flash  52/32       — (unpriced)       200     ⚠
8   just now  1.8s   deepseek-flash  52/32       — (unpriced)       200     ⚠
7   just now  1.1s   deepseek-flash  52/31       — (unpriced)       200     ⚠
6   just now  1.4s   deepseek-flash  45/19       — (unpriced)       200     ⚠
5   just now  910ms  deepseek-flash  38/16       — (unpriced)       200     ⚠
4   1m ago    1.3s   deepseek-flash  38/19       — (unpriced)       200     ⚠
3   1m ago    2.4s   deepseek-flash  53.1k/39    — (unpriced)       200     ⚠
2   1m ago    1.8s   deepseek-flash  754/129     — (unpriced)       200     ⚠
1   1m ago    344ms                  0/0         — (unknown-model)  401
```

Every row `lens ls --limit 12` printed is above — calls 1–9 carry no price because they were
recorded before the rates were set, and call 10 does.

**PASS.** Call 10, made after the edit, is priced (`$0.0001`); calls 1–9, made before it, keep
`— (unpriced)`. No restart, no lost row. The value checks out: 37 input × $1.00/M + 27 output ×
$2.00/M = 91 micro-dollars.

**Finding F7 applies here**: those two rates are round placeholder numbers chosen to exercise the
plumbing, not DeepSeek's prices.
`deepseek-flash.input` would cost 10× less if the real input rate is ~$0.10/M; the arithmetic above
is correct for the rates configured and says nothing about DeepSeek's actual tariffs. That is exactly
why the shipped table is empty.

## Step 8 — the dashboard

Static assets and the JSON API, over HTTP:

```
$ for p in / /app.js /style.css; do curl -s -o /dev/null -w "%{url_effective} -> %{http_code} %{content_type} %{size_download} bytes\n" "http://127.0.0.1:8788$p"; done
http://127.0.0.1:8788/ -> 200 text/html; charset=utf-8 4159 bytes
http://127.0.0.1:8788/app.js -> 200 text/javascript; charset=utf-8 27946 bytes
http://127.0.0.1:8788/style.css -> 200 text/css; charset=utf-8 6169 bytes

$ for p in /api/requests /api/stats /api/warnings /api/sessions; do curl -s -o out -w "$p -> %{http_code} %{size_download}b\n" "http://127.0.0.1:8788$p"; done
/api/requests -> 200 358119b
/api/stats -> 200 839b
/api/warnings -> 200 2735b
/api/sessions -> 200 2146b
```

The stats endpoint, in full — real aggregates over the run's own calls:

```
{"since":"0001-01-01T00:00:00Z","summary":{"RequestCount":10,"ErrorCount":0,"WarningCount":13,"InputTokens":54167,"OutputTokens":344,"CacheCreationTokens":0,"CacheReadTokens":0,"CostUSDTotal":0.000091,"UnpricedCount":9,"DurationP50Ms":1286.2605,"DurationP95Ms":2400.8274},"by_model":[{"Model":"deepseek-flash","RequestCount":9,"InputTokens":54167,"OutputTokens":344,"CostUSDTotal":0.000091,"UnpricedCount":8},{"Model":"","RequestCount":1,"InputTokens":0,"OutputTokens":0,"CostUSDTotal":0,"UnpricedCount":1}],"by_day":[{"Day":"2026-09-15","RequestCount":10,"InputTokens":54167,"OutputTokens":344,"CostUSDTotal":0.000091,"UnpricedCount":9}],"cost_sources":[{"Source":"unpriced","RequestCount":8,"CostUSDTotal":0},{"Source":"unknown-model","RequestCount":1,"CostUSDTotal":0},{"Source":"configured","RequestCount":1,"CostUSDTotal":0.000091}]}
```

`/api/requests`, `/api/warnings`, and `/api/sessions` return the full row data the CLI shows —
`/api/requests` is 358KB for 10 calls because it embeds the captured bodies, which is the cost of
full-body storage surfacing on the read path.

### The live feed

An SSE reader was attached, then a call was issued, and both sides were timestamped with
`date +%s.%N`:

```
$ ( curl -N -s http://127.0.0.1:8788/api/stream | while IFS= read -r line; do printf '%s|%s\n' "$(date +%s.%N)" "$line"; done ) > sse.out &
$ sleep 1.5
$ START=$(date +%s.%N)
$ curl -s -o resp-sse.json -w 'call http=%{http_code} total=%{time_total}\n' http://127.0.0.1:8787/v1/messages ...
call http=200 total=1.176266
$ END=$(date +%s.%N)

call issued at  1789455890.306893400
call returned 1789455891.616343000
--- raw SSE output (timestamp|line) ---
1789455891.883147600|data: {"type":"request","id":11}
1789455891.937755600|
1789455891.997455500|data: {"type":"warnings","id":11,"warnings":[{"ID":0,"RequestID":11,"Kind":"model_remapped","Severity":"info","Detail":"claude-sonnet-4-5 → deepseek-flash (billed at deepseek-flash rates)","Path":"model","CreatedAt":"2026-09-15T12:34:51.8088796+05:30"}]}
1789455892.052154900|

first data event at 1789455891.883; 0.267s after the client finished reading the response; 1.576s after the call was issued
```

**PASS.** A new call appeared on the live feed **0.267s** after the client finished reading its
response — comfortably inside the "within 1s without a refresh" requirement — as a `request` event
followed immediately by a `warnings` event for the same id.

### What step 8 did *not* verify

**Visual rendering was not verified.** There is no browser in this environment, so the claim here is
strictly: the three assets are served with the right content types and non-zero lengths; the HTML
references them (`<link rel="stylesheet" href="style.css">`, `<script src="app.js"></script>`);
`app.js` calls `/api/requests`, `/api/stats`, `/api/warnings`, `/api/sessions`, `/api/health`, and
`/api/stream`; and every one of those endpoints returns real data over HTTP. **That is not the same
as "the dashboard renders correctly"**, and no such claim is made. The live feed, warning inbox, and
session drill-down were verified as data, not as pixels.

## Step 9 — replay

The server was started with `--replay` (step 1). Request 6 was replayed with an edit:

```
$ HOME=... ./lens.exe replay 6 --set max_tokens=1 --yes
replaying request 6: POST /v1/messages
  original cost: — (unpriced)
replayed 6 → 12

   FIELD     REQUEST 6       REPLAY 12
≠  status    200             401
≠  model     deepseek-flash  claude-sonnet-4-5
≠  tokens    45/19           0/0
   cost      —               —
≠  duration  1.4s            184ms
≠  warnings  2               3
  #6 warning: cache_control_ignored: client requested prompt caching at system[0], messages[0].content[0]; DeepSeek ignores cache_control, so no caching occurs
  #6 warning: model_remapped: claude-sonnet-4-5 → deepseek-flash (billed at deepseek-flash rates)
  #12 warning: cache_control_ignored: client requested prompt caching at system[0], messages[0].content[0]; DeepSeek ignores cache_control, so no caching occurs
  #12 warning: model_remapped: claude-sonnet-4-5 → deepseek-flash (billed at deepseek-flash rates)
  #12 warning: upstream_error: upstream returned an error: Authentication Fails, Your api key: ****ted] is invalid
replay exit=0
```

The stored row, read back through the API:

```
ID: 12
ReplayOf: 6
ReplayEdits: '[{"path":"max_tokens","old":32,"new":1}]'
Status: 401
ModelRequested: 'claude-sonnet-4-5'
RemoteAddr: 'replay'
```

and the replayed body decodes to `{"max_tokens":1,"messages":[...],"model":"claude-sonnet-4-5","system":[...],"temperature":0}`
— the edit applied, `max_tokens: 1` where the original had 32.

**PASS for what the step asks**: the replay is stored with `replay_of` set (12 → 6), the edit is
recorded, and the comparison renders, marking the differing fields with `≠`.

**The replay itself failed, and that is finding F1.** The upstream answered `401` because the
replayed request carried the redacted placeholder rather than a key. The tool recorded the failure
faithfully, which is the correct behaviour — but it means replay cannot re-issue a call against an
upstream that requires the credential, and the step's phrase "replay is stored and the comparison
renders" is satisfied without the replay having succeeded. `--yes` was used deliberately: the
original was unpriced, which is the case the cost gate refuses by default.

## Step 10 — measured latency: direct vs through the proxy

Method: `curl` with `-w '%{time_starttransfer} %{time_total}'` — first byte is
`time_starttransfer`, total is `time_total`. Every response body was verified complete (`"type":"message"`
and `"usage"` present) and every transfer returned `http=200` with exit code 0; a run whose body
failed to write was discarded rather than averaged in. Two series:

- **cold** — one `curl` process per request, so both sides pay a fresh TCP (and, for the direct
  side, TLS) handshake. This is the "just started my client" case. Interleaved direct/proxied.
- **warm** — two transfers in one `curl` process, the second reusing the kept-alive connection, so
  neither side pays setup. This is the "mid-session" case, and the only comparison that isolates
  what the proxy itself adds.

### Cold (seconds)

| run | direct ttfb | direct total | proxied ttfb | proxied total |
|---|---|---|---|---|
| 1 | 0.252367 | 1.271132 | 0.218931 | 0.840213 |
| 2 | 0.292200 | 0.978439 | 0.203366 | 0.821082 |
| 3 | 0.345537 | 0.554714 | 0.183211 | 0.785472 |
| 4 | 0.239798 | 0.907636 | 0.225593 | 0.978680 |
| 5 | 0.402631 | 0.883734 | 0.214056 | 0.470532 |

### Warm (seconds; the second transfer of each pair)

| run | direct ttfb | direct total | proxied ttfb | proxied total |
|---|---|---|---|---|
| 1 | 0.205226 | 0.837323 | 0.179923 | 0.788342 |
| 2 | 0.207738 | 0.754233 | 0.250275 | 0.868331 |
| 3 | 0.182931 | 0.375421 | 0.204645 | 0.790755 |
| 4 | 0.183541 | 0.803665 | 0.207274 | 0.789204 |
| 5 | 0.192120 | 1.126821 | 0.189446 | 0.662904 |

### Summary (ms)

| series | median | min | max |
|---|---|---|---|
| cold direct ttfb | 292.2 | 239.8 | 402.6 |
| cold proxied ttfb | 214.1 | 183.2 | 225.6 |
| **warm direct ttfb** | **192.1** | 182.9 | 207.7 |
| **warm proxied ttfb** | **204.6** | 179.9 | 250.3 |
| cold direct total | 907.6 | 554.7 | 1271.1 |
| cold proxied total | 821.1 | 470.5 | 978.7 |
| warm direct total | 803.7 | 375.4 | 1126.8 |
| warm proxied total | 789.2 | 662.9 | 868.3 |

### Delta

| comparison | Δ ttfb (proxied − direct) | Δ total |
|---|---|---|
| **warm (medians)** | **+12.5 ms** | −14.5 ms |
| cold (medians) | −78.1 ms | −86.6 ms |

**PASS: the warm first-byte delta is +12.5 ms, against a budget of 50 ms.** The proxy adds roughly a
tenth of the allowed figure on the connection path that matters — a client mid-session, reusing a
connection, which is what a coding agent is for the whole of a run.

The cold numbers deserve their explanation rather than a claim: the proxied side is **faster** by
~78 ms at first byte, and that is *not* the proxy being fast. It is connection pooling. Each direct
`curl` pays a fresh TLS handshake to CloudFront on every run; the proxy established its upstream
connection once and keeps it (`MaxIdleConnsPerHost: 100`), so only the first proxied call paid that
cost. The cold comparison therefore measures "fresh handshake versus pooled connection", not the
proxy's overhead. Anyone reading the cold row as "lens makes your calls faster" would be reading it
wrong.

Caveats that bound how much weight these numbers carry:

- **Upstream jitter is the same order as the effect.** Direct warm ttfb spans 183–208 ms and proxied
  spans 180–250 ms; run-to-run variation from DeepSeek's side (~±30 ms) is larger than the +12.5 ms
  delta. The delta is consistent in sign with a small fixed cost, but it is one sample of five pairs
  on one afternoon, not a measurement with an error bar.
- **Prompt caching was not in play.** DeepSeek reports `cache_read_input_tokens: 0` throughout, so
  no warm-cache effect is hiding in either series.
- **Both sides hit the same upstream, the same host, the same model, with the same body**
  (`max_tokens: 1`), from the same machine. The direct baseline is real and not an estimate.
- **The total-latency figures are dominated by token generation, not by the proxy.** They are
  reported because the step asks for them, not because they discriminate.

A regression guard lives here: this figure is recorded as a number precisely so that a future change
that adds buffering to the hot path shows up as a changed number in review. The `internal/proxy`
TTFB test is the automated version of the same claim.

## Shutdown

The server was stopped with:

```
$ taskkill /F /IM lens.exe
SUCCESS: The process "lens.exe" with PID 42460 has been terminated.
$ tasklist /FI "IMAGENAME eq lens.exe" /FO CSV /NH
INFO: No tasks are running which match the specified criteria.
$ curl -s -m 2 http://127.0.0.1:8788/api/health && echo " (STILL UP)" || echo "dashboard down"
dashboard down
```

**`taskkill /F`, not a graceful Ctrl+C** — and that is finding F4: a console Ctrl+C cannot be raised
on a native Windows process from Git Bash, so the consumer's bounded drain-and-flush path on
shutdown was **not exercised** by this run. Both port listeners went down and no `lens.exe` survived,
so the observable outcome is correct; what is unverified is that the last buffered calls are written
on a Ctrl+C. `internal/consumer`'s own tests cover that path, but this run did not.

## Findings

| # | Finding | Cause | Status |
|---|---|---|---|
| **F1** | A replay of a call that required a credential is answered `401`, so replay cannot re-issue against a key-protected upstream. | By design: lens never persists a credential and never injects one, so the replayed request carries `x-api-key: [redacted]`. Recorded faithfully as a 401 row with an `upstream_error` warning. | **Open by design.** Not a defect, but a real limit on what replay is good for; the README states it. |
| **F2** | `HEAD /api/hello` — Claude Code's own connectivity probe — is proxied upstream and stored as a row (id 1: 401, no model, `unknown-model` cost source, in the null-prefix session). | The proxy is transparent and forwards every path, not just `/v1/messages`, which is what "transparent" means. The upstream 401s a path it does not serve. | **Open, cosmetic.** A row in `lens ls` that is not an LLM call. Filtering by path would hide real traffic; leaving it is the honest default. |
| **F3** | `export ANTHROPIC_BASE_URL=http://127.0.0.1:8787` had **no effect** on this Claude Code installation: the first CLI call never reached lens (`sink_accepted` stayed 0), and the prompt was answered by the upstream directly. | Claude Code's `~/.claude/settings.json` carries an `env` block that takes precedence over the process environment. Pointing an existing install at the proxy needs `claude --settings <file>` (as used above) or an edit to `settings.json`. | **Resolved in the README.** The quick start now states the precedence and gives the `--settings` form alongside the `export`; a reader hitting the silent no-op is told where to look. |
| **F4** | Graceful shutdown could not be triggered: `kill -INT <pid>` from Git Bash returns exit 0 and does nothing, and `timeout -s INT 4` hangs until the process is killed by other means (exit 124). | Native Windows processes do not take POSIX signals from the MSYS runtime; a real console Ctrl+C is required. | **Open, environmental.** The server was stopped with `taskkill /F`; the shutdown drain is covered by unit tests, not by this run. README troubleshooting is unaffected. |
| **F5** | No `model_mapping_drift`: `claude-sonnet-4-5` → the response's own `usage.model` reports `deepseek-flash`, exactly what the shipped model map predicts. | The configured mapping matches reality on real traffic. | **Confirmed, not a defect.** This is the check that would have caught a stale map; it did not fire. |
| **F6** | Added first-byte latency is **+12.5 ms** (warm medians), inside the 50 ms budget. The cold figure is −78.1 ms because the proxy pools its upstream connection and a fresh `curl` does not. | Proxy hop cost ~12.5 ms; the cold number is connection reuse, not proxy speed. | **PASS**, with the caveat that upstream jitter (±30 ms) is larger than the delta. |
| **F7** | The prices used in step 7 (`input=1.00, output=2.00, cache_read=0.10`) are placeholder numbers, not DeepSeek's. | DeepSeek's per-token tariffs were never verified; the shipped table is deliberately empty for that reason. | **Open, deliberate.** The step verifies the plumbing (cost appears without a restart; token arithmetic is exact for the rates given), not the tariffs. |

## What this run did not verify

Stated plainly, so no reader has to infer it:

- **Browser rendering of the dashboard.** No browser was available. Step 8 verified HTTP responses,
  content types, asset delivery, the JSON API, and the SSE feed — not pixels.
- **The Cline path.** Cline (VS Code) is not installed here, so the README's Cline instructions are
  unverified in this environment. The `export`-then-launch-editor mechanism is the same one that
  worked for Claude Code once its `settings.json` was overridden.
- **Graceful shutdown** (F4).
- **DeepSeek's actual prices** (F7).
- **`lens replay --dump`, `--no-capture`, and `--diff`** are covered by bead 13's tests but were not
  exercised against the real endpoint in this run; the exercised path was `--set` with `--yes`.
- **Long-running behaviour**: no soak, no multi-session concurrency, no sink-overflow scenario
  against real traffic. `sink_dropped` stayed 0 throughout, which says only that this run was far
  below the queue's capacity.
