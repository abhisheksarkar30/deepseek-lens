package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// Stats is `lens stats`'s os.Stdout-writing entrypoint.
func Stats(args []string) error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return runStats(args, os.Stdout, st)
}

type statsJSON struct {
	Since          time.Time         `json:"since"`
	Summary        *store.Summary    `json:"summary"`
	ByModel        []store.ModelStat `json:"by_model"`
	WarningsByKind map[string]int    `json:"warnings_by_kind"`
	MeanDurationMs float64           `json:"mean_duration_ms"`
}

func runStats(args []string, w io.Writer, st *store.Store) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	since := fs.String("since", "24h", "time window: a Go duration (e.g. 24h, 30m) or RFC3339 timestamp")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	sinceTime, err := parseSince(*since)
	if err != nil {
		return err
	}

	ctx := context.Background()
	summary, err := st.StatsSummary(ctx, sinceTime)
	if err != nil {
		return fmt.Errorf("stats summary: %w", err)
	}
	byModel, err := st.StatsByModel(ctx, sinceTime)
	if err != nil {
		return fmt.Errorf("stats by model: %w", err)
	}

	// ponytail: mean duration is sampled from up to store.DefaultLimit of
	// the most recent matching requests rather than a SQL AVG(duration_ns)
	// — internal/store/store.go is out of scope for this bead, and this
	// CLI is a single-user dev tool where an approximate mean over an
	// unusually large window is an acceptable trade. Add a real AVG query
	// if that precision ever matters.
	reqs, err := st.ListRequests(ctx, store.Filter{Since: sinceTime, Limit: store.DefaultLimit})
	if err != nil {
		return fmt.Errorf("list requests: %w", err)
	}
	var totalDur time.Duration
	for _, r := range reqs {
		totalDur += r.Duration
	}
	meanMs := 0.0
	if len(reqs) > 0 {
		meanMs = float64(totalDur.Milliseconds()) / float64(len(reqs))
	}

	warnings, err := st.ListWarnings(ctx, store.Filter{Since: sinceTime, Limit: store.DefaultLimit})
	if err != nil {
		return fmt.Errorf("list warnings: %w", err)
	}
	byKind := map[string]int{}
	for _, wn := range warnings {
		byKind[wn.Kind]++
	}

	if *jsonOut {
		return json.NewEncoder(w).Encode(statsJSON{
			Since: sinceTime, Summary: summary, ByModel: byModel, WarningsByKind: byKind, MeanDurationMs: meanMs,
		})
	}

	fmt.Fprintf(w, "window:        since %s\n", sinceTime.Format(time.RFC3339))
	fmt.Fprintf(w, "calls:         %d (%d errors)\n", summary.RequestCount, summary.ErrorCount)
	fmt.Fprintf(w, "tokens:        in=%s out=%s\n", humanTokens(summary.InputTokens), humanTokens(summary.OutputTokens))
	fmt.Fprintf(w, "cost:          %s\n", humanCost(&summary.CostUSDTotal))
	fmt.Fprintf(w, "warnings:      %d\n", summary.WarningCount)
	fmt.Fprintf(w, "duration:      mean=%s p50=%s p95=%s\n",
		humanDuration(msToDuration(meanMs)),
		humanDuration(msToDuration(summary.DurationP50Ms)),
		humanDuration(msToDuration(summary.DurationP95Ms)))

	if len(byKind) > 0 {
		fmt.Fprintln(w, "\nwarnings by kind:")
		kinds := make([]string, 0, len(byKind))
		for k := range byKind {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		rows := make([][]string, 0, len(kinds))
		for _, k := range kinds {
			rows = append(rows, []string{k, fmt.Sprintf("%d", byKind[k])})
		}
		fmt.Fprint(w, table([]string{"KIND", "COUNT"}, rows, 0))
	}

	if len(byModel) > 0 {
		fmt.Fprintln(w, "\nmodel split:")
		maxCount := 0
		for _, m := range byModel {
			if m.RequestCount > maxCount {
				maxCount = m.RequestCount
			}
		}
		const barWidth = 30
		for _, m := range byModel {
			barLen := 0
			if maxCount > 0 {
				barLen = m.RequestCount * barWidth / maxCount
			}
			fmt.Fprintf(w, "  %-24s %s %d\n", m.Model, strings.Repeat("█", barLen), m.RequestCount)
		}
	}

	return nil
}

func msToDuration(ms float64) time.Duration {
	return time.Duration(ms * float64(time.Millisecond))
}
