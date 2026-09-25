### Bead 14: `feat` reload gains the `HotDays` live-apply arm, read by the maintenance ticker

- **Bead ID**: br-GI-27-14
- **Priority**: P1 (high)
- **Original Estimate**: 1h
- **Dependencies**: br-GI-27-10, br-GI-27-12
- **Blocks**: None

**Description**:

B.4 (the `HotDays` half). Bead 10 created the atomic-guarded live-config struct with the `RetentionDays` arm; bead 12 added the `HotDays` config key and the maintenance goroutine body. This bead joins them: `HotDays` becomes a second live-applied field, and the maintenance ticker reads it from the shared struct.

**1. `internal/api/livecfg.go`** — add `HotDays` as the struct's second field (bead 10 designed it to take one).

**2. `internal/api/api.go`** — the reload diff treats `HotDays` as **live-applied** (like `RetentionDays`), not `restart_required`. Apply both fields under the one lock (all-or-nothing).

**3. `internal/cli/serve.go`** — the maintenance ticker's archiver step (bead 12) reads `HotDays` from the shared struct each pass, so a reload takes effect on the next tick without a restart.

**4. Acceptance — the reload diff test must cover both classes:**
- `HotDays` (and `RetentionDays`) are **live-applied** — after a reload, the ticker sees the new value.
- The **non-live set** — `BodyCapBytes` and the others (addresses, upstream, DB path, body policy, capture flag, calendar keys) — lands under **`restart_required`**.

**Rationale**:

B.4/F4.1. Splitting the `HotDays` arm out of bead 10 exists so bead 10 does not depend on the archival config (bead 12) just to build the struct; this bead closes the loop once both exist. Without it, changing `HotDays` on a running serve requires a restart — the exact inconvenience reload removes for retention.

**Outcome Definition**:

- `HotDays` is a live-applied field; a reload updates the shared struct and the maintenance ticker's next pass uses the new value.
- A changed `BodyCapBytes` (and each non-live field) appears under `restart_required`, not `applied`.
- An invalid file still applies nothing (400) — `HotDays` and `RetentionDays` are both rolled back together.
- `go build ./... && go vet ./... && go test ./...` pass.

**Test Specifications** (`internal/api/api_test.go`, `internal/cli/reload_test.go`):

- Reload a changed `HotDays`: it appears in `applied`; the maintenance ticker (or its read accessor) reflects the new value on the next pass.
- Reload a changed `BodyCapBytes`: it appears in `restart_required`, and the running value is unchanged.
- Reload with an invalid file changes neither `HotDays` nor `RetentionDays` (all-or-nothing).
- The response's `unchanged` flag is false iff at least one field differs.

**Files to Touch**:
- `internal/api/livecfg.go` (modify — add the `HotDays` field)
- `internal/api/api.go` (modify — reload diff: `HotDays` live-applied)
- `internal/api/api_test.go` (modify)
- `internal/cli/serve.go` (modify — the maintenance ticker reads `HotDays` from the shared struct)
- `internal/cli/reload_test.go` (modify — the live vs `restart_required` cases)
