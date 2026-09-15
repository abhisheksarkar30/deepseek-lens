package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// Export is `lens export`'s os.Stdout-writing entrypoint: JSONL, one
// request per line, real JSON encoding rather than the truncated terminal
// formatting show/ls use.
func Export(args []string) error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return runExport(args, os.Stdout, st)
}

func runExport(args []string, w io.Writer, st *store.Store) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	limit := fs.Int("limit", store.DefaultLimit, "max rows")
	session := fs.String("session", "", "filter by session id")
	model := fs.String("model", "", "filter by model")
	since := fs.String("since", "", "only requests since this duration-ago or RFC3339 timestamp")
	if err := fs.Parse(args); err != nil {
		return err
	}

	filter := store.Filter{Limit: *limit, SessionID: *session, Model: *model}
	if *since != "" {
		t, err := parseSince(*since)
		if err != nil {
			return err
		}
		filter.Since = t
	}

	reqs, err := st.ListRequests(context.Background(), filter)
	if err != nil {
		return fmt.Errorf("export: list requests: %w", err)
	}

	enc := json.NewEncoder(w)
	for _, r := range reqs {
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("export: encode request %d: %w", r.ID, err)
		}
	}
	return nil
}
