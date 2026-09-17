### Bead 8: Refresh the context docs for the `by_period` rename, the new `granularity`/`until` params, and the drifted route citations

- **Bead ID**: br-GI-21-08
- **Priority**: P2 (low — documentation drift, no runtime effect)
- **Dependencies**: br-GI-21-01, br-GI-21-02, br-GI-21-03, br-GI-21-04 (the store/API/CLI renames these docs describe); listed last, after the web beads (br-GI-21-05, -06, -07), so the ticket is settled before the docs describe it
- **Blocks**: None

**Description**:

The plan's Phase 5.6 / R5 refresh. Two context docs name an API this ticket renames, and the
`api-surface.md` route table carries pre-existing stale line citations that must be recomputed —
**after** the implementation edits land, not hard-coded now (`stats()` grows during this very change).
The plan counts four such citations; the real class is twenty-one (see item 2).

1. **`docs/context/api-surface.md:17`** — the `/api/stats` route row. Update the Summary cell:
   "summary, by-model, **by-day**, by-cost-source; query param `since`" becomes "summary, by-model,
   **by-period**, by-cost-source; query params `since`, **`until`**, **`granularity`**
   (`hour` / `day` / `week` / `month`, default `day`)". The response type is still `statsResponse`,
   whose `by_day` field is now `by_period` (D3). **Write the granularity list without `|` separators
   in the actual cell** — that row is a GFM table row, where a bare `|` splits the cell and silently
   corrupts the table's column count; the bead writes it with slashes for the same reason.

2. **Recompute the drifted `api.go` citations in `api-surface.md`.** The plan names four rows off by a
   uniform +72; that estimate is wrong on both counts. Every `internal/api/api.go` citation in the
   file has drifted — twenty-one of them, by +85 to +113, the spread because the edits that shifted
   them move only the rows below them. Fix the class, not the four.

   The four rows the plan names, as the pre-edit positive control (*before* values, not what to write):

   | Row | Citation before this ticket (`api-surface.md`) |
   |---|---|
   | `/api/stats` | `api.go:652-700` |
   | `/api/warnings/summary` | `api.go:701-718` |
   | `/api/warnings` | `api.go:719-754` |
   | `/api/sessions` | `api.go:755-803` |

   Enumerating the class adds the route-table rows for `/api/requests`, `/api/requests/{id}`,
   `/api/requests/{id}/replay`, `/api/sessions/{id}`, `/api/stream` and `/api/health`, plus the
   non-route citations in the same file: the `mux.Handle*` block, `methodGet`, `effectiveLimit` /
   `writePageHeaders`, `replayOriginReject`, the `!replayEnabled` guard, the `healthResponse`
   payload, and the three "same `store.Filter`" references.

   Never write the old number plus an offset — brace-match the handler or region each citation names
   in the edited file and cite the range it actually occupies. The `/api/stats` range in particular
   moves further than its neighbours as `stats()` gains the `granularity`/`until` parsing. Verify
   each new range brackets a known anchor line, so a plausible-looking number is distinguished from a
   correct one.

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
drifted citations are all in one already-open file, so fixing the whole class now costs nothing extra
(F6.4) — and fixing only the four the plan happened to notice would leave seventeen known-stale
citations in a file opened specifically to correct citations. This is the
same deliverable shape as GI-17's br-GI-11: the docs are the artifact, and the verification is a
search whose claim-bearing hit set must be empty — with the frozen `acceptance.md` transcript named
in advance so it is not mistaken for a missed hit.

**Outcome Definition**:

- `docs/context/api-surface.md`'s `/api/stats` row names `by-period`, `until`, and `granularity`.
- Every `internal/api/api.go` citation in `api-surface.md` points at the line range its handler or
  region actually occupies after this ticket's edits, verified against the edited file rather than
  against the old numbers. The plan named four; the class is twenty-one.
- `docs/context/data-model.md` names `PeriodStat`, not `DayStat`.
- `docs/context/INDEX.md`'s "Last generated / refreshed" entry is dated 2026-09-17 and scoped to the
  GI-21 story.
- A search for `DayStat`/`StatsByDay`/`by_day`/`by-day` over `docs/context/` — the tree this refresh
  owns — returns no **claim-bearing** hit. The one expected exception is the refresh stamp itself:
  item 4 requires that stamp to name the `by_day`→`by_period` rename, so its hits are mandated, not
  missed. The claim is scoped to `docs/context/` and deliberately not `docs/`, because a `docs/`-wide
  search cannot be clean: this ticket's own plan, GI-15's converged plan, and the frozen
  `acceptance.md` transcript all name the old identifiers legitimately, and this bead's item 4
  mandates writing one of them into a file the refresh edits. An earlier wording of this bullet said
  `docs/` and named only `acceptance.md`; that was unsatisfiable alongside item 4 and was corrected
  here during the impl cross-review (round 1, F1.1, category `spec`).
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
4. Re-run, scoped to `docs/context/`. Classify every remaining hit as **claim-bearing** (must be
   empty) or **the refresh stamp naming the rename** (item 4 mandates it; expected). A `docs/`-wide
   run additionally hits the frozen `acceptance.md:311` transcript, this ticket's own plan, and
   GI-15's converged plan — all of which name the old identifiers legitimately and are out of scope
   for this refresh.
5. Assert every citation resolves: for each citation in the file, confirm the cited range brackets
   the handler or region it names in `internal/api/api.go`. Use separate `-e` terms rather than a
   `\|` alternation — under ripgrep `\|` is a literal pipe and matches nothing, so the check would
   silently pass on an empty result (the same trap step 2 names; an earlier wording of this step
   used it).
6. Grep `docs/context/INDEX.md` for the refresh stamp and assert it is today's date.

**Files to Touch**:
- `docs/context/api-surface.md` (modify — the `/api/stats` route row and every drifted `api.go` citation)
- `docs/context/data-model.md` (modify — `DayStat` → `PeriodStat`)
- `docs/context/INDEX.md` (modify — the "Last generated / refreshed" stamp)
