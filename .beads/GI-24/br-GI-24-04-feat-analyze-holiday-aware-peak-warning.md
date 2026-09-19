### Bead 4: `feat` analyze: the holiday-aware `peak_pricing` warning

- **Bead ID**: br-GI-24-04
- **Priority**: P0 (critical)
- **Original Estimate**: 1.5h
- **Dependencies**: br-GI-24-01, br-GI-24-02
- **Blocks**: br-GI-24-06, br-GI-24-08

**Description**:

The rule engine learns the calendar, and the warning's sentence stops claiming a Mon–Fri window it may not have fired under.

**1. `Rules` gains a `calendar` field, `NewRules` gains a third parameter** (`internal/analyze/analyze.go:52-56`, `:68`):

```go
type Rules struct {
	modelMap  []modelMapping
	fallback  string
	maxTokens map[string]int
	calendar  pricing.Calendar
}

func NewRules(modelMap, maxTokens string, calendar pricing.Calendar) Rules { … }
```

`analyze` already imports `pricing` (`rules.go:10`), so no import changes. `Rules` is a value with its config-resolved tables baked in, and the calendar is a third such table — that is why it is a constructor argument and not a setter (the install point is compile-enforced: once the parameter exists, no caller can forget it).

`defaultRules` (`analyze.go:89`, the in-package `NewRules` call) passes the **zero calendar** and so keeps its exact current behaviour:

```go
var defaultRules = NewRules(config.DefaultModelMap, config.DefaultModelMaxTokens, pricing.Calendar{})
```

**2. `rulePeakPricing` calls the shared predicate, and its sentence describes the rule** (`internal/analyze/rules.go:258-267`):

```go
func rulePeakPricing(in ruleInput) []store.Warning {
	if !in.opts.calendar.PeakPriced(in.req.StartedAt, in.req.CostUSD) {
		return nil
	}
	return []store.Warning{warn(KindPeakPricing, sevWarn,
		"this call landed inside DeepSeek's peak-pricing window (01:00-04:00 or 06:00-10:00 UTC, on a working day); cost_usd reflects the 2x peak rate", "")}
}
```

`PeakPriced` (bead 01) is the *same* predicate the per-session rollup (bead 05) calls, so the row badge and the session header cannot disagree under one calendar. The `cost != nil && *cost > 0` gate is preserved exactly — it was already the first test in this rule.

**The hardcoded sentence at `rules.go:266` must change**, from `"(01:00-04:00 or 06:00-10:00 UTC, Mon-Fri)"` to a working-day phrasing. The moment a 调休 make-up day is honoured, the rule fires on a **Saturday**; "Mon-Fri" is then false on the day it is shown. A holiday in `OffPeakDates` is *not* the case — such a day is off-peak all day (bead 01, rule 1), so the rule never fires on one.

**3. `KindPeakPricing`'s sentence describes the rule too** (`internal/analyze/kinds.go:91-94`). Replace "01:00-04:00 or 06:00-10:00 UTC (Mon-Fri)" with working-day phrasing so a make-up Saturday does not contradict it. Keep the cell markdown-safe: no `|`, no leading `>`, no newline.

**4. `README.md`'s `peak_pricing` row changes with it, in this bead.** `readme_test.go` enforces **equality** between `AllKinds()` descriptions and the README's warning table, asserting each `KindInfo.Description` appears verbatim in the table. Change `kinds.go` alone and the test fails; change both here. Edit **only** the `peak_pricing` row at `README.md:103` (inside the `<!-- BEGIN warning kinds -->` … `<!-- END warning kinds -->` markers at `:89`/`:105`) — br-GI-24-08 owns the peak-pricing *prose* lower down (`:143-154`) and this bead must not touch it.

**5. Transient break, expected and repaired in the graph.** `NewRules`' third parameter breaks its **4 external call sites** — `internal/cli/serve.go:85`, `internal/cli/replay_test.go:93`, `internal/replay/replay_test.go:107`, and `internal/consumer/consumer_test.go:91` — all owned by br-GI-24-06. So after this bead the tree does not compile outside `internal/analyze` + `internal/pricing`. This bead's gate is:

```
go test ./internal/analyze/...
```

(The four external sites are the plan's deliberate split; do **not** update them here, or br-GI-24-06's file list becomes wrong.)

**Rationale**:

D5/§1/§2.3. `peak_pricing` currently fires from a package-level `IsPeak` that knows nothing of holidays, so it fires on a 调休 Saturday and its sentence claims a Mon–Fri window the call was not billed under. `PeakPriced` is lifted into `pricing` so warning and rollup share one predicate; the sentence is rewritten so it stays true on the day it fires. The README row is rewritten in the same bead because `readme_test.go` ties them by construction.

**Outcome Definition**:

- `analyze.NewRules` takes three arguments; `Rules` carries a `calendar`.
- `rulePeakPricing` fires iff `in.opts.calendar.PeakPriced(in.req.StartedAt, in.req.CostUSD)` — a `nil` cost or a zero cost raises nothing.
- On a `Calendar` with a National-Day weekday in `OffPeakDates`, `peak_pricing` does **not** fire on that day's peak-window instant.
- On a `Calendar` with a 调休 Saturday in `WorkDates`, `peak_pricing` **does** fire on that day's peak-window instant, and the fired `Detail` does **not** contain the string `Mon-Fri`.
- `AllKinds()`'s `KindPeakPricing` sentence and the README's `peak_pricing` row are equal (readme_test.go passes).
- `go test ./internal/analyze/...` passes. `go build ./...` is **not** expected to succeed (the four external `NewRules` sites break until br-GI-24-06) — do not treat that as a regression here.

**Test Specifications**:

`internal/analyze/analyze_test.go`:

1. Mechanical edits: the **3** in-package `NewRules` call sites in this file (`:524`, `:542`, `:555`) gain a calendar argument (use `pricing.Calendar{}` unless a case needs dates). The full grep-derived set of **8** `NewRules` call sites is: `analyze.go:89` + `analyze_test.go:524/542/555` here, and `serve.go:85` + `internal/cli/replay_test.go:93` + `internal/replay/replay_test.go:107` + `internal/consumer/consumer_test.go:91` at bead 06.
2. **Holiday weekday fires nothing**: a `Calendar` with `2026-10-01` in `OffPeakDates`; a request with a non-nil positive `CostUSD` and `StartedAt` inside the window on that date → no `peak_pricing` warning.
3. **调休 Saturday fires**: a `Calendar` with `2026-02-14` in `WorkDates`; the same request on that Saturday inside the window → one `peak_pricing` warning, and its `Detail` does **not** contain `Mon-Fri` (the §1/§2.3 assertion, satisfiable only because the sentence was rewritten).
4. **Unpriced at peak raises nothing**: a `nil` `CostUSD` and a pointer-to-`0` `CostUSD`, each at a peak instant on a plain weekday → no warning (the `PeakPriced` gate).
5. **Zero calendar is unchanged**: `pricing.Calendar{}` on a plain Wednesday inside the window still fires; on a weekend it does not. This is the regression guard for the in-package `defaultRules`.

`internal/analyze/readme_test.go`: no edit — it passes once `kinds.go` and the README row change together (item 4).

**Files to Touch**:
- `internal/analyze/analyze.go` (modify — `Rules.calendar`, `NewRules` third parameter, `defaultRules`)
- `internal/analyze/rules.go` (modify — `rulePeakPricing` uses `PeakPriced`, sentence rewritten)
- `internal/analyze/kinds.go` (modify — the `KindPeakPricing` sentence)
- `internal/analyze/analyze_test.go` (modify — 3 `NewRules` sites + holiday cases)
- `README.md` (modify — the `peak_pricing` row at `:103` only)
