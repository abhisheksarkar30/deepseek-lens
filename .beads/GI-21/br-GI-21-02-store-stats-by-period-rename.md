### Bead 2: `DayStat`→`PeriodStat` rename and `StatsByDay`→`StatsByPeriod(since, until, granularity)` generalization

- **Bead ID**: br-GI-21-02
- **Priority**: P0 (critical)
- **Original Estimate**: 1.5h
- **Dependencies**: br-GI-21-01 (needs `statsWindow`)
- **Blocks**: br-GI-21-03, br-GI-21-04

**Description**:

1. **`internal/store/types.go`**: rename `DayStat` (`types.go:182-189`) to `PeriodStat`, its `Day`
   field to `Period`. Update the doc comment to describe all four granularities' bucket formats
   (D2), e.g.:

   > `PeriodStat` is one row of `StatsByPeriod`'s result. `Period` is formatted per the requested
   > granularity, all UTC: hour `"2006-01-02T15:00"`, day `"2006-01-02"` (unchanged), week
   > `"2006-W02"` (SQLite `%W`, Monday-first — not ISO-8601 week numbering), month `"2006-01"`.
   > `UnpricedCount` is that period's share of `Summary.UnpricedCount`.

2. **`internal/store/store.go`**: rename `StatsByDay` (`store.go:507-532`) to
   `StatsByPeriod(ctx context.Context, since, until time.Time, granularity string) ([]PeriodStat, error)`.

   Add a granularity whitelist **inside this method**, before any query text is built — this is the
   only thing standing between "known granularity" and an arbitrary format string reaching SQL, since
   SQLite's `strftime` takes its format as a positional argument with no bind-parameter form:

   ```go
   var format string
   switch granularity {
   case "hour":
   	format = "%Y-%m-%dT%H:00"
   case "day":
   	format = "%Y-%m-%d"
   case "week":
   	format = "%Y-W%W" // ponytail: SQLite %W is Monday-first week-of-year, not ISO-8601
   	                    // year-boundary-carry week numbering — fine for a dashboard bucket
   	                    // label, not a "which ISO week" API.
   case "month":
   	format = "%Y-%m"
   default:
   	return nil, fmt.Errorf("store: stats by period: invalid granularity %q", granularity)
   }
   ```

   Never string-format the raw `granularity` parameter into the query text directly, even after this
   switch — always go through `format`, which only ever holds one of the four literal constants
   above. This is the property the plan's self-review calls out as the one thing worth double-checking
   in review: a pattern that string-interpolates an already-validated value is easy to copy-paste into
   a future call site that skips validation.

   Build the query as:

   ```go
   where, args := statsWindow(since, until)
   query := "SELECT strftime('" + format + "', started_at / 1000000000, 'unixepoch'), " +
   	"COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), " +
   	"COALESCE(SUM(cost_usd), 0), COALESCE(SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END), 0) " +
   	"FROM requests" + where + " GROUP BY 1 ORDER BY 1"
   ```

   using `statsWindow` from br-GI-21-01 for the same reason the other four methods do (D7). Rename
   the scan-loop variable/field `d.Day` → `d.Period` (`store.go:524-529`).

**Rationale**:

D1 — one generalized method replacing four near-duplicate queries that would differ only in their
`strftime` format string; the whitelist-before-string-build ordering is what keeps `granularity`
from ever becoming a raw SQL format-string injection point, belt-and-suspenders beyond the 400 the
API layer (br-GI-21-03) already returns for an invalid value.

**Outcome Definition**:

- `store.PeriodStat` replaces `store.DayStat` throughout `internal/store` — no remaining reference to
  `DayStat` or a `.Day` field in this package.
- `StatsByPeriod(ctx, since, until, granularity)` returns correctly bucketed rows for each of
  `"hour"`/`"day"`/`"week"`/`"month"`, honors `until` via `statsWindow`, and returns a non-nil error
  with the query never built or executed for any other `granularity` string.
- `go test ./internal/store/...` passes.
- `internal/api`, `internal/cli` still fail to compile — expected, resolved by br-GI-21-03/04.

**Test Specifications**:
- Unit Tests (rename `TestStatsByDay`, `store_test.go:482-517`, to `TestStatsByPeriod`):
  - Day granularity: preserve the existing three-bucket assertions (`store_test.go:497-516`), now
    reading `.Period` and passing `granularity="day"`, `until=time.Time{}` explicitly.
  - Hour granularity: two rows either side of an hour boundary (e.g. `13:59:30` and `14:00:30`) land
    in the `"...T13:00"` and `"...T14:00"` buckets respectively.
  - Week granularity: rows either side of a Sunday 23:59 / Monday 00:01 boundary land in different
    `%W`-numbered buckets.
  - Month granularity: rows either side of a Jan 31 23:59 / Feb 1 00:01 boundary land in
    `"2026-01"` and `"2026-02"` respectively.
  - Invalid granularity (e.g. `"fortnight"`): returns a non-nil error and a nil/empty result.
  - `until` bound specific to `StatsByPeriod`: rows at or after `until` are excluded from every
    bucket they would otherwise fall in.
  - `since > until` on `StatsByPeriod`: returns an empty (zero-length) result, not an error.

**Files to Touch**:
- `internal/store/types.go` (modify)
- `internal/store/store.go` (modify)
- `internal/store/store_test.go` (modify)
