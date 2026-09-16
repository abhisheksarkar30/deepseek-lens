package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// Purge is `lens purge`'s os.Stdout-writing entrypoint: the one-shot purge,
// for the case the scheduled purge in `lens serve` cannot serve — reclaiming
// disk when the proxy is not running.
//
// Unlike `lens replay` (which deliberately never opens the store, so the
// consumer stays the only writer while it runs — see replay.go), `lens
// purge` opens the store directly on purpose: the usual reason to purge is
// to reclaim disk, which is exactly when you would rather the proxy were
// not running. WAL plus busy_timeout(5000) serializes this process against
// a concurrently running `lens serve` cross-process (D7) — the SQLite-level
// protocol every other external opener already relies on, distinct from the
// in-process SetMaxOpenConns(1) discipline a single running process uses.
//
// It cannot use openStore (format.go): that helper discards the resolved
// config, and this command needs cfg.RetentionDays as --older-than's
// implied default.
func Purge(args []string) error {
	cfg, err := config.Load(nil)
	if err != nil {
		return err
	}
	return runPurge(args, os.Stdout, cfg)
}

// runPurge implements every guard before the destructive path — each one is
// one boolean away from a whole-table delete, since cfg.RetentionDays
// defaults to 0. Every guard below runs and can return before store.Open is
// ever called, so a refused invocation cannot have deleted anything: not
// "deleted nothing because the predicate was empty", but "never reached the
// database".
func runPurge(args []string, w io.Writer, cfg *config.Config) error {
	fs := flag.NewFlagSet("purge", flag.ContinueOnError)
	fs.SetOutput(w)
	olderThan := fs.Int("older-than", 0, "purge requests older than this many days (default: the configured retention_days, if >= 1)")
	unpriced := fs.Bool("unpriced", false, "purge rows matching the D5 unpriced predicate instead of an age threshold")
	dryRun := fs.Bool("dry-run", false, "report what would be deleted without deleting anything (also skips --vacuum)")
	yes := fs.Bool("yes", false, "confirm the delete; without it, a real (non-dry-run) purge is refused")
	vacuum := fs.Bool("vacuum", false, "run VACUUM after a real purge and report the file size before/after")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: lens purge [--older-than <days> | --unpriced] [--dry-run] [--yes] [--vacuum]")
		fmt.Fprintln(fs.Output(), "flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	olderThanProvided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "older-than" {
			olderThanProvided = true
		}
	})

	// Guard: the two predicates are never OR-ed into one WHERE clause and
	// never run as two sequential purges — passing both is refused outright.
	if olderThanProvided && *unpriced {
		return fmt.Errorf("purge: --older-than and --unpriced select different rows and cannot be combined")
	}

	var mode string
	var days int
	switch {
	case *unpriced:
		mode = "unpriced"
	case olderThanProvided:
		// A days <= 0 (zero or negative) is refused: --older-than 0 would
		// match every row (started_at < now), and a negative days is worse
		// (cutoff = now + |days| also matches every row). Same message the
		// POST /api/purge route uses for its non-positive-days case.
		if *olderThan <= 0 {
			return fmt.Errorf(`purge: days must be positive for mode "older_than"`)
		}
		mode = "older_than"
		days = *olderThan
	default:
		// Neither predicate flag given: fall back to the configured
		// retention_days, but only when it is actually configured (>= 1).
		// retention_days defaults to 0, so without this guard a bare `lens
		// purge` would be a whole-table delete — this is the guard that
		// matters most.
		if cfg.RetentionDays < 1 {
			return fmt.Errorf("purge: no predicate given and retention_days is not configured (%d); pass --older-than <days> or --unpriced", cfg.RetentionDays)
		}
		mode = "older_than"
		days = cfg.RetentionDays
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()

	var cutoff time.Time
	if mode == "older_than" {
		cutoff = time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	}

	if *dryRun {
		var count int
		if mode == "older_than" {
			count, _, _, err = st.CountPurgeable(ctx, cutoff)
		} else {
			count, err = st.CountUnpriced(ctx)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "would delete %d request(s) (dry run, nothing deleted, --vacuum skipped)\n", count)
		return nil
	}

	if !*yes {
		return fmt.Errorf("purge: refusing to delete without --yes (use --dry-run to preview first)")
	}

	var res store.PurgeResult
	if mode == "older_than" {
		res, err = st.PurgeOlderThan(ctx, cutoff)
	} else {
		res, err = st.PurgeUnpriced(ctx)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "deleted %d request(s), reconciled %d session(s)\n", res.Deleted, res.SessionsReconciled)

	if *vacuum {
		var before, after int64
		if fi, err := os.Stat(cfg.DBPath); err == nil {
			before = fi.Size()
		}
		if err := st.Vacuum(ctx); err != nil {
			return err
		}
		// The file's on-disk size only reflects VACUUM's rewrite once the
		// connection is closed (WAL's -wal/-shm sidecars are still holding
		// pages open otherwise), so the store is closed here — before the
		// "after" stat — rather than left to the deferred Close above,
		// which would run after this function has already printed a stale
		// (pre-shrink) size.
		st.Close()
		if fi, err := os.Stat(cfg.DBPath); err == nil {
			after = fi.Size()
		}
		fmt.Fprintf(w, "vacuum: %d -> %d bytes\n", before, after)
	}

	return nil
}
