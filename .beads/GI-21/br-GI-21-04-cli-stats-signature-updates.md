### Bead 4: `lens stats`/`lens doctor` follow the store signature rename

- **Bead ID**: br-GI-21-04
- **Priority**: P0 (critical)
- **Original Estimate**: 0.5h
- **Dependencies**: br-GI-21-01, br-GI-21-02
- **Blocks**: None (independent of the web/docs beads; the last bead needed before `go build ./...`
  succeeds tree-wide)

**Description**:

1. **`internal/cli/stats.go`**: update the three store calls to the new signatures —
   `st.StatsSummary(ctx, sinceTime, time.Time{})` (`stats.go:54`),
   `st.StatsByModel(ctx, sinceTime, time.Time{})` (`stats.go:58`), and
   `st.StatsByPeriod(ctx, sinceTime, time.Time{}, "day")` (renamed from
   `st.StatsByDay(ctx, sinceTime)`, `stats.go:62`). `lens stats` has no `--until` flag and gains none
   here — `until` stays `time.Time{}` (unbounded) on every CLI call. `--by day` always requests
   granularity `"day"`, the only bucket size `lens stats` has ever shown.

2. **`statsJSON`** (`stats.go:27-35`): rename `ByDay []store.DayStat \`json:"by_day"\`` to
   `ByPeriod []store.PeriodStat \`json:"by_period"\``. This is a **breaking rename of `lens stats
   --json`'s output key** (R4), distinct from the HTTP API's `by_day`→`by_period` rename
   (br-GI-21-03) — both carry the same single-user/no-external-consumer justification, called out
   separately per the plan so the justification isn't silently borrowed across surfaces.

3. **Rendering loop** (`stats.go:152-167`, the `*by == "day"` branch): `d.Day` → `d.Period`
   (`stats.go:158`). The JSON-encode call (`stats.go:104-108`) updates `ByDay: byDay` →
   `ByPeriod: byPeriod`; the local Go variable populated at `stats.go:62` may keep its existing name
   or be renamed to `byPeriod` for clarity — either is fine, but the struct field and JSON tag must
   change.

4. **`internal/cli/doctor.go`**: `st.StatsSummary(context.Background(), time.Time{})`
   (`doctor.go:159`) → `st.StatsSummary(context.Background(), time.Time{}, time.Time{})`.

5. **No changes to `internal/cli/cli_test.go`.** `TestStatsTotalsMatchFixture` and
   `TestStatsByDayBreakdown` are black-box tests that call `runStats(args, w, st)` and assert on
   printed substrings (`"day split:"`, `"COST"`, `"$0.0300"`, etc.) — neither references
   `store.DayStat`/`store.PeriodStat`, a `.Day`/`.Period` field, or any store method directly.
   Confirm this by running them, not by editing them.

**Rationale**:

F1.1/D7 — these are the two call sites outside `internal/store`/`internal/api` that break on the
signature rename; both are one-line, no-behavior-change follow-ups, not new logic. Landing this bead
after br-GI-21-01/02 is what makes `go build ./...` succeed tree-wide again (the web beads don't
affect Go compilation).

**Outcome Definition**:

- `go build ./...` succeeds.
- `lens stats --by day` and `lens stats --json` produce the same output as before this ticket, modulo
  the `by_day`→`by_period` JSON key rename.
- `lens doctor` runs without error.

**Test Specifications**:
- Unit Tests: none new. Run `go test ./internal/cli/...` and confirm `TestStatsTotalsMatchFixture`
  and `TestStatsByDayBreakdown` still pass unmodified — this is the bead's verification gate.
- Integration Tests: manual `go run ./cmd/lens doctor` against a populated DB; confirm the printed
  summary is unchanged from before this ticket.

**Files to Touch**:
- `internal/cli/stats.go` (modify)
- `internal/cli/doctor.go` (modify)
