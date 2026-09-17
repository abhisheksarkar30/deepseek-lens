### Bead 5: Stats tab markup — granularity `<select>`, From/To date inputs, metric toggle, and the history table

- **Bead ID**: br-GI-21-05
- **Priority**: P1 (high — the markup every other Stats-tab change binds to by element id; no behavior of its own)
- **Original Estimate**: 1h
- **Dependencies**: br-GI-21-03 (the API contract this markup binds to: the `granularity`/`until` params and the `by_period` response)
- **Blocks**: br-GI-21-06, br-GI-21-07

**Description**:

All changes are inside the existing `#view-stats` section (`index.html:64-79`), which today is a bare
summary table, an `<h3>Calls per day</h3>` + `<svg id="stats-chart"
aria-label="calls per day line chart">` pair (`index.html:70-71`), and the by-model table. This bead
adds only the static markup; the behavior that reads it is br-GI-21-06, and the styles it uses are
br-GI-21-07.

1. **A `.stats-controls` block above the chart**, holding three navigational filters (D4):

   ```html
   <div class="stats-controls">
     <label>Granularity
       <select id="stats-granularity">
         <option value="hour">Hour</option>
         <option value="day" selected>Day</option>
         <option value="week">Week</option>
         <option value="month">Month</option>
       </select>
     </label>
     <label>From <input type="date" id="stats-from"></label>
     <label>To <input type="date" id="stats-to"></label>
   </div>
   ```

   `<input type="date">` and `<select>` are the native platform controls (D4): no date-picker
   dependency. Both date inputs start **empty**, which round-trips to an absent `since`/`until` — the
   all-time default today (D4).

2. **A three-button metric toggle**, immediately after the controls block:

   ```html
   <div class="metric-toggle" id="stats-metric">
     <button class="metric active" data-metric="count" type="button">Calls</button>
     <button class="metric" data-metric="tokens" type="button">Tokens</button>
     <button class="metric" data-metric="cost" type="button">Cost</button>
   </div>
   ```

   **The buttons carry `class="metric"`, not `class="tab"` (D5, F6.2).** `showView()` runs an
   unscoped `document.querySelectorAll(".tab")` sweep on every view transition (`app.js:224-226`),
   toggling `active` off any `.tab` element whose `data-view` does not match the view being shown. A
   metric button carries no `data-view`, so if it shared the `.tab` class every `showView()` call —
   including the one `loadStats()` triggers when entering Stats (`app.js:237`) — would strip `active`
   from all three, leaving the selected metric visually deselected each time the user returns to the
   tab. The buttons likewise carry **no `data-view` attribute**, for the same reason: they are not
   view-switchers. `data-metric` is their own key, read by br-GI-21-06.

3. **The chart heading and `aria-label` become dynamic** (D5). Give the heading an id so the JS can
   rewrite it: `<h3 id="stats-chart-title">Calls per day</h3>`. Keep the `<svg id="stats-chart">`
   and its initial `aria-label="calls per day line chart"` as-is; br-GI-21-06 updates both to reflect
   the selected metric and granularity (e.g. "Tokens per week", "Cost per hour").

4. **A history table below the chart** (D6), between the `<svg>` (`index.html:71`) and the existing
   `<h3>By model</h3>` (`index.html:72`):

   ```html
   <h3>History</h3>
   <div class="table-wrap">
     <table>
       <thead><tr><th>Period</th><th>Calls</th><th>Tokens</th><th>Cost</th></tr></thead>
       <tbody id="stats-history"></tbody>
     </table>
   </div>
   ```

   This is the per-bucket table the chart cannot replace: a chart is eyeballed, a table is read
   exactly (D6). It reuses the existing `.table-wrap`/`<table>` styling the by-model table already
   uses; no new table CSS.

The element ids introduced here (`stats-granularity`, `stats-from`, `stats-to`, `stats-metric`,
`stats-chart-title`, `stats-history`) are the contract br-GI-21-06 reads — do not rename them there
without updating this markup.

**Rationale**:

The ticket asks for the breakdown *in the Stats tab*, so the controls and the read-exactly table are
part of the deliverable, not decoration. Splitting the markup from its behavior keeps each bead's
verification crisp: this bead is verifiable by the served bytes alone, and the JS bead is verifiable
against fixed ids it neither invents nor changes.

**Outcome Definition**:

- `#view-stats` contains a `.stats-controls` block with `#stats-granularity` (four options:
  `hour`/`day`/`week`/`month`, `day` selected), `#stats-from`, and `#stats-to` date inputs.
- A `#stats-metric` container holds three `<button class="metric">` elements with `data-metric`
  values `count`/`tokens`/`cost`, `count` carrying `active` initially, and **none of them carrying
  `data-view`**.
- The chart heading is `<h3 id="stats-chart-title">` and the chart `<svg>` keeps `id="stats-chart"`.
- A history table with `<tbody id="stats-history">` and headers Period/Calls/Tokens/Cost sits below
  the chart `<svg>` and above the by-model heading.
- The served page (`GET /`) contains every id and class above.

**Test Specifications**:

- Integration Tests (`internal/web` is `go:embed`-ed, so every byte assertion is made against what
  the **running server serves**, never against the file on disk — a binary built before the edit keeps
  serving the old assets. This is the same technique GI-17 used for its Settings-tab markup, and this
  repo has already been bitten by serving stale embedded assets):
  - `curl -s http://localhost:<port>/ | grep -q 'id="stats-granularity"'`
  - `curl -s http://localhost:<port>/ | grep -q 'id="stats-from"'` and `grep -q 'id="stats-to"'`
  - `curl -s http://localhost:<port>/ | grep -q 'class="metric'` and confirm each metric button also
    shows `data-metric` and **does not** show `data-view`
  - `curl -s http://localhost:<port>/ | grep -q 'id="stats-history"'` and `grep -q 'id="stats-chart-title"'`
  - `curl -s http://localhost:<port>/ | grep -q 'type="date"'` (both date inputs served)
- Unit Tests: none — this bead is static markup; no `app.js` change to type-check.
- E2E: manual — open the Stats tab; the controls, the metric toggle, and an empty history table render
  (their behavior is br-GI-21-06's, verified there).

**Files to Touch**:
- `internal/web/index.html` (modify — the stats-controls block, metric toggle, dynamic heading id, and history table inside `#view-stats`)
