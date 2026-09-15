package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
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
	ByDay          []store.DayStat   `json:"by_day"`
	WarningsByKind map[string]int    `json:"warnings_by_kind"`
	MeanDurationMs float64           `json:"mean_duration_ms"`
}

func runStats(args []string, w io.Writer, st *store.Store) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	since := fs.String("since", "24h", "time window: a Go duration (e.g. 24h, 30m) or RFC3339 timestamp")
	by := fs.String("by", "model", "breakdown section: model or day")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *by != "model" && *by != "day" {
		return fmt.Errorf("--by: invalid value %q (want model or day)", *by)
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
	byDay, err := st.StatsByDay(ctx, sinceTime)
	if err != nil {
		return fmt.Errorf("stats by day: %w", err)
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
			Since: sinceTime, Summary: summary, ByModel: byModel, ByDay: byDay,
			WarningsByKind: byKind, MeanDurationMs: meanMs,
		})
	}

	fmt.Fprintf(w, "window:        since %s\n", sinceTime.Format(time.RFC3339))
	fmt.Fprintf(w, "calls:         %d (%d errors)\n", summary.RequestCount, summary.ErrorCount)
	fmt.Fprintf(w, "tokens:        in=%s out=%s\n", humanTokens(summary.InputTokens), humanTokens(summary.OutputTokens))
	fmt.Fprintf(w, "cost:          %s\n", costCell(summary.CostUSDTotal, summary.RequestCount, summary.UnpricedCount))
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

	if *by == "day" {
		if len(byDay) > 0 {
			fmt.Fprintln(w, "\nday split:")
			rows := make([][]string, 0, len(byDay))
			for _, d := range byDay {
				rows = append(rows, []string{
					d.Day,
					strconv.Itoa(d.RequestCount),
					fmt.Sprintf("%s/%s", humanTokens(d.InputTokens), humanTokens(d.OutputTokens)),
					costCell(d.CostUSDTotal, d.RequestCount, d.UnpricedCount),
				})
			}
			fmt.Fprint(w, table([]string{"DAY", "CALLS", "IN/OUT TOK", "COST"}, rows, 0))
		}
		return nil
	}

	if len(byModel) > 0 {
		fmt.Fprintln(w, "\nmodel split:")
		rows := make([][]string, 0, len(byModel))
		for _, m := range byModel {
			rows = append(rows, []string{
				m.Model,
				strconv.Itoa(m.RequestCount),
				fmt.Sprintf("%s/%s", humanTokens(m.InputTokens), humanTokens(m.OutputTokens)),
				costCell(m.CostUSDTotal, m.RequestCount, m.UnpricedCount),
			})
		}
		fmt.Fprint(w, table([]string{"MODEL", "CALLS", "IN/OUT TOK", "COST"}, rows, 0))
	}

	return nil
}

// costCell formats an aggregate cost under the bead's mixed-pricing rule:
// when some of the group's calls have no price at all, the sum is partial and
// says so. Printing a bare "$1.23" over a group that also contains unpriced
// calls would report a total the tool cannot actually know — the whole point
// of making "unpriced" a first-class state.
//
// priced is the group's total call count, since unpriced is always a subset
// of it.
func costCell(total float64, count, unpriced int) string {
	switch {
	case unpriced <= 0:
		return fmt.Sprintf("$%.4f", total)
	case unpriced >= count:
		return fmt.Sprintf("— + %d unpriced", unpriced)
	default:
		return fmt.Sprintf("$%.4f + %d unpriced", total, unpriced)
	}
}

func msToDuration(ms float64) time.Duration {
	return time.Duration(ms * float64(time.Millisecond))
}
