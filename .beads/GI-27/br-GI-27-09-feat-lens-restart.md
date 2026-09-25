### Bead 9: `feat` `lens restart` with detached spawn and rollback

- **Bead ID**: br-GI-27-09
- **Priority**: P0 (critical)
- **Original Estimate**: 2h
- **Dependencies**: br-GI-27-08
- **Blocks**: None

**Description**:

B.3. `lens restart` stops the running `serve` (via bead 08's state file and shutdown route) and relaunches it detached, with a rollback path. Plan §5 B.3, §5 B.5.

1. **Strip `restart`'s own flags (`--exe`, `--timeout`) BEFORE `config.Load` (B.3 step 1).** `config.Load`'s flag set is closed and rejects unknown flags (`config.go:270-271`, `ContinueOnError`), which would make `--exe` dead on arrival. Same pattern as bead 08's `--timeout` strip.
2. **Read the state file.** **No file:** refuse with `serve is not running (no state file at <path>); start with lens serve` and exit non-zero. With a file: retain its `exe`/`args`/`cwd` for rollback and spawn (the file is deleted by the graceful exit in step 3).
3. **`GET /api/health`.** Two sub-cases:
   - **(a) Running (200 OK):** proceed to step 4.
   - **(b) Health refuses:** also dial the proxy and dashboard addresses from the retained state file — or, when the file is the early write with empty address fields, from `cfg.ProxyAddr`/`cfg.DashboardAddr` (F7.3; `config.Load` has already run) — through bead 08's loopback-normalizing dial helper. If a port still responds (serve is starting but health is not ready): treat as running, proceed to step 4. If **both** ports refuse **and** the file's recorded pid is **not alive** (`isProcessAlive`, bead 08): **stale file** — log "stale state file, removing", remove the file, skip directly to step 5. If **both** ports refuse but the pid is still alive (F6.5): treat as *starting* and proceed to step 4, where the shutdown POST and wait fail closed on `--timeout` rather than spawning a second instance.
4. **`POST /api/shutdown`**, wait until both ports refuse a dial **and** the state file is gone (bounded by `--timeout`) — proving the drain finished and the store is closed.
5. **Spawn `exe <args…>` detached**, `cmd.Dir` set to the retained `cwd` (so relative paths in `args`, e.g. `--db-path`, resolve against the original working directory), stdout+stderr appended to `log_path`. Windows `CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS`; Unix `Setsid` — **two small build-tagged files**. Never prepend a second `serve` (`args` already begins with it).
6. **Poll `/api/health` until 200 or `--timeout`.**
   - **On success:** report pid, exe, log path, and the measured gap.
   - **On timeout, no `--exe`:** print `timed out waiting for new process to bind; it may still be starting — check lens doctor`; do **not** kill the child (it may be mid-`CREATE INDEX` on first start after the E upgrade).
   - **On timeout or unhealthy, `--exe` given:** kill the new process, respawn the retained previous `exe`/`args` with `cmd.Dir` = retained `cwd`, wait for health, print the log tail, exit non-zero naming which binary is serving.
7. **Pre-listener boot time (B.3 step 7).** `store.Open` (on the E upgrade: 20-60 s `CREATE INDEX`), `checkRedaction` (full-table scan), and `purgeOnStartup` (unbounded when `RetentionDays > 0`) all run before the listeners bind. The default 30 s health timeout covers a normal boot; document that `--timeout 180s` is for the first restart after the index build and for large stores with retention enabled.

`--exe` is the answer to the locked-binary problem: build elsewhere, `lens restart --exe D:\build\lens.exe`.

**Register the command** in `cmd/lens/main.go:15-28`.

**Rationale**:

B.3. A served `lens.exe` cannot be replaced on Windows while running; `restart` is how an operator swaps the binary or reloads a rebuilt one. `--exe` plus the kill/respawn rollback exists so a bad build does not leave the machine with no working proxy — fail open (the operator's session) at the process level. Detaching is what lets the child outlive the console that launched it.

**Outcome Definition**:

- `lens restart` strips `--exe`/`--timeout` before `config.Load`.
- No state file → refuses with the message and exits non-zero.
- Running / starting / stale-file / no-port sub-cases behave as step 3.
- A running serve is stopped through `POST /api/shutdown` and the wait is for the state file to disappear, not just the ports.
- The child is detached, spawns with `cmd.Dir` = retained `cwd`, and survives its parent.
- On `--exe` failure the previous binary is respawned and the command exits non-zero naming which binary serves.
- `go build ./... && go vet ./... && go test ./...` pass; no test touches the configured live ports.

**Test Specifications** (`internal/cli/restart_test.go`, all ephemeral ports + temp DB):

- **Helper-process child survives its parent** — the detach test: spawn, exit the parent, assert the child is still responding to `/api/health`.
- **Rollback path** with a deliberately unhealthy `--exe`: the previous binary is respawned and health returns 200; the command exits non-zero and names the serving binary.
- **Timeout with no `--exe`** does **not** kill the child.
- **Stale state file:** pre-seeded, dead pid, nothing listening → removes the file and proceeds to **spawn** (step 5) without burning `--timeout`.
- **Pid alive but old `started_at`, both ports refusing, nothing listening** → treated as *starting*, not removed, falls through to the shutdown POST and bounded wait.
- **Dead pid, no file after remove** → the "no state file" refusal (step 2) on a second run.
- **Consumer drain slow:** restart waits for the state file gone, not just port refusal.

**Files to Touch**:
- `internal/cli/restart.go` (create — `lens restart`)
- `internal/cli/spawn_windows.go` / `internal/cli/spawn_unix.go` (create — build-tagged detached-spawn flags)
- `internal/cli/restart_test.go` (create)
- `cmd/lens/main.go` (modify — register `restart` in `commands` at :15-28)
- `docs/context/cli-and-tooling.md` (modify — the new command)
