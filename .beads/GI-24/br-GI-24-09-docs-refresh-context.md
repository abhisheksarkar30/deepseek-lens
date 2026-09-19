### Bead 9: `docs` refresh `docs/context/`

- **Bead ID**: br-GI-24-09
- **Priority**: P1 (high)
- **Original Estimate**: 1.5h
- **Dependencies**: br-GI-24-06, br-GI-24-07, br-GI-24-08
- **Blocks**: None

**Description**:

The Phase 5.6 context refresh (`CLAUDE.md`: "Update `docs/context/` in the same PR"). The story changes the pricing input, a response shape, two config keys, and the doctor surface, so the docs go stale the moment it lands.

**The enumeration is six files.** Take the list from **§6's `docs/context/*.md` row** and nowhere else: **6 files + the `INDEX.md` stamp**. (When this bead was written, §9 item 4 carried a second copy of that list naming only four — omitting `data-model.md` and `workflows.md`, both of which §6 requires. Plan v9 removed the copy rather than re-syncing it, so §6 is now the only place the list lives. Do not trust a restatement of it, including this one: work from §6.)

1. **`docs/context/data-model.md`** — note that **no** schema change occurred (the §3 constraint: `sessions` gains no column), but the `GET /api/sessions/{id}` response shape does: `sessionDetail` now carries a `peak` object. Add it to the query-only shapes list if that list names response shapes (the same list GI-21's br-GI-21-08 corrected for `DayStat`).

2. **`docs/context/api-surface.md`** — the `/api/sessions/{id}` route row gains the `peak` field; the `/api/prices` row (GET and POST) gains `off_peak_dates` and `work_dates`. **Recompute any `internal/api/api.go` citation** these edits shift, verified against the edited file rather than the old numbers (the br-GI-21-08 discipline: citations drift by a spread, not a uniform offset — brace-match the handler, do not add an offset).

3. **`docs/context/build-and-run.md`** — two new env vars: `LENS_OFF_PEAK_DATES` and `LENS_WORK_DATES`, with their `config.toml` keys and flags.

4. **`docs/context/workflows.md`** — the cost step's new input: `pricing.Compute` now takes a `Calendar`, so the pipeline order is unchanged but the cost step reads the calendar field.

5. **`docs/context/testing-and-quality.md`** — the paragraph at `:66-71` names `pricing.IsPeak`, which br-GI-24-02 **removed**, and describes the weekday/weekend and UTC-boundary peak tests br-GI-24-02 rewrote as `Calendar` methods. Update the symbol names and point at `internal/pricing/calendar_test.go` for the holiday/precedence/keying cases, `pricing_test.go` for the `Compute`-level ones.

6. **`docs/context/cli-and-tooling.md`** — the `doctor` row at `:10` enumerates the PASS/WARN/FAIL checks and the printed config, both of which this story changes: br-GI-24-07 adds the `peak_calendar` coverage check and the `off_peak_dates` / `work_dates` printed rows.

7. **`docs/context/INDEX.md`** — restamp the "Last generated / refreshed" entry, the way the GI-15, GI-17 and GI-21 entries above it did (a refresh that does not restamp is indistinguishable from a stale one). The plan does not name this file; every prior Phase 5.6 refresh in this repo set the stamp, so it is included here. New entry: `<date>, REFRESH mode, scoped to the GI-24 holiday-aware peak pricing story`, naming the new `Calendar`, the two new config keys, the session `peak` rollup, and the `doctor` coverage check.

**Rationale**:

The context docs are the cold-start map an agent reads to pick this repo up; they name `pricing.IsPeak` (removed), the old `Compute` signature, and a `doctor` check list that no longer matches. The refresh rides along in the same PR per `CLAUDE.md`, and the citation recompute rides along because the drifted citations are in a file already open for the response-shape change — fixing only the ones noticed would leave known-stale citations behind.

**Outcome Definition**:

- All six `docs/context/*.md` files above are updated; the count stated is six, matching §6's list (the plan's §9 item 4 undercounts it).
- `docs/context/build-and-run.md` names both new env vars.
- `docs/context/api-surface.md` names the `peak` field and the two `/api/prices` fields; every `internal/api/api.go` citation it carries resolves to the range it names in the edited file.
- `docs/context/testing-and-quality.md` no longer names `pricing.IsPeak` as a live symbol.
- `docs/context/cli-and-tooling.md`'s `doctor` row names the `peak_calendar` check.
- `docs/context/INDEX.md`'s refresh stamp is dated and scoped to GI-24.
- A search over `docs/context/` for `pricing.IsPeak` returns no **claim-bearing** hit (the symbol no longer exists; the INDEX stamp may name it as a removed symbol).

**Test Specifications**:

Documentation, so the verification is the searches, shown rather than asserted:

1. **Before** editing, positive control: `rg -n --glob 'docs/context/*.md' -e 'pricing.IsPeak' -e 'LENS_OFF_PEAK_DATES' docs/context/` — record the hits (`testing-and-quality.md` names the symbol; nothing names the env var yet). A search returning nothing before the change is a broken search, not a clean tree.
2. Confirm the pattern uses separate `-e` terms or `|`-free alternation, never `\|` (ripgrep treats `\|` as a literal pipe and silently matches nothing).
3. Edit.
4. Re-run; classify every remaining `pricing.IsPeak` hit as **claim-bearing** (must be empty) or **the INDEX stamp naming the removed symbol** (expected, per item 7).
5. For each `internal/api/api.go` citation in `api-surface.md`, confirm the cited range brackets the handler or region it names in the edited file (not the old number + an offset).
6. Grep `docs/context/INDEX.md` for the refresh stamp and assert it is dated.

**Files to Touch**:
- `docs/context/data-model.md` (modify)
- `docs/context/api-surface.md` (modify — the drill-down and `/api/prices` rows, plus any drifted `api.go` citations)
- `docs/context/build-and-run.md` (modify — two env vars)
- `docs/context/workflows.md` (modify — the cost step's calendar input)
- `docs/context/testing-and-quality.md` (modify — the `pricing.IsPeak` paragraph at `:66-71`)
- `docs/context/cli-and-tooling.md` (modify — the `doctor` row at `:10`)
- `docs/context/INDEX.md` (modify — the refresh stamp)
