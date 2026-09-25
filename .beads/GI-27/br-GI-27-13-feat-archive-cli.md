### Bead 13: `feat` `lens archive status|run|restore` + the dashboard archive line

- **Bead ID**: br-GI-27-13
- **Priority**: P2 (medium)
- **Original Estimate**: 2h
- **Dependencies**: br-GI-27-11, br-GI-27-12
- **Blocks**: None

**Description**:

C.2 CLI + dashboard rows (plan §6). Expose and operate the archive bead 11/12 built.

**1. `lens archive status`** — prints the hot boundary, archived/unarchived row counts, archive size and file count, **missing markers** (a marker for a row whose hot body was NULLed but whose day-file row is gone, or a body gone with no marker), and **restore duplicates** (a row both archived and re-populated in the hot DB). Read-only.

**2. `lens archive run [--dry-run] [--yes]`** — runs the archiver. `--dry-run` reports what would move without moving it. `--yes` is the **same gate as `purge`** (`internal/cli/purge.go`).

**3. `lens archive restore --since --until [--dry-run] [--yes]`** — the inverse: one hot transaction **per row**, **never drop a marker for a body not restored**, and **delete the day-file row only AFTER the hot commit** (the mirror of bead 11's archive ordering: hot copy first, archive drop last). Same `--yes` gate.

**4. Dashboard** (`internal/web/app.js` + `index.html`) — the request detail shows **"bodies loaded from the archive (YYYY-MM-DD)"** when the detail was hydrated from a day file (bead 11's hydration path). The archive-behaviour itself is bead 11's; this bead adds the surface indicator.

**Register `archive` in `cmd/lens/main.go:15-28`** and add it to `docs/context/cli-and-tooling.md`.

**Rationale**:

C.2. `archive status` is the operator's only way to see what moved and to detect the two failure shapes (missing marker, restore duplicate) the crash-safe two-step exists to keep as duplicates-never-loss; `run`/`restore` give a manual lever without waiting for the 24 h tick. The dashboard indicator is what stops a body shown from the archive being mistaken for a hot body, especially after `lens purge` removed archived bodies.

**Outcome Definition**:

- `lens archive status` prints the hot boundary, archived/unarchived counts, archive size/files, missing markers, and restore duplicates.
- `archive run [--dry-run] [--yes]` runs the archiver; `--dry-run` changes nothing; `--yes` is required off `--dry-run` (the `purge` gate).
- `archive restore --since --until` restores per row, never drops a marker without a restore, and deletes the day-file row after the hot commit; `--dry-run` reports without changing.
- The dashboard request detail names the archive day when the detail came from a day file.
- `go build ./... && go vet ./... && go test ./...` pass.

**Test Specifications** (`internal/cli/archive_test.go`):

- `status` counts match a fixture with one archived and one hot row; a hand-inserted missing marker and a restore duplicate are each reported.
- `run --dry-run` leaves the DB byte-identical; `run --yes` archives the eligible rows.
- `restore` round-trips a row: archive → restore → the hot body is byte-identical and the day-file row is gone and the marker is gone, in that order (fault-inject between the hot commit and the day-file delete → the body is still recoverable, no marker dropped for an unrestored body).
- `--dry-run` without `--yes` succeeds and changes nothing; without `--dry-run` and without `--yes` refuses.
- Dashboard: a request-detail payload whose body came from a day file carries the archive-day indicator.

**Files to Touch**:
- `internal/cli/archive.go` (create — `status`, `run`, `restore`)
- `internal/cli/archive_test.go` (create)
- `cmd/lens/main.go` (modify — register `archive` at :15-28)
- `internal/web/app.js` (modify — request-detail archive indicator)
- `internal/web/index.html` (modify — the indicator element)
- `docs/context/cli-and-tooling.md` (modify — the new command)
