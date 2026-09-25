### Bead 2: `feat` `idx_requests_stats` covering index + EXPLAIN plan-assertion test (backup first)

- **Bead ID**: br-GI-27-02
- **Priority**: P0 (critical)
- **Original Estimate**: 1.5h
- **Dependencies**: None (but see the stash precondition below)
- **Blocks**: br-GI-27-11, br-GI-27-15

**Description**:

**Step 0 — precondition, do this first (plan §13).** An uncommitted prototype of A.1/A.2 is in the working tree (`internal/proxy/proxy.go`, `internal/sink/sink.go`, `internal/parse/sse.go`, `internal/consumer/consumer.go`, `internal/store/store.go`, `internal/api/api.go`, `internal/cli/stats.go`, `internal/web/app.js`, `internal/web/index.html`, plus tests). Run `git stash -u` on it **before** touching anything here (the plan file itself must be committed first if it is untracked, or it goes into the stash too). It is a reference only — **never `git stash pop`**. Adopting it bypasses bead boundaries and carries the pre-review consumer-guard bug (F1.1: it tests post-decode headers). The stash also fixes the base-tree line numbers this bead cites.

**1. Add the index to `internal/store/schema.sql`** (after the existing indexes at :45-46):

```sql
CREATE INDEX IF NOT EXISTS idx_requests_stats ON requests(
    started_at, duration_ns, error_text, model_resolved, model_requested,
    cost_source, cost_usd, input_tokens, output_tokens,
    cache_creation_tokens, cache_read_tokens);
```

`started_at` **leads** (deliberate divergence from claude-lens's `idx_events_stats`, which led with token columns): a window query *seeks* and a covering scan is bounded by the window. The column set covers the five aggregate statements in `store.go` — `StatsSummary` (:438), `durationPercentiles` (:475), `StatsByModel` (:517), `StatsByPeriod` (:571), `StatsByCostSource` (:606). **Re-derive the column list at bead time on the base tree** — every line citation in this bead set was taken against the dirty working tree (which still holds the un-stashed A.1/A.2 prototype), so all of them shift once `git stash -u` runs; re-read the five statements with `git show HEAD:internal/store/store.go`, confirm each still selects only these columns, and re-check any other cite in a bead before acting on it.
- Keep `idx_requests_started_at` (:45). It is a prefix of the new index but serves `ORDER BY started_at DESC` without the wider entries. Dropping it is a measured follow-up, not part of this story (claude-lens's redundant-index drop regressed a DELETE to a full scan).
- Ordering note only — bead 15 (8 MiB cap) is gated on this: a wide hot file makes these aggregate statements take 22-56 s cold at today's 4.7 GB.

**2. The migration is just `Open` re-running `schema.sql`.** `Open` applies `CREATE TABLE/INDEX IF NOT EXISTS` on every start (`store.go:91-98`, `:121`); there is no migration framework (`schema.sql:1-8`).

**Backup first — this is a schema change.** Before applying it against the live `~/.deepseek-lens/lens.db`, copy the DB **and** its sidecars to a separate path and state the path:
```
copy ~/.deepseek-lens/lens.db     <backupdir>/lens.db.gi27-preindex
copy ~/.deepseek-lens/lens.db-wal <backupdir>/lens.db.gi27-preindex-wal   (if present)
copy ~/.deepseek-lens/lens.db-shm <backupdir>/lens.db.gi27-preindex-shm   (if present)
```
Building the index reads every row once (~20-60 s on the live file) and **holds the writer**. Do **not** open the store from a second process while a `serve` is live during the build: `lens doctor` (or any command that opens the store) would hold the write lock for the full build and the consumer's writer would hit `busy_timeout(5000)` and drop captured calls (`consumer.go:520-521` logs and drops the failed insert). Correct sequence: `lens shutdown` first, then run `lens doctor` once to build the index on the stopped file, then start — or use `lens restart --timeout 180s` (bead 09) for that one restart. Verification uses a **copy** of the live DB, never the live file.

**Rationale**:

E/§11A. On the live 4.7 GB store the Stats aggregates run at 22-56 s cold and use **no** covering index — `EXPLAIN` shows `SEARCH … USING INDEX idx_requests_started_at` plus a table lookup per row, and the aggregated token columns sit *after* `req_body`/`resp_body` in table order, so reading them walks each row's blob overflow chain. The Stats tab fires these on each refresh. The index is the applicable half of claude-lens's GI-13 C-8. `idx_events_session_started` is **not** applicable (session queries are `session_id = ?` point aggregates, 1 ms) nor is `idx_events_cost_source`.

**Outcome Definition**:

- `idx_requests_stats` exists on the base tree as quoted above and is created on `Open` for both a fresh and an existing DB.
- For each of the five aggregate statements (`StatsSummary` main aggregate, `durationPercentiles`, `StatsByModel`, `StatsByPeriod`, `StatsByCostSource`), `EXPLAIN QUERY PLAN` shows `USING COVERING INDEX idx_requests_stats`.
- The `warnings w JOIN requests r` sub-statement inside `StatsSummary` (`store.go:457-458`) does **not** full-scan `requests` (it looks up by primary key); it is **not** asserted COVERING.
- The five queries return the same rows with and without the index (equality against a fixture).
- `go build ./... && go vet ./... && go test ./...` pass; the TTFB test stays green.

**Test Specifications**:

`internal/store/store_test.go` (modify — the plan-assertion gate, with a positive control):

1. **Positive control first**: assert the COVERING assertion **fires on a known non-covering query** (e.g. one that also selects `req_body`) before relying on a pass. A gate that returns "no COVERING" for everything is broken, not green.
2. For each of the five aggregate statements, run `EXPLAIN QUERY PLAN` and assert the output contains `USING COVERING INDEX idx_requests_stats`; build the fixture first so the plan is stable.
3. Assert the `warnings` join sub-statement does not `SCAN requests`.
4. **Equality**: run each aggregate with and without the index (same fixture) and assert identical result rows.
5. **Long-`error_text` fixture**: insert a fixture row with ~10 KB `error_text` (`schema.sql:30` — unbounded upstream error text) and confirm coverage still applies (index entries inflate but coverage holds).
6. **Migration on a copy**: build the index against a **copy** of the live DB and record the timing (indicative, not a benchmark).

**Manual / recorded**: the pre-index `EXPLAIN` plans and cold/warm timings from §11A, captured against the copy.

**Files to Touch**:
- `internal/store/schema.sql` (modify — add `idx_requests_stats` after :46)
- `internal/store/store_test.go` (modify — the plan-assertion tests + positive control)
- (no Go source change; the index is applied by the existing `Open` path)
