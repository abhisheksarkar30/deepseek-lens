package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/consumer"
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

// testAssets is a minimal in-memory replacement for internal/web.Files, so
// this package's tests don't depend on internal/web's actual content.
var testAssets = fstest.MapFS{
	"index.html": &fstest.MapFile{Data: []byte("<html><body>lens</body></html>")},
	"app.js":     &fstest.MapFile{Data: []byte("// app")},
	"style.css":  &fstest.MapFile{Data: []byte("body{}")},
}

// seedRequest inserts a reasonable default *store.Request, optionally
// tweaked by opts, and returns it with ID set — same trade internal/cli's
// tests already take (internal/cli/cli_test.go's seedRequest).
func seedRequest(t *testing.T, st *store.Store, opts func(*store.Request)) *store.Request {
	t.Helper()
	cost := 0.01
	r := &store.Request{
		StartedAt:      time.Now().Add(-time.Minute),
		TTFB:           10 * time.Millisecond,
		Duration:       50 * time.Millisecond,
		Method:         "POST",
		Path:           "/v1/messages",
		RemoteAddr:     "127.0.0.1:1234",
		Status:         200,
		ReqHeaders:     `{"Content-Type":["application/json"]}`,
		RespHeaders:    `{"Content-Type":["application/json"]}`,
		ReqBody:        []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`),
		RespBody:       []byte(`{"id":"msg_1","model":"deepseek-chat"}`),
		InputTokens:    100,
		OutputTokens:   200,
		ModelRequested: "deepseek-chat",
		ModelResolved:  "deepseek-chat",
		CostUSD:        &cost,
		PrefixHash:     "abc123",
	}
	if opts != nil {
		opts(r)
	}
	if _, err := st.InsertRequest(context.Background(), r); err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	return r
}

// newTestAPI builds a handler backed by st (or a spy wrapping it), a fresh
// broker, a real (unstarted) consumer, and in-memory assets.
func newTestAPI(t *testing.T, st Store) (http.Handler, *sink.Sink, *consumer.Consumer, *Broker) {
	t.Helper()
	sk := sink.New(16)
	cons := consumer.New(sk, nil, nil) // Run is never called in most tests; Stats() is fine on a fresh Consumer.
	broker := NewBroker()
	// No proxy handler and replay off: these tests cover the read-only API, and
	// the replay route (br-GI-1-13) is exercised in internal/replay, where a
	// real proxy and a recording upstream can be wired up.
	return New(st, sk, cons, broker, testAssets, nil, false), sk, cons, broker
}

func decodeJSON[T any](t *testing.T, body io.Reader) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(body).Decode(&v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return v
}

func TestListRequests(t *testing.T) {
	st := newTestStore(t)
	for i := 0; i < 5; i++ {
		seedRequest(t, st, nil)
	}
	handler, _, _, _ := newTestAPI(t, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/requests", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	got := decodeJSON[[]*store.Request](t, rr.Body)
	if len(got) != 5 {
		t.Fatalf("got %d requests, want 5", len(got))
	}
}

func TestListRequestsLimit(t *testing.T) {
	st := newTestStore(t)
	for i := 0; i < 5; i++ {
		seedRequest(t, st, nil)
	}
	handler, _, _, _ := newTestAPI(t, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/requests?limit=2", nil))
	got := decodeJSON[[]*store.Request](t, rr.Body)
	if len(got) != 2 {
		t.Fatalf("got %d requests, want 2 (limit=2)", len(got))
	}
}

func TestListRequestsFilters(t *testing.T) {
	st := newTestStore(t)
	seedRequest(t, st, func(r *store.Request) { r.ModelResolved = "deepseek-chat"; r.ModelRequested = "deepseek-chat" })
	seedRequest(t, st, func(r *store.Request) { r.ModelResolved = "deepseek-reasoner"; r.ModelRequested = "deepseek-reasoner" })
	old := seedRequest(t, st, func(r *store.Request) { r.StartedAt = time.Now().Add(-48 * time.Hour) })
	_ = old

	handler, _, _, _ := newTestAPI(t, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/requests?model=deepseek-reasoner", nil))
	got := decodeJSON[[]*store.Request](t, rr.Body)
	if len(got) != 1 || got[0].ModelResolved != "deepseek-reasoner" {
		t.Fatalf("model filter: got %+v", got)
	}

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/requests?since="+url.QueryEscape("1h")+"", nil))
	got = decodeJSON[[]*store.Request](t, rr.Body)
	for _, r := range got {
		if r.StartedAt.Before(time.Now().Add(-2 * time.Hour)) {
			t.Fatalf("since filter let through an old row: %+v", r)
		}
	}
	if len(got) != 2 {
		t.Fatalf("since=1h: got %d rows, want 2 (excludes the 48h-old row)", len(got))
	}
}

func TestGetRequestIncludesWarnings(t *testing.T) {
	st := newTestStore(t)
	r := seedRequest(t, st, nil)
	if err := st.InsertWarnings(context.Background(), r.ID, []store.Warning{
		{Kind: "cache_control_ignored", Severity: "warning", Detail: "dropped", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}
	handler, _, _, _ := newTestAPI(t, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/requests/%d", r.ID), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	got := decodeJSON[requestDetail](t, rr.Body)
	if got.ID != r.ID {
		t.Fatalf("ID = %d, want %d", got.ID, r.ID)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Kind != "cache_control_ignored" {
		t.Fatalf("Warnings = %+v, want one cache_control_ignored", got.Warnings)
	}
}

// sessionDetailJSON is the wire shape these tests assert on, rather than
// sessionDetail itself: a test that decodes into the struct whose promoted
// embedded pointer supplies the fields would pass whether or not the JSON
// actually carries them.
type sessionDetailJSON struct {
	ID       string          `json:"ID"`
	Calls    []store.Request `json:"calls"`
	Warnings []store.Warning `json:"warnings"`
}

// TestGetSessionListsCallsChronologically is the bead's API integration case:
// the drill-down must show the session's calls in the order the run actually
// happened, not newest-first like /api/requests, because that is the only
// order a running total reads in.
func TestGetSessionListsCallsChronologically(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	const sid = "s_1_aaaaaaaa"
	base := time.Now().Add(-10 * time.Minute)

	var ids []int64
	for i := 0; i < 3; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		r := seedRequest(t, st, func(r *store.Request) {
			r.StartedAt = at
			s := sid
			r.SessionID = &s
		})
		ids = append(ids, r.ID)
	}
	if err := st.InsertWarnings(ctx, ids[0], []store.Warning{
		{Kind: "cache_control_ignored", Severity: "warn", Path: "system[0]", Detail: "dropped", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}
	if err := st.InsertWarnings(ctx, ids[2], []store.Warning{
		{Kind: "param_ignored", Severity: "warn", Path: "top_k", Detail: "dropped", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}
	ph := "aaaaaaaaaaaaaaa1"
	if err := st.UpsertSession(ctx, &store.Session{
		ID: sid, PrefixHash: &ph, FirstSeen: base, LastSeen: base.Add(2 * time.Minute),
		RequestCount: 3, TotalInputTokens: 300, TotalOutputTokens: 600,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	handler, _, _, _ := newTestAPI(t, st)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/sessions/"+sid, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	got := decodeJSON[sessionDetailJSON](t, rr.Body)

	if got.ID != sid {
		t.Errorf("ID = %q, want %q", got.ID, sid)
	}
	if len(got.Calls) != 3 {
		t.Fatalf("got %d calls, want 3", len(got.Calls))
	}
	for i, c := range got.Calls {
		if c.ID != ids[i] {
			t.Errorf("call %d: id = %d, want %d", i, c.ID, ids[i])
		}
		if i > 0 && c.StartedAt.Before(got.Calls[i-1].StartedAt) {
			t.Errorf("call %d (%v) is older than call %d (%v)", i, c.StartedAt, i-1, got.Calls[i-1].StartedAt)
		}
	}
	// The union of everything raised across the session, in either order.
	if len(got.Warnings) != 2 {
		t.Fatalf("got %d warnings, want 2 (the union across the session)", len(got.Warnings))
	}
	kinds := map[string]bool{}
	for _, w := range got.Warnings {
		kinds[w.Kind] = true
	}
	if !kinds["cache_control_ignored"] || !kinds["param_ignored"] {
		t.Errorf("warnings = %+v, want one of each kind", got.Warnings)
	}
}

// TestListSessionsServesTheAggregates covers GET /api/sessions: the rows the
// dashboard's session table reads, with the totals UpsertSession maintains.
func TestListSessionsServesTheAggregates(t *testing.T) {
	st := newTestStore(t)
	const sid = "s_1_aaaaaaaa"
	if err := st.UpsertSession(context.Background(), &store.Session{
		ID: sid, FirstSeen: time.Now().Add(-time.Minute), LastSeen: time.Now(),
		RequestCount: 3, TotalInputTokens: 300, TotalOutputTokens: 600,
		PricedCount: 2, UnpricedCount: 1, TotalCostUSD: 0.03,
		ModelSet: "deepseek-flash", WarningCount: 4,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	handler, _, _, _ := newTestAPI(t, st)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	got := decodeJSON[[]*store.Session](t, rr.Body)
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	s := got[0]
	if s.ID != sid || s.RequestCount != 3 || s.WarningCount != 4 {
		t.Errorf("session = %+v, want id %q with 3 turns and 4 warnings", s, sid)
	}
	if s.UnpricedCount != 1 || s.PricedCount != 2 {
		t.Errorf("priced/unpriced = %d/%d, want 2/1", s.PricedCount, s.UnpricedCount)
	}
}

func TestGetSessionNotFound(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/sessions/s_1_deadbeef", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestGetRequestNotFound(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/requests/999999", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	body := decodeJSON[map[string]string](t, rr.Body)
	if body["error"] == "" {
		t.Fatalf("body = %+v, want an error message", body)
	}
}

func TestStatsTotalsMatchFixture(t *testing.T) {
	st := newTestStore(t)
	seedRequest(t, st, func(r *store.Request) { r.InputTokens = 10; r.OutputTokens = 20 })
	seedRequest(t, st, func(r *store.Request) { r.InputTokens = 30; r.OutputTokens = 40 })
	handler, _, _, _ := newTestAPI(t, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	got := decodeJSON[statsResponse](t, rr.Body)
	if got.Summary.RequestCount != 2 {
		t.Errorf("RequestCount = %d, want 2", got.Summary.RequestCount)
	}
	if got.Summary.InputTokens != 40 || got.Summary.OutputTokens != 60 {
		t.Errorf("tokens = (%d, %d), want (40, 60)", got.Summary.InputTokens, got.Summary.OutputTokens)
	}
}

// TestStatsIncludesCostSourceBreakdown is br-GI-1-11's dashboard case: a
// cost total alone cannot say whether it covered every call, so /api/stats
// carries the breakdown by source too.
func TestStatsIncludesCostSourceBreakdown(t *testing.T) {
	st := newTestStore(t)
	configured, unpriced, unknown := "configured", "unpriced", "unknown-model"
	seedRequest(t, st, func(r *store.Request) { r.CostSource = &configured })
	seedRequest(t, st, func(r *store.Request) { r.CostUSD = nil; r.CostSource = &unpriced })
	seedRequest(t, st, func(r *store.Request) { r.CostUSD = nil; r.CostSource = &unknown })

	handler, _, _, _ := newTestAPI(t, st)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "cost_sources") {
		t.Errorf("response has no cost_sources field: %s", body)
	}

	got := decodeJSON[statsResponse](t, strings.NewReader(body))
	if got.Summary.UnpricedCount != 2 {
		t.Errorf("UnpricedCount = %d, want 2", got.Summary.UnpricedCount)
	}
	bySource := map[string]int{}
	for _, c := range got.CostSources {
		bySource[c.Source] = c.RequestCount
	}
	for source, want := range map[string]int{"configured": 1, "unpriced": 1, "unknown-model": 1} {
		if bySource[source] != want {
			t.Errorf("cost_sources[%q] = %d, want %d (got %+v)", source, bySource[source], want, got.CostSources)
		}
	}
}

func TestListWarningsFilteredByKind(t *testing.T) {
	st := newTestStore(t)
	r1 := seedRequest(t, st, nil)
	r2 := seedRequest(t, st, nil)
	if err := st.InsertWarnings(context.Background(), r1.ID, []store.Warning{
		{Kind: "cache_control_ignored", Severity: "warning", Detail: "a", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertWarnings(context.Background(), r2.ID, []store.Warning{
		{Kind: "upstream_error", Severity: "error", Detail: "b", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	handler, _, _, _ := newTestAPI(t, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/warnings?kind=cache_control_ignored", nil))
	got := decodeJSON[[]*store.Warning](t, rr.Body)
	if len(got) != 1 || got[0].Kind != "cache_control_ignored" {
		t.Fatalf("got %+v, want one cache_control_ignored warning", got)
	}
}

func TestHealthReportsCounters(t *testing.T) {
	st := newTestStore(t)
	sk := sink.New(16)
	cons := consumer.New(sk, st, nil)
	broker := NewBroker()
	handler := New(st, sk, cons, broker, testAssets, nil, false)

	sk.Submit(&sink.CapturedCall{StartedAt: time.Now(), Method: "POST", Path: "/v1/messages", Status: 200})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sk.Close()
	if err := cons.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	got := decodeJSON[healthResponse](t, rr.Body)
	if got.SinkAccepted != 1 {
		t.Errorf("SinkAccepted = %d, want 1", got.SinkAccepted)
	}
	if got.ConsumerProcessed != 1 {
		t.Errorf("ConsumerProcessed = %d, want 1", got.ConsumerProcessed)
	}
}

// spyStore records the Filter it was called with, wrapping a real store so
// the rest of the endpoint still works.
type spyStore struct {
	*store.Store
	lastReqFilter store.Filter
}

func (s *spyStore) ListRequests(ctx context.Context, f store.Filter) ([]*store.Request, error) {
	s.lastReqFilter = f
	return s.Store.ListRequests(ctx, f)
}

func TestListRequestsNoUnboundedQuery(t *testing.T) {
	st := newTestStore(t)
	seedRequest(t, st, nil)
	spy := &spyStore{Store: st}
	handler, _, _, _ := newTestAPI(t, spy)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/requests", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	// limit absent must not become some large/unbounded number invented by
	// the handler — 0 is what tells store.ListRequests to apply its own
	// DefaultLimit cap (see internal/store/store.go), never an unbounded scan.
	if spy.lastReqFilter.Limit != 0 {
		t.Fatalf("Filter.Limit = %d, want 0 (store applies its own capped default)", spy.lastReqFilter.Limit)
	}
}

func TestEmbeddedAssets(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)

	for _, path := range []string{"/", "/app.js", "/style.css"} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", path, rr.Code)
		}
	}
}

func TestMethodGuard(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/requests", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestMalformedQueryParams(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)

	cases := []string{
		"/api/requests?limit=abc",
		"/api/requests?since=notatime",
	}
	for _, path := range cases {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status = %d, want 400", path, rr.Code)
		}
		body := decodeJSON[map[string]string](t, rr.Body)
		if body["error"] == "" {
			t.Errorf("GET %s: no error message in body", path)
		}
	}
}

// readSSEEvent reads lines from r until it finds a non-empty "data: " line,
// or the deadline passes.
func readSSEEvent(t *testing.T, r *bufio.Reader, deadline time.Duration) Event {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	lines := make(chan result, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			lines <- result{line, err}
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data: ") {
				return
			}
		}
	}()

	timeout := time.After(deadline)
	for {
		select {
		case res := <-lines:
			if res.err != nil {
				t.Fatalf("reading SSE stream: %v", res.err)
			}
			if strings.HasPrefix(res.line, "data: ") {
				var e Event
				if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(res.line), "data: ")), &e); err != nil {
					t.Fatalf("unmarshal SSE event: %v", err)
				}
				return e
			}
		case <-timeout:
			t.Fatal("timed out waiting for SSE event")
		}
	}
}

func TestStreamDeliversPublishedEvent(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, broker := newTestAPI(t, st)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect to /api/stream: %v", err)
	}
	defer resp.Body.Close()

	go func() {
		time.Sleep(50 * time.Millisecond)
		broker.Publish(Event{Type: "request", ID: 42})
	}()

	start := time.Now()
	e := readSSEEvent(t, bufio.NewReader(resp.Body), time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("event arrived after %v, want within 1s", elapsed)
	}
	if e.Type != "request" || e.ID != 42 {
		t.Fatalf("event = %+v, want {request 42}", e)
	}
}

// TestPublishingStoreInsertRequests covers the glue round-1 added so a grouped
// flush still feeds the dashboard's live feed (br-GI-1-09: "a request through
// the proxy appears in the live feed"): InsertRequests commits every row of a
// batch through the wrapped store and publishes exactly one {type:"request",
// id} per committed row. TestStreamDeliversPublishedEvent publishes to the
// broker directly and so never exercises this decorator.
func TestPublishingStoreInsertRequests(t *testing.T) {
	st := newTestStore(t)
	broker := NewBroker()
	events, unsub := broker.Subscribe()
	defer unsub()
	ps := NewPublishingStore(st, broker)

	reqs := make([]*store.Request, 3)
	for i := range reqs {
		reqs[i] = &store.Request{
			StartedAt:      time.Now().Add(-time.Minute),
			Method:         "POST",
			Path:           "/v1/messages",
			RemoteAddr:     "127.0.0.1:1234",
			Status:         200,
			ReqHeaders:     `{}`,
			RespHeaders:    `{}`,
			ReqBody:        []byte(`{}`),
			RespBody:       []byte(`{}`),
			ModelRequested: "deepseek-chat",
			ModelResolved:  "deepseek-chat",
			PrefixHash:     "abc123",
		}
	}

	if err := ps.InsertRequests(context.Background(), reqs); err != nil {
		t.Fatalf("InsertRequests: %v", err)
	}

	for _, r := range reqs {
		if r.ID == 0 {
			t.Fatalf("row %+v was not assigned an id — the batch did not commit", r)
		}
		select {
		case e := <-events:
			if e.Type != "request" || e.ID != r.ID {
				t.Fatalf("event = %+v, want {request %d}", e, r.ID)
			}
		case <-time.After(time.Second):
			t.Fatalf("no SSE event published for committed row %d", r.ID)
		}
	}
	select {
	case e := <-events:
		t.Fatalf("extra event %+v published, want exactly one per committed row", e)
	default:
	}

	rows, err := st.ListRequests(context.Background(), store.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(rows) != len(reqs) {
		t.Fatalf("got %d rows, want %d (every batch row committed)", len(rows), len(reqs))
	}
}

// TestStreamSlowClientIsolation exercises the broker's drop-slow-clients
// policy with a real dashboard client on one side (an actual HTTP SSE
// connection through api.stream) and a deliberately-never-drained broker
// subscriber on the other, standing in for a wedged browser tab.
//
// The stalled side is a direct broker.Subscribe(), not a second real HTTP
// connection whose socket is left unread: forcing that scenario
// deterministically requires overrunning an OS kernel socket buffer, whose
// size varies by platform/CI and isn't something this test controls,
// making a real-second-connection version of this test either pass
// vacuously (small payloads never overflow the kernel buffer, so nothing
// gets dropped) or hang (see the fix history in this file's git blame — an
// earlier version of this test flushed padded payloads until the OS buffer
// blocked and then had to wait for a close that depended on scheduling
// timing, and once hung the whole suite past its timeout). The invariant
// itself — Publish never blocks, a subscriber whose buffer fills is
// dropped rather than accumulating unboundedly — is what broker.go
// actually implements and is proven precisely, deterministically, at the
// Broker level in broker_test.go's TestBrokerSlowClientIsolation. This test
// adds the piece broker_test.go can't: that the real HTTP /api/stream
// endpoint keeps serving a well-behaved client while another subscriber on
// the same broker is being flooded.
func TestStreamSlowClientIsolation(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, broker := newTestAPI(t, st)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	fastResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer fastResp.Body.Close()

	const n = 500
	fastReader := bufio.NewReader(fastResp.Body)
	fastDone := make(chan int, 1)
	readerReady := make(chan struct{})
	go func() {
		close(readerReady)
		count := 0
		for {
			line, err := fastReader.ReadString('\n')
			if err != nil {
				fastDone <- count
				return
			}
			if strings.HasPrefix(line, "data: ") {
				count++
				if count == n {
					fastDone <- count
					return
				}
			}
		}
	}()
	// Wait for the reader goroutine to have actually started before
	// flooding the broker (see broker_test.go's TestBrokerSlowClientIsolation
	// for why this matters: an unthrottled publish loop can otherwise
	// outrun the reader's very first scheduler quantum).
	<-readerReady

	// A stalled subscriber that never reads its channel — the in-process
	// analogue of a wedged browser tab, at the exact seam broker.go guards.
	stalled, _ := broker.Subscribe()

	start := time.Now()
	for i := 0; i < n; i++ {
		broker.Publish(Event{Type: "request", ID: int64(i)})
		// Real, not cooperative, pacing: the fast reader here does actual
		// socket I/O (a syscall per network read), so it needs genuine
		// wall-clock scheduling windows to keep up with a tight publish
		// loop, same reasoning as broker_test.go's version of this test.
		// 500 * 200us = 100ms, far under the 1s bound asserted below.
		time.Sleep(200 * time.Microsecond)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("publishing %d events took %v, want the publisher to never block on a stalled subscriber", n, elapsed)
	}

	select {
	case count := <-fastDone:
		if count != n {
			t.Fatalf("fast client received %d/%d events", count, n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fast client did not receive all events in time")
	}

	// The stalled subscriber must have been dropped (its channel closed)
	// once its buffer filled, not have accumulated all n events.
	drained := 0
	for {
		_, ok := <-stalled
		if !ok {
			break
		}
		drained++
	}
	if drained >= n {
		t.Fatalf("stalled subscriber accumulated all %d events unbounded, want it dropped once its buffer filled", n)
	}
}
