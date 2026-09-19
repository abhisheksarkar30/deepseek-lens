### Bead 7: `feat` cli: `doctor` reports the calendar and its coverage

- **Bead ID**: br-GI-24-07
- **Priority**: P1 (high)
- **Original Estimate**: 1.5h
- **Dependencies**: br-GI-24-03, br-GI-24-06
- **Blocks**: br-GI-24-09

**Description**:

The shipped default is a dated fact that goes stale in 2027 and then fails **silently, in the overstating direction** — the direction that erodes trust in every other number lens prints. It cannot be made self-updating, so the mitigation is to make its expiry *visible*.

**1. A new check reports coverage** (`internal/cli/doctor.go`, appended in `runChecks` beside `providerHookCheck` at `:135`). It builds a calendar from the resolved config and asks `Calendar.Covers(time.Now().Year())` (bead 01):

- today's year is named by either set → **PASS**;
- today's year is named by neither → **WARN** (never FAIL).

**It must be incapable of returning FAIL**, because `runDoctor` turns a FAIL into a non-zero exit (`doctor.go:41-45`, `:83-85`) and lens's correctness must not depend on a date the user has not yet updated. This rule is scoped to the coverage check, which is a different check from `config_valid`.

**A malformed date does reach this check** — `runChecks` short-circuits on nothing (`doctor.go:95-99`): it appends the `config_valid` FAIL and runs every later check in the same pass. So the coverage check must **guard its own `NewCalendar` construction**: if `pricing.NewCalendar` returns an error, it **skips and appends nothing** (the malformed string is already reported as `config_valid`), leaving it incapable of FAIL on any input, well-formed or not. Do not `return` early from `runChecks`, and do not append a second FAIL.

Sketch:

```go
if cal, err := pricing.NewCalendar(cfg.OffPeakDates, cfg.WorkDates); err == nil {
	year := time.Now().Year()
	if cal.Covers(year) {
		checks = append(checks, doctorCheck{"peak_calendar", statusPass, fmt.Sprintf("off-peak/work date sets cover %d", year)})
	} else {
		checks = append(checks, doctorCheck{"peak_calendar", statusWarn, fmt.Sprintf("no configured date set names %d — peak pricing may be overstating on that year's holidays", year)})
	}
} // err != nil: config_valid already FAILed; this check adds nothing
```

**2. The printed effective config includes the calendar** (`doctor.go:54-67`). Add two rows to `cfgRows`: `{"off_peak_dates", cfg.OffPeakDates}` and `{"work_dates", cfg.WorkDates}`. A calendar that changes how money is computed must be visible in the one command whose job is "what is this install actually configured to do".

**Rationale**:

D9/§8.2. The default is exactly like `ModelMaxTokens`'s `ponytail:`-marked placeholder ceilings — a dated fact. It cannot self-update, so `doctor` (the command for "what is this install actually configured to do") surfaces the state. The "never FAIL" invariant is the same one GI-4 §4.5's `provider_hooks` check follows, and it is the one place this story could make lens depend on user-entered data; the scoped guard is what prevents that.

**Outcome Definition**:

- `lens doctor` prints a check row for the calendar's coverage of the current year.
- On a config whose date sets name the current year the check is PASS; on one that names neither, WARN. Neither changes `runDoctor`'s return value.
- With well-formed dates the exit code is zero whether the year is covered or not; with a malformed date the exit is non-zero **only** because `config_valid` FAILs, and the coverage check appends no second FAIL (it is absent from the output).
- The effective-config table prints `off_peak_dates` and `work_dates`.
- `go test ./internal/cli/...` passes.

**Test Specifications**:

`internal/cli/cli_test.go` (mirror the `provider_hooks` cases around `:799-1040`, which call `runChecks` directly and assert `c.Status`):

1. **Covered year passes**: a config whose `OffPeakDates` names `time.Now().Year()` → the check is `statusPass`.
2. **Uncovered year warns**: a config whose sets name only a past year → the check is `statusWarn`.
3. **Never fails, and never changes the exit code**: `runDoctor` with an uncovered year returns a **nil** error (exit zero), while its output still carries the WARN row.
4. **Malformed date skips**: a malformed `OffPeakDates` → `runChecks` reports a `config_valid` FAIL, and the coverage check row is **absent** (not a second FAIL). Assert no `FAIL` row exists other than `config_valid`. This is the D9 invariant: the coverage check is incapable of FAIL on any input.
5. **Printed config**: `runDoctor`'s output contains the resolved `off_peak_dates` / `work_dates` values (assert for a non-default setting so a hardcoded line would fail — the shape of `TestDoctorReportsRetentionDays` at `:1368`).

**Files to Touch**:
- `internal/cli/doctor.go` (modify — the coverage check + two effective-config rows)
- `internal/cli/cli_test.go` (modify — the cases above)
