# Bead br-GI-15-07: Pager and race guard on the warnings drill-down

**Plan Reference**: `docs/planning/GI-15-pagination.md` §UI layer (`showWarningDetail`)

- **Priority**: P1 (high)
- **Dependencies**: br-GI-15-06
- **Blocks**: br-GI-15-08 (docs refresh)

## Description

br-GI-15-06 pointed the drill-down at the server but left it fetching a fixed 1000 rows. This bead
gives it a real pager and the guard that surface needs.

**1. Named pager state.** Add to `state`: `warningDetailPage: {limit: 50, offset: 0}` and
`warningDetailFetchSeq: 0`. 50 is a select option and matches the sessions pager's initial size, so
the first fetch's applied `X-Limit` equals what the select shows.

**2. Render the pager** inside the drill-down panel, using the `renderPager` helper from
br-GI-15-05 — no second implementation. The drill-down's `{total, limit, offset}` come from
`fetchPage`'s same-named fields, which is the same source the title count (set in br-GI-15-06)
reads, so the title and the pager's "X–Y of Z" denominator always agree.

The drill-down table body is `#warnings-detail-body` and its title is `#warnings-detail-title`
(index.html:52, :56).

**Add a `<div id="warnings-detail-pager" class="pager"></div>` as a sibling of the `.table-wrap` at
index.html:53, inside `#warnings-detail`.** Same reason as the sessions pager in br-GI-15-05 — a
`<div>` inside a `<tbody>` is invalid markup. Placing it inside `#warnings-detail` rather than
outside is deliberate: that panel's `hidden` attribute already toggles with the drill-down, so the
pager appears and disappears with the table it controls and needs no show/hide code of its own.

**3. Selecting a different kind/severity group resets the drill-down pager to `offset: 0`.**
`state.warningDetailPage.offset = 0` before the fetch. Without it, clicking a group with 3
occurrences while paged to offset 50 shows an empty table — a page that does not exist for that
group.

**4. Guard the fetch with a generation counter.** At the top of the drill-down fetch increment
`state.warningDetailFetchSeq` and capture `const seq`; skip the render if `seq !==
state.warningDetailFetchSeq` after the `await`. Same increment-and-capture / discard-on-mismatch
pattern as `loadSessions()` (br-GI-15-05) and `loadWarnings()` (br-GI-15-06).

The pager is the rapid-trigger surface here for the same reason as the sessions tab: double-clicking
Next, or changing the page size right after a Next click, puts two fetches in flight and lets the
slower, earlier one overwrite the correct later page. Framing it as a counter rather than a
"disable the button while loading" flag is deliberate — it also covers the group-switch case, where
clicking group B while group A's fetch is still in flight must not let A's rows land in B's table.

## Rationale

The drill-down is the one place where a warnings group can genuinely be large — a single noisy kind
can hold thousands of rows, which is the case that made the old ≤1000-row client cache both slow
and wrong. It is also the view users reach *after* the summary tells them which kind is big, so it
is exactly where a bounded page size pays.

Splitting it from br-GI-15-06 keeps the capitalized-keys/cache-removal change reviewable on its own,
and keeps this bead's diff to "add a pager and a guard" rather than mixing in the wire-contract
hazard.

## Outcome Definition

- `node --check internal/web/app.js` passes.
- The drill-down renders a pager consistent with the sessions tab (same helper, same look).
- Page size changes the rendered row count; Next/Prev move by the page size and disable at both
  edges; the label's `Z` equals the group's `X-Total-Count` and its `Y` never exceeds `Z`.
- Switching to a different group starts at the first page.
- The title count and the pager's `Z` are the same number for the same group.
- Rapid Next/Next settles on the later page; a group switch during an in-flight fetch never renders
  the previous group's rows.

## Test Specifications

No JS harness; three-step web process (`node --check` → served-asset assertion → manual browser
check). Specific checks:

1. `node --check internal/web/app.js`.
2. Served-asset assertion: `curl -s http://localhost:<port>/app.js | grep -q warningDetailPage`.
3. Manual, against a database with **more than one page** of warnings in a single kind (seed >50 at
   the 50-row default):
   - the pager appears under the drill-down and pages through the group;
   - Next/Prev disable correctly at the first and last page, including the **last partial page** —
     the final page must not offer a Next that yields an empty table;
   - the title count equals the pager's `Z`;
   - paging to page 2 and then clicking a *different*, smaller group shows that group's first page
     (not an empty table) — the offset reset;
   - **race check**: throttle to "Slow 3G", click Next twice in quick succession (and once with a
     page-size change immediately after a Next click) and confirm the table settles on the later
     click's page; then click group B while group A's fetch is still in flight and confirm A's rows
     never render in B's table.

## Files to Touch

- `internal/web/app.js` (modify — `showWarningDetail`, `state` additions, pager render)
- `internal/web/index.html` (modify — the `#warnings-detail-pager` container required by step 2; the
  original list omitted it, but step 2 cannot be implemented without it)

**Note (added during implementation)**: step 2 needs markup, so `index.html` is a file this bead
touches. `style.css` is not — `.pager` and its children already exist from br-GI-15-05.
