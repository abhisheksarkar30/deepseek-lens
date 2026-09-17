### Bead 3: `GET /api/stats` gains `granularity`/`until`, `parseTimeBoundParam`, and the `by_period` response rename

- **Bead ID**: br-GI-21-03
- **Priority**: P0 (critical)
- **Original Estimate**: 2h
- **Dependencies**: br-GI-21-01, br-GI-21-02
- **Blocks**: br-GI-21-05, br-GI-21-06, br-GI-21-07

**Description**:

1. **Extract `parseTimeBoundParam`.** `parseSinceParam` (`api.go:254-266`) reads `?since` as a Go
   duration or RFC3339 timestamp. Pull its body into
   `parseTimeBoundParam(r *http.Request, key string) (time.Time, error)`, reading
   `r.URL.Query().Get(key)` instead of the hardcoded `"since"`. `parseSinceParam(r)` becomes a
   one-line wrapper: `return parseTimeBoundParam(r, "since")`. Its three other call sites —
   `listRequests` (`api.go:291`), `warningsSummary` (`api.go:774`), `listWarnings` (`api.go:802`) —
   are **untouched**; do not modify those three functions in this bead. Only `stats()` gains a second
   call, `parseTimeBoundParam(r, "until")`.

2. **`parseGranularityParam(r) (string, error)`**: reads `?granularity=`, defaults to `"day"` when
   absent, and returns an error for anything outside `{"hour", "day", "week", "month"}`. This is the
   API-layer half of D1's two-layer validation — the request never reaches `StatsByPeriod`'s own
   whitelist switch (br-GI-21-02) with an unvalidated value.

3. **`Store` interface** (`api.go:31-37`): update the four signatures to match br-GI-21-01/02:

   ```go
   StatsSummary(ctx context.Context, since, until time.Time) (*store.Summary, error)
   StatsByModel(ctx context.Context, since, until time.Time) ([]store.ModelStat, error)
   StatsByPeriod(ctx context.Context, since, until time.Time, granularity string) ([]store.PeriodStat, error)
   StatsByCostSource(ctx context.Context, since, until time.Time) ([]store.CostSourceStat, error)
   ```

4. **`statsResponse`** (`api.go:712-722`): add `Until time.Time \`json:"until"\``, rename
   `ByDay []store.DayStat \`json:"by_day"\`` to `ByPeriod []store.PeriodStat \`json:"by_period"\``, add
   `Granularity string \`json:"granularity"\``. No deprecation shim for the rename — the dashboard is
   the only consumer of its own API (R4).

5. **`stats()` handler** (`api.go:724-755`): parse `since` (unchanged), `until` (new, via
   `parseTimeBoundParam(r, "until")`), `granularity` (new, via `parseGranularityParam`). A parse
   failure on either time bound, or an invalid `granularity`, is `400` via the existing
   `writeError(w, http.StatusBadRequest, err.Error())` pattern, before any store call is made. Pass
   `since, until` to `StatsSummary`/`StatsByModel`/`StatsByCostSource`; pass
   `since, until, granularity` to `StatsByPeriod`. Populate the response with
   `Until: until, ByPeriod: byPeriod, Granularity: granularity`. **Do not** reject `since > until` —
   it is a well-formed empty window (every field legitimately reports zero rows), not an error
   condition.

**Rationale**:

D3/D7 — `until` becomes a real, independently-settable second bound alongside `since`, letting the UI
express an arbitrary past window (not just "the last N units up to now"), while one shared parse
helper keeps `since`/`until` parsing from drifting apart. The response echoes back what was actually
applied (`since`/`until`/`granularity`), the same "tell the client what was applied" pattern `X-Limit`
already uses on the paginated routes.

**Outcome Definition**:

- `GET /api/stats?granularity=<invalid>` and `GET /api/stats?until=<unparseable>` both return `400`
  with the existing `{"error": "..."}` shape, and reach no store call.
- `GET /api/stats` with no params behaves exactly as before this change: all-time window,
  `granularity` defaults to `"day"`.
- `GET /api/stats?until=...` narrows `summary`, `by_model`, `cost_sources`, and `by_period` together
  — all four reflect the same `[since, until)` window.
- Response body echoes `since`, `until`, and `granularity` as applied.
- `since > until` returns `200` with every field's window legitimately empty.
- `go build ./internal/api/...` and `go test ./internal/api/...` succeed. (`internal/cli` remains
  broken until br-GI-21-04 lands — expected, not a regression in this bead.)

**Test Specifications**:
- Unit Tests (`internal/api/api_test.go`):
  - One case per granularity value (`hour`/`day`/`week`/`month`): response's `by_period` shape and
    echoed `granularity` match.
  - Invalid `granularity` → `400`; a follow-up valid request still sees previously-seeded data
    (proving the invalid request never partially executed a store call).
  - Malformed `until` (e.g. `?until=notatime`) → `400`.
  - Both `since`/`until` absent → behaves exactly as before this change (regression case protecting
    the default).
  - An explicit `until` narrows `summary`, `by_model`, and `cost_sources` together with `by_period` —
    seed rows before and after the bound, assert all four fields reflect only the "before" rows. This
    is the test that would catch D7's helper being wired into only one of the six query sites.
  - `since > until` → `200` with every field's window empty, not `400`.

**Files to Touch**:
- `internal/api/api.go` (modify)
- `internal/api/api_test.go` (modify)
