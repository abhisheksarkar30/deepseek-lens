# Bead br-GI-4-01: Peak-window predicate and multiplier

**Plan Reference**: `docs/planning/GI-4-peak-cost-and-hook-integration.md` §4.2

- **Priority**: P0 (critical)
- **Dependencies**: none
- **Blocks**: br-GI-4-02, br-GI-4-03

## Description

Teach `internal/pricing` about DeepSeek's peak-pricing window. Two exported additions, no
behaviour change to any existing function:

```go
// PeakMultiplier is DeepSeek's peak/off-peak price ratio: a call placed inside the
// peak window costs this multiple of the configured off-peak rate.
const PeakMultiplier = 2.0

// IsPeak reports whether t falls inside DeepSeek's peak-pricing window:
// 01:00-04:00 or 06:00-10:00 UTC, Monday through Friday. Weekends never peak.
func IsPeak(t time.Time) bool
```

`IsPeak` must classify in **UTC**, not in `t`'s location: `t.UTC().Weekday()` and
`t.UTC().Hour()`. Writing `t.Hour()` directly is the trap this bead exists to close — it silently
answers in the caller's zone, which is correct on a UTC-configured machine and wrong by up to a
whole window on any other, with no symptom other than wrong money.

The window is duplicated from two existing implementations that lens cannot import:
`deepseek-peak-guard.sh` and `deepseek-auto-toggle.js` in the `agentic-ai-artifacts` plugin, both
of which already encode this exact window. `IsPeak` carries a comment naming both files and the
window's source, because this is now the third copy and drift between copies is the risk this
plan calls out (§8.1).

**The multiplier is a constant, deliberately not a config key.** lens makes *rates* configurable
(`prices.toml`) because the shipped table must not invent a per-model number DeepSeek's pricing
page owns. The peak ratio is a single documented global — the plugin already hardcodes `2x` in
its own blocked-prompt text — so `LENS_PEAK_MULTIPLIER` would add a config field, a `NewRules`
parameter and eight call-site updates to serve a number nobody has a reason to change. A
`ponytail:` comment names the upgrade path (move to config) for the day DeepSeek moves it.

## Rationale

This is the smallest unit that can be tested on its own, and it is the unit with the correctness
trap. Splitting it from the arithmetic (br-GI-4-02) means the window can be pinned exhaustively —
every boundary, both weekend days, and the UTC-vs-local question — without those tests being
tangled up in `Compute`'s signature change.

Getting the window wrong in either direction is a money bug: too wide and lens over-reports
spend, too narrow and it under-reports it by 2x for however many hours the boundary is off.

## Outcome Definition

- `go test ./internal/pricing/...` passes.
- `IsPeak` is true at 01:00, 03:59, 06:00, 09:59 UTC on a weekday; false at 00:59, 04:00, 05:59,
  10:00 UTC on a weekday.
- `IsPeak` is false at every hour on Saturday and Sunday.
- `IsPeak` returns the same answer for the same instant expressed in two different zones.
- `PeakMultiplier == 2.0`.
- No existing exported function in the package changes signature or behaviour.

## Test Specifications

**Use the plugin's fixture date.** `hooks/test-deepseek-auto-toggle.sh` pins **both** `FRI_PEAK`
(`2026-09-11T07:00:00Z`) and `FRI_OFF` (`2026-09-11T20:00:00Z`) to Friday **2026-09-11**; its
weekend case is Sunday **2026-09-13** (`2026-09-12` appears nowhere in the plugin harness — the
Go-side Saturday fixture below is this file's own date). Using the same Friday date
here is deliberate: this window now exists in three implementations that cannot import from each
other, and the plan's §8.1 names drift between them as the top risk. **But the shared dates do not
detect that drift** — no test in either repo executes the other implementation, so a one-sided
boundary move fails that side's *own* boundary test whether or not the dates match. Keep the shared
dates so the two suites read analogously; the thing that actually catches a wrong window is each
side's own boundary test — here, the 01:00/04:00/06:00/10:00 edges and the Friday→Saturday
transition.

- Unit Tests (`internal/pricing/pricing_test.go`):
  - **Boundaries, weekday**: table-driven over 00:59, 01:00, 03:59, 04:00, 05:59, 06:00, 09:59,
    10:00 UTC on **2026-09-11** → expect `false, true, true, false, false, true, true, false`.
  - **Weekend**: 02:00 and 07:00 UTC on **2026-09-12** (Saturday) and **2026-09-13** (Sunday) →
    all `false` (both windows covered on both days, so a weekday test that accidentally ignores
    the day still fails).
  - **UTC, not local**: the same instant constructed as `time.Date(...)` in a non-UTC zone and in
    UTC → identical `IsPeak` results. Use a fixed zone offset that crosses the window boundary
    (e.g. an instant that is 07:00 UTC / 02:00 in UTC-5) so a naive `t.Hour()` implementation
    fails this test.
  - **Zero time**: `time.Time{}` (Jan 1, year 1, 00:00 UTC — a Monday, hour 0) → `false`. This is
    the value br-GI-4-02's mechanical test edits pass, so it must be off-peak for those edits to
    preserve the tests' meaning.
- Integration Tests: none — this bead adds no call site.

## Files to Touch

- `internal/pricing/pricing.go` (modify — add `PeakMultiplier`, `IsPeak`, and the window doc
  comment naming the two plugin files it mirrors)
- `internal/pricing/pricing_test.go` (modify — window tests; `time` import)
