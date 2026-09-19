### Bead 5: `feat` api+web: the per-session peak rollup

- **Bead ID**: br-GI-24-05
- **Priority**: P0 (critical)
- **Original Estimate**: 2h
- **Dependencies**: br-GI-24-01, br-GI-24-02
- **Blocks**: br-GI-24-06, br-GI-24-08

**Description**:

The dashboard can answer "was this session expensive because of the work, or because of the clock?" — computed at read time, with **no schema change**.

**1. `api` installs the calendar through its optional-setter seam.** Add a `calendar pricing.Calendar` field to `api` (`internal/api/api.go:68-100`, beside `pricePath`) and a setter mirroring `SetPricing`/`SetRetention` (`api.go:102-115`):

```go
// SetCalendar installs the calendar the session rollup reads and the
// effective date sets GET/POST /api/prices shows. Leaving it unset is a
// supported state: the rollup then applies the window-and-weekend rule with
// no holidays (the zero calendar IS that rule), and /api/prices echoes ""/"".
func (a *api) SetCalendar(cal pricing.Calendar) { a.calendar = cal }
```

`api.New`'s signature and its **3** call sites are untouched. `internal/api` already imports `pricing` (`prices.go:10`).

**2. `sessionDetail` gains a peak rollup** (`api.go:910-926`):

```go
// sessionPeak is how much of a session's spend landed on DeepSeek's peak rate.
// Calls counts the calls PeakPriced accepted, not the calls merely inside the
// peak window: an unpriced call placed at peak is not "priced under peak
// hours" and must not be counted as though it were.
type sessionPeak struct {
	Calls   int     `json:"calls"`    // priced calls placed at peak; exact over the loaded rows
	CostUSD float64 `json:"cost_usd"` // those calls' cost; the share is CostUSD / Session.TotalCostUSD (see the cap note)
}

type sessionDetail struct {
	*store.Session
	Calls    []*store.Request `json:"calls"`
	Warnings []*store.Warning `json:"warnings"`
	Peak     sessionPeak      `json:"peak"`
}
```

**3. `getSession` computes it** (`api.go:928-971`). It already loads the session's calls before rendering (`api.go:944`), so the rollup is a loop over rows already in memory:

```go
var peak sessionPeak
for _, c := range calls {
	if a.calendar.PeakPriced(c.StartedAt, c.CostUSD) {
		peak.Calls++
		peak.CostUSD += *c.CostUSD
	}
}
```

Call the *same* predicate the warning uses (bead 04) — restating `IsPeak` here is the drift that would let a session's tagged count disagree with its own calls' badges.

**4. The rollup runs on the calendar in force NOW, and may disagree with a stored badge — deliberately.** Cost and `warnings` are frozen at ingest; the rollup is recomputed against today's calendar (D6/D8). So a row ingested before this fix on a 2026 holiday keeps its doubled `cost_usd` and its stored `peak_pricing` warning, while this rollup — run on the current calendar — counts it as not-peak. The header and that row's badge therefore disagree, and that is the intended answer, pinned by a test rather than hidden.

**5. `GET`/`POST /api/prices` carry the effective date sets** (`internal/api/prices.go`). `pricesResponse` gains:

```go
OffPeakDates string `json:"off_peak_dates"`
WorkDates    string `json:"work_dates"`
```

(values are the installed calendar's `DateSets()` — the verbatim echo). Thread the calendar into `renderPrices` (`prices.go:136`, called from GET at `:51` and POST at `:119`), so both render it; the wire names match the config keys (`work_dates`, not a "peak date"). With no calendar installed the fields are `""`/`""`.

**6. The web renders the rollup and the calendar.** `internal/web/app.js`'s `openSession` (`:763-829`) adds a rollup row to the session summary (`:775-784`) from `s.peak`; `loadSettingsPrices` (`:856+`) renders `off_peak_dates` / `work_dates` read-only.

**The zero-denominator rule:** a session can have `TotalCostUSD = 0` with calls (an all-unpriced session — the shape `store_test.go:829` asserts), and then `Peak.Calls` is 0 too, so a UI that divides `CostUSD / TotalCostUSD` renders `0/0`. The intended rendering is to **show the peak count and omit the share** (absent, not `NaN%`). Guard the division explicitly in JS.

**7. `index.html` region ownership — read this before editing.** This bead owns the **drill-down markup** and adds a **new** element for the Settings calendar display. It must **not** rewrite the existing Settings `<p class="hint">` sentence at `index.html:150-154` — that sentence ("…where the upstream signals a peak window") is corrected by **br-GI-24-08**. Add the calendar display as a new sibling element below that paragraph (e.g. a new `<dl id="settings-calendar">` / new `<p id="settings-calendar">`), leaving lines 150-154 untouched so the two beads' edits do not collide.

**Rationale**:

D5/D6. A per-call signal exists but the `sessions` table carries only `warning_count`, so the user cannot tell peak-driven spend from work-driven spend — the only actionable question peak pricing raises. A schema change is impossible (§3: `schema.sql` is `CREATE TABLE IF NOT EXISTS` only, and `doctor.go:133` reports no migration mechanism), so the rollup is computed over the calls `getSession` already loads. Reusing `PeakPriced` rather than re-deriving is what keeps the header consistent with the per-row badges.

**Outcome Definition**:

- `api.SetCalendar` exists; `api.New`'s signature and its 3 call sites are unchanged.
- `GET /api/sessions/{id}` includes `peak.calls` and `peak.cost_usd`.
- `peak.calls` counts exactly the calls `PeakPriced` accepted (an unpriced-at-peak call counts zero).
- A session whose only peak row was ingested under a different calendar than the one now installed yields `peak.calls = 0` (the documented cross-calendar split).
- `GET`/`POST /api/prices` include `off_peak_dates` / `work_dates` echoing the installed calendar's `DateSets()` verbatim; both are `""` when no calendar is installed.
- The drill-down shows the peak count; when `TotalCostUSD` is 0 the share is omitted, never `NaN%`.
- `go test ./internal/api/...` passes.

**Test Specifications**:

`internal/api/api_test.go` (drill-down rollup cases; build the handler via `newTestAPI` and `handler.SetCalendar(...)`):

1. **Mixed peak/off-peak**: a session with one peak-window call on a plain weekday and one off-window call → `peak.calls = 1`, `peak.cost_usd` equals that call's cost (micro-dollar exact).
2. **All-peak**: every call in the window → `peak.calls` equals the call count.
3. **Unpriced at peak counts zero**: a peak-instant call with `CostUSD == nil` is not counted — the case that would pass if the rollup restated `IsPeak` instead of calling `PeakPriced`.
4. **Empty session**: no calls → `peak.calls = 0`, `peak.cost_usd = 0`.
5. **All-unpriced session** (`TotalCostUSD = 0`, calls = N, none priced): `peak.calls = 0` and the share is a defined absent/zero, not `NaN`. The response body must not contain `NaN`.
6. **Cross-calendar split (D6/D8)**: seed a row whose stored `peak_pricing` warning exists but whose `StartedAt` the currently-installed calendar reads as *not* peak (or the reverse) → `peak.calls` reflects the *current* calendar, and the test asserts the disagreement is intentional rather than treating it as a bug.

`internal/api/prices_test.go` (mirror `TestGetPricesChartShape` at `:32-60`):

7. With `handler.SetCalendar(pricing.Calendar)` built from a known pair of strings, both `GET` and `POST /api/prices` return `off_peak_dates` / `work_dates` equal to those strings **verbatim** (a range stays a range). With no calendar installed, both are `""`.

**Files to Touch**:
- `internal/api/api.go` (modify — `calendar` field, `SetCalendar`, `sessionPeak`, `sessionDetail.Peak`, `getSession` rollup)
- `internal/api/api_test.go` (modify — rollup cases 1–6)
- `internal/api/prices.go` (modify — `pricesResponse` fields, calendar into `renderPrices`)
- `internal/api/prices_test.go` (modify — the two new fields)
- `internal/web/app.js` (modify — rollup row in `openSession`, read-only calendar in Settings, zero-denominator guard)
- `internal/web/index.html` (modify — drill-down markup; a **new** element for the calendar display; do not touch `:150-154`)
