package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

func purgeTestConfig(t *testing.T, dbPath string, retentionDays int) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DBPath = dbPath
	cfg.RetentionDays = retentionDays
	return cfg
}

// newPurgeStore opens a fresh store at a temp path, seeds n generic
// (recent, priced-by-omission) rows, closes it, and returns the path — the
// "belt" fixture for guard tests: an existing store whose rows must survive
// a refusal.
func newPurgeStore(t *testing.T, n int) (string, int) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	for i := 0; i < n; i++ {
		seedRequest(t, st, nil)
	}
	st.Close()
	return dbPath, n
}

func countRows(t *testing.T, dbPath string) int {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	n, err := st.CountRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("CountRequests: %v", err)
	}
	return n
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	return fi.Size()
}

// seedPricedRow inserts a request explicitly marked priced. seedRequest's
// own default leaves CostSource nil, which purgeUnpricedWhere's
// COALESCE(cost_source,'unpriced') folds into the unpriced bucket
// regardless of CostUSD — real inserts always populate CostSource, so a
// "priced, must survive --unpriced" fixture has to say so explicitly.
func seedPricedRow(t *testing.T, st *store.Store) {
	t.Helper()
	configured := "configured"
	seedRequest(t, st, func(r *store.Request) { r.CostSource = &configured })
}

// seedUnpricedRow inserts a request matching the D5 unpriced predicate
// (internal/store's purgeUnpricedWhere): COALESCE(cost_source,'unpriced')
// = 'unpriced' and positive tokens (seedRequest's default tokens are
// already positive).
func seedUnpricedRow(t *testing.T, st *store.Store) {
	t.Helper()
	unpriced := "unpriced"
	seedRequest(t, st, func(r *store.Request) {
		r.CostUSD = nil
		r.CostSource = &unpriced
	})
}

// seedManyOldRows inserts n rows with large bodies, all older than age, so a
// purge over them frees enough pages for VACUUM to visibly shrink the file
// — a handful of small rows would not reliably move the file size at all.
func seedManyOldRows(t *testing.T, st *store.Store, n int, age time.Duration) {
	t.Helper()
	big := []byte(strings.Repeat("x", 8192))
	for i := 0; i < n; i++ {
		seedRequest(t, st, func(r *store.Request) {
			r.StartedAt = time.Now().Add(-age).Add(time.Duration(i) * time.Second)
			r.ReqBody = big
			r.RespBody = big
		})
	}
}

// assertPurgeGuardRefuses runs runPurge with args and retentionDays and
// checks it refuses in both senses the bead requires:
//   - braces: pointing DBPath at a path that does not exist yet, so the
//     guard's refusal is observable as "store.Open never ran" (the file was
//     never created), not just "returned an error".
//   - belt: an existing store's seeded rows are still all present
//     afterwards.
func assertPurgeGuardRefuses(t *testing.T, args []string, retentionDays int, wantMsg string) {
	t.Helper()

	t.Run("before store.Open", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "does-not-exist-yet", "lens.db")
		cfg := purgeTestConfig(t, dbPath, retentionDays)
		var buf bytes.Buffer
		err := runPurge(args, &buf, cfg)
		if err == nil {
			t.Fatal("runPurge: expected an error, got nil")
		}
		if wantMsg != "" && !strings.Contains(err.Error(), wantMsg) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), wantMsg)
		}
		if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
			t.Errorf("store.Open ran before the guard — %s exists", dbPath)
		}
	})

	t.Run("existing rows survive", func(t *testing.T) {
		dbPath, seeded := newPurgeStore(t, 2)
		cfg := purgeTestConfig(t, dbPath, retentionDays)
		var buf bytes.Buffer
		err := runPurge(args, &buf, cfg)
		if err == nil {
			t.Fatal("runPurge: expected an error, got nil")
		}
		if n := countRows(t, dbPath); n != seeded {
			t.Errorf("row count = %d, want unchanged %d", n, seeded)
		}
	})
}

// 1. --older-than 0 and --older-than -1: refused, store untouched.
func TestPurgeOlderThanNonPositiveRefused(t *testing.T) {
	for _, days := range []string{"0", "-1"} {
		t.Run("older-than="+days, func(t *testing.T) {
			assertPurgeGuardRefuses(t, []string{"--older-than", days}, 0, "must be positive")
		})
	}
}

// 2. retention_days = 0, no explicit --older-than: refused.
func TestPurgeImpliedDefaultRefusedWhenRetentionZero(t *testing.T) {
	assertPurgeGuardRefuses(t, nil, 0, "retention_days is not configured")
}

// 3. retention_days = -5, no explicit --older-than: refused the same way —
// the case that separates <= 0 from == 0.
func TestPurgeImpliedDefaultRefusedWhenRetentionNegative(t *testing.T) {
	assertPurgeGuardRefuses(t, nil, -5, "retention_days is not configured")
}

// 4. --older-than 30 --unpriced together: refused, naming both flags.
func TestPurgeMutuallyExclusiveFlagsRefused(t *testing.T) {
	assertPurgeGuardRefuses(t, []string{"--older-than", "30", "--unpriced"}, 0, "cannot be combined")
}

func TestPurgeMutualExclusionNamesBothFlags(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "does-not-exist-yet", "lens.db")
	cfg := purgeTestConfig(t, dbPath, 0)
	var buf bytes.Buffer
	err := runPurge([]string{"--older-than", "30", "--unpriced"}, &buf, cfg)
	if err == nil {
		t.Fatal("runPurge: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "--older-than") || !strings.Contains(err.Error(), "--unpriced") {
		t.Errorf("error = %q, want it to name both --older-than and --unpriced", err.Error())
	}
}

// 5. --unpriced alone with retention_days = 0: succeeds — the days guards
// do not apply to the unpriced predicate.
func TestPurgeUnpricedAloneIgnoresRetentionGuard(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	cfg := purgeTestConfig(t, dbPath, 0)
	var buf bytes.Buffer
	if err := runPurge([]string{"--unpriced", "--dry-run"}, &buf, cfg); err != nil {
		t.Fatalf("runPurge: %v, want success even with retention_days=0", err)
	}
}

// 7. --dry-run reports a count, deletes nothing, and skips --vacuum. The
// naive sequencing (vacuum once, then dry-run and check the size did not
// change again) proves nothing, since the first VACUUM already removed any
// free pages — this seeds a second reclaimable batch, purges it for real
// without --vacuum (free pages exist, file does not yet shrink), then
// checks --dry-run --vacuum leaves the size unchanged, with a control real
// --vacuum afterwards proving it *would* have shrunk if not skipped.
func TestPurgeDryRunSkipsVacuum(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	seedManyOldRows(t, st, 50, 40*24*time.Hour)
	st.Close()

	cfg := purgeTestConfig(t, dbPath, 0)

	var buf bytes.Buffer
	if err := runPurge([]string{"--older-than", "30", "--yes"}, &buf, cfg); err != nil {
		t.Fatalf("runPurge (purge batch A, no vacuum): %v", err)
	}
	sizeAfterA := fileSize(t, dbPath)

	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	seedManyOldRows(t, st2, 50, 40*24*time.Hour)
	st2.Close()

	buf.Reset()
	if err := runPurge([]string{"--older-than", "30", "--dry-run", "--vacuum"}, &buf, cfg); err != nil {
		t.Fatalf("runPurge (dry-run --vacuum): %v", err)
	}
	if !strings.Contains(buf.String(), "dry run") {
		t.Errorf("output = %q, want it to say dry run", buf.String())
	}
	if n := countRows(t, dbPath); n != 50 {
		t.Errorf("row count after dry run = %d, want 50 (batch B untouched)", n)
	}
	sizeAfterDryRun := fileSize(t, dbPath)
	if sizeAfterDryRun != sizeAfterA {
		t.Errorf("dry-run --vacuum changed the file size: %d -> %d, want unchanged (vacuum must be skipped)", sizeAfterA, sizeAfterDryRun)
	}

	// Control: a real (non-dry-run) --vacuum on the same state must shrink
	// the file — otherwise the assertion above cannot distinguish "skipped"
	// from "did nothing".
	buf.Reset()
	if err := runPurge([]string{"--older-than", "30", "--yes", "--vacuum"}, &buf, cfg); err != nil {
		t.Fatalf("runPurge (real --vacuum): %v", err)
	}
	sizeAfterRealVacuum := fileSize(t, dbPath)
	if sizeAfterRealVacuum >= sizeAfterDryRun {
		t.Errorf("control --vacuum did not shrink the file: %d -> %d, want smaller", sizeAfterDryRun, sizeAfterRealVacuum)
	}
}

// 8. --yes gate: without it, no write; with it, the write happens.
func TestPurgeYesGate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	seedUnpricedRow(t, st)
	seedUnpricedRow(t, st)
	st.Close()

	cfg := purgeTestConfig(t, dbPath, 0)

	var buf bytes.Buffer
	if err := runPurge([]string{"--unpriced"}, &buf, cfg); err == nil {
		t.Fatal("runPurge without --yes: expected an error, got nil")
	}
	if n := countRows(t, dbPath); n != 2 {
		t.Errorf("row count without --yes = %d, want 2 (untouched)", n)
	}

	buf.Reset()
	if err := runPurge([]string{"--unpriced", "--yes"}, &buf, cfg); err != nil {
		t.Fatalf("runPurge with --yes: %v", err)
	}
	if n := countRows(t, dbPath); n != 0 {
		t.Errorf("row count with --yes = %d, want 0 (deleted)", n)
	}
}

// 9. --vacuum: the DB file shrinks after a purge, and the reported
// before/after sizes in the output differ.
func TestPurgeVacuumReportsSizes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	seedManyOldRows(t, st, 80, 40*24*time.Hour)
	st.Close()

	cfg := purgeTestConfig(t, dbPath, 0)
	var buf bytes.Buffer
	if err := runPurge([]string{"--older-than", "30", "--yes", "--vacuum"}, &buf, cfg); err != nil {
		t.Fatalf("runPurge: %v", err)
	}

	out := buf.String()
	idx := strings.Index(out, "vacuum:")
	if idx == -1 {
		t.Fatalf("output = %q, want a vacuum report line", out)
	}
	var before, after int64
	if _, err := fmt.Sscanf(out[idx:], "vacuum: %d -> %d bytes", &before, &after); err != nil {
		t.Fatalf("could not parse vacuum report from %q: %v", out, err)
	}
	if before == after {
		t.Errorf("vacuum report shows no change: %d -> %d, want the file to shrink", before, after)
	}
	if got := fileSize(t, dbPath); got != after {
		t.Errorf("reported after-size %d does not match the actual file size %d", after, got)
	}
}

// 10. Both predicates end to end: an aged set and an unpriced set, each
// selected by its own flag, leaving the other class of row untouched.
func TestPurgeBothPredicatesEndToEnd(t *testing.T) {
	t.Run("older_than", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "lens.db")
		st, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		seedManyOldRows(t, st, 3, 40*24*time.Hour)
		seedRequest(t, st, nil) // recent, must survive
		st.Close()

		cfg := purgeTestConfig(t, dbPath, 0)
		var buf bytes.Buffer
		if err := runPurge([]string{"--older-than", "30", "--yes"}, &buf, cfg); err != nil {
			t.Fatalf("runPurge: %v", err)
		}
		if n := countRows(t, dbPath); n != 1 {
			t.Errorf("row count = %d, want 1 (only the recent row survives)", n)
		}
	})

	t.Run("unpriced", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "lens.db")
		st, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		seedUnpricedRow(t, st)
		seedUnpricedRow(t, st)
		seedPricedRow(t, st) // must survive
		st.Close()

		cfg := purgeTestConfig(t, dbPath, 0)
		var buf bytes.Buffer
		if err := runPurge([]string{"--unpriced", "--yes"}, &buf, cfg); err != nil {
			t.Fatalf("runPurge: %v", err)
		}
		if n := countRows(t, dbPath); n != 1 {
			t.Errorf("row count = %d, want 1 (only the priced row survives)", n)
		}
	})
}
