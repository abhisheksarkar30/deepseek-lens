# GI-21 — Hour/day/week/month historical breakdown in the Stats tab

**Ticket**: [#21](https://github.com/abhisheksarkar30/deepseek-lens/issues/21) ·
**Branch**: `GI-21-stats-by-period` (cut from `develop`) ·
**Plan version**: v1 ·
**Status**: draft

## Problem

The dashboard has two views of activity and both fall short of a real history:

1. **The header totals (`#total-calls`/`#total-tokens`/`#total-cost`) are a session counter, not
   history.** `state.totals` resets to `{0,0,0,0}` on every page load and only accumulates from
   in-memory SSE events from that point forward
   ([internal/web/app.js:153](../../internal/web/app.js#L153),
   [:311](../../internal/web/app.js#L311)) — refresh the tab and the number is gone. This is the
   "current session / after last refresh" view the story wants something *in addition to*, not a
   replacement for.
2. **The Stats tab's chart is day-wise but count-only.** `store.StatsByDay`
   ([internal/store/store.go:507-532](../../internal/store/store.go#L507-L532)) already aggregates
   count, tokens, and cost per UTC calendar day, but `renderStatsChart`
   ([internal/web/app.js:447-483](../../internal/web/app.js#L447-L483)) only ever plots
   `RequestCount` — tokens and cost exist solely as all-time totals (the summary table) or per-model
   totals (the by-model table), never per time bucket. There is no hour, week, or month view at all.

## Scope

In scope: a generalized period aggregation (hour/day/week/month) covering request count, tokens,
and cost, exposed through `GET /api/stats` and rendered in the existing Stats tab — additive to the
tab bar (no new tab; Stats already sits beside Live Feed) and to the header's live counter (untouched).

Out of scope: changing what the header counter does, a date-range picker (a bounded preset `since`
select is enough — see D3), per-model breakdown by period (the by-model table stays all-time), and
any change to retention/purge (GI-17) beyond noting its interaction as a risk.

## Design decisions

**D1 — One generalized `StatsByPeriod(ctx, since, granularity)` replacing `StatsByDay`, not four
near-duplicate methods.** `DayStat`/`StatsByDay` become `PeriodStat`/`StatsByPeriod`, with
`granularity` one of `"hour"`, `"day"`, `"week"`, `"month"`. Four copies of the same
scan-group-order query differing only in the `strftime` format string would be the kind of
duplication this codebase avoids elsewhere (see `purgeWhere` in GI-17). `granularity` is checked
against a `switch` whitelist inside the store method itself — not just at the API layer — before it
ever reaches the SQL string, so no caller of `StatsByPeriod` can smuggle a format string into the
query; an unrecognized value returns an error rather than falling through to a default. This is a
belt-and-suspenders rule, not a real attack surface (the only caller is the API handler, which
validates first), but the query interpolates the format string directly (SQLite has no
placeholder for a `strftime` format), so the whitelist is the only thing standing between "known
granularity" and "arbitrary format string in a SQL query."

**D2 — Bucket formats, all UTC (matching the existing day behavior — `DayStat`'s doc comment already
says "in UTC"):**

| Granularity | `strftime` format | Example |
|---|---|---|
| hour | `%Y-%m-%dT%H:00` | `2026-09-17T14:00` |
| day | `%Y-%m-%d` (unchanged) | `2026-09-17` |
| week | `%Y-W%W` | `2026-W37` |
| month | `%Y-%m` | `2026-09` |

*Ceiling*: SQLite's `%W` is week-of-year with Monday as the first day, **not** ISO-8601 week
numbering (no year-boundary carry rule). Good enough for a dashboard bucket label; not a "which ISO
week is this" API. `ponytail:` comment at the format-map call site names this.

**D3 — `GET /api/stats` gains `?granularity=hour|day|week|month` (default `day`, preserving today's
behavior when the param is absent) and continues to honor the existing `?since=` duration/RFC3339
param** ([internal/api/api.go:254-266](../../internal/api/api.go#L254-L266)) — no new time-window
mechanism. An unrecognized `granularity` is a 400, mirroring how an invalid `since` already behaves.
The response echoes the applied `granularity` back (same "tell the client what was actually applied"
pattern as `X-Limit` on the paginated routes), and `by_day`/`DayStat` become `by_period`/`PeriodStat`
in the JSON contract. This is a breaking wire-format rename with no deprecation shim: the dashboard
is the *only* consumer of its own API (single-user, embedded, no external clients, no versioning
anywhere else in this API), so there is nothing to migrate.

**D4 — The Stats tab gets a granularity `<select>` and a bounded `since` preset `<select>`
(`24h` / `7d` / `30d` / `90d` / `All time`), defaulting per granularity (hour→24h, day→30d, week→90d,
month→All time) so switching to hour granularity never silently issues an all-time query** — the
existing `idx_requests_started_at` index keeps that query itself cheap regardless, but an
unretained-data hour chart with thousands of points is a UX problem, not a performance one, and the
existing `since` param is the lazy fix (reuse, not a new windowing mechanism). Both selects reuse the
`.pager select` CSS class already used by the sessions/warnings pagers — no new CSS rule needed.

**D5 — `renderStatsChart` is generalized to plot one of three metrics via an accessor
(`RequestCount`, `InputTokens+OutputTokens`, `CostUSDTotal`) instead of being hardcoded to count**,
selected by a three-button metric toggle reusing the existing `.tab` button class (same visual
language as the main tab bar, zero new CSS). This is one parameterized chart function, not three
chart functions.

**D6 — A new history table (Period | Calls | Tokens | Cost) renders below the chart**, using the same
row-per-bucket data the chart already fetched and the existing `fmtTokens`/`fmtCostTotal` helpers and
table styling the by-model table uses. This is what actually answers "show me the day/hour-wise
records" — a chart alone can be eyeballed but not read exactly.

## What changes

### Modify

| File | Change |
|---|---|
| [internal/store/types.go](../../internal/store/types.go) | Rename `DayStat` → `PeriodStat`, `Day` field → `Period` (D1) |
| [internal/store/store.go](../../internal/store/store.go) | Rename `StatsByDay` → `StatsByPeriod(ctx, since, granularity)`; granularity → `strftime` format via whitelist `switch`; error on unrecognized granularity (D1, D2) |
| [internal/store/store_test.go](../../internal/store/store_test.go) | `TestStatsByDay` → `TestStatsByPeriod`: bucket correctness for all four granularities (including an hour/day/week/month boundary case each) plus the invalid-granularity error |
| [internal/api/api.go](../../internal/api/api.go) | `Store` interface method rename; parse/validate `?granularity=` (400 on unknown, default `"day"`); `statsResponse.ByPeriod []store.PeriodStat \`json:"by_period"\`` + `Granularity string \`json:"granularity"\``; pass both through to `StatsByPeriod` (D1, D3) |
| [internal/api/api_test.go](../../internal/api/api_test.go) | `TestStatsTotalsMatchFixture`/new cases: each granularity value, invalid value → 400, default-absent behaves as `"day"`, response echoes applied `granularity` |
| [internal/web/index.html](../../internal/web/index.html) | Stats section: granularity `<select>`, since preset `<select>`, three-button metric toggle, history table markup below the chart (D4, D5, D6) |
| [internal/web/app.js](../../internal/web/app.js) | `loadStats()` reads selected granularity/since and passes them as query params; `renderStatsChart` generalized to a metric accessor; new `renderStatsHistoryTable`; state + event wiring for the three new controls (D4, D5, D6) |
| `docs/context/api-surface.md`, `docs/context/data-model.md` | Refresh in Phase 5.6: `by_day`/`DayStat`/`StatsByDay` references become `by_period`/`PeriodStat`/`StatsByPeriod`, plus the new `granularity` param |

No new files — this generalizes one existing store method, one existing route, and one existing tab;
there's no new subsystem to give its own file.

## Contracts

### `GET /api/stats?granularity=hour&since=24h`

```json
{
  "since": "2026-09-16T14:00:00Z",
  "granularity": "hour",
  "summary": { "...": "unchanged" },
  "by_model": [ "...unchanged..." ],
  "by_period": [
    { "Period": "2026-09-16T14:00", "RequestCount": 3, "InputTokens": 1200, "OutputTokens": 900, "CostUSDTotal": 0.004, "UnpricedCount": 0 }
  ],
  "cost_sources": [ "...unchanged..." ]
}
```

`granularity=` anything other than `hour`/`day`/`week`/`month` → `400` with the same
`{"error": "..."}` shape `parseSinceParam`'s rejection already uses.

## Test strategy

**Unit — `internal/store`**: `StatsByPeriod` for each of the four granularities — correct bucket
count and correct per-bucket count/tokens/cost for rows straddling an hour boundary, a UTC day
boundary (already covered, kept), a week boundary (Sunday 23:59 vs Monday 00:01), and a month
boundary (Jan 31 23:59 vs Feb 1 00:01); an unrecognized granularity string returns an error and
touches no query.

**Unit — `internal/api`**: `?granularity=` for each valid value returns the matching `by_period`
shape; an invalid value is `400` and never reaches the store; the param absent behaves exactly as
`day` did before this change (regression case protecting the default); `granularity` in the response
echoes what was applied; combined with `?since=` still filters correctly (existing `since` tests are
the parity check).

**Web**: `node --check internal/web/app.js`; assert the granularity select, since select, metric
toggle, and history table markup appear in the bytes `/app.js` and `/` actually serve (same technique
GI-17 used for its Settings tab markup); a throwaway extract-and-run script (not committed) sanity
-checks the generalized chart's metric accessor picks the right field for each of the three metrics;
manual eyeball of switching granularity/metric/since and confirming the chart and table update
together.

**Commands**: `go build ./...`, `go vet ./...`, `go test ./...`.

## Risk areas

| # | Risk | Mitigation |
|---|---|---|
| R1 | `granularity` reaches SQL as a raw `strftime` format string with no query-parameter placeholder available for it | Whitelisted `switch` in the store method itself (defense in depth beyond the API's own 400), never a passthrough of the raw query value (D1) |
| R2 | Hour granularity with no `since` bound returns a huge, unreadable bucket set on a long-retained install | Per-granularity `since` defaults in the UI (D4); the query itself stays cheap regardless (`idx_requests_started_at` covers the range scan) — this is a UX risk, not a performance one |
| R3 | `%W` week numbering isn't true ISO-8601 | Documented ceiling (D2), acceptable for a dashboard label |
| R4 | `by_day`→`by_period` is a breaking JSON rename | No external consumers of this API exist (single-user, embedded dashboard only) — called out explicitly rather than silently changed |
| R5 | Context docs (`api-surface.md`, `data-model.md`) go stale the moment this lands | Phase 5.6 refresh scoped to exactly this rename + the new param |
| R6 | Interaction with GI-17 retention/purge: a purged install shows fewer historical buckets than a user might expect from "day/hour/week/month" framing | Not a bug — purge already deletes rows, and a period with no rows simply has no bucket. No change needed; noted so it isn't mistaken for a defect during review |

## Self-review

**As a senior engineer.** The whole change is a generalization of one existing method/route/chart
rather than new surface — `StatsByDay` already had the right query shape, so this is "parameterize
the format string and the WHERE-adjacent since param," not new architecture. The one thing worth
double-checking in implementation: SQLite's `strftime` takes the format as its *first* positional
argument with no bind-parameter form, so the whitelist-to-constant mapping has to happen in Go before
the query string is built — I'd reject any implementation that string-formats `granularity` into the
query text directly, even from an already-validated value, since that pattern is easy to
copy-paste into a future call site that skips validation.

**As a QA engineer.** Boundary cases enumerated above cover each granularity's edge (hour/day/week/
month rollover). Also worth asserting: a bucket with zero rows never appears (matches existing
`StatsByDay`'s `GROUP BY` behavior — sparse, not zero-filled) so the chart/table must handle
non-contiguous periods gracefully, same as it already tolerates a day with no data today (this is
existing behavior, not a regression risk, but worth a regression test since the rename touches the
query). `since` + `granularity` combined: an hour granularity with `since=90d` should not error, just
return a lot of buckets — capping is a UI default, not a server-side limit, so the API itself must
never reject a "large" request, only an invalid one.

**As a security engineer.** The only new input surface is `?granularity=`, read-only, same guard
posture as every other GET route (loopback-only, no auth). Its only sensitive use is inside a SQL
format-string argument (D1's whitelist closes that). No new write path, no new PII exposure — the
response shape is the same aggregate counts/costs the day view already exposed, just bucketed finer.

## Beads

Written to `.beads/GI-21/` in Phase 3.

## Change History

- v1 (2026-09-17): Initial plan.
