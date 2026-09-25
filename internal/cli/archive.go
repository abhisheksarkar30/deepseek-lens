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

// Archive is `lens archive status|run|restore`.
func Archive(args []string) error {
	cfg, err := config.Load(nil)
	if err != nil {
		return err
	}
	return runArchive(args, os.Stdout, cfg)
}

func runArchive(args []string, w io.Writer, cfg *config.Config) error {
	if len(args) == 0 {
		return fmt.Errorf("archive: usage: lens archive status|run|restore")
	}
	switch args[0] {
	case "status":
		return archiveStatus(w, cfg)
	case "run":
		return archiveRun(args[1:], w, cfg)
	case "restore":
		return archiveRestore(args[1:], w, cfg)
	default:
		return fmt.Errorf("archive: unknown subcommand %q", args[0])
	}
}

func archiveStatus(w io.Writer, cfg *config.Config) error {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	rep, err := st.ArchiveStatus(context.Background(), cfg.HotDays)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "hot_boundary: %s\n", rep.HotBoundary.Format(time.RFC3339Nano))
	fmt.Fprintf(w, "archived: %d\n", rep.Archived)
	fmt.Fprintf(w, "unarchived: %d\n", rep.Unarchived)
	fmt.Fprintf(w, "archive_files: %d\n", rep.Files)
	fmt.Fprintf(w, "archive_bytes: %d\n", rep.Bytes)
	fmt.Fprintf(w, "missing_markers: %d\n", rep.MissingMarkers)
	fmt.Fprintf(w, "restore_duplicates: %d\n", rep.RestoreDuplicates)
	return nil
}

func archiveRun(args []string, w io.Writer, cfg *config.Config) error {
	fs := flag.NewFlagSet("archive run", flag.ContinueOnError)
	fs.SetOutput(w)
	dry := fs.Bool("dry-run", false, "report what would move without moving it")
	yes := fs.Bool("yes", false, "confirm the move")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.HotDays <= 0 {
		return fmt.Errorf("archive: HotDays is 0; archival is off")
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	boundary := time.Now().Add(-time.Duration(cfg.HotDays) * 24 * time.Hour)
	n, err := st.CountArchivable(context.Background(), boundary)
	if err != nil {
		return err
	}
	if *dry {
		fmt.Fprintf(w, "dry-run: would archive %d row(s) older than %s\n", n, boundary.Format(time.RFC3339))
		return nil
	}
	if !*yes {
		return fmt.Errorf("archive: refusing to move bodies without --yes (use --dry-run to preview first)")
	}
	if err := st.ArchiveOlderThan(context.Background(), boundary, ""); err != nil {
		return err
	}
	fmt.Fprintf(w, "archived %d row(s)\n", n)
	return nil
}

func archiveRestore(args []string, w io.Writer, cfg *config.Config) error {
	fs := flag.NewFlagSet("archive restore", flag.ContinueOnError)
	fs.SetOutput(w)
	since := fs.String("since", "", "restore bodies started at or after this time (RFC3339)")
	until := fs.String("until", "", "restore bodies started before this time (RFC3339)")
	dry := fs.Bool("dry-run", false, "report what would be restored without restoring")
	yes := fs.Bool("yes", false, "confirm the restore")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *since == "" || *until == "" {
		return fmt.Errorf("archive: restore requires --since and --until")
	}
	sinceT, err := time.Parse(time.RFC3339, *since)
	if err != nil {
		return fmt.Errorf("archive: --since: %w", err)
	}
	untilT, err := time.Parse(time.RFC3339, *until)
	if err != nil {
		return fmt.Errorf("archive: --until: %w", err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if *dry {
		n, err := st.RestoreBetween(context.Background(), sinceT, untilT, "count")
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "dry-run: would restore %d row(s)\n", n)
		return nil
	}
	if !*yes {
		return fmt.Errorf("archive: refusing to restore without --yes (use --dry-run to preview first)")
	}
	n, err := st.RestoreBetween(context.Background(), sinceT, untilT, "")
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "restored %d row(s)\n", n)
	return nil
}
