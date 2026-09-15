# Bead br-GI-4-06: Refresh `docs/context/` for the peak-pricing change

**Plan Reference**: `docs/planning/GI-4-peak-cost-and-hook-integration.md` §6

- **Priority**: P1 (high)
- **Dependencies**: br-GI-4-02, br-GI-4-03, br-GI-4-04
- **Blocks**: none

## Description

This story changes things `docs/context/` makes factual claims about, so the docs are refreshed in
the same PR rather than the next unrelated audit. Run the `document-project-context` skill in
**REFRESH mode**, scoped to what this story actually changed — not a full re-scan.

Known-stale claims, verified against source during planning:

| File | What is now wrong | Where |
|---|---|---|
| `docs/context/data-model.md` | Lists the warning kinds; `peak_pricing` is missing | `:67` |
| `docs/context/glossary.md` | Defines **Kind** and gives an example set; check whether the example list is meant to be exhaustive | `:18` |
| `docs/context/architecture.md` | Describes `internal/analyze` as detecting "silent parameter drops/rewrites"; peak pricing is a billing fact rather than a divergence, so the one-line description may need widening | `:44` |
| `docs/context/testing-and-quality.md` | Describes what is tested; the pricing tests gain a time dimension | check |
| `docs/context/INDEX.md` | "Last generated/refreshed" line | check |

`docs/context/data-model.md:72` documents `cost_source` as one of four values — that stays
**correct** and must not be touched. This story deliberately adds no fifth value (see br-GI-4-03).

Also verify that no context doc claims `pricing.Compute` is a pure function of a Table *without
mentioning the time input* — the package's own doc comment says "no I/O, no config", which remains
true (the time is an argument, not a read), but a doc paraphrasing the signature would now be
stale.

## Rationale

`docs/context/` exists so an agent picking this repo up cold does not re-derive the architecture
from source. It is only worth that if it is true. Every change that invalidates a claim it makes
is the moment to fix the claim — the flywheel's Phase 6.5 exists because staleness compounds
silently across stories, which is the same failure mode this whole story is about.

## Outcome Definition

- The skill's refresh diff is reviewed: only files whose real content changed differ, and
  unaffected module docs come back as "No changes needed."
- `docs/context/data-model.md` lists `peak_pricing` among the warning kinds.
- `docs/context/data-model.md:72`'s four-value `cost_source` statement is unchanged.
- `docs/context/INDEX.md`'s "Last generated/refreshed" line is updated.
- Doc updates are committed with the code they describe (a `docs:` commit is fine), not in a
  later PR.

## Test Specifications

- No unit or integration tests. The verification is the skill's own review step: read the diff,
  confirm every changed file changed for a real reason, and confirm no doc claims something the
  code does not do.
- Cross-check: any doc sentence that quotes a `pricing.Compute` signature must match
  `internal/pricing/pricing.go` after br-GI-4-02.

## Files to Touch

- `docs/context/data-model.md` (modify — warning kind list)
- `docs/context/INDEX.md` (modify — refresh date, if the skill updates it)
- `docs/context/architecture.md` (modify — only if the `internal/analyze` description needs it)
- `docs/context/glossary.md` (modify — only if its kind list is meant to be exhaustive)
- any other `docs/context/*.md` the refresh flags as genuinely changed
