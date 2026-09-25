### Bead 6: `feat` shared `hourBound` helper, Stats hour-precision From/To, and the "Days in" selector

- **Bead ID**: br-GI-27-06
- **Priority**: P1 (high)
- **Original Estimate**: 2h
- **Dependencies**: br-GI-27-04
- **Blocks**: br-GI-27-07

**Description**:

A.2 frontend + F/§11B. The Stats tab has date-only From/To and its bounds use `utcDayBound`, which calls `toISOString()` (`app.js:452-464`, `toISOString()` at `:464`) — silently UTC. Replace the date-only pair with hour-precision `datetime-local` inputs, add the "Days in" zone selector, and route **both** this tab and the Feed (bead 07) through **one** shared helper.

**Precondition:** the A.1/A.2 prototype is stashed (bead 02 step 0, plan §13). This bead implements A.2's frontend from the plan; the prototype's tz-offset helper (named `dayBound` in the dirty tree; **`utcDayBound` in the base tree**) is superseded by `hourBound` (§11B) and must not be popped. **All line citations in this bead are base-tree coordinates** (`git show HEAD:<path>`), the tree the implementer works on after the stash.

**1. One helper — `hourBound(value, endExclusive, offsetMin | 'local')`** (`internal/web/app.js`). Returns an RFC3339 instant string for a `datetime-local` value:
- The value format is `YYYY-MM-DDTHH:mm` (hour precision from the input). **Minutes are truncated to the hour** — floor any fractional-hour value to `:00`. So `since` = start of the from-hour, `until` = **end of the to-hour** (i.e. the start of the next hour) when `endExclusive` is true — the window is `[from-hour start, to-hour end)`.
- Fixed-offset choices (`UTC`, `UTC+8`): compute from `Date.UTC(...) − offsetMin`.
- `'local'`: use the numeric constructor `new Date(y, m-1, d, h)` (DST-correct).
- **Format the output by hand** — e.g. `YYYY-MM-DDTHH:mm:ss.000Z` — **never `toISOString()`** (same pattern as claude-lens). This is the whole point of the helper: one offset implementation, no platform tz formatting.
- **Remove `utcDayBound`** (`app.js:452-464`) when `hourBound` supersedes it; its `toISOString()` at `:464` goes with it. The guard below applies to the new `hourBound` and to the removal of `utcDayBound`, **not** as a global source ban.

**2. "Days in" selector** (`internal/web/index.html`). A Local / UTC / UTC+8 control, **default Local**. The same numeric offset the zone implies is sent as `?tz_offset=` to `/api/stats` (bead 04) — minutes east of UTC. **The active zone is labelled on the chart heading or the picker** so totals can be reconciled against `lens stats` (which stays UTC — the accepted divergence bead 04 records).

**3. Stats tab From/To** (`internal/web/index.html` + `app.js`). Replace `stats-from`/`stats-to` (date inputs — the `utcDayBound` call sites at `app.js:478-479`, plus the lookback preset region at `:633-643`) with the same `datetime-local` pair (hour precision; empty = open-ended). Bounds are built with `hourBound` **in the "Days in" zone**: fixed-offset choices pass the numeric offset, Local passes `'local'`. Interpret:
- from set / to set → `[from-hour start, to-hour end)`;
- from set / to empty → start bound only;
- from empty / to set → end bound only;
- empty / empty → no window.

**4. Rendered through `hourBound`** — the values sent are the returned strings; never reshape with `new Date("YYYY-MM-DD")` (silently UTC) at any call site.

**Rationale**:

A.2/§11B/F. The Stats picker's `utcDayBound` silently interprets date-only values as UTC, so the fixed-offset zone choice (bead 04) is unreachable from the UI and the displayed days disagree with the numbers. Two independent offset implementations (one for the Feed, one for Stats) is exactly the defect this design exists to avoid — hence one `hourBound` used by both tabs (bead 07 consumes it).

**Outcome Definition**:

- `hourBound` exists in `app.js`, formats its output by hand, truncates minutes to the hour, handles fixed offsets and `'local'` (DST-correct), and is the **only** offset builder for both tabs.
- `utcDayBound` and its `toISOString()` call are removed.
- The Stats tab's From/To are `datetime-local` inputs; the "Days in" selector defaults to Local and sends `tz_offset` to `/api/stats`.
- The active zone is labelled on the chart heading or picker.
- No new `toISOString()`/`new Date("YYYY-MM-DD…")` appears in the changed code paths.
- `node --check internal/web/app.js` passes (there is no JS runtime in the toolchain).

**Test Specifications**:

- **Web asset guard** (`internal/web` test or a grep asserted in the bead's completion note): **no `utcDayBound` reference remains** — name the base symbol explicitly. A guard spelled for the prototype's `dayBound` passes vacuously: that symbol exists only in the un-stashed prototype, so it is absent from the base tree before any work is done and the grep finds nothing either way. Confirm the grep has a positive control (it matches `utcDayBound` on the un-modified base `app.js:452`) before trusting an empty result. Also: no `toISOString` in `hourBound` or its call sites; the new `hourBound` is present.
- **Format check** (static, since there is no JS runtime — `node --check` only): `hourBound` output matches `YYYY-MM-DDTHH:mm:ss.000Z` shape by reading the code, and the offset is applied by hand.
- **Manual** (plan §7 Gate): Stats tab Local vs UTC shows different daily totals for the same non-UTC data; a from/to range sends the same `since`/`until` strings a manual query would.
- DST/rollover cases are manual (no JS runtime).

**Files to Touch**:
- `internal/web/app.js` (modify — add `hourBound`, remove `utcDayBound` at :452-464, rewire stats bounds at :478-479 and :633-643; all base-tree coordinates)
- `internal/web/index.html` (modify — "Days in" selector; Stats From/To become `datetime-local`)
- (web asset guard test, if the repo has one — otherwise the grep in the completion note)
