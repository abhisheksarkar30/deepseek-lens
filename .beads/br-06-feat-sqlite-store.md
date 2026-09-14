# Bead 6: SQLite store and schema

- **Priority**: P0 (critical)
- **Dependencies**: 1
- **Blocks**: 7, 8, 9, 12, 13

## Description

Persistence via `modernc.org/sqlite` (pure Go, no CGO — this is why cross-compilation stays trivial).

**Connection posture** (latency invariant 5): one **writer** connection and one **read-only reader**
connection, both opened at startup. The writer is used by exactly one goroutine (the consumer,
bead 7) — never concurrently — which is what makes SQLite locking a non-issue. The reader is
independent, so dashboard queries never block capture. Both set `_pragma=journal_mode(WAL)`,
`_pragma=busy_timeout(5000)`, `_pragma=synchronous(NORMAL)`.

**Schema** (`schema.sql`, applied with `CREATE TABLE IF NOT EXISTS`):
`requests`, `sessions`, `warnings`, plus indices on `started_at`, `session_id`, and
`warnings.request_id`.

**All columns for all 13 beads are created here**, including ones only bead 10–13 populate:
`session_id`, `cost_usd`, `cost_source`, `stop_reason`, `replay_of`, `replay_edits`, `prefix_hash`.
This is
deliberate — it avoids introducing a migration framework in v1.
`ponytail:` no migration system; `CREATE TABLE IF NOT EXISTS` only. Add real migrations the moment
a shipped column must change.

`API` surface (methods on `*Store`):

- `InsertRequest(ctx, *Request) (int64, error)` — writer only
- `InsertWarnings(ctx, reqID int64, []Warning) error` — writer only
- `GetRequest(ctx, id) (*Request, error)`
- `ListRequests(ctx, Filter) ([]*Request, error)` — `Filter{Limit, Since, SessionID, Model, OnlyWarned, OnlyErrors}`
- `StatsSummary(ctx, since) (*Summary, error)` — counts, token totals, cost total, P50/P95 duration,
  drop count
- `StatsByModel(ctx, since)` / `StatsByDay(ctx, since)` — for dashboard charts
- `ListSessions` / `GetSession` / `UpsertSession`
- `ListWarnings(ctx, Filter)` — `Filter{Kind, Severity, Since, Limit}`
- `PurgeOlderThan(ctx, time.Time) (int64, error)` — retention
- `Close() error`

`Request` is the row struct; `ListRequests` supports `Limit: 0` meaning a sane default cap (1000),
never unbounded — plan risk: an unbounded dashboard query on a large table.

`Store.RedactCheck()` is a startup self-test asserting no `x-api-key` value is reachable from the
headers JSON in any row — belt and braces for the redaction default, so a future regression in the
proxy's redaction is caught at boot rather than at rest.

DB directory is created `0700` if absent; the DB file `0600`.

## Rationale

Storage is the least interesting file in the project and the one most likely to be reached by every
feature, so it is built once, completely, with every column any bead will need. Creating all columns
up front costs nothing and removes an entire class of cross-bead conflict (two beads adding columns
to the same table in different orders).

The single-writer discipline is not a limitation to work around — it is the design. Because exactly
one goroutine ever writes, SQLite's locking never engages, and the reader connection is free to
serve dashboard queries without contending.

## Outcome Definition

- `go test ./internal/store/...` passes.
- A `Request` written is read back field-for-field identical, including `[]byte` bodies.
- `journal_mode` is confirmed `wal` at runtime.
- Concurrent reader queries during a writer transaction do not block or error (asserted with
  timing).
- With 10,000 rows, `ListRequests{Limit: 10}` returns 10 and `StatsSummary` completes under 100ms.
- `ListRequests` with `Limit: 0` is capped, not unbounded.
- An `x-api-key` sentinel value placed in a header does not appear anywhere in the DB file.

## Test Specifications

- Integration Tests (`internal/store/store_test.go`, real SQLite in `t.TempDir()`):
  - **Round trip**: insert a fully-populated `Request`; read it back; assert every field.
  - **Bodies round trip**: `ReqBody`/`RespBody` byte slices survive exactly, including
    non-UTF8 bytes and a 256KB body.
  - **NULL handling**: nil `SessionHeader`, nil `StopReason`, nil `CostUSD` round trip as nil, not
    as `""`/`0`.
  - **WAL mode**: query `PRAGMA journal_mode`; assert `wal`.
  - **Warnings round trip**: insert a request plus 3 warnings; `ListWarnings` returns 3, filtered
    by kind returns 1.
  - **Filter — since**: insert at T-2h and T-1h; `Since: T-90m` returns 1.
  - **Filter — session**: two sessions; filter returns only the matching one.
  - **Filter — onlyWarned**: only requests with warnings.
  - **Filter — limit 0**: capped at the default, not unbounded.
  - **StatsSummary**: known fixture → exact token totals and cost sum.
  - **StatsByDay**: three days of fixtures → three buckets with correct counts.
  - **Reader does not block**: hold a writer transaction open; issue a reader query with a
    deadline; assert it completes. (Invariant 5.)
  - **Purge**: insert old and new; `PurgeOlderThan` removes exactly the old, returns the count.
  - **Redaction sweep**: raw file bytes scanned for the sentinel key value; assert absent.
  - **Idempotent schema**: opening twice does not error and does not duplicate data.
  - **Concurrent writers guarded**: calling `InsertRequest` from two goroutines is documented as
    forbidden; assert the writer's internal mutex serialises rather than corrupting.
- E2E: none.

## Files to Touch

- `internal/store/store.go` (create)
- `internal/store/schema.sql` (create)
- `internal/store/store_test.go` (create)
- `internal/store/types.go` (create — `Request`, `Warning`, `Session`, `Filter`, `Summary`)
- `go.mod` (modify — add `modernc.org/sqlite`)
