### Bead 6: `app.js` — granularity/From/To request wiring, the generalized metric chart, the history table, and the control events

- **Bead ID**: br-GI-21-06
- **Priority**: P0 (critical — this is the feature's actual behavior; the markup and CSS are inert without it)
- **Original Estimate**: 2h
- **Dependencies**: br-GI-21-03 (the `granularity`/`until` params and the `by_period` response), br-GI-21-05 (the element ids and metric buttons this bead reads)
- **Blocks**: None

**Description**:

All changes are in `internal/web/app.js`, in and around the `// ---- stats ----` block
(`app.js:410-495`).

1. **`loadStats()` reads the controls and sends them** (`app.js:412-421`). Read
   `#stats-granularity`.value, `#stats-from`.value, `#stats-to`.value; build a `URLSearchParams`
   with `granularity` always set, `since` set only when From is non-empty, and `until` set only when
   To is non-empty; fetch `/api/stats?<params>` (D3, D4). Both dates empty → no `since`/`until`, i.e.
   the all-time window, exactly today's default — the absent-bound behavior is a strict superset, not
   a change (D3).

2. **Rename the read: `data.by_day` → `data.by_period`** (`app.js:416`), the JSON-contract rename D3
   carries (the plan's F4.1 call site). Pass the same `data.by_period || []` array to **both**
   `renderStatsChart` and the new `renderStatsHistoryTable` — one fetched row set, two views (D6).

3. **Date-input → bound conversion (R8).** A picked `<input type="date">` is a wall date with no
   instant; the API's `since`/`until` are RFC3339 instants. Add one small helper:

   ```js
   // utcDayBound turns an <input type="date"> value (YYYY-MM-DD) into the RFC3339
   // UTC-midnight bound the API's since/until expect. The To bound is the start of
   // the NEXT day, because until is exclusive-upper: [start, next-start) covers the
   // whole picked day with no double-counted boundary row (R8).
   function utcDayBound(dateStr, endExclusive) {
     if (!dateStr) return "";
     const d = new Date(dateStr + "T00:00:00Z");
     if (isNaN(d.getTime())) return "";
     if (endExclusive) d.setUTCDate(d.getUTCDate() + 1);
     return d.toISOString().replace(/\.\d{3}Z$/, "Z"); // whole seconds: 2026-03-01T00:00:00Z
   }
   ```

   The explicit `T00:00:00Z` suffix pins UTC midnight regardless of the browser's local zone — a bare
   `new Date("2026-03-01")` is UTC by spec but the suffix removes the ambiguity entirely. The stripped
   `.000` matches the whole-second form the contract examples use (`2026-03-01T00:00:00Z`); Go's
   `time.Parse(time.RFC3339, …)` on the server accepts either, but matching the documented shape keeps
   the sent value legible in the network tab. Document at the call site why **To** maps to the next
   day, so "To: March 5" reads as "through the end of March 5 UTC", not "up to March 5 00:00" (R8).

4. **Per-granularity From default, only when no explicit date is set (D4, R2).** An hour view with an
   empty From would render years of buckets; default **From** on a granularity change to a sane
   lookback — hour → 24h ago, day → 30d ago, week → 90d ago, month → empty/all-time — but **never
   overwrite a From the user typed**. Concretely, on the `#stats-granularity` `change` event: if
   `#stats-from` is empty and the granularity's lookback is non-zero, set `#stats-from` to
   `now − lookback` rendered as `YYYY-MM-DD` (UTC); then reload. A non-empty From is left untouched.

   ```js
   const FROM_LOOKBACK_MS = { hour: 24 * 3600e3, day: 30 * 86400e3, week: 90 * 86400e3, month: 0 };
   ```

5. **Generalize `renderStatsChart` to a metric accessor (D5)** — one parameterized function, **not**
   three. Replace the hardcoded `RequestCount` (`app.js:454`, `:461`) with an accessor chosen by the
   selected metric:

   ```js
   const METRICS = {
     count:  { label: "Calls",  value: (d) => d.RequestCount,
               axis: (v) => String(v) },
     tokens: { label: "Tokens", value: (d) => (d.InputTokens || 0) + (d.OutputTokens || 0),
               axis: fmtTokens },
     cost:   { label: "Cost",   value: (d) => d.CostUSDTotal, axis: fmtCost },
   };
   ```

   - `maxCount` (`app.js:454`) becomes `maxValue = Math.max(1, ...byPeriod.map(acc.value))`; the plotted
     `y` uses `acc.value(d)`.
   - **The y-axis max label** (`app.js:477`, today the bare `${maxCount}`) is formatted per metric:
     count → raw integer (existing behavior), tokens → `fmtTokens`, cost → `fmtCost` (**not**
     `fmtCostTotal` — F3.2). `fmtCostTotal` needs a paired `UnpricedCount`, and the y-axis label is
     derived from a bare `Math.max()` scalar with no retained link to which bucket produced it; `fmtCost`
     is the correct axis-scale marker. The history table below uses `fmtCostTotal` per row, where the
     count is available (item 6). Printing a raw cost number here while every other formatted cost on
     the tab uses a helper would be a visible inconsistency.
   - **The x-axis label is per-granularity, not a uniform `.slice(5)`** (D5). The existing
     `d.Day.slice(5)` (`app.js:470`) yields `"09-17T14:00"` for an hour bucket — 11 chars, unreadable at
     density. Use a formatter that extracts only the relevant part: hour → the time half
     (`d.Period.split("T")[1]`, i.e. `HH:MM`); day/week/month → `d.Period.slice(5)` (`MM-DD`, `W##`,
     `MM`).
   - **`d.Day` → `d.Period`** at the label site (`app.js:470`) and anywhere else the row field is read —
     the D3 JSON-contract rename (F4.1).

6. **New `renderStatsHistoryTable(byPeriod)` (D6).** Render one row per bucket into
   `#stats-history`: Period (verbatim, `escapeHtml(p.Period)`), Calls (`p.RequestCount`), Tokens
   (`fmtTokens((p.InputTokens || 0) + (p.OutputTokens || 0))`), Cost
   (`fmtCostTotal(p.CostUSDTotal, p.UnpricedCount || 0)`). Empty state:
   `<tr><td colspan="4" class="hint">No data yet.</td></tr>`, matching the by-model table's empty row
   (`app.js:494`). This is intentionally uncapped — no pager (D6): a single-user local tool, bounded in
   practice by R2's smart-default From.

7. **Dynamic heading and `aria-label` (D5).** After a successful fetch, set
   `#stats-chart-title`.textContent to `` `${METRICS[metric].label} per ${granularity}` `` and
   `#stats-chart`.setAttribute("aria-label", `` `${METRICS[metric].label} per ${granularity} line chart` ``)
   — e.g. "Tokens per week". Both update in the same `loadStats()` call that re-renders the chart.

8. **State and event wiring (D4, D5, D6).**
   - Add `state.statsMetric = "count"` to the `state` object (`app.js:151-191`) — the selected metric
     survives re-renders and tab switches. The granularity needs no separate state; read the
     `<select>` directly.
   - **Metric toggle**: delegate a click handler on `#stats-metric`; on click, set `state.statsMetric`
     from the button's `data-metric`, toggle `.active` across the `.metric` buttons (remove from all,
     add to the clicked one), then re-render the chart **and** the heading/`aria-label` **without
     refetching** — the three metrics share the one fetched row set.
   - **Granularity `change`**: apply the per-granularity From default (item 4) then `loadStats()`.
   - **From/To `change`**: `loadStats()`.
   - **The `.metric`-not-`.tab` rule is load-bearing (D5, F6.2).** Do not add `data-view` to the
     metric buttons and do not give them the `.tab` class: `showView()`'s unscoped
     `document.querySelectorAll(".tab")` sweep (`app.js:224-226`) toggles `active` off every `.tab`
     element whose `data-view` does not match, so a `.tab`-classed metric button would be deselected on
     every view transition — including the Stats entry itself. `.metric` is outside that selector,
     which is the entire reason it exists.
   - `loadStats()` is the `showView` hook for Stats (`app.js:237`) and is also called by
     `reloadDataViews()` after a purge (`app.js:246-250`); both must re-render using the retained
     `state.statsMetric` and the current control values, never reset the metric to `count`.

**Rationale**:

D5 — one parameterized chart function with a metric accessor, not three near-duplicate renderers, and
a per-metric y-axis formatter that matches every other formatted value on the tab. D4 — native date
inputs with a per-granularity From default that guards the one real UX trap (an hour view over a
long-retained install, R2) without fighting a date the user typed. D6 — the exact-value history table
the chart cannot provide. F6.2 — the `.metric` class exists specifically so the selected metric is not
silently deselected by an unrelated selector sweep; the review rounds closed this defect, and re-using
`.tab` here would reintroduce it.

**Outcome Definition**:

- Changing the granularity, either date, or the metric updates the chart and the history table
  together from the same fetched rows.
- `GET /api/stats` is requested with `granularity` always, and `since`/`until` only when the
  corresponding date input is non-empty.
- A From of `2026-03-01` sends `since=2026-03-01T00:00:00Z`; a To of `2026-03-05` sends
  `until=2026-03-06T00:00:00Z` (start of the next day — R8).
- Switching granularity with an empty From fills From to the per-granularity lookback (hour→24h,
  day→30d, week→90d, month→empty); a non-empty From is never overwritten.
- The metric toggle switches which field is plotted and which x/y labels and heading render; the "Calls
  per day" heading becomes e.g. "Cost per hour", and the `aria-label` tracks it.
- Selecting a non-default metric, navigating away to another view, and returning leaves the correct
  metric button carrying `.active` (the F6.2 case).
- The history table renders one row per bucket with `fmtTokens`/`fmtCostTotal` cells and an empty-state
  row when there are no buckets.
- `node --check internal/web/app.js` passes.

**Test Specifications**:

- Unit Tests (throwaway extract-and-run scripts, **not committed** — the established no-JS-harness
  convention):
  - Metric accessor: for a sample row `{RequestCount: 3, InputTokens: 1200, OutputTokens: 900,
    CostUSDTotal: 0.004}`, `METRICS.count.value` is `3`, `METRICS.tokens.value` is `2100`,
    `METRICS.cost.value` is `0.004`; `METRICS.tokens.axis(2100)` is `"2.1k"` and `METRICS.cost.axis(0.004)`
    is `"$0.0040"`.
  - Date conversion: `utcDayBound("2026-03-01", false)` is `"2026-03-01T00:00:00Z"`;
    `utcDayBound("2026-03-05", true)` is `"2026-03-06T00:00:00Z"`; `utcDayBound("", false)` is `""`.
- Integration Tests (served-asset assertions against what the running server serves, GI-17's
  technique — `internal/web` is `go:embed`-ed, so assert the served bytes, never the file on disk):
  - `node --check internal/web/app.js`
  - `curl -s http://localhost:<port>/app.js | grep -q 'by_period'` — the renamed read
  - `curl -s http://localhost:<port>/app.js | grep -q 'stats-history'` and `grep -q 'granularity'`
  - positive control: `curl -s http://localhost:<port>/app.js | grep -q 'by_day'` **hits before this
    change and must not after** — an empty result is evidence only once the same search is shown to
    find the pre-edit string.
- E2E: manual, in a browser — switch granularity/metric/From/To and confirm the chart and the history
  table update together, including picking a distant past week and a distant past month; with hour
  granularity and an empty From, confirm From auto-fills to the 24h default and the chart is a sane
  size; **then** switch to a non-default metric, navigate to Feed and back to Stats, and confirm the
  correct metric button is still highlighted (the case that would expose an unguarded `.tab` class
  sharing the `showView()` sweep).

**Files to Touch**:
- `internal/web/app.js` (modify — `loadStats` control wiring and `by_period` read, the date-bound helper and per-granularity From default, the generalized `renderStatsChart` metric accessor and label formatters, the dynamic heading/`aria-label`, the new `renderStatsHistoryTable`, and the `state`/event wiring for the new controls)
