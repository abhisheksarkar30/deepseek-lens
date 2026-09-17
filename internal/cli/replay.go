package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/replay"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// replayClientTimeout is generous on purpose: the request it wraps is an LLM
// call, which legitimately runs for minutes, and the one thing worse than a slow
// replay is a client that gives up while the upstream is still billing for it.
const replayClientTimeout = 5 * time.Minute

// Replay is `lens replay`'s os.Stdout-writing entrypoint.
//
// Like every other subcommand it resolves the config itself, but unlike the
// read-only ones it does NOT open the store: `lens replay <id>` is a thin HTTP
// client for the running server's POST /api/requests/{id}/replay, so replay
// itself never becomes a second writer (br-GI-1-06's decision log entry that
// rejected "the CLI opens its own DB writer"). This is a statement about
// replay, not about every subcommand — `lens purge` (br-GI-17-08) does open
// the store directly, serialized against a running `lens serve` by WAL plus
// busy_timeout rather than by staying out entirely. The one exception here is
// --dump, which never sends anything and is therefore the one path in this
// file that may read the local database directly.
func Replay(args []string) error {
	cfg, err := config.Load(nil)
	if err != nil {
		return err
	}
	return runReplay(args, os.Stdout, cfg)
}

// replaySafety is the posture the bead requires `--help` to state plainly.
// Replay is the only path in the project that can cause spend or write to a
// remote system, and it is easy to assume "replay" means "re-run the agent".
const replaySafety = `Replay re-sends a captured request to the LLM API. It never executes anything,
never touches your repository, and never re-runs tools. A captured body may
describe tool calls the original client executed; replay re-sends that
description to the model and stores the reply — it does not act on tool_use
blocks. Replaying is billable: the original call's cost is printed first, and a
replay whose original was expensive or unpriced is refused without --yes.`

func runReplay(args []string, w io.Writer, cfg *config.Config) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	// Usage and flag errors go to w rather than os.Stderr, which keeps the
	// command's help testable through the same injected writer as its output.
	fs.SetOutput(w)
	var sets repeatedFlag
	fs.Var(&sets, "set", "<jsonpath>=<value> to change in the body before sending (repeatable)")
	dump := fs.String("dump", "", "write the edited body to this path and exit without sending (local only)")
	noCapture := fs.Bool("no-capture", false, "send the replay without recording it")
	diff := fs.Int64("diff", 0, "compare against this request id instead of the original")
	yes := fs.Bool("yes", false, "confirm a replay whose original call cost is above the threshold or unknown")
	server := fs.String("server", "", "dashboard base URL (default http://<dashboard-addr>)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: lens replay <id> [--set path=value]... [--dump file] [--no-capture] [--diff id] [--yes]")
		fmt.Fprintln(fs.Output(), "\n"+replaySafety)
		fmt.Fprintln(fs.Output(), "flags:")
		fs.PrintDefaults()
	}
	// stdlib flag stops parsing at the first non-flag argument, while the bead
	// documents the id first (`lens replay <id> [--set ...]`). Rotating a single
	// leading id to the back lets both orders work without hand-parsing the
	// command line.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(args[1:], args[0])
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("replay: want exactly one request id")
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("replay: invalid request id %q", fs.Arg(0))
	}

	// Edits are parsed before anything else runs, so a malformed --set fails
	// before a request is made and before a file is written.
	edits, err := replay.ParseSets(sets)
	if err != nil {
		return err
	}

	if *dump != "" {
		return dumpReplay(cfg.DBPath, *dump, id, edits, w)
	}

	base := strings.TrimRight(*server, "/")
	if base == "" {
		base = "http://" + cfg.DashboardAddr
	}
	client := &http.Client{Timeout: replayClientTimeout}

	orig, err := getRequestDetail(client, base, id)
	if err != nil {
		return err
	}

	// Cost gate: say what this is going to cost before spending it, and refuse
	// outright when the price is unknown — an unpriced original is exactly the
	// case where the user has no way to judge the spend. Refusing returns
	// before the POST below, so nothing is sent.
	note := costSourceNote(orig.CostSource)
	fmt.Fprintf(w, "replaying request %d: %s %s\n", orig.ID, orig.Method, orig.Path)
	fmt.Fprintf(w, "  original cost: %s%s\n", humanCost(orig.CostUSD), note)
	if reason, gated := costGate(orig.CostUSD, cfg.ReplayCostThresholdUSD); gated && !*yes {
		return fmt.Errorf("refusing to send: %s; re-run with --yes to confirm this spend", reason)
	}

	result, err := postReplay(client, base, id, sets, *noCapture)
	if err != nil {
		return err
	}
	if !result.Captured {
		fmt.Fprintf(w, "replay sent, not recorded (--no-capture): upstream status %d\n", result.Status)
		return nil
	}
	if result.Outcome == nil {
		return fmt.Errorf("replay: server recorded replay %d but returned no outcome to compare", result.ID)
	}
	fmt.Fprintf(w, "replayed %d → %d\n", id, result.ID)

	// The baseline is the original unless --diff names another capture, which is
	// how a replay is compared against, say, the previous attempt at the same
	// prompt rather than the call it was re-issued from.
	baseline := &requestDetail{Request: orig.Request, Warnings: orig.Warnings}
	if *diff != 0 {
		if baseline, err = getRequestDetail(client, base, *diff); err != nil {
			return err
		}
	}
	fmt.Fprintln(w)
	printOutcomeDiff(w, replay.OutcomeOf(baseline.Request, baseline.Warnings), *result.Outcome)
	return nil
}

// costGate reports whether a replay of a call costing cost needs explicit
// confirmation, and why. Unpriced (nil cost) is gated: the user cannot judge a
// spend the tool cannot compute, so silence would be the wrong default.
func costGate(cost *float64, thresholdUSD float64) (reason string, gated bool) {
	if cost == nil {
		return "the original call has no price, so its cost is unknown", true
	}
	if *cost > thresholdUSD {
		return fmt.Sprintf("the original call cost $%.4f, above the $%.4f threshold", *cost, thresholdUSD), true
	}
	return "", false
}

// dumpReplay is the whole --dump path: read the stored body, apply the edits,
// write the result to outPath, and stop. No HTTP request is made and no server
// needs to be running, which is what makes it the way to inspect exactly what a
// replay would send before sending it.
//
// It opens the store directly — the one place this subcommand reads the
// database — and only reads: the row it looks at is the original, and the rows
// a real replay creates are still written solely by the running server's
// consumer.
func dumpReplay(dbPath, outPath string, id int64, edits []replay.Edit, w io.Writer) error {
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	orig, err := st.GetRequest(context.Background(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no request with id %d", id)
		}
		return err
	}
	edited, err := replay.ApplyEdits(orig.ReqBody, edits)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, edited, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %d bytes of edited body to %s (nothing sent)\n", len(edited), outPath)
	return nil
}

// requestDetail mirrors internal/api's requestDetail response shape — the
// request's own fields promoted, plus its warnings — so the CLI reads exactly
// what the dashboard reads.
type requestDetail struct {
	*store.Request
	Warnings []*store.Warning `json:"warnings"`
}

// getRequestDetail reads one request through GET /api/requests/{id}, a
// read-only endpoint. Reusing it rather than opening the database is what
// keeps `lens replay` free of any local write path at all.
func getRequestDetail(client *http.Client, base string, id int64) (*requestDetail, error) {
	u := fmt.Sprintf("%s/api/requests/%d", base, id)
	res, err := client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("replay: %v (is `lens serve` running? it is required — replay never opens the database itself)", err)
	}
	defer res.Body.Close()

	var d requestDetail
	if err := decodeAPIResponse(res, &d); err != nil {
		if res.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("no request with id %d", id)
		}
		return nil, err
	}
	// --server may point at something that is not lens; a response shaped like
	// nothing at all must be an error rather than a nil dereference.
	if d.Request == nil {
		return nil, fmt.Errorf("replay: %s returned no request object for id %d", base, id)
	}
	return &d, nil
}

// postReplay asks the server to send the replay and returns its Result.
func postReplay(client *http.Client, base string, id int64, sets []string, noCapture bool) (*replay.Result, error) {
	q := url.Values{}
	for _, s := range sets {
		q.Add("set", s)
	}
	if noCapture {
		q.Set("no_capture", "true")
	}
	u := fmt.Sprintf("%s/api/requests/%d/replay", base, id)
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}

	res, err := client.Post(u, "application/json", nil)
	if err != nil {
		return nil, fmt.Errorf("replay: %v (is `lens serve` running? it is required — replay never opens the database itself)", err)
	}
	defer res.Body.Close()

	var out replay.Result
	if err := decodeAPIResponse(res, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// decodeAPIResponse decodes a JSON body, turning a non-2xx status into an error
// carrying the server's own {"error": ...} message — the same convention the
// dashboard's fetchJSON uses, so a 403 (replay disabled, or the guard) reaches
// the user as the reason it happened rather than as a bare status code.
func decodeAPIResponse(res *http.Response, v interface{}) error {
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := res.Status
		var body struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(res.Body).Decode(&body); err == nil && body.Error != "" {
			msg = body.Error
		}
		return fmt.Errorf("replay: server said %d: %s", res.StatusCode, msg)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		return fmt.Errorf("replay: decode response: %w", err)
	}
	return nil
}

// costSourceNote qualifies a cost with why it is what it is, for the source
// that says "there is no price" — see costColumn, same rule for one call.
func costSourceNote(source *string) string {
	if source == nil || *source == "configured" {
		return ""
	}
	return " (" + *source + ")"
}

// printOutcomeDiff renders the before/after comparison the bead asks for:
// status, model, tokens, cost, duration and warnings, one row per field. A
// leading "≠" marks the rows that actually differ, so the difference is not
// carried by anything a monochrome terminal or a screen reader would drop.
func printOutcomeDiff(w io.Writer, baseline, replayed replay.Outcome) {
	pair := func(o replay.Outcome) string {
		return fmt.Sprintf("%s/%s", humanTokens(int64(o.InputTokens)), humanTokens(int64(o.OutputTokens)))
	}
	dur := func(o replay.Outcome) string { return humanDuration(msToDuration(o.DurationMs)) }
	warns := func(o replay.Outcome) string { return strconv.Itoa(len(o.Warnings)) }

	cells := [][3]string{
		{"status", strconv.Itoa(baseline.Status), strconv.Itoa(replayed.Status)},
		{"model", displayOrDash(baseline.Model), displayOrDash(replayed.Model)},
		{"tokens", pair(baseline), pair(replayed)},
		{"cost", humanCost(baseline.CostUSD), humanCost(replayed.CostUSD)},
		{"duration", dur(baseline), dur(replayed)},
		{"warnings", warns(baseline), warns(replayed)},
	}
	rows := make([][]string, 0, len(cells))
	for _, c := range cells {
		mark := ""
		if c[1] != c[2] {
			mark = "≠"
		}
		rows = append(rows, []string{mark, c[0], c[1], c[2]})
	}
	fmt.Fprint(w, table([]string{"", "FIELD",
		fmt.Sprintf("REQUEST %d", baseline.ID),
		fmt.Sprintf("REPLAY %d", replayed.ID),
	}, rows, 0))

	// Warning detail, when either side has any: the counts above say whether
	// something changed, which warnings say what.
	for _, o := range []replay.Outcome{baseline, replayed} {
		for _, line := range o.Warnings {
			fmt.Fprintf(w, "  #%d warning: %s\n", o.ID, line)
		}
	}
}

func displayOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
