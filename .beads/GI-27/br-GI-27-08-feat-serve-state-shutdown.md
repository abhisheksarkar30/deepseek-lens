### Bead 8: `feat` `net.Listen` split, the joined maintenance goroutine, `serve.state.json`, `POST /api/shutdown`, and `lens shutdown`

- **Bead ID**: br-GI-27-08
- **Priority**: P0 (critical)
- **Original Estimate**: 3h (see the note at the end — this bead is deliberately the largest)
- **Dependencies**: None
- **Blocks**: br-GI-27-09, br-GI-27-10, br-GI-27-12

**Description**:

B.1 + B.2. `serve` fronts a live coding session; on Windows a running `lens.exe` cannot be replaced, and stopping it today means Ctrl-C in the right console. This bead lands the serve-side foundation the rest of workstream B builds on: a `net.Listen` split (needed to know the bound addresses), one joined maintenance goroutine, the runtime state file, the guarded shutdown route, and the `lens shutdown` client. Plan §5 B.1-B.2 and §5 B.5.

**Part 1 — `net.Listen` split (`internal/cli/serve.go`).** Today both servers call `ListenAndServe` (`:152` proxy, `:157` dashboard), which binds internally and never exposes the bound address. Refactor each to `net.Listen` + `Serve`: construct the `http.Server`s, `Listen` the proxy and dashboard addresses, then `Serve` both in goroutines. "Both listeners are up" must be a real point in the code (the state-file rewrite below depends on it).

**Part 2 — one maintenance goroutine, joined (B.1 / F4.3 / F6.1).** The existing purge ticker loop is at `:166-177` (`purgeTicker := time.NewTicker(24 * time.Hour)`). Turn it into a **single** goroutine tracked by a `sync.WaitGroup`:
- It runs `purgeOnStartup` on boot, then once per tick.
- It checks `ctx.Done()` **between operations** so it does not hold the writer past cancellation; the store call must take `ctx` (`PurgeOlderThan` → `purgeWhere` already does, `store.go:955` `BeginTx(ctx)`).
- It is joined by the `WaitGroup` **alongside `<-consumerDone`** (`:198`) before `st.Close()`.
- **There is exactly one 24 h goroutine.** `time.Ticker.C` delivers each tick to exactly one receiver, so a second reader would silently steal alternate ticks and halve both jobs. Bead 12 folds the archiver into **this same loop's body** — do not start a second goroutine.
- **Join-bound expiry is fail-closed:** if the drain bound expires with the goroutine still running, log it and **do not remove the state file** — the "file gone ⇒ drained and store closed" proof must never be claimed while a writer may be live. `shutdown` then times out (its documented behaviour) rather than proceeding on a false proof.

**Part 3 — the serve state file (B.1).** Write `<dir of DBPath>/serve.state.json`, mode `0600`, fields `{"pid","exe","args","cwd","started_at","proxy_addr","dashboard_addr","log_path"}`:
- `exe` = `os.Executable()`, `args` = `os.Args[1:]` (so `args` already begins with `serve` — restart never prepends a second one), `cwd` = `os.Getwd()`, `log_path` default `<dir of DBPath>/serve.log`; no secret in it.
- **Written twice.** **Created early** — right after `Validate`, before `store.Open` and the other pre-listener phases (`checkRedaction`, `purgeOnStartup`, the first-start `CREATE INDEX`, B.3 step 7) — carrying `pid`/`exe`/`args`/`cwd`/`started_at` with the two address fields not yet bound. **Rewritten** after both `Listen` calls succeed and before the `select`, so the addresses are the actual bound ones. Without the early write, a booting `serve` is indistinguishable from a dead one for the whole pre-listener window (F6.5).
- **The rewrite is done by the process that won both binds, whether or not it created the file (F10.1).** Ownership follows the **successful bind**, not the create.
- **The early create is `O_EXCL`, `OpenFile(..., O_CREATE|O_EXCL|O_WRONLY, 0600)` (F7.2).** On `EEXIST` the process reads the existing file and takes it over **only when its recorded pid is genuinely dead** (per `isProcessAlive` below); a file owned by a live process is left untouched, and this process just proceeds to bind — failing the bind, it exits without writing or removing.
- **`isProcessAlive` helper — one small build-tagged helper (B.1 / F8.1 / F9.1)**, Windows `OpenProcess`+`GetExitCodeProcess`, Unix a `kill(pid, 0)`-style probe. **The error contract is fail-closed:** report **dead** only on the definite "no such process" result (Unix `ESRCH`; Windows `ERROR_INVALID_PARAMETER`), and **alive** on "exists but inaccessible" (Unix `EPERM`; Windows `ERROR_ACCESS_DENIED`) **and on every other/indeterminate error**. Mapping "cannot determine" to "dead" is the one misclassification that errs the dangerous way.
- **Removal is ONE shared function applying BOTH the pid-guard and the negative bind gate (F7.4 / F10.1 / F11.1).** The file is removed only if its `pid` field equals `os.Getpid()`, **and no process that lost a bind to a live winner may remove it.** Called by the explicit normal-path teardown **and** by a single `defer` registered right after a successful create (or stale takeover). The `defer` covers the early-return `return`s at `serve.go:49`, `:54`, `:119`; it is a no-op when the pid-guard does not match, the file is already gone, or the bind gate vetoes. A process that **lost its `Listen`** to a live winner *did* lose a bind and never removes; a process that never bound is gated by the pid-guard alone.
- **Removal is the very last act of shutdown, after `st.Close()` returns.** On the normal path it is **not** a `defer` (the removal is conditional on the join and on the bind gate — two things a bare `defer` cannot express). The explicit ordered teardown: join the maintenance goroutine, call `st.Close()` explicitly, then run the shared gated removal. **Keep the `defer st.Close()` at `:57`** (F6.2 — `sql.DB.Close` is idempotent; the explicit call closes, the deferred second is a no-op, and the early `return` at `:117-120` where `proxy.NewServer` fails still releases the handle).
- **Every client-side dial normalizes a wildcard host to loopback (F5.3).** `listener.Addr()` on `0.0.0.0:port` / `[::]:port` returns the unspecified host, which is not dialable on Windows while live. Health (`GET /api/health`), the port-refusal probes, and the stale-file detection all dial through one helper that rewrites an unspecified host to `127.0.0.1` / `::1`. **When the file's address fields are empty (the early write), the dial falls back to the config-resolved `cfg.ProxyAddr`/`cfg.DashboardAddr` (F7.3)** — `shutdown`/`restart` run `config.Load` before this point.
- **Liveness is decided by dialing `/api/health`, never by the file or the pid**; a leftover file is only a hint.
- The state file is removed on graceful exit. Crashes may leave it; the stale branch below reclaims it.

**Part 4 — `POST /api/shutdown` (`internal/api/api.go`).** The handler drives `serve`'s existing `stop` (the `signal.NotifyContext` cancel at `serve.go:141`), so it reuses the current bounded drain (`shutdownGrace` + the consumer's `shutdownBound`) — no second shutdown path.
- **Security — this is a new write route on the dashboard listener.** Same `replayOriginReject` `Origin`/`Host` allowlist (`api.go:711`), parameterized by action, **plus a loopback-caller check** (the dashboard may be bound to `0.0.0.0`; a LAN caller gets 403). Re-read the GI-1 security self-review's credentialless-guard reasoning for a route that is **disruptive** rather than billable — the loopback-caller check is what carries it. Add the route to `docs/context/api-surface.md` and `docs/context/security-and-permissions.md`.
- Note for `CLAUDE.md`'s "Fail open" bullet: the dashboard listener's write routes grow from three to **five** across this bead and bead 10 (`shutdown`, then `reload`); coordinate the count with bead 01 rather than writing a number the code contradicts.

**Part 5 — `lens shutdown [--timeout 30s]` (new `internal/cli/shutdown.go`, register in `cmd/lens/main.go`:15-28).**
- **Strip its own `--timeout` BEFORE `config.Load` (F8.2).** `config.Load`'s flag set is closed (`config.go:270-271`, `ContinueOnError`) and `main.go:43` hands the whole `os.Args[2:]` to the command, so an un-stripped `--timeout` fails with "flag provided but not defined". `--timeout` bounds only the bounded wait below.
- **No state file:** skip the stale branch and dial health directly — if it answers, `POST /api/shutdown`; if it refuses, there is nothing to shut down; log and **exit 0** (F6.5).
- **With a file:** if health already refuses **and** both the proxy and dashboard ports refuse a connection (dialed from the file, or from `cfg.ProxyAddr`/`cfg.DashboardAddr` when the file is the early write with empty addresses — F7.3) **and** the file's recorded pid is **not alive** (`isProcessAlive`) — the process is gone; log "stale state file, removing", remove the file, **exit 0**. No `POST` needed, no timeout burned.
- **Both ports refuse but the pid is still alive:** `serve` is still in its pre-listener phases (or hung) — treat it as **starting**, do **not** remove the file, fall through to the shutdown POST and bounded wait (which fails closed on `--timeout`).
- **Running:** `POST /api/shutdown`, then wait until both ports refuse a connection **and** the state file no longer exists (bounded by `--timeout`). The file disappearing is the proof the drain finished and `st.Close()` returned — not merely that the ports closed.

**Rationale**:

B.1/B.2. Without a state file and a shutdown route there is no way to stop or (bead 09) restart a serve from the command line, which the locked-binary workflow on Windows requires. The early write, `O_EXCL` create, pid-death stale rule, and fail-closed liveness contract are the set of invariants that keep two concurrent `serve` processes from deleting each other's file or spawning a second proxy; the join is what makes "file gone" a sound proof of a closed store.

**Outcome Definition**:

- Both servers bind via `net.Listen`; the bound addresses are known before `Serve` starts them.
- One `WaitGroup`-tracked 24 h maintenance goroutine runs purge, checks `ctx.Done()` between operations, and is joined alongside `<-consumerDone` before `st.Close()`.
- `serve` writes `serve.state.json` early (before `store.Open`) and rewrites it with the bound addresses after both `Listen`s succeed; the bind winner is the writer and the remover.
- `isProcessAlive` reports dead only on the definite no-such-process result; all other/indeterminate errors are alive.
- The `O_EXCL` create never clobbers a live file; a stale takeover registers the same gated removal `defer`.
- Removal is one shared function (pid-guard + negative bind gate), called by the normal teardown and the early-return `defer`; removal is the last act after `st.Close()`.
- A pre-listener `Serve` return leaves no state file.
- `POST /api/shutdown` drives the existing `stop`; it rejects a non-loopback caller and a cross-origin `Origin` (403) and accepts loopback.
- `lens shutdown` strips `--timeout` before `config.Load`, handles no-file / stale-file / starting / running as above, and waits for the file to disappear, not just port refusal.
- `go build ./... && go vet ./... && go test ./...` pass; the TTFB test stays green.

**Test Specifications** (`internal/cli/serve_test.go`, `shutdown_test.go`, `internal/api/api_test.go`; all with ephemeral ports and a temp DB — **no test touches the configured live proxy/dashboard ports**):

- State file written early (exists before the pre-listener phases) so a booting `serve` owns it; removed on graceful exit.
- **A pre-listener `Serve` return leaves no state file** — drive both a `store.Open` failure (bad DB path) and an injected `proxy.NewServer` failure; assert the early-create file is gone (the shared, gated `defer`).
- `POST /api/shutdown` and the loopback check: a non-loopback caller → 403; a cross-origin `Origin` → 403; a loopback caller → accepted.
- **Pid-guard / F7.2 / F10.1:** a second process that fails to bind exits **without deleting and without overwriting** the winner's file — assert file survival **and** that the file's `pid` is still the winner's, not the exit code (F6.4).
- **Creator loses the bind:** the file carries the **serving** process's `pid`, not the creator's; `restart` acts on it; the creator's exit removes nothing (drive both orders — winner rewrites before and after the creator's `Listen` fails).
- **Stale file (dead pid):** pre-seed a file whose pid is **not alive** (spawn-and-reap a helper to obtain a definitely-dead pid) with nothing listening; `shutdown` removes it and **exits 0 without spawning**, without burning `--timeout`.
- **Pid-liveness boundary (F7.1/F8.1):** a pre-seeded file whose pid **is** alive but whose `started_at` is far in the past, both ports refusing, nothing listening → treated as *starting*, **not removed**.
- `shutdown` with no state file dials health directly and exits 0 when it refuses.
- State-file removal happens **after** `st.Close()` — a store wrapper whose `Close` asserts the file still exists.
- A wildcard-bound ephemeral listener is judged live, not stale.
- Join-bound expiry leaves the state file in place (fail-closed).
- `--timeout` is stripped before `config.Load` (a bare `lens shutdown --timeout 30s` does not fail on the flag).

**Files to Touch**:
- `internal/cli/serve.go` (modify — `net.Listen` split at :152/:157; WaitGroup-tracked maintenance goroutine from :166-177; state file create/rewrite/removal; ordered teardown; keep `defer st.Close()` at :57)
- `internal/cli/statefile.go` (create — state file read/write, the shared gated removal, loopback-normalizing dial helper, the early-write/stale-takeover logic)
- `internal/cli/procalive_windows.go` / `internal/cli/procalive_unix.go` (create — build-tagged `isProcessAlive`)
- `internal/cli/shutdown.go` (create — `lens shutdown`)
- `internal/cli/serve_test.go`, `internal/cli/shutdown_test.go` (create/modify — the cases above)
- `internal/api/api.go` (modify — `POST /api/shutdown`, loopback-caller check alongside `replayOriginReject`)
- `internal/api/api_test.go` (modify)
- `cmd/lens/main.go` (modify — register `shutdown` in the `commands` map at :15-28)
- `docs/context/api-surface.md`, `docs/context/security-and-permissions.md` (modify — the new guarded route)

**Note (flagged at beadify):** this bead realises plan §12's bead 08 verbatim, but it bundles the `net.Listen` split, the goroutine join, the state file with its F7.1/F7.2/F8.1/F9.1/F10.1/F11.1 edge-case contract, the guarded route, and the client command. That is materially more than two hours of agent work. It is kept whole because §12 and every downstream edge (09, 10, 12) name it as the single owner of the state file and the join; splitting it would require re-cutting those edges. See the returned summary.
