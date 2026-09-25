### Bead 11: `feat` `body_archive` schema (backup first), the archiver, `Filter.WithBodies`, and archive-hydrating reads

- **Bead ID**: br-GI-27-11
- **Priority**: P1 (high)
- **Original Estimate**: 3h (see the flagged note)
- **Dependencies**: br-GI-27-02
- **Blocks**: br-GI-27-12, br-GI-27-13

**Description**:

Workstream C (plan §6 C.2). Bodies are ~all of the 4.7 GB file; move them out of the hot DB into per-UTC-day compressed files, transparently hydrated on read, while every scalar/token/cost column stays in `requests` forever.

**Backup first — this bead adds a table to `lens.db` (a schema migration).** Before applying it against the live `~/.deepseek-lens/lens.db`, copy the DB **and** its sidecars to a separate path and state the path:
```
copy <dir>/lens.db     <backupdir>/lens.db.gi27-prearchive
copy <dir>/lens.db-wal <backupdir>/lens.db.gi27-prearchive-wal   (if present)
copy <dir>/lens.db-shm <backupdir>/lens.db.gi27-prearchive-shm   (if present)
```

**1. Schema — `internal/store/schema.sql`.** Add the `body_archive` marker table:
```sql
CREATE TABLE IF NOT EXISTS body_archive (
    request_id  INTEGER NOT NULL,
    day         TEXT NOT NULL,            -- the UTC day file this body moved to (YYYY-MM-DD)
    archived_at INTEGER NOT NULL,
    body_mask   INTEGER NOT NULL          -- which of req_body/resp_body moved
);
```
Applied by the store's existing mechanism — `Open` runs `schema.sql`'s `CREATE TABLE IF NOT EXISTS` on every start (`store.go:91-98`, `:121`); there is **no migration framework** (`schema.sql:1-8`) (F6.6). Add the indexes the queries need (e.g. by `request_id` and by `day`).

**2. `internal/store/archiver.go` (new).** Batch policy:
- Iterate per UTC day older than the hot window (`HotDays`, bead 12 wires the value; this bead provides the archiver that takes a boundary).
- Write the day file `<dir of DBPath>/archive/bodies-YYYY-MM-DD.db`, **zstd-compressed per row** with `github.com/klauspost/compress/zstd` (**already a dependency — no new module**; verify with `grep klauspost go.mod`).
- **Per row: the day-file insert commits FIRST, then one hot transaction writes the marker and NULLs the two bodies.** Crash between the two steps leaves a duplicate, **never a loss** (archive copy first, hot NULL last).
- Hold the single writer connection (`SetMaxOpenConns(1)`) in **short batches only** — claude-lens's GI-13 hang was a long hold of it.
- Take `ctx` into the store calls and check `ctx.Done()` between batches (the join in bead 12 depends on it).

**3. Read model decision (`internal/store`).** `ListRequests` returns rows **without bodies by default** via `Filter.WithBodies bool` (`types.go:126`):
- When `false` (the default for all `GET /api/requests` callers and every list-style CLI command), the query selects `NULL AS req_body, NULL AS resp_body` from `requestColumns`.
- When `true`, hydrate. `GetRequest`, `lens export`, `lens show`, and replay lookups set `WithBodies: true`.

**The real body-reader class (`WithBodies: true`) — enumerate in code, do not trust a summary:**
- `GetRequest` (`api.go:383`, `api.go:478` — the detail handler and the replay path)
- replay row lookup (`internal/cli/replay.go:184`)
- `lens export` (`internal/cli/export.go:45` — encodes the whole struct via `json.NewEncoder`; **list-hydration must batch by archive day**: group IDs per day file and open one day file per day, not one per row)
- `lens show` (`internal/cli/show.go:68`)

**Not body readers and not in scope:** the redaction self-test (reads `req_headers`/`resp_headers` only), the in-flight analyzers (act on the `CapturedCall`, not a stored row), and `store.go:1055,1083` (`PurgeableBytes`/`UnpricedBytes` — SQL `LENGTH()` aggregates; archived byte counts for the Purge dry-run estimate are handled separately, not via `WithBodies` hydration).

**`ListRequests` callers must pass `WithBodies: false`:** `api.go:352, 632, 657, 977` (`:977` is the session-detail handler), `internal/cli/ls.go:62`, `internal/cli/tail.go:51`, `internal/cli/stats.go:84`.

**4. One store helper** that tries the hot DB first, then the archive day file, backs hydration for each `WithBodies: true` caller. **Writers that read bodies skip archived rows** and name `archive restore` (bead 13 provides the command).

**Outcome Definition**:

- `body_archive` exists on the base tree (fresh and existing DB); the backup above was taken first and its path stated in the completion note.
- The archiver round-trips a body (archive → hydrate → identical bytes), compressing per row with zstd and committing the day-file insert before the hot marker/NULL.
- Each crash point between the two steps is recoverable from one side (duplicate, never loss).
- `ListRequests` defaults to no bodies; `Filter.WithBodies: true` hydrates through the one helper; the four readers above hydrate; front-end list paths and the enumerated `ListRequests` callers get null bodies and open **no** archive day file.
- `lens export` hydrates by day, opening one day file per day.
- `go build ./... && go vet ./... && go test ./...` pass.

**Test Specifications** (`internal/store/archiver_test.go`, plus the listed packages):

- **Round trip:** archive a row, hydrate it, assert byte-identical `req_body`/`resp_body`.
- **Every crash point:** stop after the day-file insert (before the hot commit) and after the hot commit (before the day-file row delete) — assert the body is recoverable from one side each time.
- **Positive control:** `GET /api/requests?limit=50` with archived rows returns `req_body: null, resp_body: null`, and **no archive day file is opened** (assert via a counting/failing open).
- **Hydration for each `WithBodies: true` reader** in the enumerated list (`GetRequest`, replay lookup, export, show).
- **Export batching:** an export spanning two archive days opens each day file once.
- **Fault injection is the test** (plan §6 C.3): the two stop-points above are the required cases.

**Files to Touch**:
- `internal/store/schema.sql` (modify — `body_archive`)
- `internal/store/archiver.go` (create — the archive/restore primitives, zstd, short batches)
- `internal/store/archiver_test.go` (create)
- `internal/store/types.go` (modify — `Filter.WithBodies` at :126)
- `internal/store/store.go` (modify — `ListRequests`/`requestColumns` null-body default; the hydration helper)
- `internal/store/store_test.go` (modify)
- `internal/api/api.go` (modify — `GetRequest` sets `WithBodies: true` at :398/:493; list callers pass false at :367/:647/:672/:998)
- `internal/api/api_test.go` (modify)
- `internal/cli/export.go` (modify — `WithBodies: true` + batch by day at :45)
- `internal/cli/show.go` (modify — `WithBodies: true` at :68)
- `internal/cli/replay.go` (modify — `WithBodies: true` at :184)
- `internal/cli/ls.go`, `internal/cli/tail.go`, `internal/cli/stats.go` (modify — explicit `WithBodies: false` at :62/:51/:84)

**Note (flagged at beadify):** §12 bundles the schema change, the archiver, the read-model flag, and hydration for the whole reader class into this one bead. Realistically more than two hours of agent work; kept whole because §12 fixes it as the single owner of `body_archive` and `Filter.WithBodies`, and beads 12/13 depend on both. See the returned summary.
