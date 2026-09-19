package consumer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"

	"github.com/abhisheksarkar30/deepseek-lens/internal/analyze"
	"github.com/abhisheksarkar30/deepseek-lens/internal/parse"
	"github.com/abhisheksarkar30/deepseek-lens/internal/pricing"
	"github.com/abhisheksarkar30/deepseek-lens/internal/session"
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

// fixedOffPeakStart is a pinned off-peak instant (Friday 2026-09-11, 20:00
// UTC — same fixture date pricing_test.go's friOff uses) that simpleCall
// stamps every call with, instead of time.Now(). Once the cost step prices a
// row at the row's own StartedAt (br-GI-4-02), a time.Now()-derived
// timestamp would make the exact-cost assertions below flip during
// DeepSeek's actual peak-pricing window; pinning it off-peak keeps them
// deterministic at any wall-clock hour.
var fixedOffPeakStart = time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC)

// simpleCall returns a well-formed, non-streaming, error-free call — the
// baseline fixture most tests start from and tweak.
func simpleCall() *sink.CapturedCall {
	return &sink.CapturedCall{
		StartedAt:   fixedOffPeakStart,
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

// TestDropDetectionEndToEnd is br-GI-1-10's integration case: the rule
// engine registered the way `lens serve` registers it, over a request that
// actually carries cache_control, must land in the warnings table linked to
// the request it came from.
func TestDropDetectionEndToEnd(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil, analyze.NewRules("", ""))

	call := simpleCall()
	call.ReqHeaders = http.Header{"Content-Type": {"application/json"}}
	call.ReqHeaders.Set("anthropic-beta", "prompt-caching-2024-07-31")
	call.ReqBody = []byte(`{"model":"claude-sonnet-5","max_tokens":1024,"top_k":5,"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`)

	runClosed(t, c, sk, []*sink.CapturedCall{call})

	reqs, err := st.ListRequests(context.Background(), store.Filter{})
	if err != nil || len(reqs) != 1 {
		t.Fatalf("ListRequests: %v, len=%d", err, len(reqs))
	}

	warnings, err := st.ListWarnings(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListWarnings: %v", err)
	}
	byKind := map[string]*store.Warning{}
	for _, wn := range warnings {
		byKind[wn.Kind] = wn
		if wn.RequestID != reqs[0].ID {
			t.Errorf("warning %q: RequestID = %d, want %d", wn.Kind, wn.RequestID, reqs[0].ID)
		}
		if wn.CreatedAt.IsZero() || wn.CreatedAt.Year() < 2000 {
			t.Errorf("warning %q: CreatedAt = %v, want it stamped", wn.Kind, wn.CreatedAt)
		}
	}

	cc, ok := byKind["cache_control_ignored"]
	if !ok {
		t.Fatalf("no cache_control_ignored warning; got %d warnings", len(warnings))
	}
	if !strings.Contains(cc.Detail, "messages[0].content[0]") {
		t.Errorf("cache_control Detail = %q, want it to name the site", cc.Detail)
	}
	if cc.Path != "messages[0].content[0]" {
		t.Errorf("cache_control Path = %q, want the offending site", cc.Path)
	}
	for _, kind := range []string{"param_ignored", "header_ignored"} {
		if _, ok := byKind[kind]; !ok {
			t.Errorf("no %s warning; got %d warnings", kind, len(warnings))
		}
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
		if !strings.Contains(w.Detail, "panicAnalyzer") {
			t.Errorf("Detail = %q, want it to name panicAnalyzer", w.Detail)
		}
	}
}

// panicResolver panics on every call, to exercise prepareCall's own
// recover — distinct from the post-insert analyzer recover path above.
type panicResolver struct{}

func (panicResolver) Resolve(parse.Meta, time.Time) string {
	panic("resolver boom")
}

func TestPanickingSessionResolver(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, panicResolver{})

	runClosed(t, c, sk, []*sink.CapturedCall{simpleCall(), simpleCall()})

	reqs, err := st.ListRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 0 {
		t.Fatalf("got %d rows, want 0 — a panicking resolver must drop the call, not insert it half-built", len(reqs))
	}

	if got := c.Stats().Failed; got != 2 {
		t.Fatalf("Stats().Failed = %d, want 2", got)
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
		Detail:    "always fires",
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

// failWarningsStore always fails InsertWarnings, to exercise finishCall's
// warnings-insert error branch — unlike a failed RecordCall, this one does
// count the call failed (the warnings themselves are lost, not just an
// aggregate).
type failWarningsStore struct {
	*store.Store
}

func (f *failWarningsStore) InsertWarnings(ctx context.Context, reqID int64, warnings []store.Warning) error {
	return errors.New("injected warnings failure")
}

func TestFinishCallCountsFailedOnWarningsInsertError(t *testing.T) {
	st := newTestStore(t)
	fs := &failWarningsStore{Store: st}
	sk := sink.New(16)
	c := New(sk, fs, nil, warningAnalyzer{})

	runClosed(t, c, sk, []*sink.CapturedCall{simpleCall()})

	reqs, err := st.ListRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("got %d rows, want 1 (a warnings-insert failure must not lose the already-committed row)", len(reqs))
	}
	if got := c.Stats().Failed; got != 1 {
		t.Fatalf("Stats().Failed = %d, want 1", got)
	}
}

// fixedResolver resolves every call to the same session id, letting a test
// force p.req.SessionID != nil without a real session.Resolver.
type fixedResolver struct{ id string }

func (f fixedResolver) Resolve(parse.Meta, time.Time) string { return f.id }

// errAggregator always fails RecordCall, to exercise finishCall's session-fold
// error branch — logged only, per CLAUDE.md's fail open: the row this call is
// about is already committed, so this must never count as a failed call.
type errAggregator struct{}

func (errAggregator) RecordCall(context.Context, string, *store.Request, int) error {
	return errors.New("injected aggregate failure")
}

func TestFinishCallDoesNotCountFailedOnAggregatorError(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, fixedResolver{id: "sess-x"})
	c.SetSessionAggregator(errAggregator{})

	runClosed(t, c, sk, []*sink.CapturedCall{simpleCall()})

	reqs, err := st.ListRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("got %d rows, want 1", len(reqs))
	}
	if got := c.Stats().Failed; got != 0 {
		t.Fatalf("Stats().Failed = %d, want 0 (a RecordCall failure must not count the already-committed call as failed)", got)
	}
}

// failBatchStore wraps a real Store and implements the consumer's optional
// batchInserter, failing the first InsertRequests call before delegating. It
// exists to exercise the batch-failure → per-row fallback in insertBatch:
// failNStore does not implement InsertRequests, so it takes the no-batch path
// and never reaches that branch.
type failBatchStore struct {
	*store.Store
	first bool // the consumer goroutine is the only writer of this field
}

func (s *failBatchStore) InsertRequests(ctx context.Context, reqs []*store.Request) error {
	if !s.first {
		s.first = true
		return errors.New("injected batch failure")
	}
	return s.Store.InsertRequests(ctx, reqs)
}

// TestBatchFailureFallsBackToPerRow is br-GI-1-07's "failing store (injected
// error on the first call) → the consumer continues", at the batch level: when
// a batch transaction fails, every row is retried individually, so no call is
// lost and none is counted failed.
func TestBatchFailureFallsBackToPerRow(t *testing.T) {
	st := newTestStore(t)
	fs := &failBatchStore{Store: st}
	sk := sink.New(16)
	c := New(sk, fs, nil)

	runClosed(t, c, sk, []*sink.CapturedCall{simpleCall(), simpleCall(), simpleCall()})

	reqs, err := st.ListRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 3 {
		t.Fatalf("got %d rows, want 3 (the failed batch was retried per row)", len(reqs))
	}

	stats := c.Stats()
	if stats.Processed != 3 {
		t.Errorf("Processed = %d, want 3", stats.Processed)
	}
	if stats.Failed != 0 {
		t.Errorf("Failed = %d, want 0 (the per-row fallback recovered every row)", stats.Failed)
	}
}

// panicStore panics on its first write on both paths — the grouped batch
// (InsertRequests) and the per-row fallback (InsertRequest). It proves
// insertBatch contains a store-write panic the way the rest of the pipeline
// does (br-GI-1-07: "error containment is the point of this bead"): before the
// round-1 batch split this write ran inside processCall's recover, so a
// panicking store killed the consumer goroutine and capture silently stopped.
type panicStore struct {
	*store.Store
	batchPanicked bool // the consumer goroutine is the only writer of these fields
	rowPanicked   bool
}

func (s *panicStore) InsertRequests(ctx context.Context, reqs []*store.Request) error {
	if !s.batchPanicked {
		s.batchPanicked = true
		panic("injected batch panic")
	}
	return s.Store.InsertRequests(ctx, reqs)
}

func (s *panicStore) InsertRequest(ctx context.Context, r *store.Request) (int64, error) {
	if !s.rowPanicked {
		s.rowPanicked = true
		panic("injected row panic")
	}
	return s.Store.InsertRequest(ctx, r)
}

func TestStorePanicIsContained(t *testing.T) {
	st := newTestStore(t)
	ps := &panicStore{Store: st}
	sk := sink.New(16)
	c := New(sk, ps, nil)

	// An unrecovered panic here would propagate out of Run (called
	// synchronously by runClosed) and fail the test outright — which is the
	// point: the batched write panics, the fallback's first row panics, and
	// the remaining rows still land.
	runClosed(t, c, sk, []*sink.CapturedCall{simpleCall(), simpleCall(), simpleCall()})

	reqs, err := st.ListRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 2 {
		t.Fatalf("got %d rows, want 2 (two rows survived the panics)", len(reqs))
	}
	stats := c.Stats()
	if stats.Processed != 2 {
		t.Errorf("Processed = %d, want 2", stats.Processed)
	}
	if stats.Failed != 1 {
		t.Errorf("Failed = %d, want 1 (the panicking row only)", stats.Failed)
	}
}

// countingStore wraps a real Store and counts every write that actually
// reaches it: one per grouped batch transaction, one per single insert. It is
// how the batching test measures the real write count instead of trusting the
// consumer's own Flushes counter.
type countingStore struct {
	*store.Store
	batches atomic.Uint64 // InsertRequests calls — one SQL transaction each
	singles atomic.Uint64 // InsertRequest calls
}

func (s *countingStore) InsertRequests(ctx context.Context, reqs []*store.Request) error {
	s.batches.Add(1)
	return s.Store.InsertRequests(ctx, reqs)
}

func (s *countingStore) InsertRequest(ctx context.Context, r *store.Request) (int64, error) {
	s.singles.Add(1)
	return s.Store.InsertRequest(ctx, r)
}

// TestBatchingGroupsWritesIntoOneTransaction is br-GI-1-07's batching case:
// 200 calls submitted rapidly must all land as rows, and the number of writes
// that reach the store must be materially below 200 — the asserted property
// is that the batch is grouped into a transaction, not merely that it works.
func TestBatchingGroupsWritesIntoOneTransaction(t *testing.T) {
	st := newTestStore(t)
	cs := &countingStore{Store: st}
	sk := sink.New(4096)
	c := New(sk, cs, nil)

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

	writes := cs.batches.Load() + cs.singles.Load()
	if writes == 0 {
		t.Fatal("no writes reached the store, want at least one batch")
	}
	if writes >= n {
		t.Fatalf("write count = %d (%d batch transactions + %d single inserts), want materially below %d — the batch was not grouped into a transaction",
			writes, cs.batches.Load(), cs.singles.Load(), n)
	}
	if singles := cs.singles.Load(); singles != 0 {
		t.Errorf("single inserts = %d, want 0 when the store supports a batch transaction", singles)
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

// --- session grouping (br-GI-1-12) ---

// TestSessionGroupingEndToEnd is the bead's integration case: three captured
// calls sharing an opening prompt, through the resolver and aggregator wired
// the way `lens serve` wires them, must land as one sessions row and three
// requests rows pointing at it.
func TestSessionGroupingEndToEnd(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	sess := session.New(st, 30)
	c := New(sk, st, sess)
	c.SetSessionAggregator(sess)

	runClosed(t, c, sk, []*sink.CapturedCall{simpleCall(), simpleCall(), simpleCall()})

	ctx := context.Background()
	reqs, err := st.ListRequests(ctx, store.Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 3 {
		t.Fatalf("got %d rows, want 3", len(reqs))
	}
	if reqs[0].SessionID == nil {
		t.Fatal("SessionID is unset — the resolver was not wired")
	}
	for _, r := range reqs {
		if r.SessionID == nil || *r.SessionID != *reqs[0].SessionID {
			t.Errorf("request %d: SessionID = %v, want %q", r.ID, r.SessionID, *reqs[0].SessionID)
		}
	}

	sessions, err := st.ListSessions(ctx, store.Filter{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	s := sessions[0]
	if s.ID != *reqs[0].SessionID {
		t.Errorf("session id = %q, want %q", s.ID, *reqs[0].SessionID)
	}
	if s.RequestCount != 3 {
		t.Errorf("RequestCount = %d, want 3", s.RequestCount)
	}
	// simpleCall's response reports 10 in / 20 out per call.
	if s.TotalInputTokens != 30 || s.TotalOutputTokens != 60 {
		t.Errorf("tokens = (%d, %d), want (30, 60)", s.TotalInputTokens, s.TotalOutputTokens)
	}
	if s.UnpricedCount != 3 || s.PricedCount != 0 {
		t.Errorf("priced/unpriced = %d/%d, want 0/3 (no price table was installed)",
			s.PricedCount, s.UnpricedCount)
	}
	if s.ModelSet != "claude-sonnet-5" {
		t.Errorf("ModelSet = %q, want %q", s.ModelSet, "claude-sonnet-5")
	}
}

// TestSessionSkippedWithoutAggregator keeps the pre-bead behaviour honest: a
// Consumer built with a resolver but no aggregator still sets SessionID on
// the row and writes no session row — the two halves of grouping are
// independently installable.
func TestSessionSkippedWithoutAggregator(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, session.New(st, 30))

	runClosed(t, c, sk, []*sink.CapturedCall{simpleCall()})

	reqs, err := st.ListRequests(context.Background(), store.Filter{})
	if err != nil || len(reqs) != 1 {
		t.Fatalf("ListRequests: %v, len=%d", err, len(reqs))
	}
	if reqs[0].SessionID == nil {
		t.Error("SessionID is unset, want the resolver's id")
	}
	sessions, err := st.ListSessions(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions with no aggregator installed, want 0", len(sessions))
	}
}

func rate(v float64) *float64 { return &v }

// pricedCall is simpleCall whose response reports the upstream-resolved model
// the price table is keyed by, with a known input token count.
func pricedCall(model string, inputTokens int) *sink.CapturedCall {
	call := simpleCall()
	call.RespBody = []byte(fmt.Sprintf(
		`{"model":%q,"stop_reason":"end_turn","usage":{"input_tokens":%d}}`, model, inputTokens))
	return call
}

// waitForRows polls until the store holds at least n requests, newest first.
func waitForRows(t *testing.T, st *store.Store, n int) []*store.Request {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		reqs, err := st.ListRequests(context.Background(), store.Filter{Limit: n + 10})
		if err != nil {
			t.Fatalf("ListRequests: %v", err)
		}
		if len(reqs) >= n {
			return reqs
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("fewer than %d rows within 3s", n)
	return nil
}

// TestCostStepPricesConfiguredRows is the bead's integration case: with rates
// configured, the row carries a cost_usd, a "configured" source, and the
// stored 280000-micro-dollar call reads back as exactly 0.28.
func TestCostStepPricesConfiguredRows(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil)
	c.SetPriceTable(pricing.Table{"deepseek-flash": {Input: rate(0.28)}})

	runClosed(t, c, sk, []*sink.CapturedCall{
		pricedCall("deepseek-flash", 1_000_000),
		pricedCall("deepseek-flash", 0), // a real zero, not an unpriced NULL
	})

	byTokens := map[int]*store.Request{}
	for _, r := range waitForRows(t, st, 2) {
		byTokens[r.InputTokens] = r
	}

	priced := byTokens[1_000_000]
	if priced == nil {
		t.Fatal("the 1,000,000-token row is missing")
	}
	got, err := st.GetRequest(context.Background(), priced.ID)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if got.CostUSD == nil || *got.CostUSD != 0.28 {
		t.Errorf("CostUSD = %v, want 0.28 read back from the column", got.CostUSD)
	}
	if got.CostSource == nil || *got.CostSource != pricing.SourceConfigured {
		t.Errorf("CostSource = %v, want %q", got.CostSource, pricing.SourceConfigured)
	}

	zero := byTokens[0]
	if zero == nil {
		t.Fatal("the zero-token row is missing")
	}
	if zero.CostUSD == nil {
		t.Error("CostUSD = NULL for a configured zero-token call, want a real 0")
	} else if *zero.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0", *zero.CostUSD)
	}
}

// TestCostStepPricesAtCallsOwnStartedAt is br-GI-4-02's integration case: the
// cost step prices each row at that row's own StartedAt, so a call placed
// inside DeepSeek's peak window costs exactly double the same call placed
// off-peak.
func TestCostStepPricesAtCallsOwnStartedAt(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil)
	c.SetPriceTable(pricing.Table{"deepseek-flash": {Input: rate(0.28)}})

	peak := pricedCall("deepseek-flash", 1_000_000)
	peak.StartedAt = time.Date(2026, 9, 11, 7, 0, 0, 0, time.UTC) // Friday, inside the window
	off := pricedCall("deepseek-flash", 1_000_000)
	off.StartedAt = time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC) // Friday, outside it

	runClosed(t, c, sk, []*sink.CapturedCall{peak, off})

	rows := waitForRows(t, st, 2)
	var peakCost, offCost *float64
	for _, r := range rows {
		if r.StartedAt.Equal(peak.StartedAt) {
			peakCost = r.CostUSD
		} else if r.StartedAt.Equal(off.StartedAt) {
			offCost = r.CostUSD
		}
	}
	if peakCost == nil || offCost == nil {
		t.Fatalf("peakCost=%v offCost=%v, want both rows found and priced", peakCost, offCost)
	}
	if *peakCost != 2**offCost {
		t.Errorf("peak CostUSD = %v, want exactly 2x off-peak (%v)", *peakCost, *offCost)
	}
	if *offCost != 0.28 {
		t.Errorf("off-peak CostUSD = %v, want 0.28", *offCost)
	}
}

// TestCostStepPricesAHolidayAtOffPeak is br-GI-24-02's integration case, and
// the extension of the test above: with a holiday installed on the calendar,
// a peak-window instant on that date prices at 1x while the same instant on
// the adjacent ordinary weekday still prices at 2x. The paired assertion is
// what makes the calendar provably in force rather than coincidentally
// irrelevant — a Consumer that ignored SetCalendar would fail the first leg,
// and one that priced everything off-peak would fail the second.
func TestCostStepPricesAHolidayAtOffPeak(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil)
	c.SetPriceTable(pricing.Table{"deepseek-flash": {Input: rate(0.28)}})
	cal, err := pricing.NewCalendar("2026-10-01", "") // National Day, a Thursday
	if err != nil {
		t.Fatalf("NewCalendar: %v", err)
	}
	c.SetCalendar(cal)

	holiday := pricedCall("deepseek-flash", 1_000_000)
	holiday.StartedAt = time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC) // Thursday, inside the window
	ordinary := pricedCall("deepseek-flash", 1_000_000)
	ordinary.StartedAt = time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC) // the next Thursday, not a holiday

	runClosed(t, c, sk, []*sink.CapturedCall{holiday, ordinary})

	rows := waitForRows(t, st, 2)
	var holidayCost, ordinaryCost *float64
	for _, r := range rows {
		switch {
		case r.StartedAt.Equal(holiday.StartedAt):
			holidayCost = r.CostUSD
		case r.StartedAt.Equal(ordinary.StartedAt):
			ordinaryCost = r.CostUSD
		}
	}
	if holidayCost == nil || ordinaryCost == nil {
		t.Fatalf("holidayCost=%v ordinaryCost=%v, want both rows found and priced", holidayCost, ordinaryCost)
	}
	if *holidayCost != 0.28 {
		t.Errorf("holiday CostUSD = %v, want 0.28 (1x — DeepSeek bills the whole day off-peak)", *holidayCost)
	}
	if *ordinaryCost != 0.56 {
		t.Errorf("ordinary Thursday CostUSD = %v, want 0.56 (2x — the calendar did not remove peak pricing)", *ordinaryCost)
	}
}

// TestCostStepLeavesUnpricedRowsNull is the other half: the shipped table
// knows the model but has no rates, so cost_usd stays NULL and the source
// says "unpriced" — never 0, which would read as "this call was free".
func TestCostStepLeavesUnpricedRowsNull(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil)
	c.SetPriceTable(pricing.Default())

	runClosed(t, c, sk, []*sink.CapturedCall{pricedCall("deepseek-flash", 500)})

	r := waitForRows(t, st, 1)[0]
	if r.CostUSD != nil {
		t.Errorf("CostUSD = %v, want NULL for an unpriced model", *r.CostUSD)
	}
	if r.CostSource == nil || *r.CostSource != pricing.SourceUnpriced {
		t.Errorf("CostSource = %v, want %q", r.CostSource, pricing.SourceUnpriced)
	}
}

// TestCostStepWithoutATableLeavesCostNull keeps the pre-bead behaviour: a
// Consumer built without a price table (every br-GI-1-07/10 test) writes no
// cost columns at all and is otherwise unaffected.
func TestCostStepWithoutATableLeavesCostNull(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil)

	runClosed(t, c, sk, []*sink.CapturedCall{pricedCall("deepseek-flash", 500)})

	r := waitForRows(t, st, 1)[0]
	if r.CostUSD != nil || r.CostSource != nil {
		t.Errorf("cost = (%v, %v), want both nil with no price table", r.CostUSD, r.CostSource)
	}
}

// TestCostStepTakesEffectWithoutRestart is the bead's "`lens prices --set`
// writes prices.toml and takes effect without restart": the consumer is a
// long-lived goroutine, so the second call must pick up the rewritten file
// without anything being rebuilt.
func TestCostStepTakesEffectWithoutRestart(t *testing.T) {
	st := newTestStore(t)
	path := filepath.Join(t.TempDir(), "prices.toml")
	if err := pricing.Save(path, pricing.Table{"deepseek-flash": {Input: rate(0.28)}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	sk := sink.New(16)
	c := New(sk, st, nil)
	c.SetPriceTable(pricing.NewLoader(path))

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- c.Run(ctx) }()

	if !sk.Submit(pricedCall("deepseek-flash", 1_000_000)) {
		t.Fatal("submit dropped")
	}
	before := waitForRows(t, st, 1)[0]
	if before.CostUSD == nil || *before.CostUSD != 0.28 {
		t.Fatalf("first call: CostUSD = %v, want 0.28", before.CostUSD)
	}

	// Exactly what `lens prices --set deepseek-flash.input=0.31` does.
	time.Sleep(20 * time.Millisecond) // mtime granularity, not a race guard
	if err := pricing.Save(path, pricing.Table{"deepseek-flash": {Input: rate(0.31)}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A distinct (still off-peak) StartedAt: simpleCall() now pins every call
	// to the same instant, so without this the two rows would tie on
	// started_at and rows[0] would rest on the idx_requests_started_at
	// rowid-DESC tiebreak rather than genuinely being the later row.
	second := pricedCall("deepseek-flash", 1_000_000)
	second.StartedAt = fixedOffPeakStart.Add(time.Hour)
	if !sk.Submit(second) {
		t.Fatal("submit dropped")
	}
	rows := waitForRows(t, st, 2)
	if rows[0].CostUSD == nil || *rows[0].CostUSD != 0.31 {
		t.Errorf("second call: CostUSD = %v, want 0.31 from the rewritten file", rows[0].CostUSD)
	}

	cancel()
	sk.Close()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
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

// brotliBody returns data as a brotli stream. brotli is the coding the DeepSeek
// endpoint actually answers with for a client that offers it, so it is the one
// whose absence from the pipeline made every such call report zero tokens.
func brotliBody(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatalf("brotli write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("brotli close: %v", err)
	}
	return buf.Bytes()
}

// TestBodyDecodingRecoversCompressedUsage is the regression the decode step
// exists for, asserted the way the defect showed up: a response carrying
// "Content-Encoding: br" must still land its usage on the row, and the row must
// store the body in the form every reader of it expects.
func TestBodyDecodingRecoversCompressedUsage(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil)
	c.SetBodyDecoding(1 << 20)

	call := simpleCall()
	plain := append([]byte(nil), call.RespBody...)
	call.RespHeaders.Set("Content-Encoding", "br")
	call.RespBody = brotliBody(t, plain)

	runClosed(t, c, sk, []*sink.CapturedCall{call})

	rows, err := st.ListRequests(context.Background(), store.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	r := rows[0]
	if r.InputTokens != 10 || r.OutputTokens != 20 {
		t.Errorf("tokens = in %d / out %d, want 10 / 20 — the decoded usage has to reach the row",
			r.InputTokens, r.OutputTokens)
	}
	if r.ModelResolved != "claude-sonnet-5" {
		t.Errorf("model_resolved = %q, want claude-sonnet-5", r.ModelResolved)
	}
	if !bytes.Equal(r.RespBody, plain) {
		t.Error("stored response body is not the decoded form the client received")
	}
	// The stored pair has to agree with itself: internal/api's replay re-sends
	// this body with these headers, so a surviving Content-Encoding would make a
	// replay declare an encoding its body no longer has.
	if strings.Contains(r.RespHeaders, "Content-Encoding") {
		t.Errorf("stored headers still declare an encoding: %s", r.RespHeaders)
	}
	if strings.Contains(r.RespHeaders, "Content-Length") {
		t.Errorf("stored headers still carry the encoded length: %s", r.RespHeaders)
	}
}

// TestBodyDecodingIsOptIn pins the default every test written before this step
// depends on: a Consumer that was never given SetBodyDecoding stores exactly the
// bytes that were captured, undecoded.
func TestBodyDecodingIsOptIn(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	c := New(sk, st, nil) // no SetBodyDecoding

	call := simpleCall()
	compressed := brotliBody(t, call.RespBody)
	call.RespHeaders.Set("Content-Encoding", "br")
	call.RespBody = compressed

	runClosed(t, c, sk, []*sink.CapturedCall{call})

	rows, err := st.ListRequests(context.Background(), store.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if !bytes.Equal(rows[0].RespBody, compressed) {
		t.Error("stored response body was decoded without SetBodyDecoding")
	}
	if rows[0].InputTokens != 0 || rows[0].OutputTokens != 0 {
		t.Errorf("tokens = in %d / out %d, want 0 / 0 with decoding off",
			rows[0].InputTokens, rows[0].OutputTokens)
	}
}
