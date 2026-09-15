# Bead br-GI-4-02: Apply the peak multiplier in Compute

**Plan Reference**: `docs/planning/GI-4-peak-cost-and-hook-integration.md` §4.2

- **Priority**: P0 (critical)
- **Dependencies**: br-GI-4-01
- **Blocks**: br-GI-4-05, br-GI-4-06

## Description

Make `pricing.Compute` price a call at the rate that was actually charged for it:

```go
func Compute(model string, usage parse.Usage, table Table, at time.Time) Cost
```

When `IsPeak(at)` is true, every rate used in the sum is multiplied by `PeakMultiplier` first.
Off-peak, the result is bit-for-bit what the function returns today.

**The multiplier is applied to the rates, before summation, as an exact `big.Rat` multiply** —
not to the rounded total afterwards. Applying it to the total is arithmetically equivalent only
when there is one token category; with several, `round(2 * sum)` and `2 * round(sum)` can differ
by a micro-dollar, and the second one is a second rounding site in a package whose documented
contract is that rounding happens exactly once per call, at the end. Multiplying a rate is exact
in `big.Rat`; multiplying a rounded integer is not.

The multiplier composes with the existing categories unchanged: a category whose rate is unset
still falls back to the input rate and still downgrades the source to `"approximate"`, and the
fallback rate is the peak-multiplied input rate, not the off-peak one.

Three call-site updates:

1. `internal/consumer/consumer.go:382` — pass `req.StartedAt`, the same value the row stores, so
   the price on a row is a function of the timestamp on that row and nothing else.
2. `internal/pricing/pricing_test.go` — eleven existing `Compute` calls gain `, time.Time{}`.
   The zero time is off-peak (pinned by br-GI-4-01), so all eleven keep asserting exactly what
   they asserted before. This is mechanical and must stay mechanical: if any of those assertions
   needs to change, the change is wrong.
3. `internal/consumer/consumer_test.go` — **pin the shared fixture off-peak.** `simpleCall()`
   (`internal/consumer/consumer_test.go:38`) stamps `StartedAt: time.Now()`, and `pricedCall`
   (`:832`) inherits it. Once the cost step prices a row at the row's own timestamp, the
   exact-cost assertions that inherit it — `:884` asserts `0.28`, `:961` asserts `0.28`, `:975`
   asserts `0.31` — fail during 01:00–04:00 / 06:00–10:00 UTC Mon–Fri, the exact schedule this
   bead is about. Change `simpleCall()`'s `StartedAt: time.Now()` to a fixed off-peak instant,
   e.g. `time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC)`. One edit makes the two existing
   assertions deterministic at any wall-clock time and gives the new peak case a fixed base. The
   suite must be green both inside and outside the peak window; a `time.Now()`-derived
   `StartedAt` is the specific trap this pin closes, so a future fixture must not reintroduce it.

   **The pin must not collapse two rows onto one timestamp.** `TestCostStepTakesEffectWithoutRestart`
   (`internal/consumer/consumer_test.go:942`) submits two calls via `pricedCall`/`simpleCall` and
   asserts on `rows[0]` (`:975`, `0.31`), where `rows` comes from `waitForRows` →
   `store.ListRequests` (`internal/store/store.go:341`, `ORDER BY started_at DESC LIMIT ?` — no
   secondary sort key). Today the two calls carry distinct `time.Now()` stamps, so `rows[0]` is the
   later call *by timestamp*. A single-instant pin would give both rows the same `started_at`, and
   the assertion would then pass only because equal keys come back rowid-DESC through the
   `idx_requests_started_at` index (`internal/store/schema.sql:45`) — the test's determinism would
   rest on the index/query plan rather than on the fixture. So in that one test, stamp the **second**
   call's `StartedAt` with a distinct, still-off-peak instant (e.g. the pinned base + 1 hour) so
   `rows[0]` is `0.31` because it is genuinely the later row. Both rows stay off-peak, so no
   multiplier applies and the F1.1 property (`:884`/`:961`/`:975` hold at any wall-clock hour) is
   preserved.

`cost_source` is unchanged by this bead. A peak-priced call is still `"configured"` — the
arithmetic was performed with the user's own rates, and `CostSource` describes the provenance of
the number, not the calendar. Adding a fifth value would also make the dashboard lie about a
priced call (see the plan, §4.3).

## Rationale

This is the bead that actually fixes the money. Until it lands, every call lens prices between
01:00-04:00 and 06:00-10:00 UTC on a weekday is recorded at half its real cost — about 21% of the
week, in a tool whose entire thesis is that a number is never invented.

Taking `at` as a parameter rather than reading a clock inside `Compute` is what keeps the function
pure and the package's stated design intact ("a pure function of a Table, no I/O, no config"),
and it is what lets a test pin a peak call without touching the system clock.

The rejected alternative — a `ForTime(table, at) Table` transform leaving the signature alone —
would have touched no test, but `pricing.Loader.Table()` returns its **cached** table by
reference. A transform that mutated in place would permanently double the rates for every later
call, off-peak included, which is a silent and unrecoverable mispricing. Eleven mechanical edits
are the cheaper price.

## Outcome Definition

- `go test ./...` passes **at any wall-clock time** — the shared consumer fixture is pinned
  off-peak, so the same run is green both inside and outside the peak window.
- Identical usage priced at a peak instant and an off-peak instant yields exactly `2x` the
  micro-dollars — integer equality, not a tolerance.
- Every pre-existing pricing test passes with only its extra `time.Time{}` argument added.
- An unpriced model stays `nil` at peak, and an unknown model still reports `"unknown-model"`.
- Cache-read fallback at peak uses the peak-multiplied input rate and still reports
  `"approximate"`.
- `internal/consumer/consumer.go:382` passes `req.StartedAt`.

## Test Specifications

- Unit Tests (`internal/pricing/pricing_test.go`):
  - **Exact 2x**: one fixture usage priced at 07:00 UTC Friday and at 20:00 UTC Friday → the peak
    amount is exactly `2 *` the off-peak amount.
  - **Rate-not-total rounding**: a fixture with input *and* output tokens whose off-peak total
    rounds up, priced at peak → equals the hand-computed `2 * (rate * tokens)` rounded once.
    Chosen so that `2 * round(sum)` and `round(2 * sum)` differ, which is the property this bead
    is protecting.
  - **Unpriced at peak**: model in the table with no rates, priced at 07:00 UTC Friday → `Amount
    == nil`, source `"unpriced"`.
  - **Unknown model at peak**: model absent from the table → source `"unknown-model"`.
  - **Approximate at peak**: cache-read tokens with no cache-read rate at 07:00 UTC Friday →
    source `"approximate"`, amount computed from the peak-multiplied input rate.
  - **Eleven existing cases** updated with an off-peak `time.Time{}`, assertions untouched.
- Integration Tests (`internal/consumer/consumer_test.go`):
  - The shared `simpleCall()` fixture is pinned to a fixed off-peak instant (see call-site update
    3), so `TestCostStepPricesConfiguredRows` and `TestCostStepTakesEffectWithoutRestart` assert
    `0.28`/`0.31` deterministically at any wall-clock hour. `TestCostStepTakesEffectWithoutRestart`'s
    second call carries a **distinct** off-peak `StartedAt`, so its `rows[0]` assertion is ordered
    by timestamp rather than by the `idx_requests_started_at` tiebreak (call-site update 3).
  - A call whose `StartedAt` is 07:00 UTC Friday, with configured rates, lands on the row with
    `cost_usd` exactly double the same call stamped 20:00 UTC Friday.
- E2E: deferred to `/develop-tests`.

## Files to Touch

- `internal/pricing/pricing.go` (modify — `Compute` signature, per-rate multiplier)
- `internal/pricing/pricing_test.go` (modify — 11 call sites, 5 new cases)
- `internal/consumer/consumer.go` (modify — pass `req.StartedAt` at the cost step)
- `internal/consumer/consumer_test.go` (modify — pin `simpleCall()`'s `StartedAt` off-peak;
  peak/off-peak row assertion)
