# GI-27 — Match DeepSeek's daily usage page, and port claude-lens's capture-size, archival and lifecycle model

**Ticket**: GI#27 (**issue not yet created**. #24 is the highest issue and #26 the highest PR — #26 is the
merged `develop`→`main` promotion — so 27 is the next free number; create the issue first and rename this
file if GitHub assigns another) ·
**Branch**: `GI-27-billing-fidelity-cap-archival-lifecycle`, **cut from `main`** (no `develop`, §11) ·
**Plan version**: v14 · **Status**: v2 converged (2 review rounds); v3–v5 add workstreams B–F and the
branch-flow change; **v14 is converged through round 13** (round 13 raised no findings). Ready to beadify.

## 1. Origin

The sibling repo `D:\github\claude-lens` (same architecture, later) already went through matching its
per-day cost to DeepSeek's platform usage page (its GI-11: 2026-09-21 page read **$3.25**, clens read
**$0.82**), then bounded its store with body archival and gained `shutdown` / `restart` / `reload` (its
GI-13, GI-16). This plan compares that outcome to deepseek-lens today and ports what applies, in six
workstreams:

- **A** — billing fidelity: usage survives the body cap (tail), days cut in the operator's zone (RC-1, RC-2),
  and the cap itself is raised by config to 8 MiB like claude-lens.
- **B** — lifecycle: `lens shutdown`, `lens restart`, `lens reload`.
- **C** — body archival (bodies only, per-UTC-day files, transparent reads).
- **D** — branch flow: drop the `develop` integration branch, matching claude-lens.
- **E** — performance: a covering index for the stats aggregates (claude-lens's GI-13 `idx_events_stats`).
- **F** — from/to range pickers with hour precision on the request list and the Stats tab (claude-lens GI-16 A).

Figures from deepseek-lens's live store (`~/.deepseek-lens/lens.db`, 18,325 rows) are **dated snapshots
(2026-09-25)**; the reproducible artifact is §8.

## 2. Comparison: what claude-lens has vs. deepseek-lens today

| claude-lens | deepseek-lens today | Action |
|---|---|---|
| No per-class/per-call rounding to a cent (GI-11 RC-A, the whole cost gap) | `pricing.Compute` sums exact `big.Rat`, rounds **once per call to a micro-dollar** ([pricing.go:159](../../internal/pricing/pricing.go#L159)) | **None.** Already sound. |
| DeepSeek rates + 2× peak window | Same rates in `~/.deepseek-lens/prices.toml`; peak window plus holiday calendar (GI-4, GI-24) | **None.** |
| Cache-write rate 0 for DeepSeek | Absent rate → input rate, labelled `approximate` | None. |
| `capture_complete` merge laundering (RC-B) | No cross-source merge | **N/A.** |
| Body cap 256 KB → 2 MB default, then **8 MiB by config** | Default and live config are 256 KB; usage is parsed from the capped body | **Defect (RC-1)** → A.1 tail **and** A.3 config raise. |
| Local-day window picker (C-1) | Stats picker and by-period buckets are UTC only | **Defect (RC-2)** → A.2. |
| `clens reprice` for history | none | Not needed: no rate/formula change. |
| `shutdown` (`POST /api/shutdown`), `restart`, `reload`, `serve.state.json` (GI-13/16) | Only SIGINT (`signal.NotifyContext(os.Interrupt)`, [serve.go:141](../../internal/cli/serve.go#L141)); `/api/health` exists; no state file, no relaunch, no reload | **Gap** → workstream B. |
| Body archival: `body_archive` marker table, `archive/bodies-YYYY-MM-DD.db`, `HotDays` 7 (default), `archive status\|run\|restore` (GI-16 C) | Hard-delete retention only (`PurgeOlderThan`, GI-17); bodies are ~all of the file | **Gap** → workstream C. **deepseek-lens diverges:** default `HotDays = 0` (opt-in), not 7 — see §6 C.2 Config. |
| No `develop`: feature branch PRs into `main`, guards adapted | `develop` intermediate branch; `branch-guard.yml` / `main-guard.yml` gate on it | **Change** → workstream D. |
| `idx_events_stats` covering index over the stats aggregates (GI-13 C-8); `idx_events_session_started` | Only `idx_requests_started_at` and `idx_requests_session_id`; stats aggregates read wide rows. **Measured slow (§11A).** Session index: not needed here (session queries are `session_id = ?` point aggregates, 1 ms) | **Applicable** → workstream E. |
| Calls window picker with `custom` from/to `datetime-local` (hour precision), backend already had `since`+`until` | `/api/requests` and the feed take `since` only — **no `until`**; Stats has date-only From/To | **Gap** → workstream F (needs a small backend change claude-lens did not). |

## 3. Root causes (workstream A)

### RC-1 — usage sits at the end of the stream, the capture keeps the start

The proxy tees the response into a `boundedBuffer` that keeps the **first** `BodyCapBytes` (262,144) and
drops the rest ([proxy.go](../../internal/proxy/proxy.go)). The consumer runs `parse.ExtractUsage` on that
body ([consumer.go](../../internal/consumer/consumer.go)). An SSE stream reports input/cache tokens in
`message_start` (head) but the **output-token count in the final `message_delta`** (tail). A response
over the cap therefore prices with `output_tokens = 0`.

Measured (snapshot, 2026-09-25): rows with `length(resp_body) >= 262144`: **1,487 of 18,325 (8%)**,
**all** with `output_tokens = 0`; per day 5–9% of rows (unreproduced — see §8). Their `cost_usd`
sums to $1.06; total `cost_usd` denominator ≈ $16 (unreproduced). Ordinary 200 `/v1/messages` rows
average 484 output tokens (unreproduced); the rows that overflow are long-thinking responses, so the
true loss is at least that and probably more — not measured because the tail was never stored.

**Residual:** compressed responses over the cap decode only to a prefix; their usage is also
unrecoverable. Not measured, not mitigated (no upstream `Accept-Encoding` stripping) — accepted ceiling.

### RC-2 — days are cut in UTC only

`Store.StatsByPeriod` buckets with UTC `strftime` and the Stats picker sends UTC-midnight bounds
(`utcDayBound`). A usage page is read against a calendar day in the operator's zone, so a daily total
from lens can never equal it unless that zone is UTC. (claude-lens's C-1: local-day $0.82 vs UTC-day
$0.75.) Which zone DeepSeek's page uses is **unverified** — hence a selector, not a hardcoded choice.

## 4. Workstream A — changes

### A.1 Usage tail (RC-1)

| File | Change |
|---|---|
| `internal/proxy/proxy.go` | `boundedBuffer` gains `tailCap`/`tail`: bytes past the head cap are kept as a trailing window of `usageTailBytes` (16 KiB); `Tail()` returns it (nil when nothing overflowed). Response buffer only. Byte copy, **no parsing** — the hot-path rule holds. `submit` passes the tail on. |
| `internal/sink/sink.go` | `CapturedCall.RespTail []byte`. Not stored in the DB. |
| `internal/parse/sse.go` | `ExtractUsageWithTail(head, tail, contentType)`: for `text/event-stream` with a tail, feed head, `"\n\n"` (closes the torn head event), tail (its torn first event fails to parse and is skipped). Otherwise the plain path. `ponytail:` at its top: JSON over the cap still loses usage; upgrade path is tail-based JSON extraction. |
| `internal/consumer/consumer.go` | Use it; drop the tail when the **pre-decode** `call.RespHeaders.Get("Content-Encoding")` is non-empty. Must test `call.RespHeaders`, not the post-decode `respHeaders` — `decode.Body` strips `Content-Encoding` from its returned header clone on every successful decode, so the decoded headers never fire it. |

**Why keep the tail now that the cap goes to 8 MiB (A.3):** at 8 MiB the overflow rate is near zero, but
the tail costs 16 KiB per response and turns "usage lost above the cap" from a silent under-bill into a
non-event for any stream, however long. It is the backstop; the cap raise is the bulk fix.

### A.2 Local-day stats (RC-2)

| File | Change |
|---|---|
| `internal/store/store.go` | `StatsByPeriod(..., tzOffsetMin int)`: shift `started_at` by `tzOffsetMin*60` s inside `strftime`. Fixed offset, `ponytail:` note on DST. |
| `internal/api/api.go` | `?tz_offset=` minutes east of UTC, default 0, rejected outside −720..840 (400); passed to the store. |
| `internal/cli/stats.go` | Pass `0` (CLI stays UTC). **Known, accepted divergence:** `lens stats` buckets in UTC while the dashboard defaults to Local, so the two show different daily totals for the same data when the zone is not UTC. No `--tz-offset` flag. |
| `internal/web/index.html`, `app.js` | "Days in" selector (Local / UTC / UTC+8), default Local. Bounds come from the shared `hourBound` helper (§11B), which replaces the prototype's `dayBound` — never `new Date("YYYY-MM-DD")` (silently UTC) — and the same offset is sent as `tz_offset`. **The active zone is labelled on the chart heading or picker** so totals can be reconciled against `lens stats`. |

### A.3 Body cap raised to 8 MiB — by config, not by default

claude-lens raised its cap to 8 MiB **in config** (its shipped default is 2 MiB). Same here:
`BodyCapBytes = 8388608` in `~/.deepseek-lens/config.toml` (or `--body-cap-bytes` / `LENS_BODY_CAP_BYTES`).
The shipped default stays 262144 — no code change, so the 256 KB-cap tests and docs stay true. Two facts
the plan depends on, to be re-verified in code at bead time:

- `cons.SetBodyDecoding(cfg.BodyCapBytes)` ([serve.go:115](../../internal/cli/serve.go#L115)) reuses the same value as the decoded-size limit, so the
  raise applies to both.
- **Worst-case memory is 4096 × 2 × cap** (sink capacity × both bodies per call). At 8 MiB that is
  4096 × 16 MiB ≈ 64 GiB — 32× the current bound of ~2 GiB at 256 KB. This is a structural ceiling
  that exists today and is multiplied by the cap raise. The `doctor` WARN when `BodyCapBytes > 262144`
  and `HotDays == 0` names this ceiling explicitly so the operator can make an informed decision.
- **Do not raise the cap before archival (C) and the stats index (E) are live.** At 8 MiB the hot file grows
  several times faster than today's 4.7 GB store; archival bounds it, and the index keeps the stats queries
  from reading it (§11A: they already take 22–56 s cold at today's size). Ordering: E and C before this.

`lens doctor` gains a WARN when `BodyCapBytes > 262144` and `HotDays == 0` (large bodies, no archival, high
memory ceiling).

## 5. Workstream B — `lens shutdown`, `lens restart`, `lens reload`

Ported from claude-lens GI-16 workstream B, shrunk to what deepseek-lens has.

**Why:** `serve` fronts a live coding session; on Windows a running `lens.exe` cannot be replaced.
Stopping it today means Ctrl-C in the right console, and a relaunch is by hand.

### B.1 Serve runtime state file

`serve` writes `<dir of DBPath>/serve.state.json` — created early and rewritten with the bound addresses
after both listeners are up (see the write-timing bullet below):
`{"pid","exe","args","cwd","started_at","proxy_addr","dashboard_addr","log_path"}`, `0600`, removed on
graceful exit.

- `exe` = `os.Executable()`, `args` = `os.Args[1:]`, so **`args` already begins with `serve`**; restart
  relaunches as `exe <args…>` and never prepends a second `serve`.
- `cwd` = `os.Getwd()` at serve start; `restart` sets `cmd.Dir` to this value so relative paths in `args`
  (e.g. `--db-path`) resolve against the original working directory.
- Liveness is decided by dialing `/api/health`, **never** by the file or the pid; a leftover file is a hint.
- No secret in it (paths and addresses). `log_path` default `<dir of DBPath>/serve.log`.
- **"Both listeners are up" requires a `net.Listen` split.** Bead 08 refactors both `proxySrv` and `dashSrv`
  from `ListenAndServe` to `net.Listen` + `Serve`.
- **The state file is written twice, and the early write is what closes the boot window.** It is **created
  early** — right after `Validate`, before `store.Open` and the other pre-listener phases (`checkRedaction`,
  `purgeOnStartup`, the first-start `CREATE INDEX`, B.3 step 7) — carrying `pid`/`exe`/`args`/`cwd`/
  `started_at` with the two address fields not yet bound, and is **rewritten** after both `Listen` calls
  succeed and before the `select`, so `proxy_addr` and `dashboard_addr` end up as the actual bound addresses.
  **The rewrite is done by the process that won both binds, whether or not it created the file (F10.1):**
  ownership of the file follows the **successful bind**, not the create — the port winner is the real "serve
  is running" claim — so a creator that loses its bind leaves the file to the winner and neither rewrites nor
  removes it (the removal bullet below). Without the early write a booting `serve` is indistinguishable from a
  dead one for the whole pre-listener window: both ports refuse and only a predecessor's file is on disk
  (F6.5).
- **The early create is `O_EXCL`; it never clobbers another process's file (F7.2).** A second `lens serve`
  must not overwrite a live winner's file with its own `pid` — if it did, the loser's later graceful exit
  would pass the pid-guard and delete the live winner's file, leaving `restart` unable to act (the pid-guard
  protects the file's owner, and the loser would have made itself the owner). The create is therefore
  `OpenFile(..., O_CREATE|O_EXCL|O_WRONLY, 0600)`: on `EEXIST` the process reads the existing file and takes
  it over **only when it is genuinely stale** (its recorded pid reads **dead** by the pinned `isProcessAlive`
  contract below — a definite "no such process" alone; an inaccessible or undecidable probe counts as alive);
  a file owned by a live process is left untouched, and this process just proceeds to bind and, failing the
  bind, exits without writing or removing. **A process that took over a stale file also registers the shared
  removal `defer` (F7.4):** it is likewise covered by the pid-guard-plus-bind-gate removal — not a
  pid-guard-only one — so its lost-bind and early-return paths behave exactly like the creator's.
  **The bind-failure rule applies to the file's creator too
  (F10.1):** the `O_EXCL` owner is not necessarily the port winner — a creator slow through the pre-listener
  phases can lose the bind to a second process that saw its live pid and left the file alone — so a process
  that fails its `Listen` neither rewrites nor removes the file **whether or not it created it**; file
  ownership follows the successful bind (see the write-timing and removal bullets). **The ownership check
  gates the file *write*, not service liveness:** B.1's rule stands — liveness is dialing `/api/health`,
  never the file or the pid; a stale pid merely permits takeover, and a live pid never asserts a live
  service.
- **One shared removal function — BOTH the pid-guard and the negative bind gate — backs every `Serve` return
  after the create, called by the explicit normal-path teardown AND by a single `defer` (F7.4).**
  Each `Serve` return after the create — the pre-listener `return`s at
  [serve.go:49](../../internal/cli/serve.go#L49) (calendar build),
  [:54](../../internal/cli/serve.go#L54) (`store.Open`), [:119](../../internal/cli/serve.go#L119)
  (`proxy.NewServer`) and the normal-path return at
  [:200](../../internal/cli/serve.go#L200) — must leave the right file behind. There is **one** shared removal
  function applying both the pid-guard and the negative bind gate; the explicit normal-path teardown calls it,
  and so does a single `defer` registered right after a successful create (or a stale takeover, below). The
  `defer` covers the early-return paths; it is a no-op when the pid-guard does not match, when the file is
  already gone, or when the negative bind gate vetoes. **The deferred removal is not "pid-guard-only":** it is
  the pid-guard plus a bind gate that *trivially passes* for a process that never bound — nothing was bound,
  so nothing was lost — which is why the effective condition for a pre-`Listen` return reads as the pid-guard
  alone. **The bind gate only ever vetoes a process that attempted and lost a bind, so it never vetoes an
  early return;** a process that lost its `Listen` to a live winner *did* attempt and lose a bind, so the same
  gate vetoes its removal — and because a `defer` in `Serve` runs on *every* return, not just the
  pre-`Listen` ones, the failed-bind process (which returns via the normal path at
  [:200](../../internal/cli/serve.go#L200)) is gated by the shared removal too, not a pid-guard-only one. The
  normal shutdown path does not rely on this `defer` for ordering: it runs the explicit ordered teardown
  below, whose removal is **conditional** on the join — the one property a bare `defer` cannot express while
  a writer may be live.
- **Starting-vs-stale keys off pid-death — the *same* signal the `O_EXCL` takeover uses (F7.1, F8.1).** No age
  threshold can bound the boot window: the pre-listener phases include `purgeOnStartup`, which is **unbounded
  when `RetentionDays > 0`** (B.3 step 7), so a live booting `serve` — or a hung-but-alive one — can outlast
  any fixed grace and would then be misread as stale, its file removed, and a second instance spawned: the very
  outcome the early write exists to prevent. So the stale branch keys off the file's recorded `pid`, **not**
  `started_at`: a file is stale only when its pid is **not alive** — via the one `isProcessAlive` helper the
  `O_EXCL` takeover already needs, above (a small build-tagged helper: Windows
  `OpenProcess`+`GetExitCodeProcess`, Unix a `kill(pid, 0)`-style probe) — *in addition to* health refusing
  **and** both ports refusing. **The helper's error contract is pinned, and its failure mode is fail-closed
  (F9.1):** it reports **dead** only on the definite "no such process" result — Unix `ESRCH`, Windows
  `ERROR_INVALID_PARAMETER` — and reports **alive** on "exists but inaccessible" (Unix `EPERM`, Windows
  `ERROR_ACCESS_DENIED`) **and** on every other or indeterminate error. A probe that cannot decide therefore
  means *alive*, never *dead*: mapping "cannot determine" to "dead" is the one misclassification that errs the
  dangerous way — it would let this stale branch remove a live (booting or hung) `serve`'s file and spawn a
  second instance (the F7.1/F8.1 failure), and would let the `O_EXCL` takeover overwrite a live winner's file
  (the F7.2 clobber). **Both the `O_EXCL` takeover (above) and the B.2/B.3 stale branch read the helper the
  same way:** only the "no such process" result is dead; "exists but inaccessible" and any undecidable result
  are alive. This is the `O_EXCL` takeover's own signal, so the two stale tests cannot
  disagree: the booting `serve` wrote its own live pid into the early file, so it is **starting**, never stale,
  however long its boot runs. `--timeout` bounds only how long `shutdown`/`restart` *wait*, never this
  classification. **This is a file-ownership/staleness question, not service liveness** — it extends the
  `O_EXCL` reconciliation above: a dead pid merely permits removing the file, and a live pid never asserts a
  live *service* (only health does). Pid reuse only ever errs toward *starting* (treated as not-stale ⇒ falls
  through to the shutdown POST and the bounded wait, which times out rather than spawning a second instance) —
  fail-closed; so does an undecidable liveness probe, per the contract just pinned. The port bind still bounds
  the damage further.
- **Every client-side dial normalizes a wildcard host to loopback before connecting.** `listener.Addr()` on a
  wildcard bind (`0.0.0.0:port` / `[::]:port` — a case B.2 names as supported) returns the unspecified host,
  which is not dialable on Windows while the server is alive. Health (`GET /api/health`), the port-refusal
  probes, and the stale-file detection all dial through one helper that rewrites an unspecified host to
  loopback (`127.0.0.1` for IPv4, `::1` for IPv6). Without it the stale-file rule (B.2, B.3 step 3(b)) reads a
  live wildcard-bound serve as dead, deletes its state file, and lets `restart` spawn a second instance that
  loses the bind race. Test: a wildcard-bound ephemeral listener is judged live, not stale. **When the file's
  address fields are empty — the early write, the only file the *starting* branch can see — the dial falls
  back to the config-resolved `cfg.ProxyAddr`/`cfg.DashboardAddr` (F7.3): there is nothing yet bound in the
  file to dial, and `shutdown`/`restart` run `config.Load` before this point, so the configured addresses are
  in hand.**
- **State file removal is one shared function — the pid-guard plus the bind gate, stated negatively so both
  paths read as one contract (F10.1).** The file is removed only if its `pid` field equals `os.Getpid()`, **and no process that
  lost a bind to a live winner may remove it.** That single negative form covers both paths without
  collision: a process that lost its `Listen` to a live winner never removes — it *did* lose a bind — while a
  process that never bound at all (the early-return paths, the `defer` bullet above) did not lose a bind, so
  it is gated by the pid-guard alone and removes its own file when the recorded `pid` is still its own.
  **A process that fails its `Listen` — the file's creator as much as a second starter — therefore exits
  through the graceful path but neither rewrites nor removes the file.** The pid-guard alone does not save
  the winner here: until the winner's post-`Listen` rewrite lands, the file still carries the creator's
  early-write `pid`, so a creator that lost the bind would otherwise pass the guard and delete it. The process
  that won the bind rewrote the file with its own `pid`, so its exit is the one that removes it, and `restart`
  always finds a file naming the serving process.
- **State file removal is the very last act of shutdown,** after `st.Close()` returns. **On the normal-path
  teardown it is not a `defer`, because the removal is conditional on two things a bare `defer` cannot
  express:** the fail-closed join check below forbids it while a writer may still be live, and the bind gate
  (F10.1) forbids it for a process that lost its bind to a live winner. (The early-return paths, which never
  bound and have no live writer to join, use the shared, gated removal `defer` above — see the F7.4/O_EXCL
  bullets.) The shutdown path is instead an
  explicit ordered teardown at the end of `Serve`: join the maintenance goroutine, call `st.Close()` explicitly
  — the `defer` at :57 is **kept** (F6.2: `sql.DB.Close` is idempotent, so the explicit call does the closing
  and the deferred second call is a no-op after removal; keeping it means the early `return` at
  [serve.go:117-120](../../internal/cli/serve.go#L117), where `proxy.NewServer` fails before any teardown runs,
  still releases the handle) — then run the shared, gated removal as the final statement. A restart or shutdown
  command waiting for the file to disappear can rely on the explicit close as proof that the store was closed
  cleanly.
- **There is exactly one 24 h maintenance goroutine, and the join covers it — alongside `<-consumerDone`,
  bounded by the drain bound; it checks `ctx.Done()` between operations so it does not hold the writer past
  context cancellation. Bead 08 joins this goroutine (turning the existing purge ticker's loop into a
  `WaitGroup`-tracked one); bead 12 folds the archiver into the same loop's body — purge, then archive, then
  `GCArchive` — rather than starting a second goroutine.** A second reader of `purgeTicker.C` would steal
  alternate ticks from the first (`time.Ticker.C` delivers each tick to exactly one receiver,
  [serve.go:166-177](../../internal/cli/serve.go#L166)), silently halving both jobs; one ticker, one goroutine,
  one join (F6.1).
- **Join-bound expiry is fail-closed.** If the drain bound expires with the purge/archiver goroutine still
  running, log it and **do not remove the state file** — the "file gone ⇒ drained and store closed" proof must
  never be claimed while a writer may still be live; `shutdown`/`restart` then time out (their documented
  behaviour) rather than proceed on a false proof. Iterations pass `ctx` into the store call so cancellation
  interrupts a long statement: `PurgeOlderThan` → `purgeWhere` already does
  ([store.go:955](../../internal/store/store.go#L955), `BeginTx(ctx)`); the archiver must do the same.

### B.2 `lens shutdown [--timeout 30s]`

`lens shutdown` strips its own `--timeout` **before** `config.Load`, exactly as B.3 step 1 does for `restart`:
`config.Load`'s flag set is closed (`config.go:270-290`, `ContinueOnError`) and `main.go:43` hands the whole
`os.Args[2:]` to the command, so an un-stripped `--timeout` would fail with "flag provided but not defined"
(F8.2). `--timeout` bounds only the bounded wait below.

`POST /api/shutdown` drives `serve`'s existing `stop` (the `signal.NotifyContext` cancel), so it reuses the
current bounded drain (`shutdownGrace` + the consumer's `shutdownBound`) — no second shutdown path.

**Security (this is a new write route on the dashboard listener — CLAUDE.md's "three write routes" becomes
five with reload):** same `replayOriginReject` `Origin`/`Host` allowlist, parameterized by action, **plus a
loopback-caller check** (the dashboard may be bound to `0.0.0.0`; a LAN caller gets 403). The
credentialless-guard reasoning in the GI-1 security self-review must be re-read for a route that is
disruptive rather than billable, and the loopback-caller check is what carries it. Both new routes are
listed in `docs/context/api-surface.md` and `security-and-permissions.md`.

`lens shutdown` first reads the state file and dials `GET /api/health`. **No file:** skip the stale-file branch
and dial health directly — if it answers, `POST /api/shutdown`; if it refuses, there is nothing to shut down,
so log and exit 0. (F6.5: B.2 previously left the no-file case implicit.) **With a file:** if health already
refuses **and** both the proxy and dashboard ports refuse a connection (dialed from the file, or from
`cfg.ProxyAddr`/`cfg.DashboardAddr` when the file is the early write with empty addresses — F7.3) **and** the
file's recorded `pid` is **not alive** (the `isProcessAlive` helper the `O_EXCL` takeover uses — B.1; the same
signal, so the two stale tests agree — F8.1) — the process is gone, crashed, killed, or
power-lost before its removal ran — log "stale state file, removing", remove the file, and exit 0. The process
was not running; no `POST /api/shutdown` is needed and no timeout is burned. **If both ports refuse but the
file's pid is still alive,** `serve` is still in its pre-listener phases (or hung) (F6.5): treat it as
starting, do **not** remove the file, and fall through to the shutdown POST and bounded wait below (which
fails closed on `--timeout`).

When the process is running: `POST /api/shutdown`, then wait until both ports refuse a connection **and** the
state file no longer exists (bounded by `--timeout`). The state file disappearing is the proof that the
consumer drain finished and `st.Close()` returned — not merely that the ports closed.

### B.3 `lens restart [--exe PATH] [--timeout 30s]`

1. Strip `restart`'s own flags (`--exe`, `--timeout`) **before** `config.Load` — `config.Load`'s flag set is
   closed and rejects unknown flags, which would make `--exe` dead on arrival.
2. Read the state file. **No file:** restart refuses with "serve is not running (no state file at `<path>`);
   start with `lens serve`" and exits non-zero. With a file: retain its `exe`/`args`/`cwd` for rollback and
   spawn (the file is deleted by the graceful exit in step 3).
3. `GET /api/health`. Two sub-cases:
   - **(a) Running (200 OK):** proceed to step 4.
   - **(b) Health refuses:** also dial the proxy and dashboard addresses from the retained state file —
     or, when the file is the early write whose address fields are still empty, from `cfg.ProxyAddr` /
     `cfg.DashboardAddr` (F7.3; `restart` already ran `config.Load` after stripping its own flags) — through
     the loopback-normalizing dial helper (B.1). If a port still responds (e.g. serve is starting but
     health is not ready): treat as running and proceed to step 4. If **both** ports refuse **and** the file's
     recorded `pid` is **not alive** (`isProcessAlive`, B.1 — the same signal the `O_EXCL` takeover uses):
     **stale file** — the serve process crashed or was killed before its removal ran; log "stale state file,
     removing", remove the file (the pid-guard's purpose is to protect a concurrent winner, not block
     stale-file cleanup by a separate command), and skip directly to step 5. If **both** ports refuse but the
     file's pid is still alive (F6.5): `serve` is still in its pre-listener phases (or hung) — treat it as
     starting and proceed to step 4, where the shutdown POST and wait fail closed on `--timeout` rather than
     spawning a second instance.
4. `POST /api/shutdown`, wait until both ports refuse a dial **and** the state file is gone (bounded by
   `--timeout`) — this proves the drain finished and the store is closed.
5. Spawn `exe <args…>` **detached**, `cmd.Dir` set to the retained `cwd`, stdout+stderr appended to
   `log_path`. Windows `CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS`; Unix `Setsid` — two small
   build-tagged files.
6. Poll `/api/health` until 200 or `--timeout`.
   - **On success:** report pid, exe, log path and the measured gap.
   - **On timeout, no `--exe`:** print "timed out waiting for new process to bind; it may still be
     starting — check `lens doctor`"; do **not** kill the child (it may be mid-`CREATE INDEX` on first
     start after the E upgrade).
   - **On timeout or unhealthy, `--exe` given:** kill the new process, respawn the retained previous
     `exe`/`args`, set `cmd.Dir` to retained `cwd`, wait for health, print the log tail, exit non-zero
     naming which binary is serving.
7. **Pre-listener phases** — `store.Open` (idempotent schema + on the E upgrade: 20–60 s `CREATE INDEX`),
   `checkRedaction` (full-table scan), and `purgeOnStartup` (unbounded when `RetentionDays > 0`) all run
   before the listeners bind. The default 30 s health timeout covers a normal boot; pass
   `--timeout 180s` for the first restart after the E index build and for installs with large stores and
   retention enabled.

`--exe` is the answer to the locked-binary problem: build elsewhere, `lens restart --exe D:\build\lens.exe`.

### B.4 `lens reload`

`POST /api/reload` (same two guards as shutdown). The handler re-runs `config.Load(bootArgs)` where
`bootArgs` is the **translated** post-subcommand slice `serve` received — the exact slice passed to
`config.Load` at [serve.go:35](../../internal/cli/serve.go#L35), i.e. `translateNoCapture(os.Args[2:])`, **not**
the raw `os.Args[2:]` and **not** the state file's `args`. Raw args would break a running instance started
with `lens serve --no-capture`: `translateNoCapture` rewrites that alias to `--capture=false`
([serve.go:271](../../internal/cli/serve.go#L271)) and `config.Load`'s closed flag set rejects the unknown
`--no-capture` with a 400, making a valid instance unreloadable (F6.3). The state file's `args` are out too:
`flag.Parse` stops at the first non-flag, so `["serve","--x"]` would drop every flag and reload would diff
against defaults. The handler validates the reloaded config and diffs it against the boot config.

- **Live-apply set, exactly:** `RetentionDays`, `HotDays` — both held in one small `atomic`-guarded struct
  that the purge/archive ticker reads. **Implemented in two beads:** bead 10 creates the struct and wires
  `RetentionDays` and the API's `retentionDays int` copy (`api.go:99`, `SetRetention`) through it; bead 14
  adds `HotDays` and reads it in the archiver ticker (bead 14 depends on both bead 10 for the struct and
  bead 12 for the `HotDays` config key). **The API's own `retentionDays int` copy** (`api.go:99`,
  `SetRetention`) backs `GET /api/retention` and `POST /api/purge`. After a reload, this copy must also
  be updated through the same atomic struct — not set independently — so `GET /api/retention` and a
  concurrent purge see the new value. Prices already hot-reload through `pricing.Loader`; there are no
  accounts here, so claude-lens's `Accounts` arm does not port.
- Everything else that differs is reported under `restart_required` (addresses, upstream, DB path, body
  cap/policy, capture flag, calendar keys).
- Response `{"applied":[…],"restart_required":[…],"unchanged":bool}`. An invalid file changes nothing and
  returns 400 — **all-or-nothing**.

### B.5 Risks

| Risk | Handling |
|---|---|
| Testing restart against the live proxy kills the operator's session | Every test uses ephemeral ports and a temp DB; no test touches the configured live proxy/dashboard ports; live verification is the user's, with rollback as the net. |
| Child inherits the console and dies with it | Detach flags + a helper-process test that the child survives its parent. |
| Two restarts race | The loser fails to bind and exits without deleting the winner's state file — fail-closed on the port bind, never two proxies. (The exit code is not part of the guarantee: `Serve` today logs the listener error and returns `nil` ([serve.go:179-200](../../internal/cli/serve.go#L179)), so the loser may exit 0; no bead changes `Serve`'s return, and the port bind is what actually prevents two proxies — F6.4.) |
| `reload` half-applies | Validate, then apply both fields under one lock; test all-or-nothing on a bad file. |
| Wildcard dashboard bind exposes the routes to the LAN | Loopback-caller check (B.2). |
| Consumer still draining when ports refuse | `shutdown`/`restart` wait for state file gone (not just port refusal) — state file removal is deferred after `st.Close()`, which follows `<-consumerDone`. |
| Stale state file (serve crashed/killed, removal never ran) blocks restart/shutdown | If `GET /api/health` refuses **and** both ports refuse **and** the file's recorded pid is not alive (`isProcessAlive`, B.1): treat file as stale, log, remove it, skip the shutdown POST/wait, proceed. No timeout burned; no deadlock. |
| A booting (or hung) `serve` (pre-listener phases, both ports refusing) mistaken for dead | The file is written early (B.1) carrying the booting `serve`'s own **live** pid, and the stale rule (B.2, B.3 step 3(b)) removes a file **only when** its recorded pid is **not alive** — the *same* `isProcessAlive` signal the `O_EXCL` takeover uses. A live pid is never stale, so a booting (or hung) `serve` is treated as starting and **not** removed *regardless of how long it runs*; the classification depends on neither the (unbounded, `purgeOnStartup`) boot length nor any caller's `--timeout`, so no boot duration can weaken it. A misclassification — e.g. a reused pid — errs toward *starting* (falls through to the shutdown POST and the bounded wait, which times out): at most one *proxy* survives the bind race, and a spawned second instance fails to bind and exits without deleting the winner's file. |
| Two concurrent `serve` race for the state file | The early create is `O_EXCL` (B.1), and file ownership follows the **successful bind**, not the create (F10.1). A second process cannot overwrite a live file, takes over only a genuinely stale one, and — failing its bind — rewrites and removes nothing. The process that wins both binds rewrites the file with its own `pid` **whether or not it created it**, so its exit is the one that removes it: a **creator that loses the bind** leaves the winner's file (now carrying the winner's `pid`) in place, and `restart` acts on the serving process. |
| The maintenance goroutine (purge + archiver) still holds the writer when `st.Close()` runs | Joined via WaitGroup alongside `<-consumerDone` (bead 08 creates the join; bead 12 adds the archiver to the same goroutine's loop); checks `ctx.Done()` between batches. Required for the state-file guarantee in B.1. |
| Restart times out on first start after E index build | `--timeout 180s`; with no `--exe`, a timeout does not kill the child — it may still be starting. |

## 6. Workstream C — body archival

Ported from claude-lens ADR 011 / GI-16 workstream C.

### C.1 Policy in one paragraph

After `HotDays` (default **0** — archival disabled until the operator opts in; a recommended value is 7),
the two body columns (`req_body`, `resp_body`) of a row move into a **per-UTC-day zstd-compressed SQLite
file** `<dir of DBPath>/archive/bodies-YYYY-MM-DD.db`. The row itself — every scalar, both header blobs,
every cost/token column — **stays in the hot DB forever**, so every aggregate, session, warning and stats
query is untouched and still whole-history. A `body_archive` marker table (`request_id`, `day`,
`archived_at`, `body_mask`) records what moved. Reads that need a body (request detail, replay, export)
hydrate from the archive transparently. `--retention-days` keeps meaning "delete", now including the
archived bodies.

**Rejected (same reasons as claude-lens ADR 011):** whole-row archival (breaks every aggregate); `ATTACH`ing
day files (per-connection limit, a second write path); a marker column on `requests` (rewrites the wide
table for one bit); background `VACUUM` (holds the only writer for minutes — a documented manual step).

### C.2 What changes

| Area | Change |
|---|---|
| `internal/store` schema | New `body_archive` table — **a schema migration**: back up `lens.db` (and `-wal`/`-shm`) to a separate path first and state it (global rule), applied by the store's existing mechanism — `Open` runs `schema.sql`'s `CREATE TABLE IF NOT EXISTS` on every start ([store.go:121](../../internal/store/store.go#L121)); there is no migration framework ([schema.sql:1-8](../../internal/store/schema.sql#L1-L8)), so the table is simply added to that file (F6.6). |
| `internal/store/archiver.go` (new) | Batches: per UTC day older than the hot window, per row, **one hot transaction** writes the marker and NULLs the two bodies, **after** the day-file insert commits. Holds the single writer connection (`SetMaxOpenConns(1)`) in **short batches only** — claude-lens's GI-13 hang was a long hold of it. Crash between the two steps leaves a duplicate, never a loss (archive copy first, hot NULL last). |
| Compression | `github.com/klauspost/compress/zstd` — **already a dependency**, no new module. |
| Reads | **Read model decision:** `ListRequests` returns rows **without bodies by default** via a `Filter.WithBodies bool` flag; when false (the default for all `GET /api/requests` callers and every list-style CLI command), the query selects `NULL AS req_body, NULL AS resp_body` from `requestColumns`. `GetRequest`, `lens export`, `lens show`, and replay lookups set `WithBodies: true` to hydrate. **The real reader class (`WithBodies: true`)** (enumerate at bead time): `GetRequest` (`api.go:398, 493`), replay row lookup (`cli/replay.go:184`), `lens export` (`cli/export.go:45` — encodes the whole struct via `json.NewEncoder`; list-hydration must batch by archive day: group IDs per day file and open one day file per day, not one per row), `lens show` (`cli/show.go:68`). **Not body readers and not in scope:** the redaction self-test (reads `req_headers`/`resp_headers` only), the in-flight analyzers (act on the `CapturedCall`, not a stored row), and `store.go:1063, 1091` (`PurgeableBytes`/`UnpricedBytes` — SQL `LENGTH()` aggregates that run inline; archived byte counts for the Purge dry-run estimate are handled separately, not via `WithBodies` hydration). `ListRequests` callers (`api.go:367, 647, 672, 998` (session-detail handler — `ListRequests(... SessionID: id)`, returns no bodies), `cli/ls.go:62`, `cli/tail.go:51`, `cli/stats.go:84`) do **not** need bodies and must pass `WithBodies: false`. Hydration for each `WithBodies: true` caller comes from one store helper that tries the hot DB first, then the archive day file. Writers that read bodies skip archived rows and name `archive restore`. |
| Purge | `PurgeOlderThan` / `--unpriced` also delete the archived bodies (marker cascade + day-file row) and `GCArchive` collects orphaned day rows; the dry-run byte estimate counts archived bytes. |
| Config | `HotDays int` — `--hot-days`, `LENS_HOT_DAYS`, key `HotDays`, **default 0 (archival disabled until operator opts in; a typical value is 7)**, negative rejected. **Validation:** `HotDays > RetentionDays` is rejected only when `HotDays` was explicitly set above zero **and** `RetentionDays > 0`. A defaulted `HotDays = 0` is always valid regardless of `RetentionDays`. Test: `RetentionDays=3`, no `HotDays` set → `Validate` passes; `HotDays=9`, `RetentionDays=7` → `Validate` fails. `lens doctor` prints `HotDays`, WARNs when `archive/` is unwritable, and shows an INFO suggestion to enable archival when the store exceeds a size threshold and `HotDays == 0`. |
| `serve` | **One** goroutine, started **after** the listeners are up (never on the boot path). It runs the archiver once at boot, then on each tick of the existing 24 h timer runs `purgeOnStartup`, then the archiver, then `GCArchive` — one ticker, one receiver, so neither job steals the other's ticks (F6.1). Fail-open: an error is logged, never fatal. **Joined before `st.Close()`:** the goroutine checks `ctx.Done()` between batches and is waited on via a WaitGroup alongside `<-consumerDone` (bead 08 turns the existing purge-ticker loop into that join; bead 12 adds the archive/GC steps to the same loop's body — no second goroutine) — required for the B.1 state-file-removal guarantee. **First `serve` with `HotDays > 0` set will run the archiver's first pass and move/NULL bodies older than the hot window — a bulk data migration by the CLAUDE.md rule. Back up `lens.db` (+ `-wal`, `-shm`) to a separate path before enabling `HotDays` and state the backup path; bead 12's acceptance criteria include this requirement in the `doctor` output and the bead's completion note.** |
| CLI | `lens archive status` (hot boundary, archived/unarchived counts, archive size/files, missing markers, restore duplicates), `archive run [--dry-run] [--yes]`, `archive restore --since --until [--dry-run] [--yes]` (inverse: one hot transaction per row, never drop a marker for a body not restored, delete the day-file row only *after* the hot commit). Same `--yes` gate as `purge`. |
| Dashboard | Request detail shows "bodies loaded from the archive (YYYY-MM-DD)" when applicable. |

### C.3 Risks

- **Hot file does not shrink by itself** (SQLite free pages) — stated in README; `lens purge --vacuum` exists.
- **A copy of `lens.db` alone loses older bodies** — README/doctor say so.
- **Fault injection is the test:** stop after each archiver step and each restore step and assert the body is
  recoverable from one side.
- claude-lens took 12 review rounds on this design; the point of porting its ADR is not re-litigating it, but
  every claim in this section is a hypothesis until checked against deepseek-lens's `store` (different
  schema: no `transcript_content`, no merge, table is `requests`).

## 7. Test strategy

- **A:** `proxy_test` head+tail / `Tail()` nil before overflow; `parse/sse_test` torn head + torn tail
  recovers output tokens/input/stop reason and the head alone loses them (no vacuous pass); `consumer_test`
  capped head + tail priced with the tail's output tokens, and a call whose `call.RespHeaders` carries
  `Content-Encoding` ignores the tail (set the pre-decode headers directly); `store_test` 20:00Z and 02:00Z
  in two UTC days but one IST day (`330` → 1 bucket); `api_test` `tz_offset` invalid → 400, valid → 200.
- **B:** state file written/removed; the file is written early (before the pre-listener phases) so a booting
  `serve` owns it; **a pre-listener `Serve` return leaves no state file** — drive it both ways, a `store.Open`
  failure (a bad DB path) and an injected `proxy.NewServer` failure, and assert the file the early create made
  is gone (the shared, gated early-return `defer` — pid-guard plus bind gate, F7.4/F11.1); `/api/shutdown` and `/api/reload` reject a non-loopback caller and a
  cross-origin `Origin` (403), accept loopback; reload applies the two live fields including the API's
  `retentionDays` copy (`GET /api/retention` reflects the new value after reload), reports the rest under
  `restart_required`, and applies **nothing** on an invalid file; a `--no-capture` instance reloads (the
  translated `--capture=false` alias round-trips) rather than 400-ing (F6.3); restart against ephemeral ports
  with a helper-process child surviving its parent; rollback path with a deliberately unhealthy `--exe`;
  consumer drain slow: restart waits for state file gone, not just port refusal; pid-guard: a second process
  that fails to bind exits without deleting, **and without overwriting**, the winner's state file — the
  `O_EXCL` create leaves the winner's `pid` intact and the loser removes nothing (assert file survival *and*
  that the file's `pid` is still the winner's, not the exit code — F6.4, F7.2); **the CREATOR loses the bind
  while the second process holds the port ⇒ the file carries the SERVING process's `pid`, not the creator's
  (the winner's post-`Listen` rewrite, F10.1) and `restart` acts on it** — the creator's exit removes nothing
  (drive both orders: winner rewrites before the creator's `Listen` fails, and after); stale state file (dead pid):
  `restart` with a pre-seeded state file whose recorded `pid` is **not alive** (spawn-and-reap a helper to
  obtain a definitely-dead pid) and nothing listening removes the file and proceeds to **spawn** (step 5)
  without burning `--timeout`, while `shutdown` on the same file removes it and **exits 0 without spawning**,
  also without burning `--timeout` (F8.3); a state file whose pid **is** alive (a live helper process) with
  both ports refusing is treated as *starting*, not stale — not removed (F6.5); **pid-liveness boundary
  (F7.1/F8.1): a pre-seeded file whose pid is alive but whose `started_at` is far in the past, with both ports
  refusing and nothing listening, is treated as *starting* — not removed — the case the old
  age-and-`--timeout`-keyed rule got wrong**;
  `shutdown` with no state file dials health directly and exits 0 when it refuses; state-file removal happens
  **after** `st.Close()` (a store wrapper whose `Close` asserts the file still exists); a wildcard-bound
  ephemeral listener is judged live, not stale; join-bound expiry leaves the state file in place (fail-closed).
- **C:** archiver round trip (archive → hydrate → identical bytes); every crash point of archiver and
  restore; purge deletes archived bodies and the dry-run counts them; `HotDays` validation (no `HotDays`
  set + `RetentionDays=3` passes; explicit `HotDays=9` + `RetentionDays=7` fails); hydration for each
  `WithBodies: true` reader in the enumerated list; **positive control: `GET /api/requests?limit=50` with
  archived rows returns `req_body: null, resp_body: null`; no archive day file is opened;** cancel during
  an archiver batch — assert the state file is not removed until the batch returns (goroutine-join guarantee).
- **E:** plan assertions for the five aggregate statements (with a positive control) and result equality with
  and without the index; fixture row with a long `error_text` (10 KB) to confirm coverage; migration run on
  a **copy** of the live DB, timing recorded.
- **F:** `store_test` `Until` bound (half-open, with and without `Since`); `api_test` `?until` valid → 200,
  invalid → 400, `X-Total-Count` matches the ranged list; web asset guards per §11B.
- **D:** with `actionlint`/a dry read, the adapted guards accept a `GI-<n>-…` head into `main` and reject a
  non-`GI` head.
- **Gate:** `go build ./... && go vet ./... && go test ./...`; the TTFB test must stay green (hot-path guard).
  Manual: Stats tab Local vs UTC; `lens restart` against a scratch instance.

## 8. Reproduction queries (read-only)

Capped rows and their cost impact:
```sql
SELECT count(*), sum(output_tokens=0), sum(cost_usd)
FROM requests WHERE length(resp_body) >= 262144;
```
Total cost denominator: `SELECT sum(cost_usd) FROM requests;`

Per-day overflow rate:
```sql
SELECT date(started_at), count(*) total, sum(length(resp_body) >= 262144) capped
FROM requests WHERE status=200 AND path='/v1/messages' GROUP BY 1 ORDER BY 1;
```
Average output tokens on ordinary rows:
```sql
SELECT avg(output_tokens) FROM requests
WHERE status=200 AND path='/v1/messages' AND length(resp_body) < 262144 AND output_tokens > 0;
```
`count_tokens` rows: `SELECT count(*) FROM requests WHERE path='/v1/messages/count_tokens';`
Body share of the file (archival motivation): `SELECT sum(length(req_body)+length(resp_body)) FROM requests;`

`length()` on a BLOB is bytes, so the cap comparison is exact.

Stats-query plans and timings (§11A; run `mode=ro` against a **copy** where possible — the live file took
22–56 s per aggregate): prefix each aggregate from `store.go:438-620` with `EXPLAIN QUERY PLAN` and time a
cold and a warm run. The 2026-09-25 numbers came from a throwaway script, not a committed one.

## 9. Risks and edge cases (workstream A)

- **Tail seam parse:** the tail can begin inside a `data:` line; the fragment is skipped by `applyEvent`. A cut
  loses a `message_delta` only if the window is smaller than the events after it — 16 KiB ≫ the final events.
- **Non-streaming JSON over the cap** still loses usage; not observed; `ponytail:` ceiling (A.1).
- **Hot-path cost:** past the head cap each `Write` appends then memmoves the 16 KiB window
  (`proxy.go:255-256`) — ~100× copy amplification per small chunk, bounded and post-cap, TTFB unaffected; a
  lazy ring is the upgrade path if profiling shows it. TTFB test is the gate.
- **DST:** fixed-offset bucketing is an hour off across a change; accepted.
- **History stays wrong:** the 1,487 existing rows cannot be repriced (tail never stored). Say so in the
  README/PR rather than estimating.
- **Security:** `tz_offset` is range-checked and bound as a parameter; the tail holds response bytes already
  inside the redaction/loopback model.
- **Related, not fixed:** request counts include `count_tokens` (615) and non-200 rows; cost unaffected.

## 10. Context docs to refresh (for Phase 5.6)

- `docs/context/INDEX.md:83` — `StatsByPeriod` signature gains `tzOffsetMin`.
- `docs/context/data-model.md`/schema doc — `idx_requests_stats` (verify the index list there first).
- `docs/context/api-surface.md` — `/api/requests` gains `until`; `/api/stats` gains `tz_offset`; new `POST /api/shutdown`, `POST /api/reload`
  (write-route table grows).
- `docs/context/architecture.md` / `workflows.md` — capture path keeps a response tail; serve lifecycle and
  the single purge+archive maintenance goroutine; the `serve.state.json` early write (verify wording against
  source).
- `docs/context/data-model.md` (or the schema doc) — `body_archive` table; bodies may live in day files.
- `docs/context/cli.md`/build-and-run — `shutdown`, `restart`, `reload`, `archive`, `serve.state.json`.
- `docs/context/security-and-permissions.md:6` and `README.md:274` — body-cap note (usage survives the cap;
  cap configurable to 8 MiB) and the two new guarded routes.
- `docs/context/conventions.md` and `CLAUDE.md` "Conventions/Enforcement" — no `develop` (workstream D).
- `CLAUDE.md` "Fail open" bullet — "three write routes" becomes five.

## 11. Workstream D — drop the `develop` branch

Match claude-lens: a feature branch `GI-<n>-<slug>` is cut from `main` and its PR goes **directly into
`main`**.

- `CLAUDE.md`: Branches "cut from `main`"; PR line "A story branch PRs directly into `main`"; Enforcement
  paragraph rewritten (claude-lens's `CLAUDE.md` §Enforcement is the model, minus its "differs from
  deepseek-lens" note).
- `.github/workflows/branch-guard.yml`: replace the "head must be `develop`" predicate with "head matches
  `^GI-[0-9]+-`" and the head's issue number is one the PR body closes; keep the title, closing-keyword and
  per-commit-prefix checks. `.github/workflows/main-guard.yml`: replace "landed via a merged `develop`→`main`
  PR" with "landed via a merged PR from a `GI-<n>-…` head". Port claude-lens's adapted files rather than
  writing new ones.
- Docs that describe the two-branch flow: all files listed under the grep gate below.
- **State verified 2026-09-25:** `origin/develop` has no commit `origin/main` lacks (PR #26 promoted it), so
  `develop` can be retired without losing work. Deleting the remote `develop` branch is a separate,
  user-confirmed step — not part of any bead.
- **Ordering:** `pull_request` runs the workflow at the PR's merge ref — the PR head's version of the
  workflow file. `main-guard.yml`'s `push` event also runs the file at the pushed commit. This means the
  guard change is **self-applying**: a PR from `GI-27-…` that carries the updated `branch-guard.yml`
  governs its own check. No separate pre-PR is needed; the first PR that carries the updated guard is the
  guard change itself.
- **Bead 01 is grep-driven, word-bounded.** After the change, `git grep -nw develop` (equivalently
  `git grep -nE '\bdevelop\b'`) outside `docs/planning/`, `planning/`, `.beads/`, and historical
  spec/design files (`docs/superpowers/specs/`) must return no matches. **The pattern must be
  word-bounded:** the bare substring `develop` also matches the English word "developer"
  (`README.md:12`, `docs/context/architecture.md:11`, `docs/context/build-and-run.md:19`,
  `docs/context/testing-and-quality.md:113`, `docs/context/INDEX.md:7`), which must not be edited — so the
  unbounded form can never reach zero matches outside the exclusions and the gate would be unsatisfiable
  (F9.2). The positive control is the pre-change **word-bounded** grep (confirms the pattern matches the
  known branch references before the change). **Known files carrying a real branch reference, and so
  requiring a change:** `CLAUDE.md` (:50, :57, :63), `.github/workflows/branch-guard.yml` (:18),
  `.github/workflows/main-guard.yml` (:33), `docs/context/conventions.md` (:128),
  `docs/context/testing-and-quality.md` (:101-102), and `docs/context/INDEX.md` (:122 — **one**
  occurrence). `README.md`, `docs/context/architecture.md` and `docs/context/build-and-run.md` carry
  **no** branch reference — their only `develop` match is the English word "developer" — so they are **not**
  part of workstream D and do not appear on the change list; they are still refreshed by bead 16 for their
  own reasons (§10: body-cap note, serve lifecycle, new CLI), not for the `develop` drop. The design spec
  (`docs/superpowers/specs/2026-09-14-deepseek-lens-design.md`)
  is historical and stays unchanged — the grep scope excludes it.

## 11A. Workstream E — covering index for the stats aggregates

**Applicable — and worse here than it was in claude-lens.** claude-lens added `idx_events_stats` (GI-13,
bead 08) so `/api/stats` walks a narrow index instead of a multi-GB table. Measured on this machine's live
store, **read-only, on 2026-09-25** (`mode=ro`, Python `sqlite3`, one cold run each — indicative, not a
benchmark):

| Query (as `store.go` issues it) | Plan today | Time |
|---|---|---|
| `StatsSummary` aggregate over a window | `SEARCH … USING INDEX idx_requests_started_at` (a table lookup per row) | **22 s** |
| `StatsByPeriod` (strftime group) | same + `USE TEMP B-TREE FOR GROUP BY` | **56 s** |
| `StatsByModel` | same + temp b-tree | **55 s** |
| whole-table aggregate | `SCAN requests` | 22 s |
| `ListRequests` newest 50 | `SEARCH … idx_requests_started_at` | 0.11 s |
| `CountRequests` with `since` | `COVERING INDEX idx_requests_started_at` | 0.05 s |
| per-session aggregate (`session_id = ?`) | `idx_requests_session_id` | 1 ms |

The file is 4.7 GB for 18,325 rows (~260 KB/row) and the aggregated columns (`input_tokens` …) sit **after**
`req_body`/`resp_body` in the table's column order, so reading them means walking each row's blob overflow
chain — consistent with the times, but the mechanism is a hypothesis until an `EXPLAIN` + I/O check on a copy
(the plans above prove only that no covering index exists). Every stats query the Stats tab fires pays this
on each refresh; the list/count/session paths are already fine.

**Change:** `internal/store/schema.sql` gains

```sql
CREATE INDEX IF NOT EXISTS idx_requests_stats ON requests(
    started_at, duration_ns, error_text, model_resolved, model_requested,
    cost_source, cost_usd, input_tokens, output_tokens,
    cache_creation_tokens, cache_read_tokens);
```

covering `StatsSummary`, `durationPercentiles`, `StatsByModel`, `StatsByPeriod` and `StatsByCostSource`
(columns enumerated from the five queries; re-derive at bead time on the **base tree** — line cites from
the dirty working tree will shift after `git stash`). **Deliberate difference from claude-lens:** it led
with the token columns and put `started_at` last, forcing a full covering-index scan; here `started_at`
leads, so a window query *seeks* and a covering scan is bounded by the window. The warnings join
(`r.id = w.request_id`) uses the rowid every index carries. `idx_requests_started_at` is kept (it is a
prefix of the new index but serves `ORDER BY started_at DESC` without the wider entries); dropping it is a
measured follow-up, not part of this story — claude-lens's dropping-a-"redundant"-index attempt regressed a
DELETE to a full scan.

- **Migration = schema change:** applied by `Open`'s idempotent `schema.sql` (`CREATE INDEX IF NOT EXISTS`;
  verify that `Open` does run it on an existing file — `store.go:91-98` says so). **Back up `lens.db` (+ `-wal`
  `-shm`) to a separate path first and state it.** Building the index reads every row once, ~20–60 s on the live
  file, holding the writer. **Do not open the store from a second process while `serve` is live during this
  build** — calling `lens doctor` (or any command that opens the store) while `serve` is running would hold the
  write lock for the full build time, causing the consumer's writer to hit `busy_timeout(5000)` and drop
  captured calls (`consumer.go:527` logs and drops). The correct sequence: **shut down first** (`lens shutdown`),
  then run `lens doctor` once to apply the schema and build the index on the stopped file, then start. Or
  use `lens restart --timeout 180s` for that one restart — B.3 spawns the new process and polls health;
  the 180 s gives `CREATE INDEX` time to complete. The verification uses a **copy** of the live DB, never
  the live file.
- **Cost:** ~18 K small entries (a few MB, though `error_text` is unbounded upstream error text per
  `schema.sql:30` and can inflate index entries for rows with long error payloads — accepted, not a
  blocker) and one extra index entry per insert — negligible next to a multi-KB body write; the single
  writer is unaffected.
- **Gate (with a positive control):** a test that runs `EXPLAIN QUERY PLAN` for each of **five aggregate
  statements**: `StatsSummary`'s main aggregate, `durationPercentiles`, `StatsByModel`, `StatsByPeriod`,
  and `StatsByCostSource`; asserts `USING COVERING INDEX idx_requests_stats` for each. The
  `warnings w JOIN requests r` sub-statement within `StatsSummary` (`store.go:457-458`) is checked
  separately: assert it does **not** perform a full scan of `requests` (it should look up by primary key),
  not for COVERING INDEX. Prove the COVERING assertion fires on a known non-covering query (e.g. one that
  also selects `req_body`) before relying on an empty result as a pass. Add a fixture row with a long
  `error_text` (e.g. 10 KB) and confirm coverage still applies. Also assert the queries return the same
  rows before and after the index (equality against a fixture).
- **Not applicable from claude-lens:** `idx_events_session_started` (deepseek-lens session work is
  `session_id = ?` point aggregates, 1 ms; no per-session `ORDER BY started_at` scan) and
  `idx_events_cost_source` (its purge-unpriced predicate here is not an equality seek on that column —
  verify against `purgeUnpricedWhere` at bead time).

## 11B. Workstream F — from/to range pickers with hour precision

claude-lens's Calls tab has a `custom` window: two `datetime-local` inputs, **from** and **to**, both *hour*
selections inclusive of the hour picked (`since` = start of the from-hour, `until` = end of the to-hour), built
through **one** `timeWindow` helper that constructs the `±hh:mm` offset by hand — never `toISOString()`, never
`new Date("YYYY-MM-DD…")`. Its backend already took `since` **and** `until`. deepseek-lens differs:

- **Backend gap:** `Filter` (`types.go:126`) and `requestWhere` (`store.go:327`) carry `Since` only; `listRequests` reads
  only `?since` (`api.go:341`). Add `Filter.Until` → `started_at < ?` in `requestWhere` (shared with
  `CountRequests`, so `X-Total-Count` stays consistent — the reason it is shared) and read `?until` with the
  existing `parseTimeBoundParam`. `since >= until` is not rejected (matches `/api/stats`' documented
  behaviour). `warnings` and `sessions` routes stay `since`-only — out of scope.
- **Where:** deepseek-lens's request list *is* the live **Feed** tab (`app.js:316`, `/api/requests?limit=50`),
  which has no window control today. Add a compact **Range** control to it: From/To `datetime-local`, a Clear
  button. With a range set the feed **stops prepending SSE rows** and shows the matching page(s) with the
  existing pager pattern and `X-Total-Count`; a banner says "range view — live paused"; Clear resumes live.
- **Stats tab:** replace the date-only `stats-from`/`stats-to` with the same `datetime-local` pair (hour
  precision; empty = open-ended). Interpreted in the "Days in" zone (A.2): fixed-offset choices use
  `Date.UTC(...) − offset`, **Local** uses the numeric `new Date(y, m-1, d, h)` constructor (DST-correct).
  This supersedes A.2's `dayBound`: one helper, `hourBound(value, endExclusive, offsetMin | 'local')`, used by
  both tabs — a second offset implementation is the defect this design exists to avoid.

| from | to | Filter |
|---|---|---|
| set | set | `[from-hour start, to-hour end)`; if `to` < `from` show an inline message and apply **no** window |
| set | empty | `since` only |
| empty | set | `until` only |
| empty | empty | no window |

**Acceptance:** the Feed range and the Stats range for the same two instants send identical `since`/`until`
strings (query-string equality is row equality — same store filter); `X-Total-Count` equals the row count of
the range; an SSE row arriving while a range is set is not shown; `hourBound` **formats its output by hand**
(e.g. `YYYY-MM-DDTHH:mm:ss.000Z`) **without calling `toISOString()`** — same pattern as claude-lens; the
existing `dayBound` use of `toISOString()` (`app.js:474`) is removed when `hourBound` supersedes it;
**minutes are truncated to the hour** (`hourBound` floors any fractional-hour value to `:00` — inputs are
hour-precision in this UI); inputs are `datetime-local`; the guard applies to the new `hourBound` helper
and the removal of `dayBound`, not as a global source ban; DST/rollover cases are manual (no JS runtime in
the toolchain — `node --check` only).

## 12. Proposed beads (draft — beadify only after re-review)

| # | Title | Depends |
|---|---|---|
| 01 | Drop `develop`: CLAUDE.md, both guards, conventions docs, all grep-identified files (D); **grep-driven gate** | — |
| 02 | `idx_requests_stats` covering index + plan-assertion test with positive control (E; **backup first**) | — |
| 03 | Usage tail: `boundedBuffer` tail, `RespTail`, `ExtractUsageWithTail`, consumer wiring, tests (A.1) | — |
| 04 | `StatsByPeriod` tz offset + `/api/stats?tz_offset` + tests (A.2 backend) | — |
| 05 | `Filter.Until` + `/api/requests?until` + tests (F backend) | — |
| 06 | Shared `hourBound` helper; Stats tab hour-precision From/To + "Days in" selector (A.2 frontend, F) | 04 |
| 07 | Feed Range control: pager, live-paused banner, Clear (F) | 05, 06 |
| 08 | `net.Listen` split; join the existing purge goroutine via WaitGroup (ctx-checked); serve state file + `POST /api/shutdown` + `lens shutdown` (B.1, B.2) | — |
| 09 | `lens restart` with detach + rollback (B.3) | 08 |
| 10 | `POST /api/reload` + `lens reload`, `RetentionDays` arm via atomic struct (B.4; struct designed to take `HotDays` as a second field in bead 14; includes routing `api.go:99`/`SetRetention` through the struct) | 08 |
| 11 | `body_archive` schema (**backup first**) + archiver + `Filter.WithBodies` + hydrating reads for every `WithBodies: true` reader (C.2) | 02 |
| 12 | `HotDays` config + Validate + archiver steps folded into the **single** serve maintenance goroutine bead 08 joins (no second goroutine) + purge/`GCArchive`; **backup-first requirement in doctor output** (C.2) | 08, 11 |
| 13 | `lens archive status\|run\|restore` + dashboard archive line (C.2) | 11, 12 |
| 14 | `reload` gains the `HotDays` arm; the maintenance ticker reads it from the shared struct; acceptance: reload diff test covers `HotDays` as live-applied and the non-live set (`BodyCapBytes`, etc.) under `restart_required` (B.4) | 10, 12 |
| 15 | 8 MiB cap in config + doctor WARN + README (A.3) | 02, 12 |
| 16 | Refresh `docs/context/*` (§10) | all |

## 13. Working-tree note

An uncommitted **prototype of A.1 and A.2** (all tests passing) is in the working tree, written before this
plan was requested. It is not committed and not on a branch.

**Required path:** `git stash -u` the prototype before bead 02, then implement bead by bead per the plan as
reviewed. Adopting it as-is bypasses bead boundaries and carries the pre-review consumer-guard bug (F1.1: it
tests post-decode headers, `consumer.go:368`) into history. It is a reference only — never `git stash pop`.
Note the stash also holds this plan file (untracked) unless it is committed first; commit the plan before
stashing.

Create GI#27 on GitHub before the first bead and confirm the number matches this filename.

## Change History

### v14 — round 12 triage (2026-09-25)

- **F12.1 (MAJOR, JUSTIFIED):** the round-11 wording made the F7.4 early-return `defer` "pid-guard-only" and
  said the bind-winner gate "applies **only** to the normal-path teardown" — but a `defer` inside `Serve` runs
  on *every* return from `Serve`, and a process whose `Listen` failed returns through the **normal path**
  (`serve.go:181-184` `errCh` → `stop()` → the sole normal `return` at `serve.go:200`), not an early return
  (verified: `Serve` is one function, `serve.go:34-201`). For that lost-bind process the file still carries its
  own `pid` until the winner's post-`Listen` rewrite lands, so a pid-guard-only defer would match the guard and
  delete the winner's live file — the F10.1 clobber the removal bullet forbids and that §7 B's creator-loses-bind
  test asserts cannot happen — and the removal bullet (universal negative gate) and the F7.4 bullet (gate
  excluded from the defer) defined the same case two ways. **Fixed (adopting the reviewer's fix exactly):** there
  is now **one shared removal function applying both the pid-guard and the negative bind gate**, called by the
  explicit normal-path teardown AND by the early-return `defer`. The `defer` is no longer described as
  "pid-guard-only": it is the pid-guard plus a bind gate that *trivially passes* for a process that never bound
  (nothing was bound, so nothing was lost), so the effective condition for a pre-`Listen` return reads as the
  pid-guard alone; and "the bind gate only ever vetoes a process that attempted and lost a bind, so it never
  vetoes an early return". The "pid-guard-only" phrase is dropped at the F7.4 bullet (heading and body), the
  teardown bullet, and the §7 B F7.4/F11.1 cross-reference, which now names the shared, gated removal. The
  stale-takeover bullet states that a process taking over a stale file registers the same shared, gated `defer`
  (so its lost-bind and early-return paths match the creator's). §7 B's "creator's exit removes nothing (winner
  rewrites after)" assertion is **kept** — deleting it would reintroduce the F10.1 file-clobber.

### v13 — round 11 triage (2026-09-25)

- **F11.1 (MAJOR, JUSTIFIED):** round 10 added "and this process won both binds" to the removal bullet, which
  made "won the bind" a **necessary** condition of *any* removal and so contradicted the F7.4 early-return
  `defer`, leaving the never-bound case undefined: a `Serve` return at `serve.go:49/54/119` executes before
  either `Listen` (verified: those three returns are all pre-listener), so under the removal bullet's literal
  reading every failed boot (a bad DB path failing `store.Open`, a bad calendar, a failed `proxy.NewServer`)
  would leave a `serve.state.json` behind — the exact leftover F6.5/F7.4 exist to eliminate. Self-healing (a
  later `restart`/`shutdown` reclassifies the dead-pid file as stale) so bounded, not fatal — but the model was
  not readable one way and F7.4's guarantee was defeated by its own sibling bullet. **Fixed (conductor's
  directive):** the removal bullet now states the gate **negatively** — "the file is removed only if its `pid`
  equals `os.Getpid()`, and **no process that lost a bind to a live winner may remove it**" — so the two paths
  read as one contract without collision: a process that lost its `Listen` *did* lose a bind and never removes,
  while a process that never bound did not lose a bind and is gated by the pid-guard alone. The F7.4 bullet now
  says the early-return `defer` is **pid-guard-only** and that the bind gate applies **only** to the normal-path
  teardown; the teardown bullet's gate was reworded to the same negative form ("for a process that lost its
  bind to a live winner"). §7 B gains the test: a pre-listener `Serve` return (a `store.Open` failure, or an
  injected `proxy.NewServer` failure) leaves no state file. F7.2 (only the bind winner ever writes) and F10.1
  (ownership follows the successful bind) are unchanged.

### v12 — round 10 triage (2026-09-25)

- **F10.1 (MAJOR, JUSTIFIED):** the round-7/8 concurrent-`serve` model equated the file **owner** (winner of
  the early `O_EXCL` create) with the **bind winner**, but the create is decided before `store.Open` and the
  other pre-listener phases, while the port is decided at `Listen`. A creator slow through those phases can
  lose the bind to a second process that saw its live pid and (correctly) left the file alone; that second
  process serves, while the creator's failed `Listen` routes through `serve.go:183`'s `stop()` into the
  **normal** teardown, whose pid-guarded removal matches (the file still carried the creator's pid) and
  deletes the serving process's file — leaving `restart` to hit B.3 step 2's "no state file" branch. **Fixed
  (conductor's directive):** file ownership now follows the **successful bind**, not the create. The
  post-`Listen` rewrite is done by whichever process won both binds **whether or not it created the file**;
  only that process's exit runs the pid-guarded removal; a process whose `Listen` fails — **creator
  included** — neither rewrites nor removes (the existing "failing the bind exits without writing or
  removing" rule, extended to the creator). F7.2 is kept intact: only the bind winner ever writes, so a live
  winner's file is never clobbered. Updated B.1 (write-timing, `O_EXCL`, removal, teardown bullets), the B.5
  concurrent-`serve` row, and §7 B, which gains the CREATOR-loses-the-bind case (the file carries the
  **serving** process's pid; `restart` acts on it).
- **F10.2 (NIT, JUSTIFIED):** B.1's `O_EXCL` bullet still read "a file owned by a live **or recent**
  process is left untouched" — a vestige of the round-8 `bootGrace` age rule that round 8 removed and round 9
  replaced with pid-death. With staleness defined purely as "the recorded pid is not alive", there is no
  "recent" category. **Fixed:** deleted "or recent" — the clause now reads "a file owned by a live process is
  left untouched".

### v11 — round 9 triage (2026-09-25)

- **F9.1 (MAJOR, JUSTIFIED):** the round-8 fix introduced the `isProcessAlive` helper as the sole stale signal
  but never pinned its *failure* semantics, and the natural `err != nil ⇒ not alive` reading maps an
  undecidable probe to **dead** — the one misclassification that errs the dangerous way. An `EPERM`
  (`kill(pid,0)`, Unix) or `ERROR_ACCESS_DENIED` (`OpenProcess`, Windows) means the process **exists** but is
  inaccessible; treating it as dead would let B.3 step 3(b) remove a live (booting or hung) `serve`'s file and
  spawn a second instance (the F7.1/F8.1 failure) and let the `O_EXCL` takeover overwrite a live winner's file
  (the F7.2 clobber). "Fail-closed" held for pid reuse only; a probe failure was fail-**open**. **Fixed:** B.1
  now pins the error contract — **dead** only on the definite "no such process" result (Unix `ESRCH`; Windows
  `ERROR_INVALID_PARAMETER`), **alive** on "exists but inaccessible" (Unix `EPERM`; Windows
  `ERROR_ACCESS_DENIED`) and on every other/indeterminate error, so an undecidable probe is fail-closed — and
  states that both the `O_EXCL` takeover and the B.2/B.3 stale branch read it this way. The `O_EXCL` takeover
  bullet (B.1) was updated to cite the pinned contract.
- **F9.2 (MINOR, JUSTIFIED):** the §11 D gate `git grep -n develop` is a substring match, so it also hits the
  English word "developer" (`README.md:12`, `architecture.md:11`, `build-and-run.md:19`,
  `testing-and-quality.md:113`, `INDEX.md:7`) — the gate could never reach zero matches, and `README.md`,
  `docs/context/architecture.md` and `docs/context/build-and-run.md` carry **no** branch reference at all and
  were wrongly listed as requiring a change. **Fixed:** the gate is now word-bounded (`git grep -nw develop` /
  `git grep -nE '\bdevelop\b'`), the pre-change positive control is kept word-bounded, the three files were
  dropped from the D change list (with a note that bead 16 still refreshes architecture/build-and-run for their
  own reasons, not for the `develop` drop), and the change list is now the true branch-reference set
  (`CLAUDE.md:50,57,63`, `branch-guard.yml:18`, `main-guard.yml:33`, `conventions.md:128`,
  `testing-and-quality.md:101-102`) with `INDEX.md:122` corrected from "2 occurrences" to **one**.
- **F9.3 (NIT, JUSTIFIED):** §11B cited `` `Filter` and `requestWhere` … (`store.go:327-356`) `` but that range
  is only `requestWhere` (`store.go:327`); the `Filter` struct lives at `types.go:126` and is where bead 05
  must add `Until`. **Fixed:** split the cite to `Filter` (`types.go:126`) and `requestWhere` (`store.go:327`).

### v10 — round 8 triage (2026-09-25)

- **F8.1 (MAJOR, JUSTIFIED):** v9's `bootGrace = 5 min` age threshold was false on two counts. (i) It claimed
  to be "≥ the worst-case pre-listener time", but the same phase includes `purgeOnStartup`, **unbounded when
  `RetentionDays > 0`** (B.3 step 7) — so a live booting `serve` could age past `bootGrace` and be classified
  stale, the F6.5/F7.1 failure at a larger threshold. (ii) For a boot outlasting `bootGrace`, B.3 step 3(b)
  removed the file and skipped to step 5 (spawn) — contradicting "never a second instance". **Fix (conductor's
  unification):** dropped `bootGrace` entirely and keyed the stale rule on the **same signal the `O_EXCL`
  takeover already uses** — a file is stale only when its recorded pid is **not alive** (the one
  `isProcessAlive` helper), *in addition to* health refusing and both ports refusing. A live pid (booting or
  hung) is now un-staleable **regardless of age**, so no boot length can weaken it and the
  `bootGrace`-vs-unbounded-`purgeOnStartup` tension is gone. Extended the existing `O_EXCL` reconciliation: the
  pid check answers a file-ownership/staleness question, not the service-liveness question health answers. Kept
  the early write (it puts the booting serve's own live pid on disk). Updated B.1, B.2, B.3 step 3(b), both B.5
  rows, and §7 B. Corrected B.5's honest bound: at most one *proxy* survives the bind race; a misclassified
  boot (e.g. reused pid) errs toward *starting* and its spawned instance fails to bind and exits without
  deleting the winner's file.
- **F8.2 (MINOR, JUSTIFIED):** `lens shutdown` was documented to use `--timeout`, but the
  flag-stripping-before-`config.Load` requirement was stated for `restart` only. `config.Load`'s flag set is
  closed (`config.go:270-290`) and `main.go:43` passes `os.Args[2:]` straight through, so `lens shutdown
  --timeout 30s` would fail "flag provided but not defined". Gave B.2 the same treatment as B.3 step 1 —
  retitled `### B.2 lens shutdown [--timeout 30s]` and added the strip-before-`config.Load` sentence.
- **F8.3 (NIT, JUSTIFIED):** §7 B's stale-file bullet bundled `shutdown` into "proceeds to **spawn**", but
  `shutdown` never spawns (B.2: remove + exit 0). Split the bullet: `restart` removes the file and proceeds to
  spawn (step 5) without burning `--timeout`; `shutdown` removes it and exits 0 without spawning, also without
  burning `--timeout`.

### v9 — round 7 triage (2026-09-25)

- **F7.1 (JUSTIFIED):** the boot-window rule keyed starting-vs-stale off `--timeout` (default 30 s), but the
  plan's own worst-case pre-listener phase is 20–60 s (`CREATE INDEX`, B.3 step 7): a live booting `serve`
  older than 30 s was therefore classified stale, its file removed, and a second instance spawned — exactly
  what F6.5 set out to prevent. Decoupled the threshold: a fixed `bootGrace = 5 * time.Minute` constant (B.1),
  documented as ≥ the worst-case pre-listener time; `--timeout` now bounds only how long callers *wait*.
  Restated B.5's row conditionally ("removes it **only when** `started_at` is older than `bootGrace`",
  independent of any caller's `--timeout`) and added the §7 B boundary test (file older than `--timeout` but
  younger than `bootGrace` ⇒ *starting*, not removed).
- **F7.2 (JUSTIFIED):** the early write was unconditional on the one shared path, so two concurrent `serve`
  processes each overwrote the single file; the loser's early write landing after the winner's post-`Listen`
  rewrite gave the file the loser's `pid`, and the pid-guard then let the loser delete a live winner's file.
  Specified an `O_EXCL` create (B.1): on `EEXIST`, take over only a genuinely stale file (dead pid) and never
  overwrite a live/recent one; the loser binds-and-exits without writing or removing. Kept B.1's
  "liveness is health, never file/pid" rule coherent — the ownership check gates the file *write*, not service
  liveness. Added a B.5 risk row and strengthened the §7 B pid-guard test (assert the winner's `pid` survives).
- **F7.3 (MINOR, JUSTIFIED):** the stale/boot dial read the two addresses from the state file, but during the
  boot window the file is the early write with those fields empty — nothing to dial. Added the explicit
  fallback (B.1 dial helper, B.2, B.3 step 3(b)): dial `cfg.ProxyAddr`/`cfg.DashboardAddr` when the file's
  addresses are empty (`shutdown`/`restart` run `config.Load` first).
- **F7.4 (MINOR, JUSTIFIED):** the removal was required on three early-return paths (`serve.go:49,54,119`) but
  the plan only excluded a `defer` without naming the replacement, and its ordering rationale was backwards (a
  removal `defer` registered before `st.Close()`'s runs *after* it under LIFO — the wanted order). Named the
  mechanism: one idempotent, pid-guarded `defer` registered right after the create covers the early returns,
  alongside the explicit ordered teardown for the normal path; kept only the conditionality argument for why
  the normal-path removal is not a bare `defer`.

### v8 — round 6 triage (2026-09-25)

- **F6.1 (JUSTIFIED):** the archiver was modelled as a *second* goroutine yet told to run "on the existing
  24 h ticker" — C.2 contradicted B.1 and bead 14. `time.Ticker.C` delivers each tick to one receiver, so a
  second reader would steal alternate ticks from the purge. Adopted the conductor's option (a): **one**
  maintenance goroutine (bead 08's join), with bead 12 folding purge → archive → `GCArchive` into the same
  loop body, no second goroutine. Fixed B.1's join paragraph, C.2's serve row, bead 12's title and bead 14.
- **F6.2 (JUSTIFIED):** B.1 dropped `defer st.Close()`, leaving the store unclosed on `Serve`'s early return
  at serve.go:117-120. Kept the defer (idempotent no-op after the explicit close) and rely on the explicit
  `st.Close()` before the pid-guarded removal for ordering.
- **F6.3 (JUSTIFIED):** B.4 fed `config.Load` the raw `os.Args[2:]`; `serve` actually passes
  `translateNoCapture(args)` (serve.go:35). Raw args make a `--no-capture` instance unreloadable (unknown
  flag → 400). Specified the translated slice and added the `--no-capture` reload test.
- **F6.4 (JUSTIFIED):** B.5 claimed the bind-race loser "exits non-zero"; `Serve` logs the listener error and
  returns `nil` (serve.go:179-200), so it exits 0. Per the conductor, fixed B.5's wording to "fails to bind and
  exits without deleting the winner's file" and dropped the exit-code claim (no bead changes `Serve`'s return).
- **F6.5 (JUSTIFIED):** the stale rule ("health refuses **and** both ports refuse ⇒ stale") misfires during the
  pre-listener window (`store.Open`/`CREATE INDEX`, `checkRedaction`, `purgeOnStartup`) where a live *booting*
  `serve` also refuses both ports. Closed the window: the state file is created **early** (before
  `store.Open`) and the stale rule now also requires the file's `started_at` to be older than the boot window
  (`--timeout`); a younger file is treated as *starting*. Defined B.2's previously implicit no-file case. Added
  the fail-closed boot-window test and B.5 risk row. (The early write also made B.1's "written far later, so a
  removal `defer` would run before `st.Close()`" rationale stale; it now rests on the removal being
  *conditional* on the join, which a `defer` cannot express.)
- **F6.6 (NIT, JUSTIFIED):** the "bead 09 of GI-17" migration precedent does not exist (`br-GI-17-09` is the
  Settings price table; GI-17 has no schema migration). Dropped the citation; the mechanism is `Open` running
  `schema.sql`'s `CREATE TABLE IF NOT EXISTS` on every start (store.go:121), no migration framework
  (schema.sql:1-8).

### v7 — round 5 triage (2026-09-25)

- **F5.1 (JUSTIFIED):** Fixed the shutdown-teardown mechanism in B.1. `st.Close()` is `defer`red at
  serve.go:57 (before the state file exists), so a removal `defer` registered later runs *before* it (LIFO) —
  the guarantee was unimplementable as written. Specified an explicit ordered teardown (join goroutines,
  `st.Close()` called explicitly, pid-guarded removal as the final statement). Added the store-wrapper test to
  §7 B that asserts the file still exists inside `Close`.
- **F5.2 (JUSTIFIED):** Bead 12 now depends on **08** (the archiver goroutine needs bead 08's WaitGroup join
  mechanism and the `net.Listen` split's "after listeners are up" point). Bead 08's title names joining the
  existing purge goroutine via a ctx-checked WaitGroup. (Bead 14 → 08 is transitive via 10 — left as is.)
- **F5.3 (JUSTIFIED):** Wildcard binds (`0.0.0.0:port` / `[::]:port`) return an unspecified `Addr()`, which
  is not dialable on Windows while live — the round-4 stale-file rule would then delete a live serve's state
  file and let `restart` lose the bind race. B.1 now requires every client-side dial (health, port-refusal,
  stale detection) to normalize an unspecified host to loopback; B.3 step 3(b) points at that helper. Added a
  wildcard-bound ephemeral-listener test to §7 B.
- **F5.4 (MINOR, JUSTIFIED):** Chose the join-bound-expiry behaviour: fail-closed — on bound expiry with a
  writer goroutine still running, log and **do not** remove the state file, so the "file gone ⇒ drained"
  proof is never false. Added the fail-closed test to §7 B; noted the purge path already passes `ctx` into the
  store call (`store.go:955` `BeginTx(ctx)`) and the archiver must too.

### v6 — round 4 triage (2026-09-25)

- **F4.1 (JUSTIFIED):** Fixed bead-ordering contradiction. Bead 10 now implements only the `RetentionDays` arm (atomic struct designed to take a second field); bead 14 adds `HotDays` and reads it in the archiver ticker (depends on 10 for the struct and 12 for the config key). B.4 clarifies which bead does what. Bead 14 acceptance note added: reload diff test must cover `HotDays` as live-applied.
- **F4.2 (JUSTIFIED):** Added stale-state-file handling to B.2 (`lens shutdown`) and B.3 step 3: if `GET /api/health` refuses AND both ports refuse, the file is stale — log, remove, skip the shutdown POST, proceed. Avoids burning `--timeout` on a dead process. Added B.5 risk rows. Added stale-file test to §7 B.
- **F4.3 (JUSTIFIED):** Added goroutine-join requirement to B.1 (final paragraph) and C.2 serve row: the existing purge goroutine (bead 08) and archiver goroutine (bead 12) must both be joined via a WaitGroup alongside `<-consumerDone` before `st.Close()` runs; each must check `ctx.Done()` between operations. Added B.5 risk row. Added cancel-during-batch test to §7 C.
- **F4.4 (JUSTIFIED):** Fixed three misclassifications in C.2 Reads row. (a) `api.go:998` moved from "replay row lookups" (`WithBodies: true`) to the `ListRequests`/`WithBodies: false` callers — it is the session-detail handler. (b) `store.go:1063, 1091` (`PurgeableBytes`/`UnpricedBytes`) removed from the `WithBodies: true` class — they are SQL `LENGTH()` aggregates, not hydrating reads. (c) `cli/export.go:45` clarified as a `WithBodies: true` list caller; added requirement to batch hydration by archive day.
- **F4.5 (JUSTIFIED):** §1 "four workstreams" corrected to "six workstreams" (A–F).

### v5 — round 3 triage (2026-09-25)

- **F3.1 (JUSTIFIED):** decided the read model in the plan: `ListRequests` gains `Filter.WithBodies bool`
  (default false — `NULL` for both body columns); `GetRequest`, `lens export`, `lens show`, and replay
  lookups are the real `WithBodies: true` readers. Replaced the enumerated list in C.2 with the correct one.
  Removed non-readers (redaction self-test, in-flight analyzers). Added positive-control test: feed list
  with archived rows returns null bodies and opens no day file. Added `Filter.WithBodies` to bead 11.
- **F3.2 (JUSTIFIED):** validation rule tightened: `HotDays > RetentionDays` is rejected only when
  `HotDays` was explicitly set above zero and `RetentionDays > 0`; `HotDays = 0` (default) always passes.
  Tests: explicit and defaulted cases. (Resolved in practice by F3.12's default-0 change.)
- **F3.3 (JUSTIFIED):** state file removal is the very last act after `st.Close()` (pid-guarded). Shutdown
  and restart wait for state file gone as proof of drain + store-close, not just port refusal. Test added.
- **F3.4 (JUSTIFIED):** reversed the migration guidance: shut down first, run `lens doctor`, then start (or
  use `--timeout 180s`). Removed the incorrect advice to run `lens doctor` while serve is live.
- **F3.5 (JUSTIFIED):** added `cwd` field to state file; `cmd.Dir` set on spawn. Specified "no state file"
  branch (refuse with message). Distinguished timeout-but-alive from exited: with no `--exe`, timeout does
  not kill the child; rollback kill only on `--exe`. Named pre-listener phases explicitly.
- **F3.6 (JUSTIFIED):** added `net.Listen` split to bead 08 file list. State file removal is pid-guarded.
  Added risk row for the pid-guard. Test: two concurrent start attempts, only the winner's file survives.
- **F3.7 (JUSTIFIED):** `POST /api/reload` routes `retentionDays` through the same atomic struct as
  `HotDays`. Test: `GET /api/retention` reflects the new value after reload.
- **F3.8 (JUSTIFIED):** corrected worst-case memory in A.3: 4096 × 2 × cap (~64 GiB at 8 MiB; 32× the
  current ~2 GiB). Doctor WARN names the ceiling. Expanded the risk row in B.5.
- **F3.9 (JUSTIFIED):** narrowed the `toISOString` guard in §11B: `hourBound` formats by hand; existing
  `dayBound` use is removed when superseded; minutes are truncated to the hour; guard applies to the new
  helper, not as a global source ban.
- **F3.10 (JUSTIFIED):** replaced the ordering-risk hedge with the fact (`pull_request` runs the PR head's
  workflow; no separate D-1 pre-PR needed). Made bead 01 grep-driven with a positive control and a complete
  file list from the `git grep develop` result.
- **F3.11 (PARTIAL):** gate narrowed to five aggregate statements; warnings-join sub-statement checked for
  "no full scan of requests" separately (not COVERING). Added long-`error_text` fixture. Marked cites as
  "re-derive on base tree."
- **F3.12 (JUSTIFIED):** `HotDays` default changed from 7 to **0** (archival disabled until opt-in) per
  conductor directive. First `serve` with `HotDays > 0` is a bulk migration; backup-first requirement added
  to C.2 serve row and bead 12 acceptance criteria. Doctor INFO suggests enabling archival when store is large.

### v4 — index and range pickers (2026-09-25)

- **E (§11A):** the claude-lens covering index **is applicable and more urgent here**: on the live 4.7 GB
  store the Stats aggregates run at 22–56 s cold and use no covering index. Adds `idx_requests_stats` with
  `started_at` leading (unlike claude-lens), a plan-assertion gate with a positive control, backup-first, and
  the first-start build-time/restart-timeout interaction. `idx_events_session_started` judged not applicable.
- **F (§11B):** from/to `datetime-local` pickers (hour precision) on the Feed and Stats tabs via one shared
  `hourBound`. Unlike claude-lens the backend needs `Filter.Until`/`?until` on `/api/requests`. A.2's
  `dayBound` is superseded.
- A.3 now gates the 8 MiB raise on E as well as C. Beads 13 → 16.
- The timings are single cold runs from a throwaway read-only script; the blob-overflow explanation of *why*
  is a hypothesis to confirm on a copy.

### v3 — scope added after round 2 (2026-09-25)

- Renumbered **GI-26 → GI-27**: #26 is the merged `develop`→`main` promotion PR (issues and PRs share one
  number space). The review artifacts moved to `planning/GI-27/`.
- **Branch flow:** cut from `main`, drop `develop` (§11, workstream D), per the user's instruction to match
  claude-lens. Verified `develop` has nothing `main` lacks.
- **Body cap:** the earlier "256 KB cap stays, raising it grows a 4.7 GB store" non-change is replaced —
  claude-lens raised its cap to 8 MiB by config; here A.3 does the same **after** archival (C) exists, and the
  tail is kept as a backstop rather than the sole fix.
- **New workstreams:** B (`shutdown`/`restart`/`reload`) and C (body archival) ported from claude-lens
  GI-16, adapted to deepseek-lens's schema (no `transcript_content`, no accounts, `requests` table). C adds a
  schema migration, so the plan now carries the backup-first rule the earlier "no migration" claim excluded.
- Beads regrouped 3 → 13. **These additions have not been through cross-review.**

### v2 — round 1 triage (2026-09-25)

- **F1.1:** consumer guard tests `call.RespHeaders` (pre-decode). **F1.2:** dropped the vacuous "0 rows carry
  Content-Encoding" claim; compressed-over-cap is a residual. **F1.3:** §8 gained reproducible queries.
  **F1.4:** hot-path cost risk rewritten (memmove of the 16 KiB window). **F1.5 (partial):** `ponytail:` site
  named. **F1.6 (override):** no `--tz-offset` flag; divergence documented, zone labelled on the dashboard.
  **F1.7:** stash-not-pop path and issue-before-bead requirement.
