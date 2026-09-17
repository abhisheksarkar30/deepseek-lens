# GI-21 — Hour/day/week/month historical breakdown in the Stats tab

**Ticket**: [#21](https://github.com/abhisheksarkar30/deepseek-lens/issues/21) ·
**Branch**: `GI-21-stats-by-period` (cut from `develop`) ·
**Plan version**: v2 ·
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
and cost, plus an arbitrary (not just rolling-from-now) date range — a distant day, a specific past
week or month — exposed through `GET /api/stats` and rendered in the existing Stats tab. Additive to
the tab bar (no new tab; Stats already sits beside Live Feed) and to the header's live counter
(untouched).

Out of scope: changing what the header counter does, per-model breakdown by period (the by-model
table stays all-time), and any change to retention/purge (GI-17) beyond noting its interaction as a
risk.

## Design decisions

**D1 — One generalized `StatsByPeriod(ctx, since, until, granularity)` replacing `StatsByDay`, not
four near-duplicate methods.** `DayStat`/`StatsByDay` become `PeriodStat`/`StatsByPeriod`, with
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

**D3 — `GET /api/stats` gains `?granularity=hour|day|week|month` (default `day`) and a new `?until=`
bound, paired with the existing `?since=`** ([internal/api/api.go:254-266](../../internal/api/api.go#L254-L266)).
`since`/`until` both accept the same duration-or-RFC3339 forms — `parseSinceParam` is generalized to
`parseTimeBoundParam(r, key)` and called twice, once per param, rather than duplicated. `since`
unbounded (absent) means "from the beginning," exactly as today; `until` unbounded (absent) means
"through now," also exactly as today — so the two-param form is a strict superset of the one-param
behavior, not a breaking change to what an absent bound means. Together they let the UI express an
**arbitrary window**, not just "the last N units up to now": a specific past day is
`since=2026-03-01T00:00:00Z&until=2026-03-02T00:00:00Z`, a specific past week or month follows the
same shape. `since` bounds `started_at >= ?`; `until` bounds `started_at < ?` (exclusive), so a whole
calendar day/week/month is expressed as `[start, start-of-next)` with no double-counted boundary row.
An unrecognized `granularity`, or a `since`/`until` that fails to parse, is a 400 — mirroring how an
invalid `since` already behaves. `since > until` is **not** rejected: it is a well-formed empty
window (every query already answers "0 rows" correctly for a range with nothing in it; see Test
strategy). The response echoes both bounds and the applied `granularity` back (same "tell the client
what was actually applied" pattern as `X-Limit` on the paginated routes), and `by_day`/`DayStat`
become `by_period`/`PeriodStat` in the JSON contract. The rename has no deprecation shim: the
dashboard is the *only* consumer of its own API (single-user, embedded, no external clients, no
versioning anywhere else in this API), so there is nothing to migrate.

**D4 — The Stats tab gets a granularity `<select>` plus two native `<input type="date">` fields
(From / To) instead of a `since`-only preset select.** A relative "last N" preset can't express "the
week of March 3rd" or "last November" — an arbitrary past window needs two independent bounds, and
`<input type="date">` is the native platform control for picking one (rung 4 of the ladder: no JS
date-picker dependency). Both fields are optional — empty From/To round-trips to an absent
`since`/`until`, i.e. all-time, matching today's default. Leaving From empty with hour granularity
selected is still the one UX trap worth guarding: default **From** to a sane per-granularity lookback
(hour→24h ago, day→30d ago, week→90d ago, month→empty/all-time) the *first* time a user switches
granularity with no explicit date set, so an hour view doesn't silently render years of buckets — but
never fight an explicit date the user typed in. The granularity `<select>` reuses the `.pager select`
CSS class already used by the sessions/warnings pagers; the date inputs need no new CSS beyond normal
form-field spacing already in `style.css`.
*Skipped*: quick-jump preset buttons ("Last 7d", "This month") that just fill the two date fields —
convenient but not what was asked for; add if the two raw date fields prove tedious in practice.

**D5 — `renderStatsChart` is generalized to plot one of three metrics via an accessor
(`RequestCount`, `InputTokens+OutputTokens`, `CostUSDTotal`) instead of being hardcoded to count**,
selected by a three-button metric toggle reusing the existing `.tab` button class (same visual
language as the main tab bar, zero new CSS). This is one parameterized chart function, not three
chart functions.

**D6 — A new history table (Period | Calls | Tokens | Cost) renders below the chart**, using the same
row-per-bucket data the chart already fetched and the existing `fmtTokens`/`fmtCostTotal` helpers and
table styling the by-model table uses. This is what actually answers "show me the day/hour-wise
records" — a chart alone can be eyeballed but not read exactly.

**D7 — `until` touches every query `/api/stats` runs, not just `StatsByPeriod`, through one shared
helper rather than six copies of the same conditional.** `StatsSummary` (two queries: the main
aggregate and the warnings join), `durationPercentiles` (the p50/p95 helper `StatsSummary` calls),
`StatsByModel`, `StatsByPeriod`, and `StatsByCostSource` all currently filter `WHERE started_at >= ?`
— if only `StatsByPeriod` learned about `until`, the summary/by-model numbers on the same tab would
silently describe a *different* window than the chart and history table sitting next to them, which
is exactly the "two numbers on one screen that can't be read as agreeing" failure `CostSourceStat`
already exists to avoid for cost totals. So every one of those six call sites gains the same
`AND started_at < ?` when `until` is set, built by one small helper
(`statsWindow(since, until time.Time) (whereSQL string, args []any)`) rather than six hand-written
conditionals that would drift out of sync with each other over time.

## What changes

### Modify

| File | Change |
|---|---|
| [internal/store/types.go](../../internal/store/types.go) | Rename `DayStat` → `PeriodStat`, `Day` field → `Period` (D1) |
| [internal/store/store.go](../../internal/store/store.go) | Rename `StatsByDay` → `StatsByPeriod(ctx, since, until, granularity)`; granularity → `strftime` format via whitelist `switch`; error on unrecognized granularity; add `until time.Time` to `StatsSummary`, `StatsByModel`, `StatsByCostSource`, `durationPercentiles`; new `statsWindow(since, until) (string, []any)` helper used by all five methods' six query sites (D1, D2, D7) |
| [internal/store/store_test.go](../../internal/store/store_test.go) | `TestStatsByDay` → `TestStatsByPeriod`: bucket correctness for all four granularities (including an hour/day/week/month boundary case each), the invalid-granularity error, an `until` bound on each of the five methods, and a `since > until` case asserting an empty (not error) result |
| [internal/api/api.go](../../internal/api/api.go) | `Store` interface method renames/signature changes; `parseSinceParam` → `parseTimeBoundParam(r, key)` called for both `since` and `until`; parse/validate `?granularity=` (400 on unknown, default `"day"`); `statsResponse` gains `Until time.Time`, `ByPeriod []store.PeriodStat \`json:"by_period"\``, `Granularity string \`json:"granularity"\``; pass all bounds through to every stats call (D1, D3, D7) |
| [internal/api/api_test.go](../../internal/api/api_test.go) | `TestStatsTotalsMatchFixture`/new cases: each granularity value, invalid value → 400, default-absent behaves as `"day"`, response echoes applied `granularity`/`since`/`until`, an explicit `until` narrows `by_model`/`cost_sources`/`summary` together with `by_period` (not just the period breakdown), malformed `until` → 400, `since > until` → 200 with empty results everywhere |
| [internal/web/index.html](../../internal/web/index.html) | Stats section: granularity `<select>`, From/To `<input type="date">` pair, three-button metric toggle, history table markup below the chart (D4, D5, D6) |
| [internal/web/app.js](../../internal/web/app.js) | `loadStats()` reads selected granularity/From/To and passes them as `granularity`/`since`/`until` query params (date-input values converted to RFC3339 day boundaries); per-granularity From default only when the user hasn't set an explicit date (D4); `renderStatsChart` generalized to a metric accessor; new `renderStatsHistoryTable`; state + event wiring for the new controls (D4, D5, D6) |
| `docs/context/api-surface.md`, `docs/context/data-model.md` | Refresh in Phase 5.6: `by_day`/`DayStat`/`StatsByDay` references become `by_period`/`PeriodStat`/`StatsByPeriod`, plus the new `granularity`/`until` params |

No new files — this generalizes one existing store method, one existing route, and one existing tab;
there's no new subsystem to give its own file.

## Contracts

### `GET /api/stats?granularity=week&since=2026-03-01T00:00:00Z&until=2026-04-01T00:00:00Z`

```json
{
  "since": "2026-03-01T00:00:00Z",
  "until": "2026-04-01T00:00:00Z",
  "granularity": "week",
  "summary": { "...": "bounded by [since, until) like every other field here" },
  "by_model": [ "...bounded the same way..." ],
  "by_period": [
    { "Period": "2026-W09", "RequestCount": 3, "InputTokens": 1200, "OutputTokens": 900, "CostUSDTotal": 0.004, "UnpricedCount": 0 }
  ],
  "cost_sources": [ "...bounded the same way..." ]
}
```

`until` absent → unbounded upper (through now), exactly like `since` absent today means unbounded
lower. `granularity=` anything other than `hour`/`day`/`week`/`month`, or a `since`/`until` that
fails to parse, → `400` with the same `{"error": "..."}` shape the existing `since` rejection uses.
`since > until` → `200` with every field's window legitimately empty (0 rows, empty `by_period`) —
not an error.

## Test strategy

**Unit — `internal/store`**: `StatsByPeriod` for each of the four granularities — correct bucket
count and correct per-bucket count/tokens/cost for rows straddling an hour boundary, a UTC day
boundary (already covered, kept), a week boundary (Sunday 23:59 vs Monday 00:01), and a month
boundary (Jan 31 23:59 vs Feb 1 00:01); an unrecognized granularity string returns an error and
touches no query.

**Unit — `internal/api`**: `?granularity=` for each valid value returns the matching `by_period`
shape; an invalid `granularity` or a malformed `until` is `400` and never reaches the store; both
params absent behave exactly as before this change (regression case protecting the default);
`granularity`/`since`/`until` in the response echo what was applied; an explicit `until` narrows
`summary`, `by_model`, and `cost_sources` together with `by_period` — the test that would catch D7's
helper being wired into only one of the six query sites; `since > until` returns `200` with every
field's window empty, not a `400`.

**Web**: `node --check internal/web/app.js`; assert the granularity select, the two date inputs, the
metric toggle, and the history table markup appear in the bytes `/app.js` and `/` actually serve (same
technique GI-17 used for its Settings tab markup); a throwaway extract-and-run script (not committed)
sanity-checks the generalized chart's metric accessor picks the right field for each of the three
metrics, and that a date-input value converts to the RFC3339 bound the API expects; manual eyeball of
switching granularity/metric/From/To and confirming the chart and table update together, including
picking a distant past week and month.

**Commands**: `go build ./...`, `go vet ./...`, `go test ./...`.

## Risk areas

| # | Risk | Mitigation |
|---|---|---|
| R1 | `granularity` reaches SQL as a raw `strftime` format string with no query-parameter placeholder available for it | Whitelisted `switch` in the store method itself (defense in depth beyond the API's own 400), never a passthrough of the raw query value (D1) |
| R2 | Hour granularity with no `From` date set returns a huge, unreadable bucket set on a long-retained install | Per-granularity `From` default in the UI, only when the user hasn't typed an explicit date (D4); the query itself stays cheap regardless (`idx_requests_started_at` covers the range scan) — this is a UX risk, not a performance one |
| R3 | `%W` week numbering isn't true ISO-8601 | Documented ceiling (D2), acceptable for a dashboard label |
| R4 | `by_day`→`by_period` is a breaking JSON rename | No external consumers of this API exist (single-user, embedded dashboard only) — called out explicitly rather than silently changed |
| R5 | Context docs (`api-surface.md`, `data-model.md`) go stale the moment this lands | Phase 5.6 refresh scoped to exactly this rename + the new `granularity`/`until` params |
| R6 | Interaction with GI-17 retention/purge: a purged install shows fewer historical buckets than a user might expect from "day/hour/week/month" framing | Not a bug — purge already deletes rows, and a period with no rows simply has no bucket. No change needed; noted so it isn't mistaken for a defect during review |
| R7 | `until` added to five methods/six query sites piecemeal, leaving one query still unbounded while the rest of the tab's numbers are bounded | D7's single `statsWindow` helper used at every site, plus the explicit cross-field test in Test strategy that would fail if any one site were missed |
| R8 | A date-only `<input type="date">` is ambiguous about which instant it means as a bound (local midnight? UTC midnight?) | The frontend converts a picked date to UTC-midnight RFC3339 for both From (start of day) and To (start of the *next* day, matching `until`'s exclusive-upper semantics) — documented at the conversion call site so "To: March 5" reads as "through the end of March 5 UTC," not "up to March 5 00:00" |

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
query). `since`/`until` + `granularity` combined: an hour granularity with a 90-day window should not
error, just return a lot of buckets — capping is a UI default, not a server-side limit, so the API
itself must never reject a "large" request, only an invalid one. The cross-field consistency case
(R7) is the one I'd insist on before calling this done: it's easy to wire `until` into
`StatsByPeriod` and forget `StatsByCostSource`, and the bug would look like "the numbers don't add up"
rather than a crash — the kind of defect that survives casual testing.

**As a security engineer.** The new input surface is `?granularity=` and `?until=`, both read-only,
same guard posture as every other GET route (loopback-only, no auth). `granularity`'s only sensitive
use is inside a SQL format-string argument (D1's whitelist closes that); `until` parses through the
same `time.ParseDuration`/`time.Parse(RFC3339, …)` path `since` already uses and binds as a query
parameter, not string-interpolated, so it carries no new injection surface. No new write path, no new
PII exposure — the response shape is the same aggregate counts/costs the day view already exposed,
just bucketed finer and now window-bounded on both ends.

## Beads

Written to `.beads/GI-21/` in Phase 3.

## Change History

- v1 (2026-09-17): Initial plan.
- v2 (2026-09-17): Added `until` as a second, independent bound alongside `since` (D3, D7) so an
  arbitrary past window — not just "the last N units up to now" — can be selected; replaced the D4
  preset `since` select with two native date inputs (From/To); `until` threaded through all five
  stats methods/six query sites via one shared helper, not just `StatsByPeriod`, so every number on
  the tab describes the same window. Prompted by user feedback that a distant day/week/month must be
  filterable, not only a rolling window from now.
