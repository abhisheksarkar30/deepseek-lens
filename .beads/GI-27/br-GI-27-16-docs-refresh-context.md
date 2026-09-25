### Bead 16: `docs` refresh `docs/context/*`

- **Bead ID**: br-GI-27-16
- **Priority**: P2 (medium)
- **Original Estimate**: 1.5h
- **Dependencies**: br-GI-27-01 … br-GI-27-15 (all)
- **Blocks**: None

**Description**:

The Phase 5.6 context refresh (`CLAUDE.md`: "Update `docs/context/` in the same PR"). This story changes the capture path, a store signature, the request/stats query surface, two new guided routes, the serve lifecycle, a new table, and four new CLI commands, so the docs go stale the moment it lands. Take the list from **plan §10** and nowhere else.

1. **`docs/context/INDEX.md`** — `StatsByPeriod`'s signature gains `tzOffsetMin` (`:83`); restamp the "Last generated / refreshed" entry the way the GI-15/GI-17/GI-21/GI-24 entries above it did (a refresh that does not restamp is indistinguishable from a stale one). New entry: `<date>, REFRESH mode, scoped to the GI-27 billing-fidelity, cap, archival and lifecycle story`. Note the plan §10 also lists `data-model.md` for `idx_requests_stats` — verify the index list there first, then add it.
2. **`docs/context/data-model.md`** (or the schema doc) — `idx_requests_stats`; the `body_archive` table; **bodies may live in per-UTC-day archive files**, not only in `requests`.
3. **`docs/context/api-surface.md`** — `/api/requests` gains `until`; `/api/stats` gains `tz_offset`; new `POST /api/shutdown` and `POST /api/reload` (the write-route table grows to five). **Recompute any `internal/api/api.go` citation** these edits shift, verified against the edited file rather than the old numbers (br-GI-21-08 / br-GI-24-09 discipline: brace-match the handler, do not add an offset).
4. **`docs/context/architecture.md`** and **`docs/context/workflows.md`** — the capture path keeps a **response tail** (bead 03); the serve lifecycle and the single purge+archive maintenance goroutine (beads 08/12); the `serve.state.json` **early write** (verify the wording against source, not against the plan).
5. **`docs/context/cli-and-tooling.md`** and **`docs/context/build-and-run.md`** — the new commands `shutdown`, `restart`, `reload`, `archive`, and `serve.state.json`.
6. **`docs/context/security-and-permissions.md`** (`:6`) and **`README.md`** (`~:274`) — the body-cap note (usage survives the cap; the cap is configurable to 8 MiB) and the two new guarded routes are listed. NOTE: bead 15 already edits the README body-cap note and bead 01 already edits `conventions.md` and the `CLAUDE.md` bullets — do not duplicate; reconcile so each file ends consistent.
7. **`docs/context/conventions.md`** and **`CLAUDE.md`** "Conventions/Enforcement" — no `develop` (workstream D). Also the `CLAUDE.md` "Fail open" bullet: "three write routes" becomes **five**. Bead 01 owns these edits; this bead **verifies** them against the merged source and fixes only drift.

**The two-branch `develop` drop is bead 01's; this bead confirms it landed.** The docs refresh rides in the same PR per `CLAUDE.md`.

**Rationale**:

The context docs are the cold-start map an agent reads to pick this repo up; after this story they name a capture path without a tail, a `StatsByPeriod` without the offset, an API surface without `until`/`tz_offset`/the two routes, and a serve lifecycle without the state file or the archive goroutine. The refresh rides along in the same PR per `CLAUDE.md`, and the citation recompute rides along because the drifted citations are in files already open for the surface changes.

**Outcome Definition**:

- Every `docs/context/*.md` file named in §10 is updated; the `INDEX.md` refresh stamp is dated and scoped to GI-27.
- `api-surface.md` names `until`, `tz_offset`, `POST /api/shutdown`, `POST /api/reload`; every `internal/api/api.go` citation it carries resolves to the range it names in the edited file.
- `architecture.md`/`workflows.md` name the response tail, the single maintenance goroutine, and the state-file early write.
- `security-and-permissions.md` lists the two new guarded routes; the README body-cap note is consistent with bead 15.
- A search over `docs/context/` for the removed-old-shape symbols (`utcDayBound`-era prose, the pre-tail capture description, a `develop`-flow sentence) returns no claim-bearing hit.

**Test Specifications**:

Documentation, so the verification is the searches, shown rather than asserted:

1. **Before** editing, positive control: `rg -n --glob 'docs/context/*.md' -e 'tz_offset' -e 'until' -e 'shutdown' -e 'HotDays' -e 'body_archive' docs/context/` — record the hits (the new symbols are absent; the old shapes are present). A search returning nothing before the change is a broken search, not a clean tree.
2. Use separate `-e` terms, never `\|` (ripgrep treats `\|` as a literal pipe and matches nothing).
3. Edit.
4. Re-run; assert the new symbols are now named in the expected files and no claim-bearing hit for the old shapes remains.
5. For each `internal/api/api.go` citation in `api-surface.md`, confirm the cited range brackets the handler or region it names in the edited file (not the old number + an offset).
6. Grep `docs/context/INDEX.md` for the refresh stamp and assert it is dated.

**Files to Touch**:
- `docs/context/INDEX.md` (modify — `:83` signature + refresh stamp)
- `docs/context/data-model.md` (modify — `idx_requests_stats`, `body_archive`, bodies-in-day-files)
- `docs/context/api-surface.md` (modify — `until`, `tz_offset`, the two routes, drifted cites)
- `docs/context/architecture.md` (modify — tail, maintenance goroutine, state file)
- `docs/context/workflows.md` (modify — same)
- `docs/context/cli-and-tooling.md` (modify — `shutdown`, `restart`, `reload`, `archive`)
- `docs/context/build-and-run.md` (modify — `serve.state.json`, the new commands, the 8 MiB env/flag)
- `docs/context/security-and-permissions.md` (modify — `:6` body-cap note; the two guarded routes)
- `README.md` (modify — reconcile with bead 15's body-cap note)
- `docs/context/conventions.md` (modify — verify bead 01's no-`develop` edit)
- `CLAUDE.md` (modify — verify bead 01's Conventions/Enforcement and "Fail open" five-route edit)
