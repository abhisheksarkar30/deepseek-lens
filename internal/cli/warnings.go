package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// Warnings is `lens warnings`'s os.Stdout-writing entrypoint — the CLI
// face of the flagship feature: "what is being dropped?"
func Warnings(args []string) error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return runWarnings(args, os.Stdout, st)
}

type warningGroup struct {
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"`
	Count    int       `json:"count"`
	LastSeen time.Time `json:"last_seen"`
}

func runWarnings(args []string, w io.Writer, st *store.Store) error {
	fs := flag.NewFlagSet("warnings", flag.ContinueOnError)
	detail := fs.Bool("detail", false, "list individual occurrences with their request ids")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	all, err := st.ListWarnings(context.Background(), store.Filter{Limit: store.DefaultLimit})
	if err != nil {
		return fmt.Errorf("list warnings: %w", err)
	}

	type key struct{ kind, severity string }
	groups := map[key]*warningGroup{}
	var order []key
	for _, wn := range all {
		k := key{wn.Kind, wn.Severity}
		g, ok := groups[k]
		if !ok {
			g = &warningGroup{Kind: wn.Kind, Severity: wn.Severity}
			groups[k] = g
			order = append(order, k)
		}
		g.Count++
		if wn.CreatedAt.After(g.LastSeen) {
			g.LastSeen = wn.CreatedAt
		}
	}
	sort.Slice(order, func(i, j int) bool {
		return groups[order[i]].Count > groups[order[j]].Count
	})

	if *jsonOut {
		enc := json.NewEncoder(w)
		if *detail {
			return enc.Encode(all)
		}
		out := make([]*warningGroup, 0, len(order))
		for _, k := range order {
			out = append(out, groups[k])
		}
		return enc.Encode(out)
	}

	now := time.Now()
	rows := make([][]string, 0, len(order))
	for _, k := range order {
		g := groups[k]
		rows = append(rows, []string{g.Kind, g.Severity, fmt.Sprintf("%d", g.Count), relTime(g.LastSeen, now)})
	}
	fmt.Fprint(w, table([]string{"KIND", "SEVERITY", "COUNT", "LAST SEEN"}, rows, 0))

	if *detail {
		fmt.Fprintln(w)
		drows := make([][]string, 0, len(all))
		for _, wn := range all {
			drows = append(drows, []string{fmt.Sprintf("%d", wn.RequestID), wn.Kind, wn.Severity, relTime(wn.CreatedAt, now), wn.Message})
		}
		fmt.Fprint(w, table([]string{"REQUEST", "KIND", "SEVERITY", "AGE", "MESSAGE"}, drows, 0))
	}
	return nil
}
