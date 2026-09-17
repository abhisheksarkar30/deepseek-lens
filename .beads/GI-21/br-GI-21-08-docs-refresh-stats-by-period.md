### Bead 8: Refresh the context docs for the `by_period` rename, the new `granularity`/`until` params, and the drifted route citations

- **Bead ID**: br-GI-21-08
- **Priority**: P2 (low — documentation drift, no runtime effect)
- **Dependencies**: br-GI-21-01, br-GI-21-02, br-GI-21-03, br-GI-21-04 (the store/API/CLI renames these docs describe); listed last, after the web beads (br-GI-21-05, -06, -07), so the ticket is settled before the docs describe it
- **Blocks**: None

**Description**:

The plan's Phase 5.6 / R5 refresh. Two context docs name an API this ticket renames, and the
`api-surface.md` route table carries four pre-existing stale line citations that must be recomputed —
**after** the implementation edits land, not hard-coded now (`stats()` grows during this very change).

1. **`docs/context/api-surface.md:17`** — the `/api/stats` route row. Update the Summary cell:
   "summary, by-model, **by-day**, by-cost-source; query param `since`" becomes "summary, by-model,
   **by-period**, by-cost-source; query params `since`, **`until`**, **`granularity`**
   (`hour`|`day`|`week`|`month`, default `day`)". The response type is still `statsResponse`, whose
   `by_day` field is now `by_period` (D3).

2. **Recompute the four stale citations in the same route table.** All four are off by the same
   **+72-line** drift from one historical edit, and all four are adjacent rows in one already-opened
   file:

   | Row | Citation today (`api-surface.md`) | Actual handler line today (`internal/api/api.go`) |
   |---|---|---|
   | `/api/stats` | `api.go:652-700` | `stats()` at `api.go:724` |
   | `/api/warnings/summary` | `api.go:701-718` | `warningsSummary()` at `api.go:773` |
   | `/api/warnings` | `api.go:719-754` | `listWarnings()` at `api.go:791` |
   | `/api/sessions` | `api.go:755-803` | `listSessions()` at `api.go:827` |

   The right-hand column is the **pre-edit positive control**, not the value to write. Recompute each
   range from the edited `api.go` (locate the handler with `grep -n 'func (a \*api) <name>'`) and cite
   the range the handler actually occupies once this ticket's edits have landed — the `/api/stats`
   range in particular moves further as `stats()` gains the `granularity`/`until` parsing. Rows
   `/api/warnings/summary`, `/api/warnings`, and `/api/sessions` handlers are not edited by this
   ticket, but the +72 drift is in the *citation*, not the code, so they are corrected here while the
   file is open (F6.4).

3. **`docs/context/data-model.md:25`** — the query-only shapes list reads
   `Filter`, `Summary`, `ModelStat`, **`DayStat`**, `CostSourceStat`, `WarningGroup`; change `DayStat`
   to **`PeriodStat`** (D1). No `DayStat`/`by_day`/`StatsByDay` reference should remain in either
   context doc.

4. **Restamp `docs/context/INDEX.md:58-60`** — the "Last generated / refreshed" entry. Both prior
   refreshes (the GI-15 and GI-17 entries above it) set this stamp; a refresh that does not restamp is
   indistinguishable from a stale one. New entry: `2026-09-17, REFRESH mode, scoped to the GI-21
   stats-by-period story`, naming the `by_day`→`by_period` rename, the new `granularity`/`until`
   params, and the corrected route citations.

5. **Do NOT rewrite `docs/acceptance.md:311`.** That line is a captured `GET /api/stats` payload
   containing `"by_day":[{... "Day":"2026-09-15" ...}]`. The document's own header
   (`acceptance.md:7-9`) states it is "a recording of a run that actually happened. Every block below
   is verbatim output, pasted as captured." It is a historical record of that run, not a live claim
   about the current contract — the same frozen-record rule GI-17's br-GI-11 applied to prior tickets'
   plans. The `by_day`/`DayStat` searches will hit it; it stays.

**Rationale**:

R5 — the context docs go stale the moment this lands: they name `by_day`/`DayStat`/`StatsByDay`, which
no longer exist, and they omit the two new query params. The citation recompute rides along because the
four rows share one offset and one file, so fixing them now costs nothing extra (F6.4). This is the
same deliverable shape as GI-17's br-GI-11: the docs are the artifact, and the verification is a
search whose claim-bearing hit set must be empty — with the frozen `acceptance.md` transcript named
in advance so it is not mistaken for a missed hit.

**Outcome Definition**:

- `docs/context/api-surface.md`'s `/api/stats` row names `by-period`, `until`, and `granularity`.
- The four route-table citations point at the line ranges their handlers actually occupy after this
  ticket's edits (verified against `internal/api/api.go`, not against the old numbers).
- `docs/context/data-model.md` names `PeriodStat`, not `DayStat`.
- `docs/context/INDEX.md`'s "Last generated / refreshed" entry is dated 2026-09-17 and scoped to the
  GI-21 story.
- A search for `DayStat`/`StatsByDay`/`by_day`/`by-day` over `docs/` returns only the frozen
  `acceptance.md` transcript (and, if `--hidden` is used, this ticket's own beads quoting the
  patterns).
- `docs/acceptance.md` is unchanged.

**Test Specifications**:

Documentation, so the verification is the searches themselves, shown rather than asserted (the
br-GI-17-11 discipline — a search that returns nothing *before* the change is a broken search, not a
clean tree):

1. **Before** editing, run the search and record hits — this is the positive control.
   Positive controls today: `docs/context/data-model.md:25` (`DayStat`) and
   `docs/context/api-surface.md:17` (`by-day`):
   ```
   rg -n -e 'DayStat' -e 'StatsByDay' -e 'by_day' -e 'by-day' docs/
   ```
2. Confirm the pattern uses `|`-free alternation or separate `-e` terms, never `\|` (Rust regex treats
   `\|` as a literal pipe and silently matches nothing, which is how a gate becomes decoration).
3. Edit.
4. Re-run. Classify every remaining hit as **claim-bearing** (must be empty), the **frozen
   `acceptance.md:311` transcript** (stays), or **the pattern quoting itself** (if `--hidden` pulls in
   this bead). Any claim-bearing hit that survives is this bead's to fix.
5. Assert the route citations resolve: for each of the four rows, `grep -n 'func (a \*api)
   stats\|warningsSummary\|listWarnings\|listSessions' internal/api/api.go` and confirm the cited
   range brackets the handler's actual line.
6. Grep `docs/context/INDEX.md` for the refresh stamp and assert it is today's date.

**Files to Touch**:
- `docs/context/api-surface.md` (modify — the `/api/stats` route row and the four recomputed line citations)
- `docs/context/data-model.md` (modify — `DayStat` → `PeriodStat`)
- `docs/context/INDEX.md` (modify — the "Last generated / refreshed" stamp)
