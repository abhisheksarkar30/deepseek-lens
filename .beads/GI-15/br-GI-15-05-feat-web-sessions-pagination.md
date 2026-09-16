# Bead br-GI-15-05: Shared pager, page-aware fetch, and the sessions tab

**Plan Reference**: `docs/planning/GI-15-pagination.md` §UI layer

- **Priority**: P0 (critical)
- **Dependencies**: br-GI-15-03
- **Blocks**: br-GI-15-06

## Description

Build the dashboard's pagination plumbing once, and prove it on the sessions tab — the simpler of
the two call sites, and the one that is currently worst (it renders the whole unbounded list in a
single `<table>`).

**1. Extract `fetchRaw`, don't duplicate the error path.** `fetchJSON` (app.js:104-112) does
`fetch()`, checks `!res.ok`, extracts the error message, and returns `res.json()`. The paged fetch
needs the same `ok`/error handling plus the response headers. Extract:

```js
async function fetchRaw(url)      // fetch() + the !res.ok error extraction → returns checked res
async function fetchJSON(url)     // (await fetchRaw(url)).json()
async function fetchPage(url)     // reads the three headers off the same res
                                  // → {items, total, limit, offset}
```

Chosen over the reverse nesting (`fetchJSON` wrapped around `fetchPage`) because non-paginated
endpoints — `/api/stats`, `/api/warnings/summary` — carry no pagination headers, so `fetchRaw` is
the honest shared base and `fetchPage` stays pagination-specific.

**2. `renderPager(container, {total, limit, offset}, onPageChange)`** — one helper, two call sites
(sessions here, the warnings drill-down in br-GI-15-07); not duplicated per table. Renders:

- a page-size `<select>` with options 25/50/100/200,
- Prev/Next buttons, disabled at the edges — `offset > 0` for Prev, `offset + limit < total` for
  Next,
- an "X–Y of Z" label,

and calls `onPageChange({limit, offset})` on interaction.

**Changing the page-size `<select>` always resets `offset` to 0** before calling `onPageChange`. A
user on page 3 of 100-row pages who switches to 25 returns to the top; without the reset they land
on item 200 *of 25-row pages*, which is a page they never chose.

**The `<select>` is driven by the applied `X-Limit`, not a private constant.** Its value is
initialised from — and re-synced to — the effective limit the server reports, which is the same
value the label and the Next/Prev math use. Since `X-Limit` reports the *applied* page size
(br-GI-15-03), a select whose shown value disagreed with the fetched page size would render a label
and edge-disablement that do not match the rows on screen. Both call sites therefore use a fixed
**initial page size of 50** — one of the select's options — stored on `state`, so the first fetch's
applied limit equals the select's initial value by construction. The initial value and the first
request's `?limit=` cannot diverge.

**3. Named state, not an anonymous module variable.** Add to the `state` object (app.js:116-126,
alongside `warnedIds`/`feedRowLimit`):

- `sessionsPage: {limit: 50, offset: 0}` — the pager's own limit/offset. 50 is the select's initial
  value.
- `sessionsFetchSeq: 0` — the race guard below.

**4. `loadSessions()` (app.js:390-410)** — reads `{limit, offset}` from `state.sessionsPage`, calls
`/api/sessions?limit=&offset=` via `fetchPage`, renders `items` into `#sessions-body`, and renders the
pager. It currently calls `fetchJSON("/api/sessions")` at app.js:393 and renders everything.

**The pager's container is a new element, and it cannot go inside `#sessions-body`.** That id is a
`<tbody>` (index.html:88) inside `.table-wrap` → `<table>` (index.html:80-90); a `<div>` there is
invalid markup the browser will relocate, and the helper needs a real container. Add
`<div id="sessions-pager" class="pager"></div>` as a **sibling** of `.table-wrap` inside
`#view-sessions` — above the table (before index.html:80) is the natural spot. `renderPager` takes
that element, and that is what makes the helper reusable: br-GI-15-07 adds a same-shaped sibling
container inside `#warnings-detail`, with no shared markup beyond the class.

Two details in the existing render to preserve while editing it:

- The empty-state row uses `colspan="7"` (app.js:403). With a pager, "empty" now has two causes:
  no sessions at all, and a valid page past the end (reachable through the accepted
  `X-Total-Count` staleness in br-GI-15-03). Keep the colspan, but do not let the hint text claim
  "No sessions recorded yet." for the second case — a page the user asked for reading as "nothing
  here" is what makes a staleness blip look like data loss.
- The per-row click wiring at app.js:404-406 rebinds after every render, so paging re-wires
  automatically. Nothing to change, just do not move it out of the render path.

**Guard the fetch with a generation counter.** At the top, increment `state.sessionsFetchSeq` and
capture `const seq`; after the `await` resolves, skip the render if `seq !== state.sessionsFetchSeq`.
The pager is exactly the surface that invites rapid repeated triggers — double-clicking Next, or
clicking the page-size select right after a Next click — and two fetches can be in flight with the
slower, *earlier* one resolving last and overwriting the correct later page. This mirrors the
`request` SSE Promise queue precedent at app.js:679-683.

**5. `index.html` / `style.css`** — add the `#sessions-pager` container (above) and the `.pager`
classes (page-size select, prev/next buttons, count label) once, reused via the same class by both
tables.

## Rationale

The pager is shared, the fetch layer is shared, and the race guard is the same pattern in both
places — so all of it lands once, in the bead that first needs it. Doing the sessions tab first is
deliberate: it is a single table with no client-side derivation, so it proves `renderPager`,
`fetchPage`, and the header contract end-to-end before the warnings tab adds a second moving part
(SSE-driven refresh) on top.

Nothing from the warnings tab is touched here, so br-GI-15-06 can be reviewed against a pager that
is already known to work.

## Outcome Definition

- `node --check internal/web/app.js` passes — a syntax gate `go build`/`go vet` cannot provide,
  since this is JS.
- `fetchRaw` exists; `fetchJSON` is a thin wrapper over it; `fetchPage` returns
  `{items, total, limit, offset}`.
- The page-size select changes the number of rendered rows; Next/Prev move by the page size and
  disable at both edges; the label reads `X–Y of Z`.
- The select's displayed value always equals the `X-Limit` of the page on screen, including on
  first load (50).
- Rapid Next/Next leaves the table on the later page — the earlier, slower response does not
  overwrite it.
- No other tab's behavior changes.

## Test Specifications

No JS test harness exists in this repo (confirmed by `docs/context/testing-and-quality.md`), so
verification is the project's documented three-step web-change process, in order:

1. **Syntax gate**: `node --check internal/web/app.js` — before touching a browser.
2. **Served-asset assertion**: build fresh (`go build ./cmd/lens && ./lens serve`, or
   `go run ./cmd/lens serve`) and confirm the *served* bytes contain the new code, e.g.
   `curl -s http://localhost:<port>/app.js | grep -q renderPager && echo ok`, likewise `fetchPage`.
   This guards the `go:embed` stale-binary trap — a binary built before the edit keeps serving old
   assets and makes a correct change look broken.
3. **Manual in-browser check**: seed more than one page of sessions and verify
   - the page-size select changes the rendered row count,
   - Next/Prev move correctly and disable at the first/last page,
   - the "X–Y of Z" label matches the rows on screen and `Z` matches `X-Total-Count`,
   - the select's value matches `X-Limit` after a load (check the response headers in DevTools →
     Network on the `/api/sessions` request),
   - **race check**: throttle to "Slow 3G" in DevTools, click Next twice in quick succession (then
     once more with a page-size change immediately after a Next click) and confirm the table settles
     on the *later* click's page — the earlier, slower response must not overwrite it.

## Files to Touch

- `internal/web/app.js` (modify — `fetchRaw`/`fetchJSON`/`fetchPage`, `renderPager`, `state`
  additions, `loadSessions`)
- `internal/web/index.html` (modify — pager markup and its anchor for the sessions table)
- `internal/web/style.css` (modify — pager classes)

## Review Notes

**A real bug was found and fixed by the harness, in `renderPager`.** With `total=120, limit=50,
offset=200` the label rendered **"201–120 of 120"** — a backwards range directly above a "nothing on
this page" row. Fixed by clamping `first` to `total` as well as `last`. This is exactly the accepted
`X-Total-Count` staleness br-GI-15-03 declined to prevent server-side, so the client has to render
it sanely rather than the server having to make it unreachable.

**How it was verified.** `node --check` for syntax, then asserted against the bytes a *fresh* `go
build` **serves** — not the files on disk, because `internal/web` is `go:embed`-ed and a
pre-existing binary keeps serving old assets. Both pager containers, all three generation counters,
and the absence of `groupWarnings` were asserted in the served `app.js`; `groupWarnings` returns 0
occurrences. Then the boundary arithmetic and the two race patterns were driven from a throwaway
Node script that **extracts `renderPager` out of the shipped source** rather than restating it, so
the check cannot drift from production code. That script is deliberately not committed — the
no-JS-harness convention still holds, and it was scratch verification for one change.

**`fetchPage` defaults `items` to `[]`** because the store encodes an empty result as `null` (every
list method declares `var out []T`), which would otherwise blow up every caller's `.map`. Found by
reading the store, not by hitting it.

**Ordering note.** The page-size select's `change` handler resets the offset to 0. Keeping it would
strand a user on page 3 *of 25-row pages* at item 200 — a page they never chose, reading as missing
data. The bead's outcome list did not name this case; it is the one addition.
