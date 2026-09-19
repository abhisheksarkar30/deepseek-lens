### Bead 1: `feat` pricing: the calendar type, date grammar, and holiday-aware window

- **Bead ID**: br-GI-24-01
- **Priority**: P0 (critical)
- **Original Estimate**: 2h
- **Dependencies**: None
- **Blocks**: br-GI-24-02, br-GI-24-03, br-GI-24-04, br-GI-24-05

**Description**:

Create `internal/pricing/calendar.go`. `internal/pricing` already owns money and the peak window (`Internal/pricing/pricing.go:41-49`'s package-level `IsPeak`), and it imports only `internal/parse` — the calendar is the window's other half, so it belongs beside it. This bead adds **no dependency** to the package and changes no existing symbol; it is pure addition.

Define:

```go
// Calendar decides peak vs off-peak for an instant: DeepSeek's peak window and
// weekend rule, plus two configured date sets that answer the rest-day
// question — an off-peak (rest-day) set and a work-day set.
//
// The zero value is meaningful and is exactly the pre-holiday behaviour: no
// dates configured, so IsPeak is the window-and-weekend rule alone.
type Calendar struct {
	offPeak map[string]bool // "2006-01-02" UTC → a rest day: off-peak for the whole day
	work    map[string]bool // "2006-01-02" UTC → a work day: peak inside the window, like any weekday (调休)

	// the two strings NewCalendar was given, kept so DateSets can echo them
	// back verbatim — the read-only Settings display (bead 05) and doctor's
	// print of the effective calendar (bead 07) both read this
	offPeakSrc string
	workSrc    string
}

// NewCalendar parses the two date sets. Both may be empty.
func NewCalendar(offPeak, work string) (Calendar, error)

// DateSets returns the two configured date strings exactly as supplied to
// NewCalendar — the verbatim echo, not a re-render of the parsed maps. A
// range comes back as its range ("2026-02-15..2026-02-23"), not expanded
// into nine dates, and the string matches config.toml / env / flag byte for
// byte. The zero calendar returns ("", "").
func (c Calendar) DateSets() (offPeak, work string)

// IsPeak reports whether t is billed at DeepSeek's peak rate.
func (c Calendar) IsPeak(t time.Time) bool

// Covers reports whether year is named by either configured date set. It
// exists for doctor's coverage check (bead 07), not for pricing — IsPeak
// never consults it.
func (c Calendar) Covers(year int) bool

// PeakPriced is the one predicate behind both the peak_pricing warning and
// the per-session rollup (D5): cost != nil && *cost > 0 && c.IsPeak(at).
func (c Calendar) PeakPriced(at time.Time, cost *float64) bool
```

**The date-list grammar** (the smallest thing that mirrors the source notice):

```
2026-01-01..2026-01-03,2026-02-15..2026-02-23,...
```

Comma-separated items, each `YYYY-MM-DD` or `YYYY-MM-DD..YYYY-MM-DD` **inclusive**, whitespace trimmed around the whole string and around each item. An empty (or all-whitespace) string is the empty set. Ranges are not a convenience — the State Council notice is seven ranges, so the shipped default (bead 03) reads like the notice it cites. Parse dates with Go's `time.Parse("2006-01-02", …)`, which already requires zero-padding.

**A malformed item is an error naming the offending item**, never a skipped entry. Malformed includes: a missing zero-pad (`2026-1-1`), a non-date, a reversed range (`A..B` with `A > B`), and an empty item from a trailing or doubled comma. **A date present in both sets is also an error**, naming the date — that is the one case where "last rule wins" would silently produce an arbitrary answer.

**Precedence inside `IsPeak`** — one question (is this a rest day?) followed by one window rule, resolved in this order, each step pinned independently by a test:

1. the date is in `offPeak` → **off-peak for the whole day**;
2. else the date is in `work` → apply the window rule, **skipping the weekend check** (a 调休 make-up day is a working day, and must beat rule 3 or it is inexpressible);
3. else Saturday or Sunday → **off-peak**;
4. else inside `01:00–04:00` or `06:00–10:00` UTC → **peak**;
5. else → **off-peak**.

The date key is the **UTC date** — `t.UTC().Format("2006-01-02")`. Record the reasoning as a comment in the code, not just the plan:

> Beijing date *D* spans `[D-1 16:00 UTC, D 16:00 UTC)`. Both peak windows sit inside the `00:00–16:00 UTC` half, so every peak window on Beijing date *D* carries UTC date *D*. The two keyings agree — and only *because* rules 1–3 make both sets window-scoped: outside the window a work day and an off-peak holiday give the same answer the weekday/weekend rule already gives, so the only instants either set can matter are peak-window instants, which never straddle the `16:00 UTC` boundary.

Mark that last condition with a `ponytail:` note — if DeepSeek ever moves a window across `16:00 UTC`, UTC keying and Beijing keying part ways.

**The zero-value obligation is the load-bearing part of this bead:** `Calendar{}` must reproduce the old package function byte for byte — weekend rule, both windows, UTC-not-local, zero-time-is-off-peak. Every mechanical edit in bead 02 depends on it.

**Do not define the 2026 date strings here.** `DefaultOffPeakDates` / `DefaultWorkDates` live in `internal/config` (bead 03), by the `ModelMap` precedent. This bead ships the type and the grammar only; its tests use inline literals.

**Rationale**:

D1/D2/D3/D4. DeepSeek publishes peak `01:00–04:00` and `06:00–10:00` UTC Mon–Fri and bills everything else off-peak *including statutory holidays* — so lens currently overcharges 19 weekdays of 2026 at 2×. The holiday calendar cannot be computed (it is administratively declared each year), so it must be data; this bead supplies the container and the grammar, and the container is deliberately the leaf package that already owns the window, so `IsPeak` has one home rather than a second copy that can drift.

The zero value's exact equivalence to the old rule is what lets 18 `Compute` call sites and 5 `IsPeak` assertions be edited mechanically in bead 02 without re-deriving each one's meaning.

**Outcome Definition**:

- `NewCalendar("", "")` returns a `Calendar` whose `IsPeak` is identical to the pre-GI-24 package-level `IsPeak` for every input, including Saturdays/Sundays, both windows' boundaries, a non-UTC location, and `time.Time{}`.
- `NewCalendar` accepts a single date, a range, several comma-separated items, and surrounding whitespace; an empty string is the empty set.
- `NewCalendar` returns an error, naming the offending item, for: a missing zero-pad, a non-date, a reversed range, and an empty item (trailing/double comma).
- `NewCalendar` returns an error, naming the date, when a date is present in both sets.
- `DateSets()` returns exactly the two strings passed to `NewCalendar`, verbatim (a range comes back as its range); the zero calendar returns `("", "")`.
- `Covers(y)` is true iff year `y` appears in either set's ranges; false otherwise.
- `PeakPriced(at, cost)` is `cost != nil && *cost > 0 && c.IsPeak(at)` — a nil cost and a zero cost both yield false.
- `go test ./internal/pricing/...` passes.

**Test Specifications**:

`internal/pricing/calendar_test.go` (new):

1. **Grammar**: one date; one range; several comma-separated items; surrounding whitespace (whole string and per item); the empty string as the empty set. A rejection table for each malformed shape above, asserting the error message **names the offending item**.
2. **Both-sets contradiction**: the same date in `offPeak` and `work` is rejected, naming the date.
3. **Precedence, each rule pinned independently** (D3):
   - a `work` date at `20:00 UTC` (off-window) is off-peak **while the same date at `02:00 UTC` is peak** — the case that catches a whole-day-peak regression;
   - a weekday holiday in `offPeak` at `02:00 UTC` is off-peak (it must beat rule 4);
   - a plain Saturday at `02:00 UTC` is off-peak;
   - a plain Wednesday at `02:00 UTC` is peak.
4. **UTC keying (D4)**: a holiday expressed in Beijing time on both sides of the `16:00 UTC` boundary classifies the same as its UTC-keyed date — `IsPeak` at `D-1 16:30 UTC`, `D 00:30 UTC`, `D 02:00 UTC`, `D 09:00 UTC` all return the holiday's answer. **And the counterexample the corrected model must answer off-peak**: `2026-02-14` (a 调休 Saturday) at `16:30 UTC` is off-peak, matching the Beijing-keyed `2026-02-15 00:30` holiday reading.
5. **Zero-calendar equivalence**: the four shapes the existing `TestIsPeak*` functions cover — window boundaries, weekend, UTC-vs-local, `time.Time{}` — re-asserted against `Calendar{}`, unchanged in expectation. This is the regression guard that makes bead 02's mechanical edits safe.
6. **`Covers`**: true for a year named by either set, false for a year named by neither.
7. **`PeakPriced`**: `nil` cost → false; a pointer to `0` → false; a positive cost at a peak instant → true; a positive cost off-peak → false.

**Files to Touch**:
- `internal/pricing/calendar.go` (create)
- `internal/pricing/calendar_test.go` (create)
