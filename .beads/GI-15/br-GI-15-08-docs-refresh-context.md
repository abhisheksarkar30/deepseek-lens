# Bead br-GI-15-08: Refresh `docs/context/` for the pagination change

**Plan Reference**: `docs/planning/GI-15-pagination.md` §Verification step 5

- **Priority**: P1 (high)
- **Dependencies**: br-GI-15-03, br-GI-15-04, br-GI-15-05, br-GI-15-06, br-GI-15-07
- **Blocks**: none

## Description

This story changes things `docs/context/` makes factual claims about, so the docs are refreshed in
the same PR rather than the next unrelated audit. Run the `document-project-context` skill in
**REFRESH mode**, scoped to what this story changed — not a full re-scan.

Known-stale claims, verified against source during planning:

| File | What is now wrong | Where |
|---|---|---|
| `docs/context/api-surface.md` | `/api/requests`, `/api/warnings`, `/api/sessions` accept no `offset`; the three new response headers (`X-Total-Count`/`X-Limit`/`X-Offset`) are undocumented; `/api/warnings/summary` does not exist | check |
| `docs/context/data-model.md` | The `warnings` table row does not list `idx_warnings_kind_severity_created_at`; the `types.go` type enumeration is missing `WarningGroup` | index row + `:18-20` |
| `docs/context/testing-and-quality.md` | Names `groupWarnings`, a function this story deletes | `:15` |
| `docs/context/INDEX.md` | "Last generated/refreshed" line | check |

The `data-model.md:18-20` row is the one that needs a real edit rather than a check: that list
enumerates `types.go`'s exported types **exhaustively** (`Request`, `Session`, `Warning`, `Filter`,
`Summary`, `ModelStat`, `DayStat`, `CostSourceStat`), so the new exported `WarningGroup` makes it go
stale the moment br-GI-15-01 lands.

For `api-surface.md`, the interesting part is the header contract, not just the new params: the docs
should record that the list bodies stay bare arrays and that `X-Limit` reports the **applied** page
size (so an absent `?limit` reports `DefaultLimit`, not `0`), since that is the non-obvious rule a
consumer computing `nextOffset = offset + X-Limit` depends on.

This story adds **no** new entity, permission, or flow beyond those rows, so the refresh should be
small. Treat any other file the skill wants to change as a finding to review, not an expected edit.

## Rationale

`docs/context/` exists so an agent picking this repo up cold does not re-derive the architecture
from source, and it is only worth that while it is true. This story shifts an API contract in a way
that is invisible to a reader of the code — bodies unchanged, headers new — which is precisely the
kind of change a doc is more likely to miss than catch. The flywheel's Phase 6.5 exists because that
staleness compounds silently across stories.

## Outcome Definition

- The skill's refresh diff is reviewed: only files whose real content changed differ, and
  unaffected module docs come back as "No changes needed."
- `docs/context/api-surface.md` documents `?offset` on the three list routes, the three response
  headers (with `X-Limit` as the applied page size), and the `/api/warnings/summary` route.
- `docs/context/data-model.md` lists the new index and adds `WarningGroup` to the types enumeration.
- `docs/context/testing-and-quality.md` no longer names `groupWarnings`.
- `docs/context/INDEX.md`'s "Last generated/refreshed" line is updated.
- No doc claims a `/api/warnings/summary` limit/offset param, or an envelope-shaped list body.
- Doc updates are committed with the code they describe (a `docs:` commit is fine), not in a later
  PR.

## Test Specifications

- No unit or integration tests. The verification is the skill's own review step: read the diff,
  confirm every changed file changed for a real reason, and confirm no doc claims something the code
  does not do.
- Cross-check, by hand, after the refresh:
  - any documented `X-Limit` value matches br-GI-15-03's effective-limit rule;
  - the documented route list matches the `mux.Handle*` calls in `internal/api/api.go`;
  - the `types.go` type enumeration in `data-model.md` matches the exported types actually in
    `internal/store/types.go` — this is the exhaustive list, so a missing `WarningGroup` is the
    failure mode.

## Files to Touch

- `docs/context/api-surface.md` (modify)
- `docs/context/data-model.md` (modify — index row and type enumeration)
- `docs/context/testing-and-quality.md` (modify — drop the `groupWarnings` reference)
- `docs/context/INDEX.md` (modify — refresh date)
- any other `docs/context/*.md` the refresh genuinely flags as changed

## Review Notes

**The refresh stayed scoped.** Four files changed and no others; every other module doc came back
"No changes needed." This bead's "treat any other file the skill wants to change as a finding, not an
expected edit" held — no fifth file was edited.

**All four cross-checks pass.** The documented route list matches the `mux.Handle*` block; the
`types.go` enumeration in `data-model.md` lists all nine exported types in
`internal/store/types.go` (`Request`, `Warning`, `WarningGroup`, `Session`, `Filter`, `Summary`,
`ModelStat`, `DayStat`, `CostSourceStat`) — the exhaustive list the bead flagged as the failure mode;
the documented `X-Limit` value matches the effective-limit rule verified live against a running
server; and a grep sweep found no doc claiming a `limit`/`offset` on `/api/warnings/summary` or an
envelope-shaped list body.

**Three pre-existing errors were corrected, not passed over.** These are *not* GI-15's changes — the
skill landed on the rows and they disagreed with the code:

1. `api-surface.md`'s per-route line citations had drifted from the real handler ranges.
2. `data-model.md` claimed the `warnings` foreign key was "(no enforced FK constraint)". It **is**
   enforced: the DSN sets `_pragma=foreign_keys(ON)` (`store.go:43`), confirmed empirically — an
   insert with a bad `request_id` fails with `FOREIGN KEY constraint failed (19)`. Corrected, along
   with the note that `PurgeOlderThan` deletes warnings explicitly anyway rather than leaning on the
   cascade.
3. Several `store.go` line references had shifted.

**One deliberate non-edit.** `docs/planning/GI-15-pagination.md` still describes `groupWarnings`
throughout. That is correct — the plan is the design record for a story that deletes it, and
"the plan says we removed X" is not a stale claim. Context docs were left clean of it; the plan was
not touched.

**One thing considered and left alone.** GI-15's new tests are not added to
`testing-and-quality.md`'s curated "Explicitly covered, by design" list. That list is not exhaustive
by construction — it selects tests that encode a non-obvious guarantee — and the pager's boundary
arithmetic is already described in the "Verifying a web change without a harness" paragraph
immediately above it.
