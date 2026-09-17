# GI-21 — Hour/day/week/month historical breakdown in the Stats tab

**Ticket**: [#21](https://github.com/abhisheksarkar30/deepseek-lens/issues/21) ·
**Branch**: `GI-21-stats-by-period` (cut from `develop`) ·
**Plan version**: v10 ·
**Status**: converged

## Problem

The dashboard has two views of activity and both fall short of a real history:

1. **The header totals (`#total-calls`/`#total-tokens`/`#total-cost`) are a recent-calls-plus-live
   counter, not a full persistent history.** On every page load, `loadInitialFeed()` resets
   `state.totals` to zero and immediately backfills it from the last ~50 requests fetched from
   `/api/requests?limit=50`
   ([internal/web/app.js:306-317](../../internal/web/app.js#L306-L317)); after that, SSE events
   accumulate on top via `bumpTotals()`
   ([internal/web/app.js:193-198](../../internal/web/app.js#L193-L198)). The counter therefore reflects at
   most the last `DefaultLimit` (~50) calls plus whatever arrived over SSE since that load — not the
   full retention history. This is the "recent-calls-plus-live counter" view the story wants
   something *in addition to*, not a replacement for.
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
`since`/`until` both accept the same duration-or-RFC3339 forms — a new `parseTimeBoundParam(r, key)`
function handles both params; `parseSinceParam(r)` is **kept as a thin wrapper** around
`parseTimeBoundParam(r, "since")`, leaving its three other callers in `listRequests` (api.go:291),
`warningsSummary` (api.go:774), and `listWarnings` (api.go:802) unchanged. Only `stats()` gains a
second call to `parseTimeBoundParam(r, "until")`. `since`
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
never fight an explicit date the user typed in. The granularity `<select>`, From, and To
`<input type="date">` controls need a new small rule in `style.css` —
`background: var(--bg-alt); color: var(--fg); border: 1px solid var(--border)` — matching the
neutral-border style `.pager select` (`style.css:110-113`) uses for navigational controls. (Note:
`td.price-cell input` at `style.css:259` uses `var(--accent)` for its border — the visual language
for "directly editable data" — which would be the wrong signal for period filter controls; the stats
controls are navigational filters like the pager, so they use `var(--border)` to match it.)
`.pager select` is a CSS *descendant* selector that only applies inside a `<div class="pager">`
wrapper; it cannot be applied to a standalone element. The stylesheet has no generic `<input>` or
`<select>` styling rule, so an unstyled native date input renders with browser-default chrome against
the dark theme.
*Skipped*: quick-jump preset buttons ("Last 7d", "This month") that just fill the two date fields —
convenient but not what was asked for; add if the two raw date fields prove tedious in practice.

**D5 — `renderStatsChart` is generalized to plot one of three metrics via an accessor
(`RequestCount`, `InputTokens+OutputTokens`, `CostUSDTotal`) instead of being hardcoded to count**,
selected by a three-button metric toggle using a distinct `.metric` class styled to match
`.tab`/`.tab.active` via a new one-line CSS rule added to `style.css`. A shared `.tab` class cannot
be used here: `showView()` runs an unscoped `document.querySelectorAll(".tab")` sweep
(`app.js:224-226`) on every view transition, toggling `active` based on `data-view` match — a
metric button carries no `data-view`, so every `showView()` call (including the one `loadStats()`
triggers when entering Stats) would strip `active` from all three metric buttons, leaving the
selected metric visually deselected each time the user returns to the Stats view. This is one parameterized chart function, not three
chart functions. The x-axis label formatter is per-granularity rather than a uniform `.slice(5)`:
the existing `d.Day.slice(5)` (`app.js:470`) gives `"09-17T14:00"` for an hour bucket (11 chars,
hard to read at density), so the formatter extracts only the relevant portion — hour → `HH:MM` (split
on `T`, take the time part); day → `.slice(5)` (`MM-DD`); week → `.slice(5)` (`W##`); month →
`.slice(5)` (`MM`). The y-axis max-value label (currently `${maxCount}` at `app.js:477`) must also
be formatted per the selected metric: count → raw integer (existing behavior); tokens → `fmtTokens`
(the `k`/`M`-suffix helper already used everywhere else tokens appear); cost → `fmtCost` (the plain
`$X.XXXX` formatter, **not** `fmtCostTotal`). The y-axis label is an axis-scale marker derived from
a bare `Math.max(...)` scalar with no retained link to which bucket produced it, so no `UnpricedCount`
is available for `fmtCostTotal`'s second argument. `fmtCost` here is correct and deliberate: a scale
marker's job is to set the axis range; the D6 history table cells below the chart use `fmtCostTotal`
per-row where the `UnpricedCount` is actually available. Printing a raw cost number in the y-axis max
label while every other formatted instance on the tab uses the helper would be a visible inconsistency. The `<h3>Calls per day</h3>` heading and `aria-label="calls per day line chart"`
(`index.html:70-71`) are made dynamic, updating to reflect the selected metric and granularity
(e.g. "Tokens per week", "Cost per hour") by the same `loadStats()` call that re-renders the chart.

**D6 — A new history table (Period | Calls | Tokens | Cost) renders below the chart**, using the same
row-per-bucket data the chart already fetched and the existing `fmtTokens`/`fmtCostTotal` helpers and
table styling the by-model table uses. This is what actually answers "show me the day/hour-wise
records" — a chart alone can be eyeballed but not read exactly. The table is intentionally uncapped
(no pager/`X-Limit`): this is a single-user local tool, and the row count is bounded in practice by
R2's smart-default From date (which limits the window to a sane horizon when the user hasn't typed
an explicit date). A user who explicitly picks a large window (e.g. hour granularity over months)
accepts the consequence of more rows; the API never rejects such a request (see self-review/QA
section), and a large but finite table is acceptable UX for a developer observability tool. If this
proves tedious in practice, the same `.pager` pattern used by sessions and warnings can be added then.

**D7 — `until` touches every query `/api/stats` runs, not just `StatsByPeriod`, through one shared
helper rather than six copies of the same conditional.** `StatsSummary` (`store.go:405-438`, two
queries: the main aggregate and the warnings join), `durationPercentiles` (`store.go:440-465`, the
p50/p95 helper `StatsSummary` calls), `StatsByModel`, `StatsByPeriod`, and `StatsByCostSource` all
currently filter `WHERE started_at >= ?` — if only `StatsByPeriod` learned about `until`, the
summary/by-model numbers on the same tab would silently describe a *different* window than the chart
and history table sitting next to them, which is exactly the "two numbers on one screen that can't be
read as agreeing" failure `CostSourceStat` already exists to avoid for cost totals. So every one of
those six call sites gains the same `AND started_at < ?` when `until` is set, built by one small
helper (`statsWindow(since, until time.Time) (whereSQL string, args []any)`) rather than six
hand-written conditionals that would drift out of sync with each other over time.

**`statsWindow` must gate on `!until.IsZero()` for the upper bound**, mirroring the
`!f.Since.IsZero()` guard in `requestWhere` (`store.go:330`) — *not* the existing five stats
methods' unconditional `since.UnixNano()` pattern (`store.go:444` and equivalents). That pattern
works for `since` only because `time.Time{}.UnixNano()` is a large negative number that is less than
every real row's `started_at`, giving "unbounded lower" for free. The same trick is *destructive*
for `until`: a zero-value upper bound plugged unconditionally into `AND started_at < ?` is also a
large negative, which is less than every real row — so the clause excludes everything and returns
zero rows. `requestWhere`'s conditional append is the correct precedent; every implementation of
`statsWindow` must follow it.

## What changes

### Modify

| File | Change |
|---|---|
| [internal/store/types.go](../../internal/store/types.go) | Rename `DayStat` → `PeriodStat`, `Day` field → `Period` (D1) |
| [internal/store/store.go](../../internal/store/store.go) | Rename `StatsByDay` → `StatsByPeriod(ctx, since, until, granularity)`; granularity → `strftime` format via whitelist `switch`; error on unrecognized granularity; add `until time.Time` to `StatsSummary`, `StatsByModel`, `StatsByCostSource`, `durationPercentiles`; new `statsWindow(since, until) (string, []any)` helper used by all five methods' six query sites (D1, D2, D7) |
| [internal/store/store_test.go](../../internal/store/store_test.go) | `TestStatsByDay` → `TestStatsByPeriod`: bucket correctness for all four granularities (including an hour/day/week/month boundary case each), the invalid-granularity error, an `until` bound on each of the five methods, and a `since > until` case asserting an empty (not error) result. Additionally, two existing call sites also break on the `StatsSummary` signature change and must be updated: `TestStatsSummary` (`store_test.go:454`) — `s.StatsSummary(ctx, base.Add(-time.Hour))` gains a `time.Time{}` `until` argument; `TestListRequestsPerformanceAndLimits` (`store_test.go:1329`) — same one-line fix, `s.StatsSummary(ctx, base.Add(-time.Hour))` gains `time.Time{}`, with no behavior change to either test. (F2.1) |
| [internal/api/api.go](../../internal/api/api.go) | `Store` interface method renames/signature changes; new `parseTimeBoundParam(r, key)` extracted from `parseSinceParam`; `parseSinceParam(r)` kept as a thin wrapper around `parseTimeBoundParam(r, "since")` so `listRequests` (api.go:291), `warningsSummary` (api.go:774), and `listWarnings` (api.go:802) are untouched — only `stats()` gains a second call `parseTimeBoundParam(r, "until")`; parse/validate `?granularity=` (400 on unknown, default `"day"`); `statsResponse` gains `Until time.Time`, `ByPeriod []store.PeriodStat \`json:"by_period"\``, `Granularity string \`json:"granularity"\``; pass all bounds through to every stats call (D1, D3, D7) |
| [internal/api/api_test.go](../../internal/api/api_test.go) | `TestStatsTotalsMatchFixture`/new cases: each granularity value, invalid value → 400, default-absent behaves as `"day"`, response echoes applied `granularity`/`since`/`until`, an explicit `until` narrows `by_model`/`cost_sources`/`summary` together with `by_period` (not just the period breakdown), malformed `until` → 400, `since > until` → 200 with empty results everywhere |
| [internal/cli/stats.go](../../internal/cli/stats.go) | Update `StatsSummary(ctx, sinceTime)` → `StatsSummary(ctx, sinceTime, time.Time{})`, `StatsByModel(ctx, sinceTime)` → `StatsByModel(ctx, sinceTime, time.Time{})`, `StatsByDay(ctx, sinceTime)` → `StatsByPeriod(ctx, sinceTime, time.Time{}, "day")`; rename `statsJSON.ByDay []store.DayStat` → `ByPeriod []store.PeriodStat`; the struct tag also changes from `` `json:"by_day"` `` → `` `json:"by_period"` `` (stats.go:31) — this is a breaking rename of `lens stats --json`'s output key, distinct from the dashboard's `by_day`→`by_period` HTTP API rename; both carry the same single-user/no-external-consumer justification (see R4); rename `d.Day` → `d.Period` in rendering loop (`stats.go:158`); `lens stats --by day` continues working with granularity `"day"` and no `--until` flag (the CLI does not expose `--until`; until stays zero/unbounded for CLI use) (F1.1, D1) |
| [internal/cli/doctor.go](../../internal/cli/doctor.go) | Update `StatsSummary(context.Background(), time.Time{})` (`doctor.go:159`) → `StatsSummary(context.Background(), time.Time{}, time.Time{})` to match the new three-argument signature (F1.1, D7) |
| [internal/web/index.html](../../internal/web/index.html) | Stats section: granularity `<select>`, From/To `<input type="date">` pair, three-button metric toggle, history table markup below the chart; `<h3>` heading and `aria-label` made dynamic (D4, D5, D6) |
| [internal/web/app.js](../../internal/web/app.js) | `loadStats()` reads selected granularity/From/To and passes them as `granularity`/`since`/`until` query params (date-input values converted to RFC3339 day boundaries); `loadStats()`'s `data.by_day` (`app.js:416`) → `data.by_period`; per-granularity From default only when the user hasn't set an explicit date (D4); `renderStatsChart` generalized to a metric accessor with per-granularity x-axis label formatter; `renderStatsChart`'s per-row `d.Day` (`app.js:470`) → `d.Period` (D3 JSON-contract rename); dynamic heading/aria-label update; new `renderStatsHistoryTable`; state + event wiring for the new controls (D4, D5, D6) |
| [internal/web/style.css](../../internal/web/style.css) | New rule styling the stats-controls `<select>` and `<input type="date">` elements with `background: var(--bg-alt); color: var(--fg); border: 1px solid var(--border)` to match the dark theme and the neutral-border language `.pager select` uses (D4); new `.metric` / `.metric.active` rule matching the visual appearance of `.tab` / `.tab.active` so the metric toggle looks the same without sharing the selector (D5) |
| `docs/context/api-surface.md`, `docs/context/data-model.md` | Refresh in Phase 5.6: `by_day`/`DayStat`/`StatsByDay` references become `by_period`/`PeriodStat`/`StatsByPeriod`, plus the new `granularity`/`until` params; fix four stale line citations in the `api-surface.md` route table — all four are off by the same ~72-line offset from the same historical drift and must be recomputed *after* the implementation edits land (not hard-coded now, since `stats()` will grow further during this very change): `/api/stats` row (currently `api.go:652-700`), `/api/warnings/summary` row (currently `api.go:701-718`), `/api/warnings` row (currently `api.go:719-754`), and `/api/sessions` row (currently `api.go:755-803`) — all four are adjacent lines in one file already being opened, so fixing them together costs nothing extra |

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
touches no query. `statsWindow` behavior: an explicit `until` bound excludes rows at or after it;
**`since` set + `until` absent** returns all rows from `since` forward with no upper cutoff
(this is the most common call pattern and the case where an unconditional zero-value `until` would
return zero rows — must be a named test case, not just an implicit assumption); `since > until`
returns empty results, not an error.

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
picking a distant past week and month; **also**: switch to a non-default metric, then navigate away
to another tab (e.g. Feed) and back to Stats, and confirm the correct metric button is still
highlighted (the case that would expose an unguarded `.tab` class sharing the `showView()` sweep).

**`internal/cli/cli_test.go`**: No changes required to `TestStatsTotalsMatchFixture` or
`TestStatsByDayBreakdown`. Both are pure black-box tests that call `runStats(args, w, st)` and assert
on printed substrings (`"day split:"`, `"COST"`, `"$0.0300"`, etc.) — neither references
`store.DayStat`, `store.PeriodStat`, `.Day`/`.Period` field names, or any store method directly. The
renamed types/methods live entirely inside `internal/cli/stats.go` (which the plan's `stats.go` row
covers). Both tests continue passing without modification once `stats.go` itself is updated.

**Commands**: `go build ./...`, `go vet ./...`, `go test ./...`.

## Risk areas

| # | Risk | Mitigation |
|---|---|---|
| R1 | `granularity` reaches SQL as a raw `strftime` format string with no query-parameter placeholder available for it | Whitelisted `switch` in the store method itself (defense in depth beyond the API's own 400), never a passthrough of the raw query value (D1) |
| R2 | Hour granularity with no `From` date set returns a huge, unreadable bucket set on a long-retained install | Per-granularity `From` default in the UI, only when the user hasn't typed an explicit date (D4); the query itself stays cheap regardless (`idx_requests_started_at` covers the range scan) — this is a UX risk, not a performance one |
| R3 | `%W` week numbering isn't true ISO-8601 | Documented ceiling (D2), acceptable for a dashboard label |
| R4 | `by_day`→`by_period` is a breaking JSON rename in two places: (a) `GET /api/stats` HTTP response (dashboard API) and (b) `lens stats --json` CLI output (the `statsJSON.ByDay` struct tag at `stats.go:31` → `by_period`) | Both surfaces have no known external consumers — single-user local tool, no versioning contract, no documented external clients. Each rename is called out explicitly: (a) the dashboard is the only consumer of its own HTTP API; (b) `lens stats --json` is documented as scriptable (`cli-and-tooling.md:15`) but the same single-user rationale applies — called out separately so the justification is not silently borrowed across surfaces |
| R5 | Context docs (`api-surface.md`, `data-model.md`) go stale the moment this lands | Phase 5.6 refresh scoped to this rename + the new `granularity`/`until` params + recomputing (post-edit, not pre-coded) the four stale line citations in the `api-surface.md` route table (`/api/stats`, `/api/warnings/summary`, `/api/warnings`, `/api/sessions` — all off by the same offset from the same historical drift) |
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
- v3 (2026-09-17): Round-1 review triage. F1.1 — added `internal/cli/stats.go`,
  `internal/cli/doctor.go`, and `internal/cli/cli_test.go` to What changes and Test strategy
  (these call sites break on the signature renames and were absent from the plan). F1.2 — D7
  now explicitly requires `statsWindow` to gate on `!until.IsZero()` (mirroring `requestWhere`
  at `store.go:330`, not the existing stats methods' unconditional pattern); added a named
  store-level test case for "since set, until absent." F1.3 + F1.4 (combined) — D4 now states
  a new `style.css` rule is required for the stats controls; removed the false claim that
  `.pager select` is a reusable CSS class and that no CSS is needed for date inputs.
  F1.5 — D5 now specifies a per-granularity x-axis label formatter. F1.6 — D5 now specifies
  that the `<h3>` heading and `aria-label` are dynamic. F1.7 — D7 now carries correct line
  citations: `StatsSummary` at `store.go:405-438`, `durationPercentiles` at `store.go:440-465`.
- v4 (2026-09-17): Round-2 review triage. F2.1 — added `TestStatsSummary` (store_test.go:454) and
  `TestListRequestsPerformanceAndLimits` (store_test.go:1329) to the `store_test.go` What-changes
  row: both call `StatsSummary` with the old signature and need a `time.Time{}` until argument.
  F2.2 — D5 now requires the y-axis max-value label to be formatted per selected metric: raw integer
  for count, `fmtTokens` for tokens, `fmtCostTotal` for cost, matching every other instance of those
  values on the tab. F2.3 — D4 now uses `var(--border)` (not `var(--accent)`) for the stats-controls
  border, matching `.pager select`'s neutral-border language for navigational filters; the style.css
  What-changes row updated to match; the inaccurate "mirroring" claim replaced with an explicit note
  explaining why `var(--accent)` would be the wrong visual signal here. F2.4 — Phase 5.6 and R5 now
  also cover fixing the pre-existing stale line citation in `api-surface.md` (`api.go:652-700` →
  `api.go:724-755`), since the row is already being touched. F2.5 — D6 now explicitly states the
  history table is intentionally uncapped, with the single-user-tool rationale and R2's smart-default
  as the practical bound, plus an upgrade path note.
- v5 (2026-09-17): Round-3 review triage. F3.1 — D3 and the `api.go` What-changes row now clarify
  that `parseSinceParam` is kept as a thin wrapper around the new `parseTimeBoundParam(r, "since")`,
  leaving `listRequests` (api.go:291), `warningsSummary` (api.go:774), and `listWarnings` (api.go:802)
  unchanged; only `stats()` gains a second call `parseTimeBoundParam(r, "until")`. Full repo grep
  confirmed these are the only three out-of-scope callers; no new files added to the change list.
  F3.2 — D5 y-axis max label for cost metric changed from `fmtCostTotal` to `fmtCost`: `fmtCostTotal`
  requires a paired `UnpricedCount` that is unavailable from a bare `Math.max()` scalar; `fmtCost` is
  correct for an axis-scale marker; the D6 table rows use `fmtCostTotal` per-row where the count is
  available. F3.3 — Test strategy `cli_test.go` row reworded: `TestStatsTotalsMatchFixture` and
  `TestStatsByDayBreakdown` are black-box tests calling `runStats(args, w, st)` and asserting
  printed substrings — neither references renamed types/methods, so no changes are required.
- v6 (2026-09-17): Round-4 review triage. F4.1 — added explicit citation of the two JS-side
  rename call sites to the `internal/web/app.js` What-changes row: `data.by_day` (`app.js:416`) →
  `data.by_period` and `d.Day` (`app.js:470`) → `d.Period`, the JSON-contract rename D3 requires.
  These are the only call sites the Go-symbol grep patterns structurally cannot see; now explicitly
  enumerated to match the completeness standard every other renamed identifier in the plan has.
- v7 (2026-09-17): Round-5 review triage. F5.1 — moved the `v3` Change History entry from its
  out-of-order position (after `v6`) to between `v2` and `v4`, restoring chronological order
  `v1, v2, v3, v4, v5, v6`. No content change inside the `v3` entry itself.
- v9 (2026-09-17): Round-7 review triage. F7.1 — corrected wrong citation in Problem bullet 1:
  `app.js:153` (the `totals` field declaration inside the `state` object literal) replaced with
  `app.js:193-198` (`bumpTotals()`, the function that actually accumulates SSE request events
  into `state.totals`); added `bumpTotals()` function name to the prose for clarity.
- v8 (2026-09-17): Round-6 review triage. F6.1 (CONDUCTOR OVERRIDE) — corrected Problem bullet 1
  from "resets to zero, number gone on refresh" to the accurate behavior: `loadInitialFeed()`
  backfills `state.totals` from the last ~50 requests on every page load, then SSE accumulates on
  top — the counter reflects at most the last DefaultLimit calls plus live SSE, not the full
  retention history; the ticket's underlying motivation is unchanged. F6.2 — D5 metric-toggle
  buttons changed from `.tab` class to a new `.metric` class to avoid collision with `showView()`'s
  unscoped `document.querySelectorAll(".tab")` sweep (`app.js:224-226`) that would strip `active`
  from all metric buttons on every `showView()` call; `style.css` What-changes row updated to
  include the new `.metric`/`.metric.active` rule; Test strategy manual-eyeball step extended with
  the return-to-Stats case that would expose the bug. F6.3 — `internal/cli/stats.go` What-changes
  row now explicitly states the struct tag `` `json:"by_day"` `` (stats.go:31) also changes to
  `` `json:"by_period"` `` and that this is a breaking rename of `lens stats --json` output
  distinct from the dashboard API rename; R4 extended to name both surfaces and make the
  no-external-consumer justification explicit for each rather than borrowing silently from the
  dashboard-scoped argument. F6.4 — Phase 5.6 doc-update instruction for `api-surface.md`
  reworded: the `/api/stats` citation must be recomputed post-implementation (not pre-coded to
  `724-755`, which will go stale as `stats()` grows); extended to fix all four stale rows in the
  same table (`/api/stats`, `/api/warnings/summary`, `/api/warnings`, `/api/sessions`) since they
  share the same offset drift and are adjacent lines in one already-open file; R5 updated to match.
- v10 (2026-09-17): Round-8 produced VERDICT: NO_FURTHER_FINDINGS after an independent,
  cite-by-cite re-verification of the full document against the real source tree. Every citable
  claim in the plan — store method signatures, query-site counts, API handler line numbers, JS
  call sites, CSS selectors, test file line citations — was confirmed against the actual files
  with no new defect, stale citation, or internal contradiction found. No plan content changed;
  status updated from "draft" to "converged".
