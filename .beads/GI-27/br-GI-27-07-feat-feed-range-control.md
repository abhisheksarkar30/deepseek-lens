### Bead 7: `feat` Feed Range control — pager, live-paused banner, Clear

- **Bead ID**: br-GI-27-07
- **Priority**: P1 (high)
- **Original Estimate**: 2h
- **Dependencies**: br-GI-27-05, br-GI-27-06
- **Blocks**: None

**Description**:

F/§11B. deepseek-lens's request list **is** the live Feed tab (`app.js:316`, `fetchJSON("/api/requests?limit=50")`), which has no window control. Add a compact **Range** control: From/To `datetime-local`, a Clear button, paging, and a paused-live banner.

**1. The Range control** (`internal/web/index.html` + `app.js`). From/To `datetime-local` inputs plus a Clear button, built with the shared `hourBound` helper (bead 06) — **not** a second offset implementation. The zone is the "Days in" zone (bead 06): fixed offsets pass the numeric offset, Local passes `'local'`.

**2. Behaviour when a range is set.** The feed **stops prepending SSE rows** and shows the matching page(s) with the existing pager pattern and `X-Total-Count` (from `internal/api`, bead 05 — the API's `Filter.Until` + the shared `requestWhere` make the count honest). A banner says **"range view — live paused"**. **Clear resumes live** (SSE prepending restarts).
- A row arriving over SSE while a range is set is **not** shown.

**3. The window mapping** (same table as bead 06, applied to `/api/requests`):

| from | to | Filter sent |
|---|---|---|
| set | set | `[from-hour start, to-hour end)`; if `to < from` show an inline message and apply **no** window |
| set | empty | `since` only |
| empty | set | `until` only |
| empty | empty | no window |

**Files shared with bead 06:** this bead and bead 06 both edit `internal/web/app.js` and `internal/web/index.html`. The dependency edge (07 depends on 06) makes them sequential — implement bead 06 first, then this bead, so the two never hold conflicting claims on the same regions.

**Rationale**:

F/§11B. The Feed is the only request-list surface; without a range it can show only the newest 50 rows and cannot answer "what happened between two hours". The pager + `X-Total-Count` reuse the pattern already in the repo; the paused-live banner exists because a live-prepending feed and a fixed window are mutually exclusive — silently mixing them would make the range view lie about what it shows.

**Outcome Definition**:

- The Feed shows a Range control; a set range replaces live prepending with a paged range view and a "range view — live paused" banner; Clear resumes live.
- The Feed range and the Stats range for the same two instants send **identical `since`/`until` strings** (query-string equality is row equality — same store filter).
- `X-Total-Count` equals the row count of the range, and the pager steps through it.
- `to < from` shows an inline message and applies no window.
- An SSE row arriving while a range is set is not shown.
- `node --check internal/web/app.js` passes.

**Test Specifications**:

- **Web asset guard**: the Feed range control exists; the banner string is present; the range view uses `hourBound` (no second offset builder, no `toISOString`).
- **Cross-tab equality (manual/recorded)**: trigger a Feed range and a Stats range for the same two instants and assert the sent `since`/`until` query strings are byte-identical.
- **Pager**: `X-Total-Count` matches the rendered rows for a fixture range; stepping pages requests the next window.
- **Live-pause (manual)**: with a range set, no new SSE row appears; Clear restores prepending.
- DST/rollover cases are manual (no JS runtime).

**Files to Touch**:
- `internal/web/app.js` (modify — Feed Range control, pager wiring, live-paused state, use `hourBound`)
- `internal/web/index.html` (modify — Range/From/To/Clear markup and the banner)
