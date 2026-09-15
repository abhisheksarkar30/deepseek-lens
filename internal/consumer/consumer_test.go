package consumer

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/parse"
	"github.com/abhisheksarkar30/deepseek-lens/internal/sink"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// simpleCall returns a well-formed, non-streaming, error-free call — the
// baseline fixture most tests start from and tweak.
func simpleCall() *sink.CapturedCall {
	return &sink.CapturedCall{
		StartedAt:   time.Now(),
		TTFB:        2 * time.Millisecond,
		Duration:    9 * time.Millisecond,
		Method:      "POST",
		Path:        "/v1/messages",
		RemoteAddr:  "127.0.0.1:54321",
		Status:      200,
		ReqHeaders:  http.Header{"Content-Type": {"application/json"}},
		RespHeaders: http.Header{"Content-Type": {"application/json"}},
		ReqBody:     []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`),
		RespBody:    []byte(`{"model":"claude-sonnet-5","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":20}}`),
	}
}

// runClosed submits every call to a fresh sink, closes it, and runs c to
// completion (the sink close makes Run return once everything is drained —
// no context deadline needed for correctness, only as a safety net against
// a hang).
func runClosed(t *testing.T, c *Consumer, sk *sink.Sink, calls []*sink.CapturedCall) {
	t.Helper()
	for i, call := range calls {
		if !sk.Submit(call) {
			t.Fatalf("submit #%d dropped", i)
		}
	}
	sk.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestHappyPath(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil)

	runClosed(t, c, sk, []*sink.CapturedCall{simpleCall()})

	reqs, err := st.ListRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("got %d rows, want 1", len(reqs))
	}
	r := reqs[0]
	if r.ModelRequested != "claude-sonnet-5" {
		t.Errorf("ModelRequested = %q, want claude-sonnet-5", r.ModelRequested)
	}
	if r.ModelResolved != "claude-sonnet-5" {
		t.Errorf("ModelResolved = %q, want claude-sonnet-5", r.ModelResolved)
	}
	if r.InputTokens != 10 || r.OutputTokens != 20 {
		t.Errorf("tokens = (%d, %d), want (10, 20)", r.InputTokens, r.OutputTokens)
	}
	if r.StopReason == nil || *r.StopReason != "end_turn" {
		t.Errorf("StopReason = %v, want end_turn", r.StopReason)
	}
	if r.Status != 200 || r.Method != "POST" || r.Path != "/v1/messages" {
		t.Errorf("status/method/path = %d/%s/%s, want 200/POST//v1/messages", r.Status, r.Method, r.Path)
	}
	if r.ReqHeaders == "" || r.RespHeaders == "" {
		t.Errorf("headers were not persisted: req=%q resp=%q", r.ReqHeaders, r.RespHeaders)
	}

	stats := c.Stats()
	if stats.Processed != 1 || stats.Failed != 0 {
		t.Errorf("Stats = %+v, want Processed=1 Failed=0", stats)
	}
	if stats.LastWriteAt.IsZero() {
		t.Errorf("LastWriteAt is zero, want set")
	}
}

func TestStreamingCall(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil)

	sse := strings.Join([]string{
		`data: {"type":"message_start","message":{"model":"claude-sonnet-5","usage":{"input_tokens":5,"output_tokens":0}}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`,
		"",
	}, "\n\n")

	call := simpleCall()
	call.RespHeaders = http.Header{"Content-Type": {"text/event-stream"}}
	call.RespBody = []byte(sse)

	runClosed(t, c, sk, []*sink.CapturedCall{call})

	reqs, err := st.ListRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("got %d rows, want 1", len(reqs))
	}
	r := reqs[0]
	if r.InputTokens != 5 {
		t.Errorf("InputTokens = %d, want 5", r.InputTokens)
	}
	if r.OutputTokens != 42 {
		t.Errorf("OutputTokens = %d, want 42", r.OutputTokens)
	}
	if r.ModelResolved != "claude-sonnet-5" {
		t.Errorf("ModelResolved = %q, want claude-sonnet-5", r.ModelResolved)
	}
	if r.StopReason == nil || *r.StopReason != "end_turn" {
		t.Errorf("StopReason = %v, want end_turn", r.StopReason)
	}
}

func TestUnparseableRequestBody(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil)

	call := simpleCall()
	call.ReqBody = []byte("not json at all")

	runClosed(t, c, sk, []*sink.CapturedCall{call})

	reqs, err := st.ListRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("got %d rows, want 1", len(reqs))
	}
	if reqs[0].ModelRequested != "" {
		t.Errorf("ModelRequested = %q, want empty", reqs[0].ModelRequested)
	}
}

func TestUpstreamErrorCall(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil)

	call := simpleCall()
	call.Err = errors.New("upstream: connection reset")

	runClosed(t, c, sk, []*sink.CapturedCall{call})

	reqs, err := st.ListRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("got %d rows, want 1", len(reqs))
	}

	warnings, err := st.ListWarnings(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListWarnings: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1", len(warnings))
	}
	if warnings[0].Kind != "upstream_error" {
		t.Errorf("Kind = %q, want upstream_error", warnings[0].Kind)
	}
	if warnings[0].RequestID != reqs[0].ID {
		t.Errorf("warning RequestID = %d, want %d", warnings[0].RequestID, reqs[0].ID)
	}
}

// panicAnalyzer panics on every call, to exercise the analyzer recover path.
type panicAnalyzer struct{}

func (panicAnalyzer) Analyze(parse.Meta, parse.Usage, *store.Request) []store.Warning {
	panic("boom")
}

func TestPanickingAnalyzer(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil, panicAnalyzer{})

	runClosed(t, c, sk, []*sink.CapturedCall{simpleCall(), simpleCall()})

	reqs, err := st.ListRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 2 {
		t.Fatalf("got %d rows, want 2 — consumer must survive a panicking analyzer", len(reqs))
	}

	warnings, err := st.ListWarnings(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListWarnings: %v", err)
	}
	if len(warnings) != 2 {
		t.Fatalf("got %d warnings, want 2 (one per call)", len(warnings))
	}
	for _, w := range warnings {
		if w.Kind != "analyzer_panic" {
			t.Errorf("Kind = %q, want analyzer_panic", w.Kind)
		}
		if !strings.Contains(w.Message, "panicAnalyzer") {
			t.Errorf("Message = %q, want it to name panicAnalyzer", w.Message)
		}
	}
}

// warningAnalyzer always returns one deterministic warning, to prove a
// registered analyzer's output lands in the warnings table.
type warningAnalyzer struct{}

func (warningAnalyzer) Analyze(_ parse.Meta, _ parse.Usage, req *store.Request) []store.Warning {
	return []store.Warning{{
		RequestID: req.ID,
		Kind:      "test_rule",
		Severity:  "info",
		Message:   "always fires",
		CreatedAt: time.Now(),
	}}
}

func TestAnalyzerRegistration(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil, warningAnalyzer{})

	runClosed(t, c, sk, []*sink.CapturedCall{simpleCall()})

	reqs, err := st.ListRequests(context.Background(), store.Filter{})
	if err != nil || len(reqs) != 1 {
		t.Fatalf("ListRequests: %v, len=%d", err, len(reqs))
	}

	warnings, err := st.ListWarnings(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListWarnings: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1", len(warnings))
	}
	if warnings[0].Kind != "test_rule" || warnings[0].RequestID != reqs[0].ID {
		t.Errorf("warning = %+v, want kind test_rule linked to request %d", warnings[0], reqs[0].ID)
	}
}

// failNStore wraps a real Store and fails the first n calls to
// InsertRequest, to prove a store error on one call doesn't stop the
// consumer from processing the rest. It satisfies the consumer's narrow
// Store interface without needing a special build of *store.Store.
type failNStore struct {
	inner   Store
	failing int32
}

func (f *failNStore) InsertRequest(ctx context.Context, r *store.Request) (int64, error) {
	if atomic.AddInt32(&f.failing, -1) >= 0 {
		return 0, errors.New("injected store failure")
	}
	return f.inner.InsertRequest(ctx, r)
}

func (f *failNStore) InsertWarnings(ctx context.Context, reqID int64, warnings []store.Warning) error {
	return f.inner.InsertWarnings(ctx, reqID, warnings)
}

func TestFailingStore(t *testing.T) {
	real := newTestStore(t)
	fs := &failNStore{inner: real, failing: 1}
	sk := sink.New(16)
	c := New(sk, fs, nil)

	runClosed(t, c, sk, []*sink.CapturedCall{simpleCall(), simpleCall()})

	reqs, err := real.ListRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("got %d rows, want 1 (first call's insert was made to fail)", len(reqs))
	}

	stats := c.Stats()
	if stats.Processed != 1 {
		t.Errorf("Processed = %d, want 1", stats.Processed)
	}
	if stats.Failed != 1 {
		t.Errorf("Failed = %d, want 1", stats.Failed)
	}
}

func TestBatchingReducesFlushCount(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(4096)
	c := New(sk, st, nil)

	const n = 200
	calls := make([]*sink.CapturedCall, n)
	for i := range calls {
		calls[i] = simpleCall()
	}
	runClosed(t, c, sk, calls)

	reqs, err := st.ListRequests(context.Background(), store.Filter{Limit: n + 10})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != n {
		t.Fatalf("got %d rows, want %d", len(reqs), n)
	}

	stats := c.Stats()
	if stats.Flushes == 0 {
		t.Fatal("Flushes = 0, want at least 1")
	}
	if stats.Flushes >= n {
		t.Fatalf("Flushes = %d, want materially below %d — batching did not happen", stats.Flushes, n)
	}
}

func TestFlushOnQuiet(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- c.Run(ctx) }()

	if !sk.Submit(simpleCall()) {
		t.Fatal("submit dropped")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	var rows int
	for time.Now().Before(deadline) {
		reqs, err := st.ListRequests(context.Background(), store.Filter{})
		if err != nil {
			t.Fatalf("ListRequests: %v", err)
		}
		rows = len(reqs)
		if rows == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rows != 1 {
		t.Fatalf("call not flushed within 500ms of quiet (rows=%d)", rows)
	}

	cancel()
	sk.Close()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestThroughput1000(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(4096)
	c := New(sk, st, nil)

	const n = 1000
	calls := make([]*sink.CapturedCall, n)
	for i := range calls {
		calls[i] = simpleCall()
	}
	runClosed(t, c, sk, calls)

	reqs, err := st.ListRequests(context.Background(), store.Filter{Limit: n})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != n {
		t.Fatalf("got %d rows, want %d", len(reqs), n)
	}
}

func TestShutdownBound(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(200)
	c := New(sk, st, nil)

	const n = 100
	for i := 0; i < n; i++ {
		if !sk.Submit(simpleCall()) {
			t.Fatalf("submit #%d dropped", i)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	start := time.Now()
	go func() { runDone <- c.Run(ctx) }()
	cancel()

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within the shutdown bound")
	}
	elapsed := time.Since(start)
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("Run took %v to return, want well under the 2s shutdown bound", elapsed)
	}

	sk.Close()

	reqs, err := st.ListRequests(context.Background(), store.Filter{Limit: n})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != n {
		t.Fatalf("got %d rows, want %d — all already-buffered calls should flush on shutdown", len(reqs), n)
	}
}

func TestConcurrentProducerRace(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(4096)
	c := New(sk, st, nil)

	const n = 500
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			sk.Submit(simpleCall())
		}
		sk.Close()
	}()

	stopPoll := make(chan struct{})
	var pollWG sync.WaitGroup
	pollWG.Add(1)
	go func() {
		defer pollWG.Done()
		for {
			select {
			case <-stopPoll:
				return
			default:
				_ = c.Stats()
				time.Sleep(time.Millisecond)
			}
		}
	}()

	runDone := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { runDone <- c.Run(ctx) }()

	wg.Wait()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after sink closed")
	}
	close(stopPoll)
	pollWG.Wait()

	reqs, err := st.ListRequests(context.Background(), store.Filter{Limit: n})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != n {
		t.Fatalf("got %d rows, want %d", len(reqs), n)
	}
}
