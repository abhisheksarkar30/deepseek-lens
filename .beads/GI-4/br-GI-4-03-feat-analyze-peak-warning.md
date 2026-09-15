# Bead br-GI-4-03: `peak_pricing` warning kind and rule

**Plan Reference**: `docs/planning/GI-4-peak-cost-and-hook-integration.md` §4.3, §4.4

- **Priority**: P0 (critical)
- **Dependencies**: br-GI-4-01
- **Blocks**: br-GI-4-05, br-GI-4-06

## Description

br-GI-4-02 makes `cost_usd` correct. This bead makes it *legible*: a new warning kind that names
the calls which were billed at peak, so a doubled cost has an explanation attached and the user
gets the retroactive signal that part of their spend was avoidable.

Add `KindPeakPricing = "peak_pricing"` to `internal/analyze/kinds.go` with its `allKinds` entry,
and `rulePeakPricing` to the rule table in `rules.go`, plus the matching row in the README's
warning table between the `BEGIN/END warning kinds` markers. Severity `warn`.

**The rule fires only when the call was actually priced and carried spend**
(`in.req.CostUSD != nil && *in.req.CostUSD > 0`). An unpriced call carries no number, and a
configured model with zero tokens prices to a real `0` — non-nil, but no spend. A warning
asserting either was "billed at peak" would claim something lens does not know; that is the same
discipline that makes `SourceUnpriced` a first-class state instead of a zero. Off-peak, and on a
row with no `cost_usd` or a `0` cost, the rule is silent.

**No plumbing is needed to get the time.** Rules receive the whole row
(`ruleInput{meta, usage, req, opts}`), and `req.StartedAt` is already set before the cost step and
is the value the cost step prices against. So the rule reads `in.req.StartedAt` and calls
`pricing.IsPeak` — `NewRules` does not change, and neither do its eight call sites.

This means `internal/analyze` gains an import of `internal/pricing`. That is safe and does not
touch CLAUDE.md's dependency invariant: `pricing` imports only `parse`, so no cycle forms, and the
invariant that matters — that `proxy` never imports `analyze` or `store` — is pinned to `proxy`
alone.

### Why this is a warning and not a fifth `CostSource`

The obvious alternative is tagging the source `"configured-peak"` so `lens stats`'s existing
`cost_sources` breakdown splits peak spend for free. It was rejected because it would make the
dashboard **lie**: `internal/web/app.js:35-36` renders any source other than `configured` as
*"no configured price for this call (lens prices --set)"*, and a peak-priced call *is* priced.
Avoiding that false tooltip means editing badge logic in JavaScript, where this repo has no tests.

The warning table is the better home on its own merits too. It is mechanically honest —
`readme_test.go` enforces *equality*, not containment, between `AllKinds()` and the README rows,
so a kind cannot be added without its user-facing sentence — and it already carries kinds that
are not "a dropped parameter" (`upstream_error`, `analyzer_panic`), so a billing fact is not a
category error there.

## Rationale

br-GI-4-02 alone leaves a hole: a user who sees a session cost twice what they expected has no way
to learn from the tool *why*. The peak window is a scheduling fact the user can act on, and lens
is the only component that can tell them after the fact that they worked through it.

**P0, because for half this story's audience this bead is the feature rather than an explanation
of one.** A user running the plugin gets a guard that refuses the session *before* it bills, so
for them the warning is a retrospective note about a call the guard did not stop. A user running
no plugin has no guard and no toggle, and nothing else in lens knows that 02:00 UTC costs double:
for them the warning is the only peak signal that exists, and it is what makes the doubled
`cost_usd` mean anything. Where the plugin is present it prevents and lens accounts; where it is
absent, lens accounting *is* the whole of the protection (plan §4.3).

## Outcome Definition

- `go test ./internal/analyze/...` passes, including `readme_test.go`.
- `peak_pricing` appears as a row in the README warning table with the exact sentence from
  `kinds.go`.
- A priced call stamped inside the peak window produces a `peak_pricing` warning of severity
  `warn`.
- An unpriced call inside the peak window produces **no** `peak_pricing` warning.
- A configured call with **zero tokens** (a real `0` cost, non-nil) inside the peak window
  produces **no** `peak_pricing` warning.
- A priced call outside the peak window produces no `peak_pricing` warning.
- `NewRules`'s signature is unchanged.

## Test Specifications

- Unit Tests (`internal/analyze/analyze_test.go`):
  - **Peak + priced** → exactly one warning, kind `peak_pricing`, severity `warn`, on a row
    stamped 07:00 UTC Friday with a non-nil `CostUSD`.
  - **Peak + unpriced** → no `peak_pricing` warning on a row stamped 07:00 UTC Friday with
    `CostUSD == nil`.
  - **Peak + zero-token configured** → no `peak_pricing` warning: a row stamped 07:00 UTC Friday
    on a configured model with zero tokens prices to a non-nil `CostUSD == 0`, which must not
    raise a "billed at peak" claim.
  - **Off-peak + priced** → no `peak_pricing` warning on a row stamped 20:00 UTC Friday.
  - **Weekend + priced** → no warning on a row stamped 07:00 UTC Saturday (the window is a
    weekday rule; a rule that forgets the day fails here).
  - The rule composes with the existing table: a fixture that also triggers `ruleTopPClamped`
    returns both warnings, confirming the table's order is not disturbed.
- Unit Tests (`internal/analyze/readme_test.go`): passes by construction once the kind is
  registered and the row added — no edit to the test itself.
- Integration Tests: none — the rule has no call site beyond the rule table.

## Files to Touch

- `internal/analyze/kinds.go` (modify — `KindPeakPricing` constant, `allKinds` entry with its
  one-sentence description)
- `internal/analyze/rules.go` (modify — `rulePeakPricing`, registered in the `rules` table)
- `internal/analyze/analyze_test.go` (modify — six new cases)
- `README.md` (modify — the `peak_pricing` row inside the warning-kinds markers)
