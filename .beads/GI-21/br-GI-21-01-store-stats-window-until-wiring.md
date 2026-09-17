### Bead 1: `statsWindow` helper and `until` wiring into `StatsSummary`/`durationPercentiles`/`StatsByModel`/`StatsByCostSource`

- **Bead ID**: br-GI-21-01
- **Priority**: P0 (critical)
- **Original Estimate**: 1.5h
- **Dependencies**: None
- **Blocks**: br-GI-21-02, br-GI-21-03, br-GI-21-04

**Description**:

Add a shared `statsWindow(since, until time.Time) (whereSQL string, args []any)` helper to
`internal/store/store.go`, and thread a new `until time.Time` parameter through the four *existing*
stats methods — `StatsSummary`, `durationPercentiles`, `StatsByModel`, `StatsByCostSource` — at all
five of their query sites. `StatsByDay` (renamed `StatsByPeriod` in br-GI-21-02) is **not** touched
by this bead; leave it exactly as it is today.

`statsWindow` must build its clause the same way `requestWhere` does (`store.go:327-355`): each bound
is appended *conditionally*, not unconditionally —

```go
func statsWindow(since, until time.Time) (string, []any) {
	var where []string
	var args []any
	if !since.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, since.UnixNano())
	}
	if !until.IsZero() {
		where = append(where, "started_at < ?")
		args = append(args, until.UnixNano())
	}
	if len(where) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(where, " AND "), args
}
```

This is the load-bearing detail: the five methods today pass `since.UnixNano()` **unconditionally**
into `WHERE started_at >= ?`, which only works for the lower bound because a zero `time.Time`'s
`UnixNano()` is a large negative number, smaller than every real row's `started_at` — an accidental
"unbounded lower bound for free," not a deliberate design. That trick is **destructive** if copied to
the upper bound: an unconditional zero-value `until` plugged into `AND started_at < ?` is also a
large negative number, so the clause would exclude every row and every stats query would silently
return zero results the moment this bead's signature change lands anywhere `until` defaults to
`time.Time{}`. `statsWindow` must use `requestWhere`'s conditional-append pattern for **both** bounds,
not the old unconditional trick for either.

Update each of the five query sites to use `statsWindow`'s returned clause in place of the current
hardcoded `WHERE started_at >= ?`, and pass its `args` to the query call:

- `StatsSummary` (`store.go:405-438`) has **two** queries that both need the window: the main
  aggregate (`store.go:407-417`) and the warnings join (`store.go:423-425`, `... JOIN requests r ON
  r.id = w.request_id WHERE r.started_at >= ?`). `statsWindow` emits the bare column name
  `started_at`, not `r.started_at` — leave it bare; `warnings` has no `started_at` column, so SQLite
  resolves the unqualified name to `requests.started_at` without ambiguity in the joined query too.
  Do not add a table-alias prefix inside `statsWindow` itself.
- `StatsSummary` calls `durationPercentiles(ctx, since)` internally at `store.go:430` — update that
  call site to `durationPercentiles(ctx, since, until)`.
- `durationPercentiles` (`store.go:442-465`) — one query, `store.go:443-444`.
- `StatsByModel` (`store.go:482-505`) — one query, `store.go:483-490`.
- `StatsByCostSource` (`store.go:540-562`) — one query, `store.go:541-547`.

New signatures:

```go
func (s *Store) StatsSummary(ctx context.Context, since, until time.Time) (*Summary, error)
func (s *Store) durationPercentiles(ctx context.Context, since, until time.Time) (p50, p95 float64, err error)
func (s *Store) StatsByModel(ctx context.Context, since, until time.Time) ([]ModelStat, error)
func (s *Store) StatsByCostSource(ctx context.Context, since, until time.Time) ([]CostSourceStat, error)
```

**This bead leaves `internal/api` and `internal/cli` non-compiling.** That is expected and resolved
by br-GI-21-03 and br-GI-21-04 respectively, once br-GI-21-02 also lands. This bead's own gate is
`go test ./internal/store/...`, not `go build ./...`.

**Rationale**:

D7 — if `until` only reached `StatsByPeriod` (br-GI-21-02), the summary/by-model/cost-source numbers
on the same Stats tab would silently describe a different window than the chart and history table
sitting next to them. One shared helper, used at all six query sites across five methods (this bead
covers five sites across four methods; br-GI-21-02 covers the sixth), is what keeps that from drifting
out of sync — the alternative is six hand-written conditionals that a future edit fixes in one copy
and forgets in another.

**Outcome Definition**:

- `statsWindow(time.Time{}, time.Time{})` returns `("", nil)`.
- `statsWindow(since, time.Time{})` returns `(" WHERE started_at >= ?", []any{since.UnixNano()})`.
- `statsWindow(time.Time{}, until)` returns `(" WHERE started_at < ?", []any{until.UnixNano()})`.
- `statsWindow(since, until)` returns both conditions ANDed, args in `(since, until)` order.
- `StatsSummary`, `durationPercentiles`, `StatsByModel`, `StatsByCostSource` all accept `until` and
  apply it identically via `statsWindow` at every one of their query sites (both of `StatsSummary`'s).
- `go test ./internal/store/...` passes.
- `go build ./...` is **not** expected to succeed yet (internal/api, internal/cli break on the
  signature change until br-GI-21-03/04 land) — do not treat that as a regression in this bead.

**Test Specifications**:
- Unit Tests:
  - `TestStatsSummary` (`store_test.go:426-480`): update the call at `store_test.go:454` to
    `s.StatsSummary(ctx, base.Add(-time.Hour), time.Time{})`; all existing assertions unchanged
    (regression guard, no behavior change) — this is plan finding F2.1's first half.
  - `TestListRequestsPerformanceAndLimits` (`store_test.go:1292-`): update the call at
    `store_test.go:1329` the same way — F2.1's second half.
  - New `TestStatsWindowUntilBound` (or equivalent table-driven test): seed rows before and after a
    chosen instant, then assert, via `StatsSummary` and `StatsByModel`:
    - **`since` set, `until` absent** returns every row from `since` forward with no upper cutoff —
      the case Test strategy calls out by name as the one an unconditional zero-value `until` would
      silently break (must not be only an implicit assumption).
    - an explicit `until` excludes rows at or after it.
    - `since > until` returns an empty result (`0` rows / empty slices), not an error.

**Files to Touch**:
- `internal/store/store.go` (modify)
- `internal/store/store_test.go` (modify)
