package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// LS is `lens ls`'s os.Stdout-writing entrypoint.
func LS(args []string) error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return runLS(args, os.Stdout, st)
}

// lsJSONRow is one --json line's shape.
type lsJSONRow struct {
	ID           int64     `json:"id"`
	StartedAt    time.Time `json:"started_at"`
	DurationMs   float64   `json:"duration_ms"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CostUSD      *float64  `json:"cost_usd"`
	Status       int       `json:"status"`
	Warned       bool      `json:"warned"`
}

func runLS(args []string, w io.Writer, st *store.Store) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	limit := fs.Int("limit", 25, "max rows")
	session := fs.String("session", "", "filter by session id")
	model := fs.String("model", "", "filter by model")
	since := fs.String("since", "", "only requests since this duration-ago or RFC3339 timestamp")
	warn := fs.Bool("warn", false, "only requests with warnings")
	errorsOnly := fs.Bool("errors", false, "only requests with errors")
	jsonOut := fs.Bool("json", false, "emit JSON, one object per line")
	if err := fs.Parse(args); err != nil {
		return err
	}

	filter := store.Filter{Limit: *limit, SessionID: *session, Model: *model, OnlyWarned: *warn, OnlyErrors: *errorsOnly}
	if *since != "" {
		t, err := parseSince(*since)
		if err != nil {
			return err
		}
		filter.Since = t
	}

	ctx := context.Background()
	reqs, err := st.ListRequests(ctx, filter)
	if err != nil {
		return fmt.Errorf("list requests: %w", err)
	}
	warnedIDs, err := warnedRequestIDs(ctx, st)
	if err != nil {
		return fmt.Errorf("list warnings: %w", err)
	}

	if *jsonOut {
		enc := json.NewEncoder(w)
		for _, r := range reqs {
			row := lsJSONRow{
				ID: r.ID, StartedAt: r.StartedAt, DurationMs: float64(r.Duration.Milliseconds()),
				Model: displayModel(r), InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
				CostUSD: r.CostUSD, Status: r.Status, Warned: warnedIDs[r.ID],
			}
			if err := enc.Encode(row); err != nil {
				return err
			}
		}
		return nil
	}

	now := time.Now()
	rows := make([][]string, 0, len(reqs))
	for _, r := range reqs {
		warnMark := ""
		if warnedIDs[r.ID] {
			warnMark = "⚠"
		}
		rows = append(rows, []string{
			fmt.Sprintf("%d", r.ID),
			relTime(r.StartedAt, now),
			humanDuration(r.Duration),
			displayModel(r),
			fmt.Sprintf("%s/%s", humanTokens(int64(r.InputTokens)), humanTokens(int64(r.OutputTokens))),
			humanCost(r.CostUSD),
			fmt.Sprintf("%d", r.Status),
			warnMark,
		})
	}
	fmt.Fprint(w, table([]string{"ID", "TIME", "DUR", "MODEL", "IN/OUT TOK", "COST", "STATUS", "⚠"}, rows, 0))
	return nil
}

// displayModel prefers the resolved model (what the API actually reported
// back) and falls back to what the client requested when unknown.
func displayModel(r *store.Request) string {
	if r.ModelResolved != "" {
		return r.ModelResolved
	}
	return r.ModelRequested
}

// warnedRequestIDs returns the set of request ids that have at least one
// warning, from up to store.DefaultLimit of the most recent warnings.
// store.Filter has no per-request lookup, so ls and show both take this
// same one-query-then-filter-in-Go trade — fine at this CLI's dev-tool
// scale.
func warnedRequestIDs(ctx context.Context, st *store.Store) (map[int64]bool, error) {
	all, err := st.ListWarnings(ctx, store.Filter{Limit: store.DefaultLimit})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(all))
	for _, wn := range all {
		out[wn.RequestID] = true
	}
	return out, nil
}
