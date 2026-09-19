### Bead 3: `feat` config: `off_peak_dates` and `work_dates`

- **Bead ID**: br-GI-24-03
- **Priority**: P0 (critical)
- **Original Estimate**: 1.5h
- **Dependencies**: br-GI-24-01
- **Blocks**: br-GI-24-06, br-GI-24-07

**Description**:

Two new configuration keys carrying the 2026 statutory calendar, following the `ModelMap` / `ModelMaxTokens` precedent (`internal/config/config.go:26`, `:36`, `:128-129`, `:204-209`, `:237-238`, `:81-82`) in *shape* — a `Default*` const, a `LENS_*` env name, a `config.toml` key, a flag — but **not** in failure mode: a malformed date is fatal, where a bad `ModelMap` entry is skipped leniently.

**1. The shipped defaults.** Two consts beside `DefaultModelMap`:

```go
// DefaultOffPeakDates is the 2026 statutory-holiday set — DeepSeek bills these
// days off-peak for the whole day. Source: 国办发明电〔2025〕7号 (2025-11-04),
// re-checked date by date. DeepSeek's own treatment of 调休 (WorkDates) is
// unverified; see the README.
const DefaultOffPeakDates = "2026-01-01..2026-01-03,2026-02-15..2026-02-23,2026-04-04..2026-04-06,2026-05-01..2026-05-05,2026-06-19..2026-06-21,2026-09-25..2026-09-27,2026-10-01..2026-10-07"

// DefaultWorkDates is the 2026 调休 make-up work-day set. Every one falls on a
// weekend, so WorkDates is the only way to express it.
const DefaultWorkDates = "2026-01-04,2026-02-14,2026-02-28,2026-05-09,2026-09-20,2026-10-10"
```

The seven `DefaultOffPeakDates` ranges are the notice's own ranges (元旦, 春节, 清明, 劳动节, 端午, 中秋, 国庆); the six `DefaultWorkDates` are the 调休 days. Do not expand ranges into individual dates — the default's job is to read like the notice it cites, and `DateSets()` echoes it verbatim.

**2. `Config` gains two fields** (`config.go:39-62`): `OffPeakDates string` and `WorkDates string`. `Default()` (`config.go:65-85`) sets both to their `Default*` const.

**3. Every input path**, matching `ModelMap` exactly: two entries in `fieldsByEnv` (`LENS_OFF_PEAK_DATES` → `OffPeakDates`, `LENS_WORK_DATES` → `WorkDates`); two `applyKV` cases keyed by the **Config field names** (`"OffPeakDates"`, `"WorkDates"` — `applyKV` switches on field names, so the `config.toml` keys are the CamelCase names, not the snake-case wire names); two flags in `applyFlags` (`config.go:237`, `fs.StringVar(&cfg.OffPeakDates, "off-peak-dates", …)` and `fs.StringVar(&cfg.WorkDates, "work-dates", …)`).

**4. `Validate` rejects a malformed calendar** — a **single** call passing both strings, which is what makes the both-sets contradiction reachable:

```go
if _, err := pricing.NewCalendar(c.OffPeakDates, c.WorkDates); err != nil {
	return fmt.Errorf("config: validate: %w", err)
}
```

This is a new `config → pricing` import edge. It is safe — `pricing` imports only `parse` (and bead 01 adds nothing) — and it buys one grammar rather than a second date parser in `config`. Record it in the import block's comment so the reviewer sees it rather than discovering it.

`Validate` is the failure point (`config.go:278-318`), not `Load` (`Load` never calls `Validate`; `serve.go:39` and `doctor.go:95` call it explicitly). So `serve` refuses to start on a bad date and `doctor` reports it as a `config_valid` FAIL; commands that never call `Validate` (`ls`, `purge`, `replay`, …) are unaffected.

**Rationale**:

D2/§2.2. The calendar cannot be computed — it is administratively declared each year — so it must be data, and the data must be editable in one line without a code change, which is the whole reason `ModelMap` lives in config.

**The fatal-vs-lenient divergence is deliberate.** A skipped model mapping degrades to a fallback that is visible in the dashboard's model column; a silently skipped holiday date is a silent 2× overcharge on exactly the days this story exists to fix. Lenient parsing here would reintroduce the bug in a harder-to-see form, so `Validate` refuses a typo at startup, the same treatment `BodyPolicy` gets (`config.go:285-289`).

**Outcome Definition**:

- `OffPeakDates` / `WorkDates` resolve through flag > env > file > default, exactly like `ModelMap`.
- The defaults are the two strings above; with no config, `pricing.NewCalendar(cfg.OffPeakDates, cfg.WorkDates)` succeeds and `Covers(2026)` is true.
- `Validate` rejects a malformed date with the offending item named, and rejects a date present in both sets with the date named.
- A rejection prevents `serve` from starting (it calls `Validate`) but does not affect a command that never calls `Validate`.
- `go test ./internal/config/...` passes.
- `go build ./...` still succeeds after this bead (no other package breaks — this bead only adds).

**Test Specifications**:

`internal/config/config_test.go` (mirror the `TestRetentionDaysPrecedence` shape at `:273-320` and the `Validate` style at `:216-222`):

1. **Precedence table for each key**: file only; env overriding file; flag overriding env; nothing set → the default const. Assert the resolved string equals what was supplied (the grammar is not re-parsed by `Load`).
2. **Defaults parse**: `pricing.NewCalendar(config.DefaultOffPeakDates, config.DefaultWorkDates)` returns no error, and the returned calendar `Covers(2026)` is true (the default actually names the current year).
3. **Malformed date rejected**: `cfg.OffPeakDates = "2026-1-1"` (and a reversed range, and a trailing comma) each make `cfg.Validate()` return an error naming the offending item; the case targets **`Validate`, not `Load`**, since `Load` does not call it.
4. **Both-sets contradiction rejected**: a date in both `cfg.OffPeakDates` and `cfg.WorkDates` makes `Validate` fail, naming the date. This is the case that only passes because `Validate` passes both strings in one `NewCalendar` call.
5. **A valid custom calendar passes** `Validate` (e.g. a single off-peak date and a single work date).

**Files to Touch**:
- `internal/config/config.go` (modify — two `Default*` consts, two `Config` fields, two env names, two `applyKV` cases, two flags, one `Validate` check, the `pricing` import)
- `internal/config/config_test.go` (modify — the cases above)
