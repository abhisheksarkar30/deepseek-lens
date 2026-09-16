# Bead br-GI-15-06: Warnings tab groups from the server, and `warningsCache` goes away

**Plan Reference**: `docs/planning/GI-15-pagination.md` §UI layer (`loadWarnings` / `showWarningDetail`)

- **Priority**: P0 (critical)
- **Dependencies**: br-GI-15-04, br-GI-15-05
- **Blocks**: br-GI-15-07

## Description

Switch the warnings tab's summary table from client-side grouping of a capped fetch to the server's
grouped counts, and remove the in-memory cache the old design needed. Five coupled changes — they
land together because removing `warningsCache` breaks the code that reads it, and there is no
intermediate state that both compiles and behaves.

**1. `loadWarnings()` consumes the summary endpoint.** app.js:255-259 becomes a
`fetchJSON("/api/warnings/summary")`. `groupWarnings()` (app.js:240) is deleted — dead code, and its
existence is what the stale doc reference in `docs/context/testing-and-quality.md:15` points at.

**2. `renderWarningGroups(list)` reads capitalized keys.** app.js:266-279 currently reads lowercase
`g.kind` / `g.severity` / `g.count` / `g.lastSeen`, because it consumed the client-minted keys
`groupWarnings()` produced. `store.WarningGroup` carries **no `json` tags** (the repo's documented
convention for types crossing the API/SSE broker, `types.go:58-61`), so the served keys are the Go
field names. It must read `g.Kind` / `g.Severity` / `g.Count` / `g.LastSeen`.

This is the single most likely way to ship a broken tab: a literal swap-the-source edit compiles,
runs, and renders every cell `undefined`. **Do not add `json` tags to `WarningGroup` to paper over
it** — the no-tags convention is established and out of scope for this ticket. br-GI-15-01's
marshal test pins the capitalized wire form, so a future tag addition fails there rather than here.

**3. The SSE `warnings` handler refreshes through a debounce.** app.js:702-709 currently reassigns
`state.warningsCache` and calls `renderWarningGroups(state.warningsCache)` with a raw `Warning[]` —
which stops type-checking the moment (2) lands. Replace it: when a `warnings` event arrives and the
warnings tab is visible, schedule a `loadWarnings()` call through a `setTimeout`-based coalescing
guard — on each event `clearTimeout(state.warningsDebounceTimer)` and restart it with ~250 ms,
storing the new id.

Without the debounce a multi-turn warning streak fires N back-to-back summary fetches and the table
visibly flickers through N intermediate states. With it, a burst produces exactly one refetch.

**4. `showView("warnings")` (app.js:155) cancels any pending debounce timer** before calling
`loadWarnings()`:

```js
clearTimeout(state.warningsDebounceTimer);
state.warningsDebounceTimer = null;
```

A still-pending timer would otherwise fire a duplicate `loadWarnings()` immediately after `showView`
already issued one. This does **not** make fetches single-flight: `clearTimeout` is a no-op on a
timer that has already fired, so an in-flight fetch and the new tab-switch fetch can overlap. That
residual race is what (5) closes.

**5. `loadWarnings()` gets a generation counter.** Add `warningsDebounceTimer: null` and
`warningsFetchSeq: 0` to `state` (app.js:116-126). At the top of `loadWarnings()` increment
`state.warningsFetchSeq` and capture `const seq`; after the `await` resolves, skip the render if
`seq !== state.warningsFetchSeq`. This makes last-issued-wins hold regardless of network timing or
which call site — a debounce fire or a tab switch — triggered which fetch. Same pattern as the
`request` SSE Promise queue at app.js:679-683 and `loadSessions()`'s guard in br-GI-15-05.

Together (4) and (5) give the correct invariant: at most one *pending* timer at any time, and the
last-issued fetch always wins the render.

**6. Remove `state.warningsCache` and every site that reads it.** Full enumeration — all five are
addressed, none left dangling:

| Site | What it does | Resolution |
|---|---|---|
| app.js:258 | `state.warningsCache = list` | deleted with the cache |
| app.js:259 | back-fills `state.warnedIds` from `w.RequestID` | **deleted, not replaced** — accepted loss, below |
| app.js:285 | `state.warningsCache.filter(...)` in `showWarningDetail` | see (7) |
| app.js:287 | `${matches.length}` title count from that filter | see (7) |
| app.js:705-707 | SSE handler reassignment + `-feedRowLimit` slice | replaced by (3) |

The `warningsCache: []` declaration at app.js:120 goes too — once those sites are gone it is written
and read by nothing.

**Accepted loss at app.js:259, stated plainly rather than left implicit.** Line 259 is the only
non-SSE writer of `state.warnedIds`, which is what badges an already-rendered feed row
(`feedRowHTML`, app.js:175). After this change `loadWarnings()` fetches `WarningGroup[]`, which
carries no `RequestID`, so the loop has no source and is deleted. Consequence: **a feed row for a
request that was already warned *before* the page loaded shows no `⚠` badge until a fresh SSE
warning arrives for that same request.** The only remaining writer is `markFeedRowWarned`
(app.js:198-205) from the SSE path, which keeps *live* warnings badging correctly.

This is accepted for the same reason as the `lens sessions` cap: the badge is cosmetic, and the
alternative is re-issuing the exact `fetchJSON("/api/warnings?limit=1000")` full-list fetch this
ticket exists to eliminate — just to harvest IDs and discard the rows. Upgrade path if it ever
matters: page the raw `/api/warnings` and pair it with `feedRowHTML`, or add a request-id field to
the summary response. Do not implement either here.

**7. `showWarningDetail(kind, severity)` (app.js:281-299) fetches from the server.** It currently
filters `state.warningsCache` in JS — a stale snapshot, capped at 1000 on load and shrunk to the
last 200 after any SSE warning. Switch it to `fetchPage("/api/warnings?kind=&severity=&limit=&offset=")`
and render `items`. Its title (app.js:287, `${matches.length} occurrence(s)`) becomes the
drill-down's **`total`** from `X-Total-Count`: `${kind} (${severity}) — ${total} occurrence(s)`.
That is the correct global count for the group — the old `matches.length` counted only what was in
the ≤1000-row client cache — it is already in the response with no extra fetch, and it matches the
pager's "X–Y of Z" denominator that br-GI-15-07 adds beneath it.

This bead uses `fetchPage` but renders **no pager and no generation counter** — the pager guard
belongs with the rapid-trigger surface br-GI-15-07 introduces. Use a fixed `limit=1000` here so the
group-switch behavior is unchanged from today.

## Rationale

Grouping and pagination do not compose, so the summary table has to be server-derived — and once it
is, the client-side cache that existed only to feed the old grouping has no reader left. Every
change here is forced by the previous one, which is why they are one bead: splitting them would
produce commits that either do not compile or silently render `undefined` cells.

The debounce and the generation counter are not gold-plating. The old code assigned a snapshot and
re-rendered synchronously, so it had neither race; the new code awaits the network on every SSE
event and on every tab switch, which is precisely what creates both.

## Outcome Definition

- `node --check internal/web/app.js` passes.
- `groupWarnings` no longer exists in app.js.
- `renderWarningGroups` reads `g.Kind`/`g.Severity`/`g.Count`/`g.LastSeen`; every cell renders a
  real value, no `undefined`.
- `state.warningsCache` no longer exists, and no code reads it (grep clean).
- `state.warnedIds` is still written by the SSE path (`markFeedRowWarned`) and the `⚠` badge still
  appears for live warnings.
- A burst of SSE `warnings` events produces **one** summary refetch ~250 ms after the burst ends.
- Switching to the warnings tab does not double-fetch when a timer was pending.
- The drill-down title shows the true global occurrence count for the group, and its rows come from
  the server.

## Test Specifications

No JS harness; the three-step web process from br-GI-15-05 applies (`node --check` → served-asset
assertion → manual browser check). Specific checks:

1. `node --check internal/web/app.js`.
2. Served-asset assertion: `curl -s http://localhost:<port>/app.js | grep -q 'g.Kind'` (and confirm
   `grep -q groupWarnings` finds **nothing**).
3. Manual, in a browser, with a database seeded with **more than `DefaultLimit`** warnings of one
   kind:
   - the summary table's count for that kind equals the true total, not 1000 — the undercount fix,
     verified through the UI this time and not just the API test;
   - a new warning arriving over SSE updates the summary table in real time without a tab switch;
   - rapid back-to-back SSE warning events produce a single summary refetch (one Network entry, not
     N) — the debounce;
   - **generation-counter check**: throttle to "Slow 3G"; trigger an SSE burst on the warnings tab
     (starts slow fetch A via the debounce timer), switch away and immediately back before A
     resolves (starts fetch B); confirm the rendered summary reflects B and that A's possibly-stale
     data does not overwrite it when A finally lands;
   - the drill-down title count matches the summary table's count for the same group;
   - a feed row that was already warned before page load shows no `⚠` badge until a fresh SSE
     warning arrives for that request — the accepted loss, confirmed as *known behavior* rather
     than discovered as a bug.

## Files to Touch

- `internal/web/app.js` (modify — `loadWarnings`, `renderWarningGroups`, `showWarningDetail`, the SSE
  `warnings` handler, `showView`, `state`; delete `groupWarnings` and `warningsCache`)
