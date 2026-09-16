# Bead br-GI-15-01: `Filter.Offset`, shared where-builders, and the request/warning counts

**Plan Reference**: `docs/planning/GI-15-pagination.md` §Store layer

- **Priority**: P0 (critical)
- **Dependencies**: none
- **Blocks**: br-GI-15-02, br-GI-15-03, br-GI-15-04

## Description

Give `internal/store` the primitives the paginated API layers on: an offset, a count that cannot
drift from its list, and the warnings-summary aggregate. Everything here is **additive** — no
existing method changes signature, so the tree stays green.

**1. `Filter.Offset`** — `internal/store/types.go`'s `Filter` gains `Offset int` (0 = start at the
top), documented next to the existing `Limit` comment. Both `ListRequests` and `ListWarnings`
append `OFFSET ?` beside their existing `LIMIT ?`. Clamp a negative offset to 0 the way
`Limit <= 0` is already clamped to `DefaultLimit` — a caller that computes `offset` arithmetically
must not be able to turn it into a SQL error.

**2. Extract the where-builders.** Today each list method builds its own `WHERE` clause inline.
Pull each into a helper returning `(string, []any)`:

```go
func requestWhere(f Filter) (string, []any)
func warningWhere(f Filter) (string, []any)
```

`ListRequests`, `ListWarnings`, `CountRequests`, and `CountWarnings` all call these. This is the
whole point of the extraction: `X-Total-Count` is only meaningful if the count selects exactly the
rows the list would have returned, and two hand-maintained clauses is how they silently diverge.
`WarningSummary` uses `warningWhere` too.

**3. Add `id DESC` as a secondary sort key.** `ORDER BY started_at DESC, id DESC` for requests and
`ORDER BY created_at DESC, id DESC` for warnings. Without a total order, rows sharing a timestamp
(the consumer batch-inserts, so equal timestamps are normal, not exotic) can be served on two
different pages or skipped across a boundary. `id` is the table's primary key and unique, so this
makes the sort total. No new index is needed for the tiebreaker — the existing PK is the second
sort key.

**4. New counts.** `CountRequests(ctx, f) (int, error)` and `CountWarnings(ctx, f) (int, error)`:
`SELECT COUNT(*) FROM ... WHERE <shared clause>`. Both deliberately ignore `f.Limit` and
`f.Offset` — a filtered total that shrank to the page size would defeat the header.

**5. `WarningGroup` + `WarningSummary`.** New type in `internal/store/types.go`:

```go
type WarningGroup struct {
    Kind     string
    Severity string
    Count    int
    LastSeen time.Time
}
```

**No `json` tags** — this is the repo's documented convention for types crossing the API and the
SSE broker (`types.go:58-61`), and it is load-bearing for br-GI-15-06: the served keys are the
capitalized Go field names, so the dashboard must read `g.Kind`, not `g.kind`.

`WarningSummary(ctx, f Filter) ([]WarningGroup, error)` honors `Since`/`Kind`/`Severity` only, via
`warningWhere`, and returns:

```sql
SELECT kind, severity, COUNT(*), MAX(created_at)
FROM warnings WHERE <shared clause>
GROUP BY kind, severity
ORDER BY COUNT(*) DESC, kind, severity
```

The `kind, severity` secondary sort keys make tie order deterministic across re-fetches — without
them two kinds with equal counts can swap places between two loads of the same tab.

**6. Covering index**, appended to `internal/store/schema.sql`:

```sql
CREATE INDEX IF NOT EXISTS idx_warnings_kind_severity_created_at ON warnings(kind, severity, created_at)
```

`WarningSummary` necessarily scans the whole `warnings` table (that is the point — it must see past
the 1000-row cap `ListWarnings` applies). The index turns that into an index scan and satisfies
`MAX(created_at)` without touching the table. Per the plan, `schema.sql` is re-executed in full on
every `Open()`, so a plain `CREATE INDEX IF NOT EXISTS` is the whole change — there is no migration
framework and none is added.

**7. Doc comments this bead makes stale.** Three, all in this bead's scope:

- `internal/store/types.go:102-104` — the `Filter` doc comment enumerates the readers and which
  fields each honors. After this bead, name `ListRequests, ListWarnings, WarningSummary` and the
  `CountRequests/CountWarnings` variants.
- `internal/store/store.go:683` — the `ListWarnings` method comment enumerates the honored fields as
  `(Kind/Severity/Since/Limit)`; it must gain `/Offset`.
- `internal/store/store.go:304-305` (`ListRequests`) enumerates no fields, so it is **deliberately
  not changed**. Same for `internal/api/api.go:234`, whose parenthetical enumerates narrowing
  predicates and already omits `Limit`. Do not "fix" either.

**Split-comment note (read before editing `types.go:102-104`).** The plan's full rewrite of that
comment also names `ListSessions` and `CountSessions`, which do not exist until br-GI-15-02. Write
only what is true *at this commit* — `ListRequests, ListWarnings, WarningSummary` plus
`CountRequests/CountWarnings` — and let br-GI-15-02 complete the sentence. A comment committed
naming a reader that does not exist yet is exactly the staleness class this plan spent two rounds
closing; the two-step is deliberate, not an oversight.

## Rationale

Everything downstream is arithmetic over these primitives. If the count can drift from the list,
`X-Total-Count` is a lie and Next/Prev is wrong; if the sort is not total, pages overlap. Both
failures are invisible in a small development database and appear only once the table is large
enough that the bug matters — which is the exact condition this ticket exists for. So the
where-builder is shared and the tiebreaker is added *before* the first paged endpoint exists.

The count is also what makes the latent undercount fixable: today the dashboard groups at most
1000 raw rows, so a kind with more than 1000 occurrences reports a wrong total. `WarningSummary`
grouping in SQL is the fix, and the index is what keeps it from being a full table scan on every
tab open.

## Outcome Definition

- `go build ./...` and `go vet ./...` pass; `go test ./internal/store/...` passes.
- No existing exported function changes signature or behavior — this bead is purely additive.
- `Filter` has `Offset int`; `ListRequests`/`ListWarnings` honor it, clamping negatives to 0.
- `requestWhere`/`warningWhere` exist, and `ListRequests`/`ListWarnings`/`CountRequests`/
  `CountWarnings`/`WarningSummary` all route through them.
- Both list `ORDER BY` clauses carry `id DESC` as a secondary key.
- `CountRequests`/`CountWarnings` return the full filtered count regardless of `Limit`/`Offset`.
- `WarningGroup` carries **no** `json` tags.
- `WarningSummary` returns one row per distinct `(kind, severity)`, ordered highest count first,
  with ties broken by `kind, severity`.
- `schema.sql` contains `idx_warnings_kind_severity_created_at`.

## Test Specifications

- Unit Tests (`internal/store/store_test.go`), extending the existing
  `TestListRequestsPerformanceAndLimits` pattern:
  - **Offset window**: seed 10 requests; `{Limit: 5, Offset: 0}` and `{Limit: 5, Offset: 5}` return
    disjoint sets whose union is the full 10. Assert *disjointness explicitly* (compare id sets),
    not just lengths — equal-length overlapping pages is the failure this guards.
  - **Last partial page**: `{Limit: 4, Offset: 8}` over 10 rows returns exactly 2 rows.
  - **Offset past the end**: `{Limit: 10, Offset: 100}` returns an empty slice and **no error**.
  - **Negative offset** clamps to 0 rather than erroring: `{Offset: -5}` == `{Offset: 0}`.
  - **Count ignores the window**: `CountRequests` returns the same value for
    `{Limit: 1, Offset: 0}`, `{Limit: 1000, Offset: 0}`, and `{Offset: 50}`.
  - **Count honors the predicate**: seed a mixed set (some `OnlyWarned`, some `OnlyErrors`, some
    `Model`-specific) and assert `CountRequests` matches the corresponding `ListRequests` result
    length for each filter, with a `Limit` far larger than the seed so the list is not truncated.
    Same for `CountWarnings` over `Kind`/`Severity`/`Since`.
  - **Duplicate timestamps — requests**: seed several requests with **identical** `started_at`;
    page through with a small `Limit` and assert no id appears twice and every id appears exactly
    once. Fixture must use identical timestamps, matching the consumer's batch-insert pattern; the
    test passes against tiebreaker-less code if the timestamps differ.
  - **Duplicate timestamps — warnings**: same, over `created_at` with an equal `Kind`.
  - **`WarningSummary` past the cap** — the regression test for the undercount fix: seed **more
    than `DefaultLimit`** warnings of one kind and assert the group's `Count` equals the true seeded
    total, not 1000. This is the test that fails against the client-side grouping this story
    removes.
  - **`WarningSummary` ordering**: seed two kinds with unequal counts → highest first; seed two
    with **equal** counts → `kind` then `severity` order, asserted deterministically.
  - **`WarningSummary` `LastSeen`** equals the max `created_at` in the group.
  - **`WarningGroup` has no json tags**: assert the marshaled JSON of a `WarningGroup` contains the
    capitalized keys (`"Kind"`, `"Severity"`, `"Count"`, `"LastSeen"`). One assertion, and it pins
    the wire contract br-GI-15-06 depends on — if someone adds tags later, this fails loudly here
    instead of rendering `undefined` cells in the dashboard.
- Integration Tests: none — no call site yet.

## Files to Touch

- `internal/store/types.go` (modify — `Offset` on `Filter`, `WarningGroup` type, the partial
  `types.go:102-104` doc-comment rewrite per the split-comment note)
- `internal/store/store.go` (modify — `requestWhere`/`warningWhere`, `OFFSET` + `id DESC` on both
  list queries, `CountRequests`, `CountWarnings`, `WarningSummary`, the `store.go:683` comment)
- `internal/store/schema.sql` (modify — `idx_warnings_kind_severity_created_at`)
- `internal/store/store_test.go` (modify — the cases above)
