### Bead 12: `feat` `HotDays` config + the archiver folded into the single serve maintenance goroutine + purge/`GCArchive`

- **Bead ID**: br-GI-27-12
- **Priority**: P1 (high)
- **Original Estimate**: 2h
- **Dependencies**: br-GI-27-08, br-GI-27-11
- **Blocks**: br-GI-27-13, br-GI-27-14, br-GI-27-15

**Description**:

C.2 config + serve + purge rows (plan §6). This bead turns on archival: the `HotDays` key, the archiver folded into bead 08's **existing** maintenance goroutine (no second goroutine), purge of archived bodies, and `GCArchive`. Plan §6 C.1-C.2.

**1. Config (`internal/config/config.go`, follow the `RetentionDays` precedent at :100/:124/:173/:255/:288).**
- `HotDays int` field; `--hot-days` flag (`applyFlags`, near :288); `LENS_HOT_DAYS` env (`fieldsByEnv`, near :173); `config.toml` key `HotDays` (`applyKV`, near :255). **Default 0** — archival disabled until the operator opts in; a typical value is 7. **Negative rejected** by `Validate` (`:327`, beside the `RetentionDays < 0` check at :363-364).
- **Validation rule:** `HotDays > RetentionDays` is rejected **only when `HotDays` was explicitly set above zero AND `RetentionDays > 0`**. A defaulted `HotDays = 0` is always valid regardless of `RetentionDays`. Test: `RetentionDays=3`, no `HotDays` set → `Validate` passes; `HotDays=9`, `RetentionDays=7` → `Validate` fails.

**2. `serve` — fold the archive steps into bead 08's ONE maintenance goroutine, never start a second (`internal/cli/serve.go`).** The goroutine bead 08 created runs purge on boot and per tick. Bead 12:
- Runs the archiver **once at boot**, then on each tick runs `purgeOnStartup`, **then** the archiver, **then** `GCArchive` — one ticker, one receiver, so neither job steals the other's ticks (F6.1).
- **Started AFTER the listeners are up**, never on the boot path.
- **Fail-open:** an error is logged, never fatal.
- **Joined before `st.Close()`:** it checks `ctx.Done()` between batches and is waited on by the WaitGroup alongside `<-consumerDone` (bead 08's join). Pass `ctx` into the archiver's store calls so cancellation interrupts a long statement.
- **First `serve` with `HotDays > 0` moves/NULLs bodies older than the hot window — a bulk data migration by the CLAUDE.md rule.** Back up `lens.db` (+ `-wal`, `-shm`) to a separate path **before enabling `HotDays`** and state the backup path. This is a **bead acceptance criterion**: the requirement appears in the `doctor` output and in this bead's completion note.

**3. Purge deletes archived bodies (`internal/store/store.go`, `internal/store/archiver.go`).** `PurgeOlderThan` (`:1014`) and the `--unpriced` path (`purgeUnpricedWhere`) also delete the archived bodies — the marker cascade plus the day-file row. `GCArchive` collects orphaned day rows. The Purge dry-run byte estimate (`PurgeableBytes` at :1055 / `CountUnpriced` at :1070 / `UnpricedBytes` at :1083) **counts archived bytes** too (handled separately, not via `WithBodies` hydration — see bead 11). All in short batches holding the single writer connection.

**4. `lens doctor` (`internal/cli/doctor.go`, the config rows at :61/:67 and the checks).**
- Print `HotDays` beside `retention_days`.
- **WARN when `archive/` is unwritable.**
- **INFO** suggestion to enable archival when the store exceeds a size threshold and `HotDays == 0`.
- The **backup-first requirement** for a first `HotDays > 0` run must be named in the doctor output (see acceptance below).

**Rationale**:

C.1/C.2. Bodies are ~all of the 4.7 GB file, so archival is what bounds the hot file; `HotDays = 0` default keeps a fresh install's behaviour unchanged (nothing archived, nothing moved) until the operator opts in. One goroutine is a correctness requirement, not a style choice: `time.Ticker.C` delivers each tick to exactly one receiver, so a second reader would silently halve both the purge and the archive. The bulk-migration backup is the CLAUDE.md rule applied to the first real archival pass.

**Outcome Definition**:

- `HotDays` resolves through flag > env > file > default (default 0), negative rejected, and the `HotDays > RetentionDays` rule fires only for an explicitly-set positive `HotDays` with `RetentionDays > 0`.
- `serve` starts exactly one maintenance goroutine after the listeners, running purge → archive → `GCArchive` per tick; it is joined before `st.Close()` and fail-open.
- A first `serve` with `HotDays > 0` archives older bodies; the doctor output and the completion note state the required `lens.db`(+`-wal`/`-shm`) backup path taken first.
- Purge deletes archived bodies (marker cascade + day-file row) and `GCArchive` collects orphans; the dry-run byte estimate counts archived bytes.
- `doctor` prints `HotDays`, WARNs on an unwritable `archive/`, and INFO-suggests archival when the store is large and `HotDays == 0`.
- `go build ./... && go vet ./... && go test ./...` pass.

**Test Specifications**:

- `internal/config/config_test.go`: precedence table for `HotDays`; negative rejected; `RetentionDays=3` with no `HotDays` → passes; explicit `HotDays=9` with `RetentionDays=7` → fails (F3.2).
- `internal/store/store_test.go` / `archiver_test.go`: purge deletes archived bodies and the dry-run counts them; `GCArchive` removes orphaned day rows but not referenced ones.
- `internal/cli/serve_test.go`: the archiver runs at boot and per tick **inside** bead 08's goroutine (assert one goroutine, not two — e.g. that a single tick advances both purge and archive); **cancel during an archiver batch → the state file is not removed until the batch returns** (the join guarantee).
- `internal/cli/doctor_test.go`: `HotDays` printed; unwritable `archive/` → WARN; large store + `HotDays == 0` → INFO; the backup-first note present for a first `HotDays > 0` run.

**Files to Touch**:
- `internal/config/config.go` (modify — `HotDays` field/default/env/key/flag/Validate)
- `internal/config/config_test.go` (modify)
- `internal/cli/serve.go` (modify — fold archive + GC into bead 08's maintenance goroutine; boot-time first pass)
- `internal/cli/serve_test.go` (modify)
- `internal/store/store.go` (modify — `PurgeOlderThan`/`purgeUnpricedWhere` cascade; dry-run byte estimate counts archived bytes)
- `internal/store/archiver.go` (modify — `GCArchive`; take `ctx`)
- `internal/store/store_test.go` (modify)
- `internal/cli/doctor.go` (modify — print `HotDays`, archive WARN/INFO, backup-first note)
- `internal/cli/doctor_test.go` (modify)
