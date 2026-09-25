### Bead 4: `feat` `StatsByPeriod` tz offset + `/api/stats?tz_offset` (A.2 backend)

- **Bead ID**: br-GI-27-04
- **Priority**: P1 (high)
- **Original Estimate**: 1.5h
- **Dependencies**: None
- **Blocks**: br-GI-27-06

**Description**:

RC-2 / A.2. `Store.StatsByPeriod` buckets with UTC `strftime` and the Stats picker sends UTC-midnight bounds (`utcDayBound`). A usage page is read against a calendar day in the operator's zone, so a daily total from lens can never equal it unless that zone is UTC. Add a fixed-offset shift. Which zone DeepSeek's page uses is **unverified** — hence a selector (bead 06), not a hardcoded choice.

**Precondition:** the A.1/A.2 prototype is stashed (bead 02 step 0, plan §13). Do not `git stash pop`.

**1. `internal/store/store.go` — `StatsByPeriod(..., tzOffsetMin int)`.** The signature is currently `StatsByPeriod(ctx, since, until, granularity) ([]PeriodStat, error)` (`:571`). Add a trailing `tzOffsetMin int` (minutes east of UTC). Shift `started_at` by `tzOffsetMin*60` seconds inside the `strftime` call so the day/period boundary is cut in the requested zone. Add a `ponytail:` note naming the ceiling: **fixed offset, DST is an hour off across a change** (accepted). Update every caller to pass an offset.

**2. `internal/api/api.go` — `?tz_offset=`.** The `/api/stats` handler reads its params and calls `StatsByPeriod`. Add `tz_offset` = minutes east of UTC, **default 0**, parsed and **rejected outside −720..840** with a 400. Bind it as a query parameter. `api.go` already has `parseTimeBoundParam` (`:268`) for the time bounds; add a small `parseTzOffsetParam` sibling (default 0, range-checked) or inline the parse — one helper, used once.
- **Security:** the value is range-checked and bound as a parameter (plan §9); it is never interpolated into SQL.

**3. `internal/cli/stats.go` — pass `0`.** `lens stats` stays UTC. Add a comment recording the **known, accepted divergence**: the CLI buckets in UTC while the dashboard defaults to Local, so the two show different daily totals for the same data when the zone is not UTC. **No `--tz-offset` flag** (plan F1.6 override) — the active zone is labelled on the dashboard instead (bead 06).

**Rationale**:

RC-2/A.2/§3. `StatsByPeriod`'s UTC bucketing makes a lens daily total structurally unable to match a usage page read in the operator's zone. A fixed offset (rather than an IANA zone) is the deliberate small scope: it needs no tz database and covers the operator's zone; DST is the accepted hour-level error.

**Outcome Definition**:

- `StatsByPeriod` takes `tzOffsetMin` and cuts periods in the offset zone; offset 0 reproduces today's UTC bucketing exactly.
- `/api/stats` accepts `?tz_offset=` minutes east of UTC, default 0, 400 outside −720..840; the offset reaches the store.
- `lens stats` passes 0 and its output is unchanged.
- `go build ./... && go vet ./... && go test ./...` pass.

**Test Specifications**:

- `internal/store/store_test.go`: two rows at `20:00Z` and `02:00Z` (different UTC days) bucket into **one** period with `tzOffsetMin = 330` (IST) and into **two** with `0` — the RC-2 assertion, with the UTC case as the control.
- `internal/store/store_test.go`: offset 0 equals the pre-change bucketing for a fixed fixture.
- `internal/api/api_test.go`: `?tz_offset=abc` → 400; `?tz_offset=900` → 400 (out of range); `?tz_offset=-900` → 400; `?tz_offset=330` → 200 and the buckets reflect the offset; no `tz_offset` → 200 (UTC default).

**Files to Touch**:
- `internal/store/store.go` (modify — `StatsByPeriod` signature at :571 and its `strftime`; update callers)
- `internal/store/store_test.go` (modify)
- `internal/api/api.go` (modify — `/api/stats` handler parses `tz_offset`)
- `internal/api/api_test.go` (modify)
- `internal/cli/stats.go` (modify — pass 0; divergence comment)
