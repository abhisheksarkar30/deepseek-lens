package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// Show is `lens show <id>`'s os.Stdout-writing entrypoint.
func Show(args []string) error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return runShow(args, os.Stdout, st)
}

// showLineBudget caps a body's printed lines when --full is not given.
//
// ponytail: a fixed line budget standing in for the real terminal height —
// detecting that needs a syscall/ioctl this bead's stdlib-only constraint
// (no golang.org/x/term) doesn't cover cleanly cross-platform. Wire an
// actual terminal-size probe if a genuinely short terminal ever needs a
// smaller cap.
const showLineBudget = 40

func runShow(args []string, w io.Writer, st *store.Store) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	full := fs.Bool("full", false, "dump full bodies, not truncated")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: lens show <id>")
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid request id %q: %w", fs.Arg(0), err)
	}

	ctx := context.Background()
	r, err := st.GetRequest(ctx, id)
	if err != nil {
		return fmt.Errorf("request %d: %w", id, err)
	}

	allWarnings, err := st.ListWarnings(ctx, store.Filter{Limit: store.DefaultLimit})
	if err != nil {
		return fmt.Errorf("list warnings: %w", err)
	}
	var warnings []*store.Warning
	for _, wn := range allWarnings {
		if wn.RequestID == id {
			warnings = append(warnings, wn)
		}
	}

	fmt.Fprintf(w, "id:              %d\n", r.ID)
	fmt.Fprintf(w, "started_at:      %s\n", r.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "ttfb:            %s\n", humanDuration(r.TTFB))
	fmt.Fprintf(w, "duration:        %s\n", humanDuration(r.Duration))
	fmt.Fprintf(w, "method/path:     %s %s\n", r.Method, r.Path)
	fmt.Fprintf(w, "status:          %d\n", r.Status)
	fmt.Fprintf(w, "model_requested: %s\n", r.ModelRequested)
	fmt.Fprintf(w, "model_resolved:  %s\n", r.ModelResolved)
	fmt.Fprintf(w, "tokens:          in=%d out=%d cache_creation=%d cache_read=%d\n",
		r.InputTokens, r.OutputTokens, r.CacheCreationTokens, r.CacheReadTokens)
	fmt.Fprintf(w, "cost:            %s\n", humanCost(r.CostUSD))
	if r.ErrorText != nil {
		fmt.Fprintf(w, "error:           %s\n", *r.ErrorText)
	}

	fmt.Fprintf(w, "\nwarnings (%d):\n", len(warnings))
	if len(warnings) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, wn := range warnings {
		fmt.Fprintf(w, "  [%s] %s: %s\n", wn.Severity, wn.Kind, warnSite(wn))
	}

	fmt.Fprintf(w, "\nrequest headers:\n%s\n", indentBlock(prettyJSON(r.ReqHeaders)))
	fmt.Fprintf(w, "\nrequest body:\n%s\n", renderBody(r.ReqBody, *full))
	fmt.Fprintf(w, "\nresponse headers:\n%s\n", indentBlock(prettyJSON(r.RespHeaders)))
	fmt.Fprintf(w, "\nresponse body:\n%s\n", renderBody(r.RespBody, *full))

	return nil
}

// warnSite renders a warning's Detail with the site it applies to prefixed
// when the analyzer could name one.
func warnSite(wn *store.Warning) string {
	if wn.Path == "" {
		return wn.Detail
	}
	return wn.Path + ": " + wn.Detail
}

// prettyJSON re-indents s if it parses as JSON, else returns it unchanged.
func prettyJSON(s string) string {
	if s == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
		return s
	}
	return buf.String()
}

func indentBlock(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

// renderBody pretty-prints body if it parses as JSON, else prints it raw,
// truncated to showLineBudget lines unless full is set.
func renderBody(body []byte, full bool) string {
	if len(body) == 0 {
		return "  (empty)"
	}
	text := string(body)
	var buf bytes.Buffer
	if json.Indent(&buf, body, "", "  ") == nil {
		text = buf.String()
	}

	lines := strings.Split(text, "\n")
	truncated := false
	if !full && len(lines) > showLineBudget {
		truncated = true
		lines = lines[:showLineBudget]
	}
	for i, l := range lines {
		lines[i] = "  " + l
	}
	out := strings.Join(lines, "\n")
	if truncated {
		out += "\n  ... truncated, use --full to see all"
	}
	return out
}
