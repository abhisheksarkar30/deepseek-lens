package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// Sessions is `lens sessions`'s os.Stdout-writing entrypoint.
func Sessions(args []string) error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return runSessions(args, os.Stdout, st)
}

// runSessions lists the sessions the consumer has grouped calls into, most
// recently active first. The aggregates are the ones store.UpsertSession
// maintains per call — this command does no aggregation of its own, and the
// COST column reuses stats.go's mixed-pricing rule so a session whose total
// covers only some of its calls says so.
func runSessions(args []string, w io.Writer, st *store.Store) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	sessions, err := st.ListSessions(context.Background(), store.Filter{})
	if err != nil {
		return fmt.Errorf("sessions: list sessions: %w", err)
	}

	now := time.Now()
	rows := make([][]string, 0, len(sessions))
	for _, s := range sessions {
		rows = append(rows, []string{
			s.ID,
			relTime(s.FirstSeen, now),
			humanDuration(s.LastSeen.Sub(s.FirstSeen)),
			strconv.Itoa(s.RequestCount),
			humanTokens(s.TotalInputTokens + s.TotalOutputTokens),
			costCell(s.TotalCostUSD, s.RequestCount, s.UnpricedCount),
			warnCount(s.WarningCount),
		})
	}
	fmt.Fprint(w, table([]string{"ID", "STARTED", "DURATION", "TURNS", "TOKENS", "COST", "⚠"}, rows, 0))
	return nil
}

// warnCount renders the ⚠ cell: the number of warnings raised anywhere in the
// session, or nothing at all when there were none, so the column stays
// scannable down a long list.
func warnCount(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// sessionHeader prints one session's header, which is what `lens show <id>`
// prints when its argument is a session rather than a call.
func sessionHeader(w io.Writer, s *store.Session) {
	prefix := "(none)"
	if s.PrefixHash != nil {
		prefix = *s.PrefixHash
	}
	fmt.Fprintf(w, "id:              %s\n", s.ID)
	fmt.Fprintf(w, "prefix_hash:     %s\n", prefix)
	fmt.Fprintf(w, "started:         %s\n", s.FirstSeen.Format(time.RFC3339))
	fmt.Fprintf(w, "last active:     %s\n", s.LastSeen.Format(time.RFC3339))
	fmt.Fprintf(w, "duration:        %s\n", humanDuration(s.LastSeen.Sub(s.FirstSeen)))
	fmt.Fprintf(w, "turns:           %d\n", s.RequestCount)
	fmt.Fprintf(w, "tokens:          in=%s out=%s\n",
		humanTokens(s.TotalInputTokens), humanTokens(s.TotalOutputTokens))
	fmt.Fprintf(w, "cost:            %s\n", costCell(s.TotalCostUSD, s.RequestCount, s.UnpricedCount))
	fmt.Fprintf(w, "warnings:        %d\n", s.WarningCount)
	if s.ModelSet != "" {
		fmt.Fprintf(w, "models:          %s\n", s.ModelSet)
	}
	fmt.Fprintf(w, "\ncalls:\n  lens ls --session %s\n", s.ID)
}
