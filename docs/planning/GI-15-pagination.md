# GI#15: Pagination (limit/offset/page-size) for list APIs and dashboard UI

<!-- version=11 status=converged -->

## Context

None of deepseek-lens's list surfaces are paginated today:

- `GET /api/requests` and `GET /api/warnings` accept only `?limit` (default/hard cap
  `store.DefaultLimit = 1000`, [internal/store/store.go:30-32](../../internal/store/store.go#L30-L32)).
  No `offset`, and the response is a bare JSON array with no total-count/has-more signal.
- `GET /api/sessions` ([internal/api/api.go:636-647](../../internal/api/api.go#L636-L647),
  [internal/store/store.go:544-561](../../internal/store/store.go#L544-L561)) has **no limit at
  all** — a fully unbounded `SELECT ... ORDER BY last_seen DESC`.
- The dashboard (`internal/web/app.js`) has no page-size control and no prev/next anywhere: the
  sessions tab renders the entire unbounded list in one `<table>`; the warnings tab pulls up to
  1000 raw rows and groups them **client-side** into the kind×severity summary table.

As the local SQLite capture file grows over weeks of usage, these unbounded/high-cap loads get
slow both in SQL (full scans) and in the browser (large DOM tables). This plan adds real
offset/limit pagination — with a selectable per-page size — to the APIs and to the two UI views
where it actually pays for itself. GitHub issue: https://github.com/abhisheksarkar30/deepseek-lens/issues/15.

This is Phase 1+2 of the flywheel only (intake + plan) — the user explicitly asked for a plan,
not beadify/implementation. No code changes are made in this pass.

## Design decisions (and what's deliberately left out)

**Headers, not a response envelope.** `X-Total-Count` / `X-Limit` / `X-Offset` response headers
carry pagination metadata; response *bodies* stay the bare JSON arrays they are today. This is the
smaller, non-breaking diff — every existing consumer (dashboard `fetchJSON`, `api_test.go`
assertions, the `lens` CLI, which talks to `*store.Store` directly and is unaffected either way)
keeps working unchanged; the UI layer alone learns to also read the three headers.

`X-Limit` and `X-Offset` report the **effective** values the store actually applied, not the
requested ones. This matters because the two diverge at the default: an absent `?limit` parses to
`0` (`parseLimit` returns 0 for an absent param), which the store clamps to `DefaultLimit` — so the
response must carry `X-Limit: 1000`, **not** `X-Limit: 0`. A header-driven consumer computes its
next page as `nextOffset = offset + X-Limit`; reporting the raw requested `0` would leave such a
client stuck on page 1 forever, so the effective value is the only one that makes the header's
stated purpose ("carry pagination metadata") true. `X-Total-Count` is the full filtered count,
independent of the page window; `X-Offset` is the parsed offset (absent → `0`, negatives rejected
by `parseOffset` before the handler runs).

**Warnings grouping moves server-side.** The warnings tab's summary table currently groups up to
1000 raw rows in JS (`groupWarnings()` in app.js). Once `/api/warnings` is genuinely paginated,
a page of N rows can't produce a correct global group count — grouping and pagination don't
compose. Fix: add `store.WarningSummary` (`SELECT kind, severity, COUNT(*), MAX(created_at) ...
GROUP BY kind, severity ORDER BY COUNT(*) DESC, kind, severity`) and a new `GET /api/warnings/summary` route.
This is unbounded but safe — a GROUP BY result is bounded by the number of distinct (kind,
severity) pairs, not row count. The query must full-scan the `warnings` table, which is addressed
by the covering index added in the Store layer below. The existing `/api/warnings` becomes the
paginated raw list, used only by the per-group drill-down (`showWarningDetail`). This also fixes
a latent bug: today's grouping already silently undercounts once a kind has more than 1000
occurrences total, since it groups only the capped fetch.

**Scope cuts, stated explicitly rather than silently dropped:**
- *Feed tab*: no pagination widget. It's a live SSE tail (latest 50 loaded once, then capped at
  200 client-side by dropping the oldest as new rows arrive) — pagination conflicts with "always
  show latest." `/api/requests` still gains `offset` + headers for API completeness/future
  consumers, but the feed tab keeps its current behavior unchanged.
- *Session-detail calls table* (`openSession` in app.js): no pagination widget. Its running-total
  column ("cost by turn 12") is computed cumulatively across the *whole* session client-side —
  paginating would either break that running total or require new server-side "cumulative as of
  offset" fields, real added weight for a table whose row count (a single agent session's turn
  count) isn't the actual pain point this story is driven by. `ListRequests` already caps it at
  `DefaultLimit` (1000) server-side today, unchanged by this plan — so it's bounded, just not
  paginated in the UI.
- *CLI* (`lens ls`/`warnings`/`export`/etc.): the ask is "APIs... and UI" — the CLI calls
  `*store.Store` directly, not the HTTP API, and already has its own `--limit` flag
  ([internal/cli/export.go:28](../../internal/cli/export.go#L28)). Out of scope. **Note:** the
  `ListSessions(ctx)` → `ListSessions(ctx, store.Filter{})` call-site fixup in
  `internal/cli/sessions.go` is a mechanical compile fix, but it silently caps `lens sessions`
  output to `DefaultLimit` (1000) where it was previously truly unbounded. This is an accepted
  side effect: 1000 sessions is well beyond what a single-developer tool typically accumulates
  before needing this fix, and the `lens export` command already has `--limit` for users who
  need volume control. Adding `--limit` to `lens sessions` is a follow-on improvement, not part
  of this plan.

## Store layer (`internal/store`)

- **`Filter`** ([internal/store/types.go:105-122](../../internal/store/types.go#L105-L122)): add
  `Offset int` (0 = start at the top), doc comment updated alongside the existing `Limit` comment.
  Several further doc comments this change makes stale must be updated in the same change (the
  F3.4/F7.1/F8.2 staleness class — comments the plan's edits invalidate):
  - `internal/store/types.go:102-104`: rewrite the reader list to name **all four `Filter` readers
    this plan leaves in place** — `ListRequests, ListWarnings, ListSessions, and WarningSummary` —
    and enumerate the `Count*` variants (`CountRequests`/`CountWarnings`/`CountSessions`) in the
    "only the fields relevant to the call being made are read" sentence. **Governing principle (the
    class this closes):** the comment the plan *writes here* must not be invalidated by the plan's
    *own additions* — `WarningSummary` (below) and the `Count*` readers are readers the plan itself
    introduces, so an F8.2 rewrite that stopped at `ListSessions` would be stale the moment they
    land. Resulting text, e.g.: `"Filter narrows ListRequests, ListWarnings, ListSessions,
    WarningSummary, and the CountRequests/CountWarnings/CountSessions counts. Only the fields
    relevant to the call being made are read — ListRequests ignores Kind/Severity, ListWarnings
    ignores SessionID/Model/OnlyWarned/OnlyErrors, ListSessions ignores everything but Limit/Offset,
    WarningSummary ignores Limit/Offset/SessionID/Model/OnlyWarned/OnlyErrors/ReplayOf, and
    CountRequests/CountWarnings honor their List twin's predicates minus Limit/Offset while
    CountSessions takes no Filter (sessions have no filterable column, so it reads none)."`
  - `internal/store/store.go:30-32`: `"DefaultLimit is what Filter.Limit: 0 means for ListRequests
    and ListWarnings"` → `"... for ListRequests, ListWarnings, and ListSessions"` (the plan's own
    `ListSessions` bullet clamps `Limit <= 0` to `DefaultLimit`, so the sentence is no longer
    complete without it).
  - `internal/store/store.go:543` (F8.2): `"ListSessions returns every session, most recently
    active first."` → `"ListSessions returns a page of sessions, most recently active first."` —
    false once the method clamps `Limit <= 0` to `DefaultLimit` and applies `LIMIT ? OFFSET ?`; it
    returns a page, not every session.
  - `internal/store/store.go:683` (F8.2, the **fourth** member of the class — a deliberate search
    this round confirmed no others exist in `internal/`): `"ListWarnings returns warnings matching f
    (Kind/Severity/Since/Limit)"` — the parenthetical enumerates the `Filter` fields the method
    honors. Adding `Offset` to `ListWarnings` makes it stale; it must read
    `(Kind/Severity/Since/Limit/Offset)`. (Asymmetric with `ListRequests`, whose method comment at
    `store.go:304-305` enumerates no fields and so is not affected.)
- **`ListRequests`** / **`ListWarnings`** ([internal/store/store.go:306-359](../../internal/store/store.go#L306-L359),
  [683-728](../../internal/store/store.go#L683-L728)): extract each method's existing WHERE-clause
  building into a small helper (`requestWhere(f) (string, []any)`, `warningWhere(f) (string,
  []any)`) so the new Count variant can never drift from the List query. Append `OFFSET ?` next to
  the existing `LIMIT ?` (clamp negative offsets to 0, mirroring how `Limit <= 0` is already
  clamped to `DefaultLimit`). Add `id DESC` as a secondary tiebreaker to both `ORDER BY` clauses:
  `ORDER BY started_at DESC, id DESC` for requests and `ORDER BY created_at DESC, id DESC` for
  warnings. This prevents rows with equal timestamps (common in batch-inserted warnings or
  grouped-flush request writes) from appearing on multiple pages or being silently skipped across
  a page boundary.
- **New `CountRequests(ctx, f) (int, error)`** / **`CountWarnings(ctx, f) (int, error)`**: `SELECT
  COUNT(*) FROM ... WHERE <same clause>` via the shared where-builders (ignore `f.Limit`/`f.Offset`).
- **`ListSessions`** ([internal/store/store.go:544-561](../../internal/store/store.go#L544-L561)):
  change signature to `ListSessions(ctx, f Filter) ([]*Session, error)`, honoring only
  `Limit`/`Offset` (same "only the fields relevant to this call are read" convention the `Filter`
  doc comment already states for the other two methods) — `LIMIT ? OFFSET ?` added to the query,
  same `Limit <= 0 → DefaultLimit` clamp. Add `id DESC` as a secondary tiebreaker to the `ORDER
  BY` clause: `ORDER BY last_seen DESC, id DESC`. This is the same fix already applied to
  `ListRequests`/`ListWarnings` (F1.3) — without a tiebreaker, two sessions with equal
  `last_seen` values can appear on multiple pages or be silently skipped across a page boundary
  once `OFFSET` is in play. `sessions.id` is TEXT but lexicographic order is sufficient to make
  the sort total and stable across pages. This is a real (minor) behavior change: sessions were
  previously truly unbounded; now capped at 1000 by default like every other list. Worth it for
  consistency — 1000 sessions is already far beyond what a single-developer tool accumulates
  without this becoming a problem anyway.
- **New `CountSessions(ctx) (int, error)`**: `SELECT COUNT(*) FROM sessions`.
- **New `WarningSummary(ctx, f Filter) ([]WarningGroup, error)`**: honors `Since`/`Kind`/`Severity`
  only, via `warningWhere`. SQL: `SELECT kind, severity, COUNT(*), MAX(created_at) ... GROUP BY kind,
  severity ORDER BY COUNT(*) DESC, kind, severity`. New `WarningGroup{Kind, Severity string; Count int; LastSeen
  time.Time}` type in `internal/store/types.go`.
- **New index, appended to `internal/store/schema.sql`**: `CREATE INDEX IF NOT EXISTS
  idx_warnings_kind_severity_created_at ON warnings(kind, severity, created_at)`. Applied
  idempotently on every `Open()` call alongside the rest of the schema — no migration framework
  or version bump needed, consistent with this project's `CREATE TABLE IF NOT EXISTS`-only model
  (as stated at `schema.sql:1-2`). This converts `WarningSummary`'s otherwise full-scan GROUP BY
  into an index scan and lets `MAX(created_at)` be satisfied from the index. Without it the
  "unbounded but safe" claim for `GET /api/warnings/summary` holds for result-set size but not
  query cost — the full table would be scanned on every summary fetch.

**`X-Total-Count` staleness (accepted limitation).** `List*` and `Count*` are two independent,
non-transactional queries. Per CLAUDE.md, the store has exactly one writer goroutine that is
continuously inserting rows while the dashboard is open. A new row can land between the `List`
call and the immediately-following `Count` call, so `X-Total-Count` may reflect a count one row
higher than the result set the page was computed from. This is intentional and consistent with
the project's "fail open" philosophy: the dashboard is a local single-user tool, and a
momentarily-stale total-count header is cosmetic (the Next/Prev buttons may briefly show an extra
page that turns out empty). Wrapping `List`+`Count` in a `BEGIN DEFERRED` read transaction is the
upgrade path if this ever causes a real problem.

**Existing callers to update for the `ListSessions` signature change** (mechanical —
`ListSessions(ctx)` → `ListSessions(ctx, store.Filter{})`, zero value keeps today's default-limit
behavior):
[internal/cli/sessions.go:36](../../internal/cli/sessions.go#L36),
[internal/consumer/consumer_test.go:786,831](../../internal/consumer/consumer_test.go#L786),
[internal/cli/cli_test.go:356](../../internal/cli/cli_test.go#L356).
`internal/api/publishing_store.go`'s `PublishingStore` embeds `*store.Store` and overrides only
write methods, so it inherits the new signature automatically — no edit needed there.

## API layer (`internal/api/api.go`)

- **`Store` interface** ([internal/api/api.go:31-41](../../internal/api/api.go#L31-L41)): add
  `CountRequests`, `CountWarnings`, `CountSessions`, `WarningSummary`; change `ListSessions`'s
  signature to take `store.Filter`.
- **New `parseOffset(r) (int, error)`** next to the existing `parseLimit`
  ([internal/api/api.go:116-128](../../internal/api/api.go#L116-L128)): same shape, defaults to 0,
  rejects negative values.
- **New `writePageHeaders(w, total, limit, offset int)`**: sets `X-Total-Count`, `X-Limit`,
  `X-Offset`. Called right before `writeJSON` in each handler below (must run before
  `WriteHeader`, which `writeJSON` calls). `limit` is the **effective** page size the store
  applied, not the raw parsed query param — the two differ whenever `?limit` is absent (`limit`
  parses to `0`, store clamps to `DefaultLimit`). Each handler resolves the clamp *before* calling:
  `effLimit := limit; if effLimit <= 0 { effLimit = store.DefaultLimit }` (mirroring the store's own
  `Limit <= 0 → DefaultLimit` clamp). Offsets need no such resolution — `parseOffset` already
  rejects negatives, so absent → `0` is the effective offset.
- **`listRequests`** ([internal/api/api.go:160-197](../../internal/api/api.go#L160-L197)): parse
  `offset`, add to the `store.Filter`; after `ListRequests`, call `CountRequests` with the same
  filter, then `writePageHeaders(w, total, effLimit, offset)` with the effective limit resolved as
  above (never the raw `limit`), then `writeJSON` unchanged.
- **`listWarnings`** ([internal/api/api.go:614-634](../../internal/api/api.go#L614-L634)): same
  pattern with `CountWarnings` and the same effective-limit resolution before `writePageHeaders`.
- **New `warningsSummary` handler** + route `GET /api/warnings/summary` (registered alongside the
  other routes at [internal/api/api.go:79-88](../../internal/api/api.go#L79-L88)): parses
  `since`/`kind`/`severity` (no limit/offset — unbounded GROUP BY, per the design decision above),
  calls `store.WarningSummary`, `writeJSON`s the `[]store.WarningGroup`.
- **`listSessions`** ([internal/api/api.go:636-647](../../internal/api/api.go#L636-L647)): parse
  `limit`/`offset`, call `ListSessions(ctx, store.Filter{Limit: limit, Offset: offset})`, then
  `CountSessions`, `writePageHeaders(w, total, effLimit, offset)` with the same effective-limit
  resolution as `listRequests`, `writeJSON` unchanged (comment about "no limit param to
  honor" at line 637-640 gets corrected/removed since it's no longer true).

`getRequest`/`getSession` (single-resource endpoints) are untouched. There is no CSV/export
endpoint in `internal/api` — `lens export` is a CLI command that writes JSONL directly to
`*store.Store` and is already covered by the scope-cuts section above.

**Doc comment update** (`internal/api/api.go:66-68`): the existing `New` doc comment currently
reads "grouping, e.g. for the warning inbox, is the dashboard JS's job" — a parenthetical that
is now stale. After this plan, grouping happens in `store.WarningSummary` (SQL), not in the
dashboard JS. The handler itself stays thin (just calls `store.WarningSummary`), so the "no
business logic in handlers" principle still holds; only the parenthetical naming JS as the
grouping owner should be removed or updated to say grouping is now in `store.WarningSummary`.

A second comment carries the same grouping-ownership cross-reference and must move with it
(F8.2, fifth member of the staleness class — the F3.4 sibling): `internal/api/api.go:662`
(`sessionDetail.Warnings`) reads "... group them into one line per kind is the dashboard's job
(see this package's New doc comment)". The session-drill-down pairing is still
`groupSessionWarnings`'s job — that function survives this plan — so the *claim* stays true, but
the `(see this package's New doc comment)` pointer dangles once the edit above removes/retargets
the grouping discussion in `New`. Drop or retarget that parenthetical in the same change.

## UI layer (`internal/web`)

- **`app.js`**: new shared `renderPager(container, {total, limit, offset}, onPageChange)` helper —
  renders a page-size `<select>` (25/50/100/200), Prev/Next buttons (disabled at the edges,
  computed from `offset+limit < total` for Next / `offset > 0` for Prev), and an "X–Y of Z" label;
  calls `onPageChange({limit, offset})` on interaction. Changing the page-size `<select>` always
  resets `offset` to 0 before calling `onPageChange` — a user on page 3 of 100-row pages who
  switches to 25 returns to the top, not to item 200 of 25-row pages. One helper, two call sites
  (sessions, warnings drill-down) — reused, not duplicated.
  - **The `<select>` is driven by the applied `X-Limit`, not a private constant.** The select's
    value is initialised from — and re-synced to — the effective `limit` the server reports
    (`X-Limit`), the same value the "X–Y of Z" label and the Prev/Next disablement math are
    computed from (`offset+limit < total`). This is what keeps the pager self-consistent: because
    `X-Limit` reports the *applied* page size (plan:35-43), a select whose shown value disagreed
    with the fetched page size would render a label and edge-disablement that don't match the rows
    on screen. Both pager call sites therefore use a fixed **initial page size of 50** (one of the
    select's options), stored in the named `state` pager objects below, so the first fetch's
    applied `limit` equals the select's initial value by construction — the initial value and the
    first request's `?limit=` cannot diverge.
  - **Every pager-triggered fetch carries the same last-issued-wins guard the plan already gives
    `loadWarnings()`.** `renderPager`'s `onPageChange` is exactly the surface that invites rapid
    repeated triggers (double-clicking Next/Prev, or clicking the page-size `<select>` right after
    a Next click before the prior response has rendered). Two fetches can be in flight at once and
    the *slower, earlier* one can resolve last and overwrite the correct later page. So both
    pager-driven fetches get their own generation counter — `state.sessionsFetchSeq` for
    `loadSessions()` and `state.warningDetailFetchSeq` for the `showWarningDetail()` drill-down —
    following the identical increment-and-capture / discard-on-mismatch pattern described for
    `warningsFetchSeq` below (and mirroring the `request` SSE Promise queue at `app.js:679-683`).
    This is the same race class the plan licenses a real fix for in two other places; the pager is
    the new surface this ticket adds, so it gets the guard rather than an accepted-limitation note.
  - `fetchJSON` ([internal/web/app.js:104-112](../../internal/web/app.js#L104-L112)) does the
    `fetch()` + `!res.ok` error-message extraction and returns `res.json()`; the paginated fetch
    needs the *same* `ok`/error logic plus the three response headers. Do not duplicate that block:
    extract a shared `fetchRaw(url)` that performs the `fetch()` and the `!res.ok` error extraction
    and returns the checked `res`; `fetchJSON` becomes `(await fetchRaw(url)).json()`, and the new
    `fetchPage(url)` reads the three headers off the same `res` and returns `{items, total, limit,
    offset}`. (Chosen over the reverse — wrapping `fetchJSON` over `fetchPage` — because
    non-paginated endpoints such as `/api/stats` and `/api/warnings/summary` carry no pagination
    headers, so `fetchRaw` is the honest shared base and `fetchPage` stays pagination-specific.)
  - `loadSessions()` ([internal/web/app.js:390-410](../../internal/web/app.js#L390-L410)): reads its
    `{limit, offset}` from the named `state.sessionsPage` pager object (see the `state` bullet
    below), calls `/api/sessions?limit=&offset=` via
    `fetchPage`, renders the returned page, renders the pager above `#sessions-body`. Guard the
    fetch against out-of-order responses with a generation counter (`sessionsFetchSeq`, added to
    `state`): at the top, increment `state.sessionsFetchSeq` and capture `const seq`; after
    `fetchPage` resolves, skip the render if `seq !== state.sessionsFetchSeq`.
  - `loadWarnings()` / `renderWarningGroups()` ([internal/web/app.js:255-279](../../internal/web/app.js#L255-L279)):
    the grouped table now comes from `fetchJSON("/api/warnings/summary")` (no client-side
    `groupWarnings()` — that function is deleted, dead code removed). The SSE `warnings` handler
    at `app.js:702-709` currently calls `renderWarningGroups(state.warningsCache)` with a raw
    `Warning[]`; once `renderWarningGroups` is changed to consume `WarningGroup[]` from the
    summary endpoint, this call must also change. **Property casing (wire ↔ JS contract):**
    `store.WarningGroup` carries no `json` tags — the repo's established convention
    (`types.go:58-61`) — so the served JSON keys are the Go field names, capitalized:
    `renderWarningGroups` must read `g.Kind` / `g.Severity` / `g.Count` / `g.LastSeen`. It reads
    lowercase `g.kind`/`g.severity`/`g.count`/`g.lastSeen` today (`app.js:269-273`) only because it
    consumed the client-minted keys `groupWarnings()` produced (`app.js:246`), and this plan deletes
    that function — a literal swap-the-source edit renders every cell `undefined`. Do **not** add
    `json` tags to `WarningGroup` to paper over this; the no-tags convention is established and out
    of scope. (Class sweep: this is the *only* wire type the plan newly has `app.js` consume —
    `Warning` (`/api/warnings`), `Session` (`/api/sessions`), and the stats types are already
    consumed today and already read with their capitalized Go keys, and `index.html`/`style.css`
    add only markup/classes with no wire type crossing — so no other bullet carries this gap.)
    Implementation:
    - Add `warningsDebounceTimer: null` and `warningsFetchSeq: 0` to the `state` object
      (alongside `warnedIds`, `feedRowLimit`, etc. at `app.js:116-126`) — and add
      `sessionsFetchSeq: 0` and `warningDetailFetchSeq: 0` for the two pager-driven fetches. **The
      pager's own `limit`/`offset` are named on `state` too, not held as an anonymous module-level
      variable:** `sessionsPage: {limit: 50, offset: 0}` for `loadSessions()` and
      `warningDetailPage: {limit: 50, offset: 0}` for the `showWarningDetail()` drill-down. The
      initial `limit: 50` is the select's initial value (one of its four options), so the first
      fetch's applied `X-Limit` equals what the select shows — they cannot disagree. All
      other mutable UI state introduced or touched by this plan is named on `state`; the debounce
      timer, sequence counters, and pager state follow the same convention.
    - When a `warnings` SSE event arrives and the warnings tab is visible, use a
      `setTimeout`-based coalescing guard to schedule a `loadWarnings()` call — on each event,
      cancel any pending timer (`clearTimeout(state.warningsDebounceTimer)`) and restart it
      with a ~250 ms delay, storing the new timer id in `state.warningsDebounceTimer`. This
      ensures a rapid burst of N consecutive `warnings` events triggers a single
      `loadWarnings()` fetch rather than N back-to-back fetches, preventing visible flicker
      during multi-turn warning streaks.
    - In `showView("warnings")`, before calling `loadWarnings()`, always cancel any pending
      debounce timer: `clearTimeout(state.warningsDebounceTimer); state.warningsDebounceTimer =
      null` (option a — timer coordination). This prevents a *still-pending* timer from firing
      a duplicate `loadWarnings()` call immediately after `showView` has already issued one. It
      does **not** prevent two fetches from being in flight when the timer has already fired
      before the tab switch — `clearTimeout` is a no-op on a fired timer, so the in-progress
      fetch and the new tab-switch fetch overlap.
    - Give `loadWarnings()` a monotonic generation counter to close the in-flight fetch race
      (option b — mirrors the `request` SSE Promise queue at `app.js:679-683`): at the top of
      `loadWarnings()`, increment `state.warningsFetchSeq` and capture the value into a local
      `const seq`; after the `await fetchJSON(...)` call resolves, skip the render if
      `seq !== state.warningsFetchSeq` (a newer call superseded this one). This makes
      "last-issued wins" hold regardless of network timing or which call site (SSE debounce
      fire vs. tab switch) triggered which fetch. Together, options (a) and (b) give the correct
      invariant: no more than one pending debounce timer at any time (a), and the last-issued
      fetch always wins the render regardless of resolution order (b).
    The raw `state.warningsCache` accumulation and its `-feedRowLimit` slice (`app.js:705`) are
    removed with `groupWarnings()`, **including the now-unused `warningsCache: []` entry in the
    `state` object (`app.js:120`)** — once lines 258/259/285/287/705-707 are gone, that declaration
    is written and read by nothing, so it is dead code and goes too.
    - **Full displaced-site enumeration (the `warningsCache` / raw-`Warning[]` removal):** the
      sites that read the removed list are `app.js:258` (`state.warningsCache = list`), `259`
      (`for (const w of list) state.warnedIds.add(w.RequestID)`), `285` (`state.warningsCache
      .filter(...)` in `showWarningDetail`), `287` (`${matches.length}` — the title count derived
      from that filter), and `705-707` (the SSE handler's `state.warningsCache` reassignment and
      `-feedRowLimit` slice). Every one is addressed: `258` by deleting the assignment, `285`/`287`
      by the `showWarningDetail` rewrite above (server fetch + title from `X-Total-Count`'s
      `total`), and `705-707` by the SSE handler switch to a debounced `loadWarnings()`.
    - **`app.js:259` — accepted loss of the single-non-SSE `⚠` back-fill (deliberate, named side
      effect).** Line 259 is the `warnedIds` back-fill: today, opening the warnings tab fetches up
      to 1000 raw warnings and adds each `w.RequestID` to `state.warnedIds`, which is what badges
      an *already-rendered* feed row (`feedRowHTML`, `app.js:175`). After the switch,
      `loadWarnings()` fetches `/api/warnings/summary` (`WarningGroup[]` — `{Kind, Severity, Count,
      LastSeen}`, no `RequestID`), so this loop has no request-ID source and is **deleted, not
      replaced**. The consequence, stated plainly rather than left implicit: the only remaining
      writer of `state.warnedIds` is `markFeedRowWarned` (`app.js:198-205`), called from the SSE
      `warnings` branch (`app.js:703`) — so **a feed row for a request that was already warned
      *before* the page loaded shows no `⚠` badge until a fresh SSE warning arrives for that same
      request** (or the page is not reloaded). This is accepted for the same reason the `lens
      sessions` cap is (plan:73-78): the badge is cosmetic, and the alternative — keeping a
      `RequestID`-bearing source — means re-issuing the exact `fetchJSON("/api/warnings?limit=1000")`
      full-list fetch this ticket exists to eliminate, just to harvest IDs and then discard the rows.
      The summary endpoint carries counts, not IDs, so there is no cheaper request-ID source; the
      one-fetch-per-tab-open cost is not worth a stale-cosmetically-missing badge. Upgrade path if
      this ever matters: page the raw `/api/warnings` and pair it with `feedRowHTML`, or add a
      `warned_request_ids`-style field to the summary response. The `warnedIds` Set's other
      reader/writer (`markFeedRowWarned`, the SSE path) is untouched and keeps live warnings badging
      correctly.
  - `showWarningDetail()` ([internal/web/app.js:281-299](../../internal/web/app.js#L281-L299)):
    currently filters `state.warningsCache` (initialized at 1000 on load, but shrunk to the last
    200 warnings — `feedRowLimit` — after any live SSE warning event) in JS. Switches to calling
    `/api/warnings?kind=&severity=&limit=&offset=` via `fetchPage` directly against the server,
    driving its `{limit, offset}` from the named `state.warningDetailPage` pager object (below),
    with its own pager. Selecting a different kind/severity group resets the drill-down pager
    (`state.warningDetailPage.offset`) to 0. Guard the drill-down fetch with a generation counter
    (`warningDetailFetchSeq`, added to `state`), using the same increment-and-capture /
    skip-render-on-mismatch pattern as `loadSessions()`, so a slow response to an earlier pager
    click cannot overwrite a later one.
    With `warningsCache` removed (see above), this switch is required instead of optional.
    **Title count (displaced by the `warningsCache` removal).** The title line
    (`app.js:287`, `${matches.length} occurrence(s)`) reads the deleted `matches` — a filtered
    slice of `state.warningsCache` (`app.js:285`). It becomes the drill-down's **`total`** from
    `X-Total-Count` returned by `fetchPage`: `${kind} (${severity}) — ${total} occurrence(s)`. That
    is the correct global count for the group (the old `matches.length` was only ever the count
    within the ≤1000-row client cache), it is already in the response — no extra fetch — and it
    matches the pager's "X–Y of Z" denominator beneath it.
- **`index.html`** / **`style.css`**: add the pager's markup/classes (page-size select, prev/next
  buttons, count label) once, reused via the same class for both tables.

## Test strategy

- **Store** (`internal/store/store_test.go`, extending the existing `TestListRequestsPerformanceAndLimits`
  pattern at [internal/store/store_test.go:872-903](../../internal/store/store_test.go#L872-L903)):
  `Filter{Limit, Offset}` returns the correct slice window (page 2 doesn't repeat page 1's rows,
  last partial page returns the remainder, offset past the end returns empty not an error);
  `CountRequests`/`CountWarnings`/`CountSessions` match filtered row counts, independent of
  `Limit`/`Offset`; `ListSessions` now honors `Limit`/`Offset` and its default-limit clamp;
  `WarningSummary` groups correctly across the *full* dataset (including past the 1000-row cap
  that `ListWarnings` itself would apply) — this is the test that proves the "grouping now sees
  everything" fix. **Additionally**: seed duplicate `started_at`/`created_at` timestamps (matching
  the batch-insert pattern in the real consumer) and assert that no row appears on two pages and no
  row is missing across page boundaries, confirming the `id DESC` tiebreaker is effective. Extend
  this same duplicate-timestamp test to `ListSessions`: seed two sessions with identical
  `last_seen` values and assert neither appears on two pages nor is dropped across the page
  boundary, confirming the `last_seen DESC, id DESC` tiebreaker for sessions.
- **API** (`internal/api/api_test.go`): `X-Total-Count`/`X-Limit`/`X-Offset` present and correct on
  `/api/requests`, `/api/warnings`, `/api/sessions`. `X-Limit`'s assertion target is the
  **effective** page size, not the raw request: `?limit` **absent** → `X-Limit == store.DefaultLimit`
  (1000), `?limit=25` → `X-Limit == 25` (the absent-limit case is the regression guard for the
  `X-Limit: 0` bug). `X-Offset` is `0` when absent, the parsed value otherwise; `X-Total-Count` is
  the full filtered count, independent of the page window. `?offset=` combined with `?limit=`
  returns the right window; negative offset → 400 (mirrors existing negative-limit handling); new
  `/api/warnings/summary` route returns grouped counts matching a hand-seeded fixture with >1000
  warnings of one kind (regression test for the undercount bug), and the groups are ordered
  highest-count-first.
- **UI**: no JS test harness in this repo (confirmed via `docs/context/testing-and-quality.md`) —
  three steps in order per the project's documented web-change verification process
  (`docs/context/testing-and-quality.md:18-24`):
  1. `node --check internal/web/app.js` — syntax gate before touching a browser; catches
     stray errors that `go build`/`go vet` cannot (JS, not Go).
  2. Build fresh (`go build ./cmd/lens && ./lens serve` or `go run ./cmd/lens serve`), then
     assert the served asset contains the new code: e.g.
     `curl -s http://localhost:<port>/app.js | grep -q renderPager && echo ok` (and likewise
     for `fetchPage`). This guards against the `go:embed` stale-binary trap — a binary built
     before the edit keeps serving old assets.
  3. Manual in-browser check: seed >1 page of sessions and >1 page of warnings of one kind;
     verify page-size select changes row count, Next/Prev move correctly and disable at the
     edges, the warnings summary counts match the true total, a new warning arriving via SSE
     updates the summary table in real time without a tab-switch, and rapid back-to-back SSE
     warning events produce a single summary re-fetch (debounce fires once after the burst). Also
     verify the generation-counter guard: throttle the network (DevTools Network panel → "Slow 3G"
     or equivalent) or add a brief temporary delay to one `fetchJSON` call; trigger an SSE warning
     burst while on the warnings tab (starts a slow fetch A via the debounce timer), then switch
     away and immediately back to the warnings tab before A resolves (starts fetch B); confirm the
     rendered summary reflects B's data, and that A's (possibly stale) data does not overwrite it
     when A finally resolves. Also confirm the pager guards: with the same throttle, click Next
     twice in quick succession (or click the page-size `<select>` immediately after Next) on the
     sessions tab and on a warnings drill-down, and verify the table settles on the later click's
     page — the earlier, slower response must not overwrite it.

## Verification

1. `go build ./...`, `go vet ./...`.
2. `go test ./internal/store/... ./internal/api/...` — new pagination tests above, plus the full
   existing suite (must still pass, including `TestListRequestsPerformanceAndLimits`'s unchanged
   assertions).
3. `go test ./...` (full suite — catches the mechanical `ListSessions` call-site updates in
   `internal/cli` and `internal/consumer` tests).
4. Manual dashboard check per the UI test strategy above.
5. Run the context-docs refresh skill (REFRESH mode, scoped to this ticket) after implementation,
   to update `docs/context/api-surface.md` (new `offset` param on three routes, three new response
   headers, the new `/api/warnings/summary` route, and shifted line citations) and
   `docs/context/data-model.md` (new `idx_warnings_kind_severity_created_at` index on the
   `warnings` table row **and** the new `WarningGroup` type added to the types.go enumeration at
   `data-model.md:18-20`, which lists that file's Go types exhaustively — `Request`, `Session`,
   `Warning`, `Filter`, `Summary`, `ModelStat`, `DayStat`, `CostSourceStat` — and so goes stale on
   the plan's own new exported type) and `docs/context/testing-and-quality.md` (the `groupWarnings`/
   `groupSessionWarnings` reference at line 15 names a function deleted by this plan). This
   follows the same pattern as the GI-9 refresh recorded at `docs/context/INDEX.md:58-64`.

## Change History

### v11 (round-10 triage)
- **F10.1** (JUSTIFIED): The displaced-site enumeration for the `warningsCache`/raw-`Warning[]`
  removal named `258/285/705-707` but omitted `app.js:259` (the `warnedIds` back-fill loop reading
  `w.RequestID`) and `app.js:287` (the `showWarningDetail` title count reading `matches`). The
  plan now enumerates all of `258/259/285/287/705-707` and states each one's resolution. For `259`,
  the resolution is **accepted loss** of the single-non-SSE `⚠` back-fill — named explicitly,
  with the rationale (no `RequestID` in `WarningGroup`; the alternative is re-issuing the full
  1000-row `/api/warnings` fetch this ticket removes) and the user-visible symptom (a feed row for
  a request warned before page load shows no `⚠` until a fresh SSE warning arrives for it), in the
  same "accepted side effect" style as the `lens sessions` cap. For `287`, the title count becomes
  the drill-down `total` from `X-Total-Count`.
- **F10.2** (JUSTIFIED): Verification step 5's `data-model.md` refresh clause named only the new
  index; since the plan adds the exported `store.WarningGroup` type and `data-model.md:18-20`
  enumerates `types.go`'s types exhaustively, the clause now also names adding `WarningGroup` to
  that enumeration.
- **F10.3** (JUSTIFIED): The pager's `limit`/`offset` were an unnamed "module-level pager state"
  with no specified initial page size. The plan now names them on `state` —
  `sessionsPage: {limit: 50, offset: 0}` and `warningDetailPage: {limit: 50, offset: 0}` — matching
  the convention the plan already follows for `sessionsFetchSeq`/`warningDetailFetchSeq`/
  `warningsDebounceTimer`, and pins the initial page size to 50 (a select option). The
  `renderPager` bullet now states the `<select>` is initialised from and re-synced to the applied
  `X-Limit`, so the select's initial value and the first fetch's applied limit cannot disagree
  (they drive the "X–Y of Z" label and Prev/Next disablement).

### v10 (round-9 triage)
- **F9.1** (JUSTIFIED): The `types.go:102-104` rewrite the plan instructed named only
  `ListRequests, ListWarnings, ListSessions` — but the plan *also* adds `WarningSummary` as a
  fourth `Filter` reader and the three `Count*` variants, so the comment the plan itself writes is
  stale the moment the plan's own additions land. The `Filter` bullet now names all four readers
  (`ListRequests, ListWarnings, ListSessions, WarningSummary`) plus the `Count*` variants and gives
  the resulting text, with the *governing principle* stated (a comment the plan writes must not be
  invalidated by the plan's own additions). Per the human's direction to close the class by
  enumeration, a fresh codebase sweep was run for any *other* comment this plan's additions make
  stale: the only remaining candidate, `internal/api/api.go:234` ("ListWarnings filters by
  Kind/Severity/Since only"), was checked and **excluded** — its parenthetical enumerates the
  *predicate* (narrowing) fields, not the windowing fields, and already omits `Limit`, so adding
  the `Offset` window does not make it false (same reasoning as the `store.go:304-305` exclusion).
  No other member found; the class is closed.
- **F9.2** (JUSTIFIED): `renderWarningGroups` switching to consume `WarningGroup[]` from
  `/api/warnings/summary` changes the property reads from lowercase (the deleted `groupWarnings()`
  minted `kind`/`severity`/`count`/`lastSeen`) to the capitalized wire keys, because
  `store.WarningGroup` carries no `json` tags (repo convention, `types.go:58-61`). The
  `renderWarningGroups` bullet now states the reads explicitly as `g.Kind`/`g.Severity`/`g.Count`/
  `g.LastSeen` and forbids papering over it with `json` tags. Per the human's direction to fix the
  class, the plan was swept for every *other* wire type it newly has `app.js` consume:
  `WarningGroup` is the only one — `Warning`, `Session`, and the stats types are already consumed
  today with capitalized reads, and `index.html`/`style.css` add no wire crossing. Recorded in the
  bullet so no later round re-opens it.

### v9 (round-8 triage)
- **F8.1** (JUSTIFIED): `X-Limit` reported the *requested* limit (`0` when `?limit` absent) while
  the store applied `DefaultLimit` (1000) — an inconsistent header that stalls any consumer doing
  `nextOffset = offset + X-Limit`. The `writePageHeaders` bullet now states `limit` is the
  **effective** value and each handler resolves the clamp (`effLimit := limit; if effLimit <= 0 {
  effLimit = store.DefaultLimit }`) before calling it; `listRequests`/`listWarnings`/`listSessions`
  were updated to pass `effLimit`. The Design decisions "Headers, not a response envelope" paragraph
  now states headers report effective values, with the `?limit` absent → `X-Limit: 1000, not 0`
  worked example. The API test strategy's `X-Limit` assertion now has explicit targets
  (absent → `store.DefaultLimit`, `?limit=25` → 25).
- **F8.2** (JUSTIFIED): Added the stale `internal/store/store.go:543` comment ("returns every
  session" → "returns a page of sessions") to the enumerated stale-comment list in the `Filter`
  bullet. Per the human's direction to sweep the class rather than patch one instance, a fresh
  search found one more of the same class: `internal/store/store.go:683` (the `ListWarnings` method
  comment enumerates the honored `Filter` fields `(Kind/Severity/Since/Limit)` and must gain
  `/Offset`) — folded into the same bullet as the **fourth** member. Also folded in the F3.4
  sibling `internal/api/api.go:662` (the `sessionDetail.Warnings` comment's dangling
  `(see ... New doc comment)` pointer) as the fifth. `ListRequests`'s method comment
  (`store.go:304-305`) enumerates no fields, so it is deliberately not changed — the class is now
  closed.

### v8 (round-7 triage)
- **F7.1** (JUSTIFIED): `ListSessions` becoming a third `Filter` reader makes two doc comments
  stale. Added an explicit instruction to the `Filter` bullet to update
  `internal/store/types.go:102-104` (add `ListSessions` to the narrowing list; state it ignores
  everything but `Limit`/`Offset`) and `internal/store/store.go:30-32` (add `ListSessions` to the
  `DefaultLimit` enumeration).
- **F7.2** (JUSTIFIED): Extended the `warningsCache`-removal sentence to include the now-dead
  `warningsCache: []` declaration at `app.js:120`, and dropped `warningsCache` from the `state`
  enumeration in the new-field sub-bullet.
- **F7.3** (JUSTIFIED — option (a) per human decision): Added generation-counter guards to both
  pager-driven fetches — `state.sessionsFetchSeq` in `loadSessions()` and
  `state.warningDetailFetchSeq` in `showWarningDetail()`'s drill-down — using the same
  increment-and-capture / discard-on-mismatch pattern as `warningsFetchSeq`. Stated the invariant
  once in the `renderPager` bullet (the pager is the rapid-trigger surface) and extended the UI
  test strategy's race check to cover rapid Next/Next and page-size-after-Next clicks.
- **F7.4** (JUSTIFIED): Replaced the vague "sibling `fetchPage`" wording with an explicit shared
  base: extract `fetchRaw(url)` (fetch + `!res.ok` error extraction, returns checked `res`);
  `fetchJSON` becomes `(await fetchRaw(url)).json()` and `fetchPage` reads the headers off the same
  `res`. Chose this over `fetchJSON`-over-`fetchPage` because non-paginated endpoints
  (`/api/stats`, `/api/warnings/summary`) carry no pagination headers.

### v7 (round-6 triage)
- **F6.1** (JUSTIFIED): Added `id DESC` secondary tiebreaker to `ListSessions`'s `ORDER BY`
  clause (`ORDER BY last_seen DESC, id DESC`), extending the same fix already applied to
  `ListRequests`/`ListWarnings` in F1.3. Added rationale in the Store layer bullet. Extended the
  duplicate-timestamp test in the Test strategy to also seed two sessions with identical
  `last_seen` and assert no row appears on two pages nor is dropped.
- **F6.2** (JUSTIFIED): Added Verification step 5 to run the context-docs refresh skill (REFRESH
  mode, scoped to this ticket) after implementation, covering the stale `api-surface.md` (no
  `offset`, no three new response headers, no `/api/warnings/summary` route) and
  `data-model.md` (warnings table missing `idx_warnings_kind_severity_created_at` index row).
- **F6.3** (JUSTIFIED): Folded into the same Verification step 5 added for F6.2 —
  `docs/context/testing-and-quality.md:15` names `groupWarnings`/`groupSessionWarnings`; once
  `groupWarnings()` is deleted, that reference becomes stale. The refresh step now explicitly
  names `testing-and-quality.md` as a target.

### v6 (round-5 triage)
- **F5.1** (JUSTIFIED): Added generation-counter race verification sub-step to UI test strategy
  step 3: throttle network / add temporary delay, trigger SSE burst on warnings tab (starts slow
  fetch A), switch away and back before A resolves (starts fetch B), confirm B's data wins and A's
  does not overwrite it.
- **F5.2** (JUSTIFIED): Added `, kind, severity` as a secondary sort key to `WarningSummary`'s
  `ORDER BY COUNT(*) DESC` in both the Store layer bullet and the Design decisions SQL snippet, so
  tie order is deterministic across re-fetches.
- **F5.3** (JUSTIFIED): Corrected the route-registration citation in the API layer's
  `warningsSummary` handler bullet from `api.go:66-81` to `api.go:79-88` (lines 79-88 are the
  actual `mux.HandleFunc`/`mux.Handle` calls; lines 66-78 are the doc comment and function
  signature).

### v5 (round-4 triage)
- **F4.1** (JUSTIFIED): Added generation-counter guard (option b) to `loadWarnings()`:
  `state.warningsFetchSeq` incremented and captured before each `fetchJSON` call; response
  discarded if the captured value no longer equals `state.warningsFetchSeq` (superseded by a
  newer call). Added `warningsFetchSeq: 0` to the `state` object alongside
  `warningsDebounceTimer: null`. Corrected the invariant sentence: option (a) guarantees no
  more than one pending debounce timer; option (b) guarantees last-issued fetch wins the render.
  Removed the false "at most one fetch in flight at any time" claim, which did not hold because
  `clearTimeout` is a no-op on an already-fired timer.

### v4 (round-3 triage)
- **F3.1** (JUSTIFIED): Expanded the SSE `warnings` debounce implementation to require that
  `showView("warnings")` always cancels any pending debounce timer before calling
  `loadWarnings()`, establishing a single-flight invariant (at most one `loadWarnings()` in
  flight at any time). Added rationale citing the `request` SSE Promise-queue precedent at
  `app.js:679-683`.
- **F3.2** (JUSTIFIED): Added `warningsDebounceTimer: null` to the `state` object description
  so the timer holder is explicitly named, consistent with the plan's naming convention for all
  other mutable UI state it introduces or touches.
- **F3.3** (JUSTIFIED): Added to the `renderPager` bullet that changing the page-size `<select>`
  always resets `offset` to 0 before calling `onPageChange`.
- **F3.4** (JUSTIFIED): Added a doc-comment update note to the API layer section: the grouping
  parenthetical in `internal/api/api.go:66-68` must be removed or updated since grouping now
  lives in `store.WarningSummary`, not in the dashboard JS.

### v3 (round-2 triage)
- **F2.1** (JUSTIFIED): Replaced "New schema migration (next version in `internal/store/schema.sql`)"
  header with "New index, appended to `internal/store/schema.sql`" and added an explicit note that
  the index is applied idempotently via `Open()` with no migration framework, consistent with
  `schema.sql:1-2`.
- **F2.2** (JUSTIFIED): Removed the false "CSV/export path are untouched" clause; replaced with a
  clarifying note that `internal/api` has no CSV/export endpoint and `lens export` is CLI-only.
- **F2.3** (JUSTIFIED): Expanded the UI test strategy bullet into the project's documented
  three-step web-change process (`node --check` syntax gate → served-asset assertion → manual
  eyeball), per `docs/context/testing-and-quality.md:18-24`.
- **F2.4** (JUSTIFIED): Added one sentence to `showWarningDetail()` bullet: selecting a different
  kind/severity group resets the drill-down pager to `offset: 0`.
- **F2.5** (OVERRIDE): Updated the SSE `warnings` handler implementation sentence to specify a
  concrete `setTimeout`-based coalescing guard (~250 ms) around the `loadWarnings()` call, with
  `clearTimeout` on each new event so a burst of N events triggers one refetch, not N.

### v2 (round-1 triage)
- **F1.1** (JUSTIFIED): Added SSE handler migration to Store/UI sections — `renderWarningGroups`
  switch to `WarningGroup[]` requires the SSE `warnings` handler to call `loadWarnings()` instead
  of passing raw `Warning[]`; `warningsCache` accumulation removed along with `groupWarnings()`.
- **F1.2** (JUSTIFIED): Added covering index `idx_warnings_kind_severity_created_at` to schema
  migration; qualified the "unbounded but safe" claim with the query-cost context.
- **F1.3** (JUSTIFIED): Added `id DESC` tiebreaker to both `ORDER BY` clauses (requests and
  warnings); added duplicate-timestamp test to the test strategy.
- **F1.4** (OVERRIDE): Added accepted-limitation note for `X-Total-Count` staleness per conductor
  override — no transactional consistency added.
- **F1.5** (JUSTIFIED): Expanded scope-cuts section to explicitly call out the silent `lens
  sessions` cap-at-1000 side effect and rationale for accepting it.
- **F1.6** (JUSTIFIED): Corrected `showWarningDetail` description from "capped-at-1000" to
  "initialized at 1000 on load, shrunk to last 200 (feedRowLimit) after any SSE warning event."
- **F1.7** (JUSTIFIED): Fixed "three call sites" → "two call sites" in `renderPager` bullet.
- **F1.8** (JUSTIFIED): Added `ORDER BY COUNT(*) DESC` to `WarningSummary` SQL; added
  highest-count-first ordering assertion to API test strategy.
