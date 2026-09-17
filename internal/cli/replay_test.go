package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/analyze"
	"github.com/abhisheksarkar30/deepseek-lens/internal/api"
	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/consumer"
	"github.com/abhisheksarkar30/deepseek-lens/internal/proxy"
	"github.com/abhisheksarkar30/deepseek-lens/internal/sink"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
	"github.com/abhisheksarkar30/deepseek-lens/internal/web"
)

// replayUpstream is the recording fake the CLI is pointed at. Counting hits is
// how every "refused, so nothing was sent" assertion is made.
type replayUpstream struct {
	*httptest.Server

	mu     sync.Mutex
	bodies []string
}

func newReplayUpstream(t *testing.T) *replayUpstream {
	t.Helper()
	u := &replayUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.bodies = append(u.bodies, string(body))
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_2","model":"deepseek-flash","stop_reason":"end_turn",`+
			`"usage":{"input_tokens":11,"output_tokens":22}}`)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *replayUpstream) hits() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bodies)
}

func (u *replayUpstream) lastBody() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) == 0 {
		return ""
	}
	return u.bodies[len(u.bodies)-1]
}

// replayRig is one assembled lens process — store, sink, consumer, proxy
// handler, dashboard API behind a real HTTP server — plus a recording upstream
// and the config `lens replay` would resolve against it.
type replayRig struct {
	store *store.Store
	up    *replayUpstream
	dash  *httptest.Server
	cfg   *config.Config
}

func newReplayRig(t *testing.T) *replayRig {
	t.Helper()
	up := newReplayUpstream(t)

	dbPath := filepath.Join(t.TempDir(), "lens.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	cfg := config.Default()
	cfg.DBPath = dbPath
	cfg.UpstreamURL = up.URL
	cfg.Capture = true

	sk := sink.New(64)
	cons := consumer.New(sk, st, nil, analyze.NewRules(cfg.ModelMap, cfg.ModelMaxTokens))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = cons.Run(ctx)
	}()

	proxyHandler, err := proxy.New(cfg, sk)
	if err != nil {
		t.Fatalf("proxy.New: %v", err)
	}
	dash := httptest.NewServer(api.New(st, sk, cons, api.NewBroker(), web.Files, proxyHandler, true))
	cfg.DashboardAddr = strings.TrimPrefix(dash.URL, "http://")

	t.Cleanup(func() {
		dash.Close()
		cancel()
		<-done
		st.Close()
	})
	return &replayRig{store: st, up: up, dash: dash, cfg: cfg}
}

// cfgCopy returns a copy so a test can point one field somewhere else without
// disturbing the rest of the rig.
func (r *replayRig) cfgCopy() *config.Config {
	c := *r.cfg
	return &c
}

// withBody is the opts hook for cli_test.go's seedRequest: it swaps in a body
// the replay's --set can actually edit.
func withBody(body string) func(*store.Request) {
	return func(r *store.Request) { r.ReqBody = []byte(body) }
}

const editableBody = `{"model":"deepseek-chat","temperature":0.2,"messages":[{"role":"user","content":"hi"}]}`

// --- the cost gate: refuse before sending, send on --yes ---

func TestReplayCostGateRefusesAnUnpricedOriginalWithoutYes(t *testing.T) {
	rig := newReplayRig(t)
	orig := seedRequest(t, rig.store, func(r *store.Request) {
		r.CostUSD = nil // unpriced: the cost is unknown, which is the worst case to spend blind on
		r.ReqBody = []byte(editableBody)
	})

	var buf bytes.Buffer
	err := runReplay([]string{fmt.Sprint(orig.ID)}, &buf, rig.cfgCopy())
	if err == nil {
		t.Fatal("runReplay: want a refusal for an unpriced original without --yes, got nil")
	}
	if !strings.Contains(err.Error(), "no price") || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error = %v, want it to explain the unknown cost and name --yes", err)
	}
	if !strings.Contains(buf.String(), "original cost: —") {
		t.Errorf("output = %q, want the original's unknown cost printed before the decision", buf.String())
	}
	if n := rig.up.hits(); n != 0 {
		t.Fatalf("upstream hits = %d, want 0: a refused replay must send nothing", n)
	}
}

func TestReplayCostGateRefusesAnExpensiveOriginalWithoutYes(t *testing.T) {
	rig := newReplayRig(t)
	orig := seedRequest(t, rig.store, func(r *store.Request) {
		cost := 5.00 // well above the 0.25 default
		r.CostUSD = &cost
		r.ReqBody = []byte(editableBody)
	})

	var buf bytes.Buffer
	err := runReplay([]string{fmt.Sprint(orig.ID)}, &buf, rig.cfgCopy())
	if err == nil {
		t.Fatal("runReplay: want a refusal above the threshold without --yes, got nil")
	}
	if !strings.Contains(err.Error(), "threshold") {
		t.Errorf("error = %v, want it to name the threshold", err)
	}
	if !strings.Contains(buf.String(), "original cost: $5.0000") {
		t.Errorf("output = %q, want the $5.0000 cost printed", buf.String())
	}
	if n := rig.up.hits(); n != 0 {
		t.Fatalf("upstream hits = %d, want 0", n)
	}
}

func TestReplayCostGateSendsWithYesAndWithACheapOriginal(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		cost *float64
	}{
		{"yes overrides the gate", []string{"--yes"}, ptrTo(5.00)},
		{"a cheap original needs no flag", nil, ptrTo(0.001)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newReplayRig(t)
			orig := seedRequest(t, rig.store, func(r *store.Request) {
				r.CostUSD = tc.cost
				r.ReqBody = []byte(editableBody)
			})

			args := append([]string{fmt.Sprint(orig.ID), "--set", "temperature=0.7"}, tc.args...)
			var buf bytes.Buffer
			if err := runReplay(args, &buf, rig.cfgCopy()); err != nil {
				t.Fatalf("runReplay(%q): %v", args, err)
			}
			if n := rig.up.hits(); n != 1 {
				t.Fatalf("upstream hits = %d, want exactly 1", n)
			}
			if body := rig.up.lastBody(); !strings.Contains(body, `"temperature":0.7`) {
				t.Errorf("upstream body = %s, want the --set edit applied", body)
			}
			if !strings.Contains(buf.String(), fmt.Sprintf("replayed %d →", orig.ID)) {
				t.Errorf("output = %q, want the new request id printed", buf.String())
			}
		})
	}
}

// The gate is a config value, not a constant: a user who raises the threshold
// stops being asked about a call that used to need --yes.
func TestReplayCostGateFollowsTheConfiguredThreshold(t *testing.T) {
	rig := newReplayRig(t)
	orig := seedRequest(t, rig.store, func(r *store.Request) {
		cost := 5.00
		r.CostUSD = &cost
		r.ReqBody = []byte(editableBody)
	})

	cfg := rig.cfgCopy()
	cfg.ReplayCostThresholdUSD = 10.00
	var buf bytes.Buffer
	if err := runReplay([]string{fmt.Sprint(orig.ID)}, &buf, cfg); err != nil {
		t.Fatalf("runReplay: %v", err)
	}
	if n := rig.up.hits(); n != 1 {
		t.Fatalf("upstream hits = %d, want 1 under a raised threshold", n)
	}
}

// --- the send path never opens the database ---

// Replay's send path must not become a second writer (lens purge, br-GI-17-08,
// is the one CLI path that deliberately does open the store directly — see
// replay.go's prelude). Pointing DBPath at an unopenable path is the direct
// test: if runReplay opened the store, this would fail.
func TestReplaySendPathNeverOpensTheDatabase(t *testing.T) {
	rig := newReplayRig(t)
	orig := seedRequest(t, rig.store, withBody(editableBody))

	// A file where the database's parent directory would have to be: store.Open
	// cannot create it, so any attempt to open the store fails loudly.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg := rig.cfgCopy()
	cfg.DBPath = filepath.Join(blocker, "lens.db")

	var buf bytes.Buffer
	if err := runReplay([]string{fmt.Sprint(orig.ID)}, &buf, cfg); err != nil {
		t.Fatalf("runReplay: %v (the send path must not touch the local database)", err)
	}
	if n := rig.up.hits(); n != 1 {
		t.Fatalf("upstream hits = %d, want 1", n)
	}
}

func TestReplayWithoutAServerFailsClearlyAndWritesNothing(t *testing.T) {
	rig := newReplayRig(t)
	orig := seedRequest(t, rig.store, withBody(editableBody))

	cfg := rig.cfgCopy()
	// A port nothing is listening on. `lens replay` is a client of the running
	// server's API; with no server there is nowhere to send from.
	cfg.DashboardAddr = "127.0.0.1:1"

	var buf bytes.Buffer
	err := runReplay([]string{fmt.Sprint(orig.ID)}, &buf, cfg)
	if err == nil {
		t.Fatal("runReplay: want an error with no server running, got nil")
	}
	if !strings.Contains(err.Error(), "lens serve") {
		t.Errorf("error = %v, want it to say the server is required", err)
	}
	if n := rig.up.hits(); n != 0 {
		t.Fatalf("upstream hits = %d, want 0 with no server", n)
	}
	reqs, err := rig.store.ListRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("store has %d requests, want just the seeded original: nothing was written", len(reqs))
	}
}

// --- --dump: purely local, no send, no server ---

func TestReplayDumpWritesTheEditedBodyAndSendsNothing(t *testing.T) {
	rig := newReplayRig(t)
	orig := seedRequest(t, rig.store, withBody(editableBody))

	out := filepath.Join(t.TempDir(), "would-send.json")
	// --server points at a dead port on purpose: --dump must not need a server.
	var buf bytes.Buffer
	err := runReplay([]string{fmt.Sprint(orig.ID), "--set", "temperature=0.7",
		"--dump", out, "--server", "http://127.0.0.1:1"}, &buf, rig.cfgCopy())
	if err != nil {
		t.Fatalf("runReplay --dump: %v", err)
	}

	written, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", out, err)
	}
	if !strings.Contains(string(written), `"temperature":0.7`) {
		t.Errorf("dumped body = %s, want the edit applied", written)
	}
	if !strings.Contains(string(written), `"content":"hi"`) {
		t.Errorf("dumped body = %s, want the rest of the body intact", written)
	}
	if n := rig.up.hits(); n != 0 {
		t.Fatalf("upstream hits = %d, want 0: --dump never sends", n)
	}
	if !strings.Contains(buf.String(), "nothing sent") {
		t.Errorf("output = %q, want it to say nothing was sent", buf.String())
	}
	// Reading the stored body is all --dump does to the database: no row is
	// written, because --dump never opens a writer at all (the running
	// server's consumer is the only writer that ever inserts a request row —
	// lens purge, the one CLI path that opens the store, only deletes).
	reqs, err := rig.store.ListRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("store has %d requests, want just the seeded original", len(reqs))
	}
}

func TestReplayDumpRejectsABadEdit(t *testing.T) {
	rig := newReplayRig(t)
	orig := seedRequest(t, rig.store, withBody(editableBody))

	out := filepath.Join(t.TempDir(), "never-written.json")
	var buf bytes.Buffer
	err := runReplay([]string{fmt.Sprint(orig.ID), "--set", "nope.deep=1", "--dump", out}, &buf, rig.cfgCopy())
	if err == nil {
		t.Fatal("runReplay --dump: want an error for a path that does not exist")
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("the dump file was written despite the edit failing")
	}
}

// --- --no-capture ---

func TestReplayNoCaptureSendsButIsNotRecorded(t *testing.T) {
	rig := newReplayRig(t)
	orig := seedRequest(t, rig.store, withBody(editableBody))

	var buf bytes.Buffer
	if err := runReplay([]string{fmt.Sprint(orig.ID), "--no-capture", "--set", "temperature=0.7"}, &buf, rig.cfgCopy()); err != nil {
		t.Fatalf("runReplay --no-capture: %v", err)
	}
	if n := rig.up.hits(); n != 1 {
		t.Fatalf("upstream hits = %d, want 1: --no-capture still sends", n)
	}
	if !strings.Contains(buf.String(), "not recorded") {
		t.Errorf("output = %q, want it to say the replay was not recorded", buf.String())
	}
	replays, err := rig.store.ListRequests(context.Background(), store.Filter{ReplayOf: &orig.ID})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(replays) != 0 {
		t.Fatalf("got %d replay rows, want 0 with --no-capture", len(replays))
	}
}

// --- the diff ---

func TestReplayDiffComparesAgainstTheOriginal(t *testing.T) {
	rig := newReplayRig(t)
	orig := seedRequest(t, rig.store, func(r *store.Request) {
		r.ReqBody = []byte(editableBody)
		r.InputTokens = 100
		r.OutputTokens = 200
		r.Status = 201
	})
	if err := rig.store.InsertWarnings(context.Background(), orig.ID, []store.Warning{
		{Kind: "dropped_param", Severity: "warn", Detail: "top_k dropped", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}

	var buf bytes.Buffer
	if err := runReplay([]string{fmt.Sprint(orig.ID)}, &buf, rig.cfgCopy()); err != nil {
		t.Fatalf("runReplay: %v", err)
	}
	out := buf.String()

	// The baseline's own values and the replay's, side by side.
	for _, want := range []string{
		fmt.Sprintf("REQUEST %d", orig.ID),
		"201",     // the original's status
		"100/200", // the original's tokens
		"11/22",   // the replay's, parsed from the upstream reply
		"deepseek-flash",
		"warnings",
		"dropped_param: top_k dropped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output missing %q:\n%s", want, out)
		}
	}
}

func TestReplayDiffAgainstAnotherRequest(t *testing.T) {
	rig := newReplayRig(t)
	orig := seedRequest(t, rig.store, withBody(editableBody))
	other := seedRequest(t, rig.store, func(r *store.Request) {
		r.Status = 503
		r.InputTokens = 77
		r.OutputTokens = 88
	})

	var buf bytes.Buffer
	if err := runReplay([]string{fmt.Sprint(orig.ID), "--diff", fmt.Sprint(other.ID)}, &buf, rig.cfgCopy()); err != nil {
		t.Fatalf("runReplay --diff: %v", err)
	}
	if !strings.Contains(buf.String(), fmt.Sprintf("REQUEST %d", other.ID)) {
		t.Errorf("output = %q, want the comparison against request %d", buf.String(), other.ID)
	}
	if !strings.Contains(buf.String(), "77/88") {
		t.Errorf("output = %q, want the other request's tokens", buf.String())
	}
}

func TestReplayDiffAgainstAMissingRequestErrors(t *testing.T) {
	rig := newReplayRig(t)
	orig := seedRequest(t, rig.store, withBody(editableBody))

	var buf bytes.Buffer
	err := runReplay([]string{fmt.Sprint(orig.ID), "--diff", "9999"}, &buf, rig.cfgCopy())
	if err == nil {
		t.Fatal("runReplay --diff 9999: want an error for a request id that does not exist")
	}
	if !strings.Contains(err.Error(), "9999") {
		t.Errorf("error = %v, want it to name the missing id", err)
	}
}

// --- flags and help ---

func TestReplayRejectsAMalformedSetBeforeSending(t *testing.T) {
	rig := newReplayRig(t)
	orig := seedRequest(t, rig.store, withBody(editableBody))

	var buf bytes.Buffer
	if err := runReplay([]string{fmt.Sprint(orig.ID), "--set", "temperature"}, &buf, rig.cfgCopy()); err == nil {
		t.Fatal("runReplay --set temperature: want an error for a set without a value")
	}
	if n := rig.up.hits(); n != 0 {
		t.Fatalf("upstream hits = %d, want 0", n)
	}
}

func TestReplayRequiresExactlyOneID(t *testing.T) {
	rig := newReplayRig(t)
	var buf bytes.Buffer
	if err := runReplay(nil, &buf, rig.cfgCopy()); err == nil {
		t.Fatal("runReplay with no id: want an error")
	}
	if err := runReplay([]string{"1", "2"}, &buf, rig.cfgCopy()); err == nil {
		t.Fatal("runReplay with two ids: want an error")
	}
}

// The help text has to say what replay does and does not do: "replay" reads like
// "re-run the agent", and it is the one path that can spend money.
func TestReplayHelpStatesTheSafetyPosture(t *testing.T) {
	rig := newReplayRig(t)
	var buf bytes.Buffer
	if err := runReplay([]string{"--help"}, &buf, rig.cfgCopy()); err == nil {
		t.Fatal("runReplay --help: want flag.ErrHelp")
	}
	out := buf.String()
	for _, want := range []string{
		"never executes anything",
		"never touches your repository",
		"does not act on tool_use",
		"billable",
		"--yes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("--help output missing %q:\n%s", want, out)
		}
	}
}

func ptrTo(v float64) *float64 { return &v }
