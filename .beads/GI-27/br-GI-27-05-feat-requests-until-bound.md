### Bead 5: `feat` `Filter.Until` + `/api/requests?until` (F backend)

- **Bead ID**: br-GI-27-05
- **Priority**: P1 (high)
- **Original Estimate**: 1h
- **Dependencies**: None
- **Blocks**: br-GI-27-07

**Description**:

§11B backend gap. `/api/requests` and the feed take `since` only — there is **no `until`**, and the Stats tab has date-only From/To. claude-lens's backend already took both; deepseek-lens needs the small addition the plan calls out.

**1. `internal/store/types.go` — `Filter.Until`.** The struct is at `:126`, `Since time.Time` at `:129`. Add `Until time.Time` (zero = unbounded).

**2. `internal/store/store.go` — `requestWhere`.** The predicate builder is at `:327` (and is **shared** with `CountRequests` at `:426`, which is why `X-Total-Count` stays consistent with the ranged list — the reason it is shared). Add `Until` → `started_at < ?` (half-open), applied only when non-zero, beside the existing `Since` clause.

**3. `internal/api/api.go` — `?until`.** `listRequests` reads only `?since` today (via `parseSinceParam` → `parseTimeBoundParam`, `:268`, `:282-286`). Read `?until` with the **same** `parseTimeBoundParam` and put it on the `Filter`. `since >= until` is **not** rejected — this matches `/api/stats`' documented behaviour; an empty range simply returns no rows.
- `warnings` and `sessions` routes stay `since`-only — **out of scope**, do not touch them.
- `X-Total-Count` comes from the shared `requestWhere` through `CountRequests`, so it automatically reflects the range — assert this (F acceptance).

**Rationale**:

§11B. The Feed Range control (bead 07) and the Stats hour-precision pickers (bead 06) both need a closed `[from, to)` window; without `Until` the API can express only an open-ended lower bound. Sharing `requestWhere` with `CountRequests` is what keeps the pager's total honest.

**Outcome Definition**:

- `Filter.Until` is applied as `started_at < ?` when non-zero; `CountRequests` honours it through the shared `requestWhere`.
- `GET /api/requests?until=…` returns only rows before the bound; combined with `?since` it returns the half-open window.
- An invalid `?until` → 400 (via `parseTimeBoundParam`); an absent `?until` leaves `Until` zero (unbounded).
- `since >= until` returns an empty list, not a 400.
- `X-Total-Count` equals the row count of the ranged list.
- `go build ./... && go vet ./... && go test ./...` pass.

**Test Specifications**:

- `internal/store/store_test.go`: `Until` bound is half-open (a row exactly at `until` is excluded, a row at `until-1ns` included), with and without a `Since`; `CountRequests` matches `ListRequests` length for the same `Filter`.
- `internal/api/api_test.go`: `?until` valid → 200; `?until=notatime` → 400; `?since=…&?until=…` → the windowed rows; `X-Total-Count` equals the ranged list length; `since > until` → 200 with an empty list (not 400).

**Files to Touch**:
- `internal/store/types.go` (modify — add `Until` to `Filter` at :126)
- `internal/store/store.go` (modify — `requestWhere` at :327)
- `internal/store/store_test.go` (modify)
- `internal/api/api.go` (modify — `listRequests` reads `?until` via `parseTimeBoundParam`)
- `internal/api/api_test.go` (modify)
