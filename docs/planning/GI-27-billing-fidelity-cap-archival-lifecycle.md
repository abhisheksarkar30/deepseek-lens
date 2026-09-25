# GI-27 — Match DeepSeek's daily usage page, and port claude-lens's capture-size, archival and lifecycle model

**Ticket**: GI#27 (**issue not yet created**. #24 is the highest issue and #26 the highest PR — #26 is the
merged `develop`→`main` promotion — so 27 is the next free number; create the issue first and rename this
file if GitHub assigns another) ·
**Branch**: `GI-27-billing-fidelity-cap-archival-lifecycle`, **cut from `main`** (no `develop`, §11) ·
**Plan version**: v6 · **Status**: v2 converged (2 review rounds); v3–v5 add workstreams B–F and the
branch-flow change and are **re-reviewed through round 4** — run plan-conductor again before beadifying.

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

`serve` writes `<dir of DBPath>/serve.state.json` once both listeners are up:
`{"pid","exe","args","cwd","started_at","proxy_addr","dashboard_addr","log_path"}`, `0600`, removed on
graceful exit.

- `exe` = `os.Executable()`, `args` = `os.Args[1:]`, so **`args` already begins with `serve`**; restart
  relaunches as `exe <args…>` and never prepends a second `serve`.
- `cwd` = `os.Getwd()` at serve start; `restart` sets `cmd.Dir` to this value so relative paths in `args`
  (e.g. `--db-path`) resolve against the original working directory.
- Liveness is decided by dialing `/api/health`, **never** by the file or the pid; a leftover file is a hint.
- No secret in it (paths and addresses). `log_path` default `<dir of DBPath>/serve.log`.
- **"Both listeners are up" requires a `net.Listen` split.** Bead 08 refactors both `proxySrv` and `dashSrv`
  from `ListenAndServe` to `net.Listen` + `Serve` — the state file is written after both `Listen` calls
  succeed and before the `select`, so the `proxy_addr` and `dashboard_addr` fields are the actual bound
  addresses.
- **State file removal is pid-guarded:** the file is removed only if its `pid` field equals `os.Getpid()`.
  A loser that fails to bind exits through the graceful path but does not delete the winner's file.
- **State file removal is the very last act of shutdown,** after `st.Close()` (the final `defer`). A restart
  or shutdown command waiting for the file to disappear can rely on that as proof that the store was closed
  cleanly. **This guarantee holds only if every goroutine that holds the store's writer connection — the
  24 h purge ticker goroutine and the archiver goroutine — is joined (via a WaitGroup or done channel, bounded
  by the drain bound) alongside `<-consumerDone` before `st.Close()` runs, and each checks `ctx.Done()`
  between operations so it does not hold the writer past context cancellation. Bead 08 must join the existing
  purge goroutine; bead 12 must join the archiver goroutine the same way.**

### B.2 `lens shutdown`

`POST /api/shutdown` drives `serve`'s existing `stop` (the `signal.NotifyContext` cancel), so it reuses the
current bounded drain (`shutdownGrace` + the consumer's `shutdownBound`) — no second shutdown path.

**Security (this is a new write route on the dashboard listener — CLAUDE.md's "three write routes" becomes
five with reload):** same `replayOriginReject` `Origin`/`Host` allowlist, parameterized by action, **plus a
loopback-caller check** (the dashboard may be bound to `0.0.0.0`; a LAN caller gets 403). The
credentialless-guard reasoning in the GI-1 security self-review must be re-read for a route that is
disruptive rather than billable, and the loopback-caller check is what carries it. Both new routes are
listed in `docs/context/api-surface.md` and `security-and-permissions.md`.

`lens shutdown` first reads the state file (if present) and dials `GET /api/health`. If health already
refuses **and** both the proxy and dashboard ports refuse a connection (the process is gone — crashed, killed,
or power-lost before its `defer` ran): log "stale state file, removing", remove the file, and exit 0. The
process was not running; no `POST /api/shutdown` is needed and no timeout is burned.

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
   - **(b) Health refuses:** also dial the proxy and dashboard addresses from the retained state file. If both
     ports also refuse: **stale file** — the serve process crashed or was killed before its `defer` ran; log
     "stale state file, removing", remove the file (the pid-guard's purpose is to protect a concurrent winner,
     not block stale-file cleanup by a separate command), and skip directly to step 5. If a port still
     responds (e.g. serve is starting but health is not ready): treat as running and proceed to step 4.
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
`bootArgs` is the **post-subcommand** slice `serve` received (`os.Args[2:]`, **not** the state file's `args`
— `flag.Parse` stops at the first non-flag, so `["serve","--x"]` would drop every flag and reload would
diff against defaults), validates it, and diffs against the boot config.

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
| Two restarts race | The loser fails to bind and exits non-zero — fail-closed on the port, never two proxies. The loser's pid-guarded exit does not delete the winner's state file. |
| `reload` half-applies | Validate, then apply both fields under one lock; test all-or-nothing on a bad file. |
| Wildcard dashboard bind exposes the routes to the LAN | Loopback-caller check (B.2). |
| Consumer still draining when ports refuse | `shutdown`/`restart` wait for state file gone (not just port refusal) — state file removal is deferred after `st.Close()`, which follows `<-consumerDone`. |
| Stale state file (serve crashed/killed, defer never ran) blocks restart/shutdown | If `GET /api/health` refuses **and** both ports refuse: treat file as stale, log, remove it, skip the shutdown POST/wait, proceed. No timeout burned; no deadlock. |
| Purge or archiver goroutine still holds writer when `st.Close()` runs | Both goroutines joined via WaitGroup alongside `<-consumerDone` (bead 08 + 12); each checks `ctx.Done()` between batches. Required for the state-file guarantee in B.1. |
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
| `internal/store` schema | New `body_archive` table — **a schema migration**: back up `lens.db` (and `-wal`/`-shm`) to a separate path first and state it (global rule), applied by the store's existing schema/migration mechanism (verify at bead time — bead 09 of GI-17 is the precedent). |
| `internal/store/archiver.go` (new) | Batches: per UTC day older than the hot window, per row, **one hot transaction** writes the marker and NULLs the two bodies, **after** the day-file insert commits. Holds the single writer connection (`SetMaxOpenConns(1)`) in **short batches only** — claude-lens's GI-13 hang was a long hold of it. Crash between the two steps leaves a duplicate, never a loss (archive copy first, hot NULL last). |
| Compression | `github.com/klauspost/compress/zstd` — **already a dependency**, no new module. |
| Reads | **Read model decision:** `ListRequests` returns rows **without bodies by default** via a `Filter.WithBodies bool` flag; when false (the default for all `GET /api/requests` callers and every list-style CLI command), the query selects `NULL AS req_body, NULL AS resp_body` from `requestColumns`. `GetRequest`, `lens export`, `lens show`, and replay lookups set `WithBodies: true` to hydrate. **The real reader class (`WithBodies: true`)** (enumerate at bead time): `GetRequest` (`api.go:398, 493`), replay row lookup (`cli/replay.go:184`), `lens export` (`cli/export.go:45` — encodes the whole struct via `json.NewEncoder`; list-hydration must batch by archive day: group IDs per day file and open one day file per day, not one per row), `lens show` (`cli/show.go:68`). **Not body readers and not in scope:** the redaction self-test (reads `req_headers`/`resp_headers` only), the in-flight analyzers (act on the `CapturedCall`, not a stored row), and `store.go:1063, 1091` (`PurgeableBytes`/`UnpricedBytes` — SQL `LENGTH()` aggregates that run inline; archived byte counts for the Purge dry-run estimate are handled separately, not via `WithBodies` hydration). `ListRequests` callers (`api.go:367, 647, 672, 998` (session-detail handler — `ListRequests(... SessionID: id)`, returns no bodies), `cli/ls.go:62`, `cli/tail.go:51`, `cli/stats.go:84`) do **not** need bodies and must pass `WithBodies: false`. Hydration for each `WithBodies: true` caller comes from one store helper that tries the hot DB first, then the archive day file. Writers that read bodies skip archived rows and name `archive restore`. |
| Purge | `PurgeOlderThan` / `--unpriced` also delete the archived bodies (marker cascade + day-file row) and `GCArchive` collects orphaned day rows; the dry-run byte estimate counts archived bytes. |
| Config | `HotDays int` — `--hot-days`, `LENS_HOT_DAYS`, key `HotDays`, **default 0 (archival disabled until operator opts in; a typical value is 7)**, negative rejected. **Validation:** `HotDays > RetentionDays` is rejected only when `HotDays` was explicitly set above zero **and** `RetentionDays > 0`. A defaulted `HotDays = 0` is always valid regardless of `RetentionDays`. Test: `RetentionDays=3`, no `HotDays` set → `Validate` passes; `HotDays=9`, `RetentionDays=7` → `Validate` fails. `lens doctor` prints `HotDays`, WARNs when `archive/` is unwritable, and shows an INFO suggestion to enable archival when the store exceeds a size threshold and `HotDays == 0`. |
| `serve` | One goroutine started **after** the listeners are up (never on the boot path), runs the archiver at boot and on the existing 24 h ticker after `purgeOnStartup`, then `GCArchive`. Fail-open: an error is logged, never fatal. **Joined before `st.Close()`:** the archiver goroutine checks `ctx.Done()` between batches and is waited on via a WaitGroup alongside `<-consumerDone` (bead 12; bead 08 must do the same for the existing purge ticker goroutine) — required for the B.1 state-file-removal guarantee. **First `serve` with `HotDays > 0` set will run the archiver's first pass and move/NULL bodies older than the hot window — a bulk data migration by the CLAUDE.md rule. Back up `lens.db` (+ `-wal`, `-shm`) to a separate path before enabling `HotDays` and state the backup path; bead 12's acceptance criteria include this requirement in the `doctor` output and the bead's completion note.** |
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
- **B:** state file written/removed; `/api/shutdown` and `/api/reload` reject a non-loopback caller and a
  cross-origin `Origin` (403), accept loopback; reload applies the two live fields including the API's
  `retentionDays` copy (`GET /api/retention` reflects the new value after reload), reports the rest under
  `restart_required`, and applies **nothing** on an invalid file; restart against ephemeral ports with a
  helper-process child surviving its parent; rollback path with a deliberately unhealthy `--exe`; consumer
  drain slow: restart waits for state file gone, not just port refusal; pid-guard: a second process that
  fails to bind exits without deleting the winner's state file; stale state file: `restart` (and
  `shutdown`) with a pre-seeded state file and nothing listening proceeds to spawn without burning
  `--timeout`.
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
  archiver goroutine (verify wording against source).
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
- **Bead 01 is grep-driven.** After the change, `git grep -n develop` outside `docs/planning/`,
  `planning/`, `.beads/`, and historical spec/design files (`docs/superpowers/specs/`) must return no
  matches. The positive control is the pre-change grep (confirms the pattern matches known files before
  the change). Known files requiring a change: `CLAUDE.md`, `.github/workflows/branch-guard.yml`,
  `.github/workflows/main-guard.yml`, `README.md`, `docs/context/conventions.md`,
  `docs/context/architecture.md`, `docs/context/build-and-run.md`, `docs/context/testing-and-quality.md`,
  `docs/context/INDEX.md` (2 occurrences). The design spec (`docs/superpowers/specs/2026-09-14-deepseek-lens-design.md`)
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

- **Backend gap:** `Filter` and `requestWhere` carry `Since` only (`store.go:327-356`); `listRequests` reads
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
| 08 | `net.Listen` split; serve state file + `POST /api/shutdown` + `lens shutdown` (B.1, B.2) | — |
| 09 | `lens restart` with detach + rollback (B.3) | 08 |
| 10 | `POST /api/reload` + `lens reload`, `RetentionDays` arm via atomic struct (B.4; struct designed to take `HotDays` as a second field in bead 14; includes routing `api.go:99`/`SetRetention` through the struct) | 08 |
| 11 | `body_archive` schema (**backup first**) + archiver + `Filter.WithBodies` + hydrating reads for every `WithBodies: true` reader (C.2) | 02 |
| 12 | `HotDays` config + Validate + `serve` archive goroutine + purge/`GCArchive`; **backup-first requirement in doctor output** (C.2) | 11 |
| 13 | `lens archive status\|run\|restore` + dashboard archive line (C.2) | 11, 12 |
| 14 | `reload` gains the `HotDays` arm; archiver ticker reads it from the shared struct; acceptance: reload diff test covers `HotDays` as live-applied and the non-live set (`BodyCapBytes`, etc.) under `restart_required` (B.4) | 10, 12 |
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
