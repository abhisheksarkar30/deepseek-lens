package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// Tail is `lens tail`'s os.Stdout-writing entrypoint. It blocks until
// SIGINT.
func Tail(args []string) error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runTail(ctx, args, os.Stdout, st)
}

const (
	tailPollInterval = 500 * time.Millisecond
	tailWindow       = 20
)

// runTail polls st every tailPollInterval and prints calls as they arrive,
// until ctx is done. When w is a TTY it redraws a rolling table in place
// with ANSI cursor movement; otherwise (piped output) it appends one plain
// line per new call — the bead's TTY-fallback requirement.
func runTail(ctx context.Context, args []string, w io.Writer, st *store.Store) error {
	fs := flag.NewFlagSet("tail", flag.ContinueOnError)
	session := fs.String("session", "", "filter by session id")
	warn := fs.Bool("warn", false, "only requests with warnings")
	if err := fs.Parse(args); err != nil {
		return err
	}
	filter := store.Filter{Limit: tailWindow, SessionID: *session, OnlyWarned: *warn, WithBodies: false}

	tty := isTTY(w)
	seen := make(map[int64]bool)

	poll := func() error {
		reqs, err := st.ListRequests(context.Background(), filter)
		if err != nil {
			return err
		}
		// reqs is newest-first; fresh collects the not-yet-seen ones in
		// chronological (oldest-first) order for stable append output.
		var fresh []*store.Request
		for i := len(reqs) - 1; i >= 0; i-- {
			if !seen[reqs[i].ID] {
				fresh = append(fresh, reqs[i])
			}
		}
		for _, r := range reqs {
			seen[r.ID] = true
		}
		if len(fresh) == 0 {
			return nil
		}

		now := time.Now()
		if tty {
			fmt.Fprint(w, ansiHome(w))
			rows := make([][]string, 0, len(reqs))
			for i := len(reqs) - 1; i >= 0; i-- {
				rows = append(rows, tailRow(reqs[i], now))
			}
			fmt.Fprint(w, table([]string{"ID", "TIME", "DUR", "MODEL", "IN/OUT TOK", "COST", "STATUS"}, rows, 0))
			return nil
		}

		for _, r := range fresh {
			fmt.Fprintln(w, tailLine(r, now))
		}
		return nil
	}

	if err := poll(); err != nil {
		return err
	}
	ticker := time.NewTicker(tailPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := poll(); err != nil {
				return err
			}
		}
	}
}

func tailRow(r *store.Request, now time.Time) []string {
	return []string{
		fmt.Sprintf("%d", r.ID), relTime(r.StartedAt, now), humanDuration(r.Duration), displayModel(r),
		fmt.Sprintf("%s/%s", humanTokens(int64(r.InputTokens)), humanTokens(int64(r.OutputTokens))),
		humanCost(r.CostUSD), fmt.Sprintf("%d", r.Status),
	}
}

func tailLine(r *store.Request, now time.Time) string {
	return fmt.Sprintf("%d  %s  %s  %s  %s/%s  %s  %d",
		r.ID, relTime(r.StartedAt, now), humanDuration(r.Duration), displayModel(r),
		humanTokens(int64(r.InputTokens)), humanTokens(int64(r.OutputTokens)),
		humanCost(r.CostUSD), r.Status)
}
