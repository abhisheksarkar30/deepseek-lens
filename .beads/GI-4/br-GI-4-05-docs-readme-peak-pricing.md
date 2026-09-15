# Bead br-GI-4-05: Document peak pricing in the README and the price-file header

**Plan Reference**: `docs/planning/GI-4-peak-cost-and-hook-integration.md` §4.2

- **Priority**: P2 (medium)
- **Dependencies**: br-GI-4-02, br-GI-4-03
- **Blocks**: none

**Depends on br-GI-4-03 for a file-level reason, not a semantic one**: both beads edit
`README.md` (03 adds the `peak_pricing` row inside the warning-kinds markers; this bead edits the
cost-section prose around it). Serialising them keeps two diffs to one file from racing. If they
are ever implemented out of order, rebase rather than merging both edits by hand.

## Description

A user who configures a rate in `prices.toml` and then sees a call cost twice their arithmetic has
no way to know the tool is right and they are reading it wrong. Two surfaces say so:

1. **`README.md`** — the cost section states the window, the multiplier, and that both are
   DeepSeek's, not lens's. It should also name the two effects the user will actually observe: a
   `cost_usd` that is 2x the off-peak arithmetic, and a `peak_pricing` warning on the call.

2. **The price-table header written by `pricing.Save`** (`internal/pricing/table.go`) — the
   generated `prices.toml` opens with a comment block explaining that rates are per million tokens
   and that a model with no rates is unpriced. That comment is where a person is standing when
   they set a rate, so it is the right place for one line noting that these are **off-peak** rates
   and that peak hours are billed at `PeakMultiplier` of them.

Wording must be careful about what is and is not configurable: the *rates* are the user's, the
*window* and the *ratio* are DeepSeek's and are compiled in. Do not invite the reader to set them.

## Rationale

The peak window is an invisible multiplier on every number in this tool for 21% of the week. That
is exactly the kind of fact that must be written down where the number is configured and where the
number is explained, not only in a plan document nobody reads after the PR merges.

The `prices.toml` header matters more than the README here: the README is read once, and the
header is read every time someone opens the file to change a rate.

## Outcome Definition

- `README.md` names the peak window (01:00-04:00 and 06:00-10:00 UTC, Mon-Fri) and the 2x
  multiplier, and states that both are DeepSeek's schedule rather than a lens setting.
- The README's warning table already carries the `peak_pricing` row (br-GI-4-03); this bead's
  cost-section prose links the two — a doubled cost and the warning that explains it.
- A freshly written `prices.toml` (via `lens prices --set`) carries the off-peak note in its
  header.
- The header text change is reflected in the write/read round-trip test's expectations if that
  test asserts on the header — if it does not, no test change is needed.
- `go test ./internal/pricing/...` passes.

## Test Specifications

- Unit Tests: `internal/pricing/pricing_test.go`'s table round-trip test asserts on parsed
  content, not the comment header — so this bead should need no test change. If it turns out that
  the header is asserted, update that assertion rather than weakening it.
- Verification: write a table with `lens prices --set deepseek-flash.input=0.28`, open the file,
  confirm the off-peak note is present and the rates parse back unchanged.
- Docs check: `go test ./internal/analyze/...` (the README warning-table check) still passes after
  the README edit — the `BEGIN/END warning kinds` markers and the table rows must be left intact.

## Files to Touch

- `README.md` (modify — cost section prose; do not disturb the warning-kinds markers)
- `internal/pricing/table.go` (modify — the `Save` header comment block)
