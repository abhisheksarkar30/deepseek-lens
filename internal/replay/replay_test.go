// Package replay_test holds the replay path's integration tests: a real store,
// a real consumer (the single writer), the real proxy Handler, the real API
// endpoint behind an httptest server, and a recording fake upstream. It is an
// external test package because internal/api imports internal/replay (the
// endpoint applies the edits), so a same-package test importing the API would
// be an import cycle.
//
// The assertion that matters most in every rejected case is that the fake
// upstream saw **zero** hits: the guard's whole claim is that it rejects before
// any upstream send, and a status-code assertion alone would not catch a guard
// that ran after the spend.
package replay_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/analyze"
	"github.com/abhisheksarkar30/deepseek-lens/internal/api"
	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/consumer"
	"github.com/abhisheksarkar30/deepseek-lens/internal/pricing"
	"github.com/abhisheksarkar30/deepseek-lens/internal/proxy"
	"github.com/abhisheksarkar30/deepseek-lens/internal/replay"
	"github.com/abhisheksarkar30/deepseek-lens/internal/sink"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// upstreamBody is what the fake upstream answers with: a small non-streaming
// response whose usage the consumer can actually parse, so a replayed row has
// real tokens and a real resolved model to diff.
const upstreamBody = `{"id":"msg_2","model":"deepseek-flash","stop_reason":"end_turn",` +
	`"usage":{"input_tokens":11,"output_tokens":22}}`

// upstream is the recording fake the replay path is tested against. It counts
// hits and remembers every request body, which is how a test asserts both
// "nothing was sent" and "exactly this was sent".
type upstream struct {
	*httptest.Server

	mu     sync.Mutex
	bodies []string
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.bodies = append(u.bodies, string(body))
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, upstreamBody)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *upstream) hits() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bodies)
}

func (u *upstream) lastBody() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) == 0 {
		return ""
	}
	return u.bodies[len(u.bodies)-1]
}

// stack is one assembled lens process, minus the two listeners: the store, the
// sink, the consumer goroutine, the proxy Handler, and the dashboard handler
// behind a real HTTP server.
type stack struct {
	store *store.Store
	up    *upstream
	dash  *httptest.Server
}

func newStack(t *testing.T, replayEnabled bool) *stack {
	t.Helper()
	up := newUpstream(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "lens.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	cfg := config.Default()
	cfg.UpstreamURL = up.URL
	cfg.Capture = true

	sk := sink.New(64)
	cons := consumer.New(sk, st, nil, analyze.NewRules(cfg.ModelMap, cfg.ModelMaxTokens, pricing.Calendar{}))

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

	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}}
	dash := httptest.NewServer(api.New(st, sk, cons, api.NewBroker(), assets, proxyHandler, replayEnabled))

	t.Cleanup(func() {
		dash.Close()
		cancel() // the consumer drains and flushes on ctx cancellation
		<-done
		st.Close()
	})
	return &stack{store: st, up: up, dash: dash}
}

// seedOriginal inserts the captured request a replay is re-issued from, with a
// cost the cost gate would accept (the gate itself is internal/cli's, tested
// there).
func (s *stack) seedOriginal(t *testing.T) *store.Request {
	t.Helper()
	cost := 0.001
	r := &store.Request{
		StartedAt:   time.Now().Add(-time.Minute),
		Duration:    50 * time.Millisecond,
		Method:      "POST",
		Path:        "/v1/messages",
		RemoteAddr:  "127.0.0.1:1234",
		Status:      200,
		ReqHeaders:  `{"Content-Type":["application/json"],"X-Api-Key":["[redacted]"]}`,
		RespHeaders: `{"Content-Type":["application/json"]}`,
		ReqBody:     []byte(`{"model":"deepseek-chat","temperature":0.2,"messages":[{"role":"user","content":"hi"}]}`),
		RespBody:    []byte(upstreamBody),
		CostUSD:     &cost,
		PrefixHash:  "abc123",
	}
	if _, err := s.store.InsertRequest(context.Background(), r); err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	return r
}

// post issues POST /api/requests/<id>/replay[?query] against the dashboard,
// after letting mutate adjust the request (Host, Origin).
func (s *stack) post(t *testing.T, id int64, query string, mutate func(*http.Request)) *http.Response {
	t.Helper()
	url := fmt.Sprintf("%s/api/requests/%d/replay", s.dash.URL, id)
	if query != "" {
		url += "?" + query
	}
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if mutate != nil {
		mutate(req)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func decodeResult(t *testing.T, res *http.Response) replay.Result {
	t.Helper()
	var out replay.Result
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode replay result: %v", err)
	}
	return out
}

func (s *stack) replayRowCount(t *testing.T, origID int64) int {
	t.Helper()
	rows, err := s.store.ListRequests(context.Background(), store.Filter{ReplayOf: &origID, Limit: 10})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	return len(rows)
}

// --- the happy path: enabled, loopback Host, no Origin (the CLI's request) ---

func TestReplayEndpointAcceptsAndRecordsTheReplay(t *testing.T) {
	s := newStack(t, true)
	orig := s.seedOriginal(t)

	res := s.post(t, orig.ID, "set=temperature=0.7", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.StatusCode, readBody(t, res))
	}
	got := decodeResult(t, res)
	if !got.Captured || got.ID == 0 {
		t.Fatalf("result = %+v, want a captured replay with an id", got)
	}
	if got.Outcome == nil {
		t.Fatal("result carries no outcome, want the recorded call's fields")
	}
	if got.Outcome.InputTokens != 11 || got.Outcome.OutputTokens != 22 {
		t.Errorf("outcome tokens = %d/%d, want 11/22 (parsed from the upstream reply)",
			got.Outcome.InputTokens, got.Outcome.OutputTokens)
	}
	if got.Outcome.Model != "deepseek-flash" {
		t.Errorf("outcome model = %q, want the upstream-resolved one", got.Outcome.Model)
	}

	// Exactly one upstream hit, carrying the edited body: the replay really was
	// sent, and really was sent with the edit applied.
	if n := s.up.hits(); n != 1 {
		t.Fatalf("upstream hits = %d, want exactly 1", n)
	}
	if body := s.up.lastBody(); !strings.Contains(body, `"temperature":0.7`) {
		t.Errorf("upstream body = %s, want the --set edit applied", body)
	}

	// The new row is a first-class request, linked to the original and
	// documenting what was changed.
	row, err := s.store.GetRequest(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("GetRequest(%d): %v", got.ID, err)
	}
	if row.ReplayOf == nil || *row.ReplayOf != orig.ID {
		t.Errorf("ReplayOf = %v, want %d", row.ReplayOf, orig.ID)
	}
	if row.ReplayEdits == nil || !strings.Contains(*row.ReplayEdits, `"path":"temperature"`) {
		t.Errorf("ReplayEdits = %v, want the applied edit recorded", row.ReplayEdits)
	}
	if row.Status != 200 {
		t.Errorf("status = %d, want 200", row.Status)
	}
	if string(row.ReqBody) == string(orig.ReqBody) {
		t.Error("the stored replay body equals the original, want the edited one")
	}
}

// The dashboard's own request: a same-origin browser fetch sends Origin equal to
// the Host it is talking to, which is the case the guard must let through.
func TestReplayEndpointAcceptsTheDashboardsOwnOrigin(t *testing.T) {
	s := newStack(t, true)
	orig := s.seedOriginal(t)

	res := s.post(t, orig.ID, "", func(r *http.Request) { r.Header.Set("Origin", s.dash.URL) })
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for the dashboard's own origin: %s", res.StatusCode, readBody(t, res))
	}
	if n := s.up.hits(); n != 1 {
		t.Fatalf("upstream hits = %d, want 1", n)
	}
}

// --- rejections: every one must be a 403 with zero upstream hits ---

func TestReplayEndpointRejectsWhenDisabled(t *testing.T) {
	s := newStack(t, false)
	orig := s.seedOriginal(t)

	res := s.post(t, orig.ID, "set=temperature=0.7", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 when replay is disabled: %s", res.StatusCode, readBody(t, res))
	}
	assertNothingSent(t, s, orig.ID)
}

// replayRejectedCount reads /api/health's replay_rejected counter — the
// server-side trace a rejected replay probe leaves behind.
func (s *stack) replayRejectedCount(t *testing.T) uint64 {
	t.Helper()
	res, err := http.Get(s.dash.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer res.Body.Close()
	var body struct {
		ReplayRejected uint64 `json:"replay_rejected"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode /api/health: %v", err)
	}
	return body.ReplayRejected
}

func TestReplayEndpointCountsRejections(t *testing.T) {
	s := newStack(t, true)
	orig := s.seedOriginal(t)

	before := s.replayRejectedCount(t)
	res := s.post(t, orig.ID, "", func(r *http.Request) {
		r.Header.Set("Origin", "http://evil.example")
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a foreign Origin: %s", res.StatusCode, readBody(t, res))
	}
	if after := s.replayRejectedCount(t); after != before+1 {
		t.Fatalf("replay_rejected = %d, want %d (one rejection counted)", after, before+1)
	}
}

func TestReplayEndpointRejectsACrossOriginPage(t *testing.T) {
	s := newStack(t, true)
	orig := s.seedOriginal(t)

	res := s.post(t, orig.ID, "", func(r *http.Request) {
		r.Header.Set("Origin", "http://evil.example")
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a foreign Origin: %s", res.StatusCode, readBody(t, res))
	}
	assertNothingSent(t, s, orig.ID)
}

// A different loopback port is a different origin, and must be rejected: "the
// dashboard's own origin" is not merely "some loopback address".
func TestReplayEndpointRejectsAnotherLoopbackOrigin(t *testing.T) {
	s := newStack(t, true)
	orig := s.seedOriginal(t)

	res := s.post(t, orig.ID, "", func(r *http.Request) {
		r.Header.Set("Origin", "http://127.0.0.1:1")
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a different loopback origin: %s", res.StatusCode, readBody(t, res))
	}
	assertNothingSent(t, s, orig.ID)
}

// DNS rebinding: the connection arrives over loopback but the Host is a foreign
// name, which is what a rebinding page looks like.
func TestReplayEndpointRejectsANonLoopbackHost(t *testing.T) {
	s := newStack(t, true)
	orig := s.seedOriginal(t)

	res := s.post(t, orig.ID, "", func(r *http.Request) { r.Host = "sneaky.example" })
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a non-loopback Host: %s", res.StatusCode, readBody(t, res))
	}
	assertNothingSent(t, s, orig.ID)
}

// A rebinding host is rejected even when the Origin agrees with it, so the two
// controls cannot be satisfied into a bypass by a page that sets both.
func TestReplayEndpointRejectsARebindingHostWithMatchingOrigin(t *testing.T) {
	s := newStack(t, true)
	orig := s.seedOriginal(t)

	res := s.post(t, orig.ID, "", func(r *http.Request) {
		r.Host = "sneaky.example"
		r.Header.Set("Origin", "http://sneaky.example")
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a rebinding Host: %s", res.StatusCode, readBody(t, res))
	}
	assertNothingSent(t, s, orig.ID)
}

func assertNothingSent(t *testing.T, s *stack, origID int64) {
	t.Helper()
	if n := s.up.hits(); n != 0 {
		t.Fatalf("upstream hits = %d, want 0: a rejected replay must not reach the upstream at all", n)
	}
	if n := s.replayRowCount(t, origID); n != 0 {
		t.Fatalf("replay rows = %d, want 0: a rejected replay must not be recorded", n)
	}
}

func TestReplayEndpointRejectsAMissingCapture(t *testing.T) {
	s := newStack(t, true)

	res := s.post(t, 999, "", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown request id: %s", res.StatusCode, readBody(t, res))
	}
	if n := s.up.hits(); n != 0 {
		t.Fatalf("upstream hits = %d, want 0", n)
	}
}

func TestReplayEndpointRejectsABadEditWithoutSending(t *testing.T) {
	s := newStack(t, true)
	orig := s.seedOriginal(t)

	res := s.post(t, orig.ID, "set=messages.99.content=x", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unapplyable edit: %s", res.StatusCode, readBody(t, res))
	}
	if msg := readBody(t, res); !strings.Contains(msg, "out of range") {
		t.Errorf("error body = %s, want it to explain the out-of-range index", msg)
	}
	assertNothingSent(t, s, orig.ID)
}

// --- --no-capture: sent, but deliberately not recorded ---

func TestReplayNoCaptureSendsWithoutWritingARow(t *testing.T) {
	s := newStack(t, true)
	orig := s.seedOriginal(t)

	res := s.post(t, orig.ID, "set=temperature=0.7&no_capture=true", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.StatusCode, readBody(t, res))
	}
	got := decodeResult(t, res)
	if got.Captured {
		t.Errorf("result = %+v, want Captured false", got)
	}
	if got.ID != 0 {
		t.Errorf("id = %d, want 0: no row was written, so there is no id", got.ID)
	}
	if got.Status != 200 {
		t.Errorf("status = %d, want the upstream's 200 read off the live response", got.Status)
	}

	if n := s.up.hits(); n != 1 {
		t.Fatalf("upstream hits = %d, want exactly 1: --no-capture must still send", n)
	}
	if n := s.replayRowCount(t, orig.ID); n != 0 {
		t.Fatalf("replay rows = %d, want 0: --no-capture must write nothing", n)
	}
}

// A replay with no --set still records its linkage: replay_of is what says a
// call was re-issued at all, and replay_edits stays NULL when nothing was
// changed, so the column's NULL-ness keeps meaning "this replay altered
// nothing".
func TestReplayWithoutEditsRecordsLinkageButNoEditsBlob(t *testing.T) {
	s := newStack(t, true)
	orig := s.seedOriginal(t)

	res := s.post(t, orig.ID, "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.StatusCode, readBody(t, res))
	}
	row, err := s.store.GetRequest(context.Background(), decodeResult(t, res).ID)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if row.ReplayOf == nil || *row.ReplayOf != orig.ID {
		t.Errorf("ReplayOf = %v, want %d", row.ReplayOf, orig.ID)
	}
	if row.ReplayEdits != nil {
		t.Errorf("ReplayEdits = %v, want nil for a replay with no edits", row.ReplayEdits)
	}
	if string(row.ReqBody) != string(orig.ReqBody) {
		t.Error("the stored body differs from the original, but no edits were asked for")
	}
	if n := s.up.hits(); n != 1 {
		t.Fatalf("upstream hits = %d, want 1", n)
	}
}

// TestOutcomeOfModelFallback covers OutcomeOf's one branch: a request whose
// resolved model is unknown — an unpriced/unparsed upstream response — must
// still name the model by what the client asked for, since every surface that
// shows a model falls back the same way (internal/cli's displayModel).
func TestOutcomeOfModelFallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested string
		resolved  string
		want      string
	}{
		{"resolved model wins", "deepseek-v4-pro", "deepseek-v4-flash", "deepseek-v4-flash"},
		{"unresolved falls back to requested", "deepseek-v4-pro", "", "deepseek-v4-pro"},
		{"both empty stays empty", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := replay.OutcomeOf(&store.Request{
				ModelRequested: tc.requested,
				ModelResolved:  tc.resolved,
			}, nil)
			if got.Model != tc.want {
				t.Errorf("OutcomeOf().Model = %q, want %q", got.Model, tc.want)
			}
		})
	}
}

// TestOutcomeOfCarriesWarnings asserts one "kind: detail" line per warning,
// in the order attached — the whole reason Outcome carries strings and not a
// count.
func TestOutcomeOfCarriesWarnings(t *testing.T) {
	got := replay.OutcomeOf(&store.Request{}, []*store.Warning{
		{Kind: string(analyze.KindUpstreamError), Detail: "502"},
		{Kind: "analyzer_panic", Detail: "cost: index out of range"},
	})
	want := []string{"upstream_error: 502", "analyzer_panic: cost: index out of range"}
	if len(got.Warnings) != len(want) {
		t.Fatalf("OutcomeOf().Warnings = %v, want %v", got.Warnings, want)
	}
	for i := range want {
		if got.Warnings[i] != want[i] {
			t.Errorf("Warnings[%d] = %q, want %q", i, got.Warnings[i], want[i])
		}
	}
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return strings.TrimSpace(string(b))
}
