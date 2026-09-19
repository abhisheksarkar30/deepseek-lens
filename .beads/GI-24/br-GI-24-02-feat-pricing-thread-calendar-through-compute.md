### Bead 2: `feat` pricing: thread the calendar through `Compute`

- **Bead ID**: br-GI-24-02
- **Priority**: P0 (critical)
- **Original Estimate**: 1.5h
- **Dependencies**: br-GI-24-01
- **Blocks**: br-GI-24-04, br-GI-24-05, br-GI-24-06

**Description**:

The cost step stops asking the package-level function and starts asking the calendar, so a holiday is priced at 1×.

**1. `pricing.Compute` gains a `Calendar` and hoists the peak test** (`internal/pricing/pricing.go:140-178`). New signature:

```go
func Compute(model string, usage parse.Usage, table Table, at time.Time, cal Calendar) Cost
```

Today `IsPeak(at)` is called **twice** (`pricing.go:149` and `:162`). Replace both with one hoisted local so the window is asked once:

```go
peak := cal.IsPeak(at)
```

and use `peak` at both sites. Update `Compute`'s doc comment (which names `IsPeak`) to name the calendar.

**2. Remove the package-level `pricing.IsPeak` entirely** (`pricing.go:33-49`), and move its window/weekend logic into `Calendar.IsPeak` (bead 01 already has an equivalent body — delete the duplicate here). There must be exactly one way to ask "is this instant peak". Its doc comment's "keep it in sync with the two hook copies by hand" note moves onto `Calendar.IsPeak`.

**3. `internal/consumer` installs the calendar through the optional-setter seam.** Add a `calendar pricing.Calendar` field to `Consumer` (beside `prices`, `consumer.go:102`) and a setter mirroring `SetPriceTable` (`consumer.go:127-131`):

```go
// SetCalendar installs the calendar the pre-insert cost step prices against.
// Leaving it unset is legal and prices with the window-and-weekend rule alone,
// which is the pre-GI-24 behaviour.
func (c *Consumer) SetCalendar(cal pricing.Calendar) { c.calendar = cal }
```

Pass the field at the cost call (`consumer.go:430`):

```go
cost := pricing.Compute(req.ModelResolved, usage, c.prices.Table(), req.StartedAt, c.calendar)
```

The zero `Calendar` is a valid default here, so an un-wired consumer behaves exactly as today.

**4. This bead leaves `internal/analyze` non-compiling, on purpose.** Removing `pricing.IsPeak` breaks its one production caller, `internal/analyze/rules.go:262` — repaired by br-GI-24-04, which rewrites that line to `in.opts.calendar.PeakPriced(...)`. Because `internal/consumer/consumer_test.go` imports `internal/analyze` (its `NewRules` call at `:91`), **`go test ./internal/consumer/...` does not build either until br-GI-24-04 lands**, so the new holiday-priced case below is *authored* here but first *runs* at br-GI-24-06 (which restores the full build). This bead's own gate is therefore:

```
go test ./internal/pricing/...      # the only package it fully owns that still builds
go build ./internal/consumer/       # consumer.go itself does not import analyze
```

Do **not** treat `go build ./...` failing as a regression in this bead — that is expected and repaired in the graph (the same transient break GI-21's br-GI-21-01 documents for its own signature change).

**Rationale**:

D5/D6. The calendar is a new *input* to the existing cost step and reorders nothing (the cost step stays before `InsertRequest`, `CLAUDE.md`'s fixed pipeline order). Threading it as a field and a parameter — not a global — is what makes the holiday behaviour testable per call.

Deleting the package function rather than keeping it as a shim is deliberate: two ways to ask the same question is exactly the drift this story exists to remove, and it is the same "exactly one way to ask" discipline `CostSource` encodes.

**Outcome Definition**:

- `Compute` takes a `Calendar`; a call at a configured `offPeak` date is priced at exactly 1× the ordinary off-peak amount (integer equality on micro-dollars, no tolerance).
- `Compute` asks `cal.IsPeak` once per call, not twice.
- `pricing.IsPeak` no longer exists as a package-level symbol (`pricing.` has no `IsPeak` function in godoc).
- `Consumer.SetCalendar` exists, mirrors `SetPriceTable`, and leaving it unset is legal (`go build ./internal/consumer/` succeeds; a Consumer built with no calendar prices on the window-and-weekend rule).
- Every one of the **18** `Compute` call sites in `internal/pricing/pricing_test.go` (grep-derived) is updated and passes a zero `Calendar{}`; the **5** assertion sites of the old `IsPeak` tests across **4** functions become method calls on a zero calendar, **unchanged in expectation**.
- `go test ./internal/pricing/...` passes. `go build ./...` is **not** expected to succeed (analyze breaks on the removed symbol) — see the transient-break note above.

**Test Specifications**:

`internal/pricing/pricing_test.go`:

1. Mechanical edits (regression guard, no behaviour change): the **18** `Compute(...)` call sites — grep `Compute(` under `internal/pricing/pricing_test.go` and edit every one — gain a trailing `Calendar{}`. Lines as read on `4ed8cc8`: `24, 35, 46, 62, 78, 103, 125, 131, 139, 150, 173, 252, 253, 269, 273, 281, 289, 297`.
2. Mechanical edits: the old package-level `IsPeak(...)` assertions become `Calendar{}.IsPeak(...)` — **5** assertion sites across **4** functions (`pricing_test.go:207`, `:220`, `:232`, `:235`, `:244`; in `TestIsPeakBoundariesWeekday`, `TestIsPeakWeekendNeverPeaks`, `TestIsPeakUsesUTCNotLocal`, `TestIsPeakZeroTimeIsOffPeak`). Every `want` stays as written — the zero calendar *is* the old rule (bead 01).
3. New `TestComputeHolidayIsOffPeak`: a `Calendar` with an `offPeak` date priced at `02:00 UTC` on that date is exactly `1×` the same usage's off-peak amount, while the *same* usage on the same weekday one week later (unconfigured) is exactly `PeakMultiplier×`. Integer equality on the resulting micro-dollars, in the style of `TestComputePeakIsExactlyDoubleOffPeak` (`pricing_test.go:249-260`).
4. New `TestComputeWorkDayIsPeakInsideWindowOnly`: a `Calendar` with a `work` date (a Saturday) prices a window instant at `PeakMultiplier×` and an off-window instant (`20:00 UTC`) at `1×`.

`internal/consumer/consumer_test.go` (authored here; first runs at br-GI-24-06):

5. A holiday-priced row: build a Consumer, `SetCalendar` with an `offPeak` holiday date, `SetPriceTable` with a fixed rate, and insert a call whose `StartedAt` is a peak-window instant on that date; assert its stored `CostUSD` is the **1×** amount. Assert the same call at the same wall-clock on an adjacent ordinary weekday stores the **2×** amount. The paired assertion is what makes the calendar provably in force rather than coincidentally irrelevant.

**Files to Touch**:
- `internal/pricing/pricing.go` (modify — `Compute` signature, hoisted `peak`, delete package-level `IsPeak`)
- `internal/pricing/pricing_test.go` (modify — 18 `Compute` sites, 5 `IsPeak` assertions, holiday/work-day cases)
- `internal/consumer/consumer.go` (modify — `calendar` field, `SetCalendar`, the `Compute` call at `:430`)
- `internal/consumer/consumer_test.go` (modify — the holiday-priced row)
