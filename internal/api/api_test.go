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
	"github.com/abhisheksarkar30/deepseek-lens/internal/pricing"
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
func newTestAPI(t *testing.T, st Store) (*api, *sink.Sink, *consumer.Consumer, *Broker) {
	t.Helper()
	sk := sink.New(16)
	cons := consumer.New(sk, nil, nil) // Run is never called in most tests; Stats() is fine on a fresh Consumer.
	broker := NewBroker()
	// No proxy handler and replay off: these tests cover the routes that only
	// read, plus prices_test.go's POST /api/prices cases; the replay route
	// (br-GI-1-13) is exercised in internal/replay, where a real proxy and a
	// recording upstream can be wired up.
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

func TestListRequestsUntil(t *testing.T) {
	st := newTestStore(t)
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedRequest(t, st, func(r *store.Request) { r.StartedAt = t0 })
	seedRequest(t, st, func(r *store.Request) { r.StartedAt = t0.Add(time.Hour) })
	handler, _, _, _ := newTestAPI(t, st)

	until := url.QueryEscape(t0.Add(time.Hour).Format(time.RFC3339))
	since := url.QueryEscape(t0.Format(time.RFC3339))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/requests?since="+since+"&until="+until, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	got := decodeJSON[[]*store.Request](t, rr.Body)
	if len(got) != 1 {
		t.Fatalf("window len = %d, want 1", len(got))
	}
	if rr.Header().Get("X-Total-Count") != "1" {
		t.Fatalf("X-Total-Count = %q, want 1", rr.Header().Get("X-Total-Count"))
	}

	rr = httptest.NewRecorder()
	later := url.QueryEscape(t0.Add(2 * time.Hour).Format(time.RFC3339))
	earlier := url.QueryEscape(t0.Add(3 * time.Hour).Format(time.RFC3339))
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/requests?since="+earlier+"&until="+later, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("inverted status = %d, want 200", rr.Code)
	}
	empty := decodeJSON[[]*store.Request](t, rr.Body)
	if len(empty) != 0 {
		t.Fatalf("since > until len = %d, want 0", len(empty))
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
	Peak     struct {
		Calls   int     `json:"calls"`
		CostUSD float64 `json:"cost_usd"`
	} `json:"peak"`
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

// peakAt and offAt share the fixture instants internal/analyze's own
// peak-pricing cases use: 07:00 UTC inside the window, 20:00 UTC outside it,
// on the same ordinary Friday (2026-09-11).
var (
	peakAt = time.Date(2026, 9, 11, 7, 0, 0, 0, time.UTC)
	offAt  = time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC)
)

// callSeed is one call to plant in a session: when it started and what it
// cost. A nil cost is an unpriced call.
type callSeed struct {
	at   time.Time
	cost *float64
}

// seedSession plants seeds as a session's calls and upserts the session row
// with total as its TotalCostUSD, returning the inserted calls (ids set) in
// seed order. The calls go through seedRequest, so they carry whatever the
// rest of the suite's default request does.
func seedSession(t *testing.T, st *store.Store, sid string, total float64, seeds ...callSeed) []*store.Request {
	t.Helper()
	inserted := make([]*store.Request, 0, len(seeds))
	for _, s := range seeds {
		inserted = append(inserted, seedRequest(t, st, func(r *store.Request) {
			r.StartedAt = s.at
			r.CostUSD = s.cost
			id := sid
			r.SessionID = &id
		}))
	}
	if err := st.UpsertSession(context.Background(), &store.Session{
		ID: sid, FirstSeen: peakAt.Add(-time.Hour), LastSeen: peakAt,
		RequestCount: len(seeds), TotalCostUSD: total,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	return inserted
}

// getSessionRaw issues GET /api/sessions/{id} and returns the recorder, so a
// case that cares about the raw body (the NaN check) can read it.
func getSessionRaw(t *testing.T, handler *api, sid string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/sessions/"+sid, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions/%s: status = %d, want 200: %s", sid, rr.Code, rr.Body.String())
	}
	return rr
}

// TestGetSessionPeakRollup is br-GI-24-05's case list: the drill-down's
// answer to "was this session expensive because of the work, or the clock".
func TestGetSessionPeakRollup(t *testing.T) {
	t.Run("mixed peak and off-peak", func(t *testing.T) {
		st := newTestStore(t)
		seedSession(t, st, "s_1_mixed", 0.84,
			callSeed{at: peakAt, cost: f64(0.56)},
			callSeed{at: offAt, cost: f64(0.28)},
		)
		handler, _, _, _ := newTestAPI(t, st)

		got := decodeJSON[sessionDetailJSON](t, getSessionRaw(t, handler, "s_1_mixed").Body)
		if got.Peak.Calls != 1 {
			t.Errorf("peak.calls = %d, want 1 (the window call only)", got.Peak.Calls)
		}
		if got.Peak.CostUSD != 0.56 {
			t.Errorf("peak.cost_usd = %v, want 0.56 — exactly the window call's cost", got.Peak.CostUSD)
		}
	})

	t.Run("all peak", func(t *testing.T) {
		st := newTestStore(t)
		seedSession(t, st, "s_1_allpeak", 1.12,
			callSeed{at: peakAt, cost: f64(0.56)},
			callSeed{at: peakAt.Add(time.Minute), cost: f64(0.56)},
		)
		handler, _, _, _ := newTestAPI(t, st)

		got := decodeJSON[sessionDetailJSON](t, getSessionRaw(t, handler, "s_1_allpeak").Body)
		if got.Peak.Calls != 2 {
			t.Errorf("peak.calls = %d, want 2 (every call in the session)", got.Peak.Calls)
		}
	})

	// The case that would pass if the rollup restated IsPeak instead of
	// calling PeakPriced: an unpriced call at a peak instant was not "priced
	// under peak hours".
	t.Run("unpriced at peak counts zero", func(t *testing.T) {
		st := newTestStore(t)
		seedSession(t, st, "s_1_unpriced", 0,
			callSeed{at: peakAt, cost: nil},
			callSeed{at: peakAt.Add(time.Minute), cost: nil},
		)
		handler, _, _, _ := newTestAPI(t, st)

		got := decodeJSON[sessionDetailJSON](t, getSessionRaw(t, handler, "s_1_unpriced").Body)
		if got.Peak.Calls != 0 || got.Peak.CostUSD != 0 {
			t.Errorf("peak = %+v, want zero: an unpriced call is not priced under peak hours", got.Peak)
		}
	})

	t.Run("empty session", func(t *testing.T) {
		st := newTestStore(t)
		seedSession(t, st, "s_1_empty", 0)
		handler, _, _, _ := newTestAPI(t, st)

		got := decodeJSON[sessionDetailJSON](t, getSessionRaw(t, handler, "s_1_empty").Body)
		if got.Peak.Calls != 0 || got.Peak.CostUSD != 0 {
			t.Errorf("peak = %+v, want zero for a session with no calls", got.Peak)
		}
	})

	// The all-unpriced shape (TotalCostUSD = 0 with calls): the share the UI
	// renders is CostUSD / TotalCostUSD, which is 0/0 here. The response
	// carries the two raw numbers and never a share, so nothing can go out as
	// NaN — asserted on the bytes, because a NaN in the body would be a
	// marshal error rather than a readable field.
	t.Run("all-unpriced session has no NaN in the body", func(t *testing.T) {
		st := newTestStore(t)
		seedSession(t, st, "s_1_zero", 0,
			callSeed{at: peakAt, cost: nil},
			callSeed{at: offAt, cost: nil},
		)
		handler, _, _, _ := newTestAPI(t, st)

		body := getSessionRaw(t, handler, "s_1_zero").Body.String()
		if strings.Contains(body, "NaN") {
			t.Errorf("response body contains NaN: %s", body)
		}
		got := decodeJSON[sessionDetailJSON](t, strings.NewReader(body))
		if got.Peak.Calls != 0 {
			t.Errorf("peak.calls = %d, want 0", got.Peak.Calls)
		}
	})

	// The documented cross-calendar split (D6/D8): the row carries a stored
	// peak_pricing warning because it was ingested when the calendar did not
	// know the date, but the calendar installed now reads it as off-peak. The
	// header and the badge disagree, and that is the intended answer.
	t.Run("cross-calendar split is intentional", func(t *testing.T) {
		st := newTestStore(t)
		holiday := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
		calls := seedSession(t, st, "s_1_split", 0.28, callSeed{at: holiday, cost: f64(0.28)})
		if err := st.InsertWarnings(context.Background(), calls[0].ID, []store.Warning{
			{Kind: "peak_pricing", Severity: "warn", Detail: "stored when the calendar did not know", CreatedAt: time.Now()},
		}); err != nil {
			t.Fatalf("InsertWarnings: %v", err)
		}

		cal, err := pricing.NewCalendar("2026-10-01", "")
		if err != nil {
			t.Fatalf("NewCalendar: %v", err)
		}
		handler, _, _, _ := newTestAPI(t, st)
		handler.SetCalendar(cal)

		got := decodeJSON[sessionDetailJSON](t, getSessionRaw(t, handler, "s_1_split").Body)
		if got.Peak.Calls != 0 {
			t.Errorf("peak.calls = %d, want 0: today's calendar prices that day off-peak", got.Peak.Calls)
		}
		if len(got.Warnings) != 1 || got.Warnings[0].Kind != "peak_pricing" {
			t.Errorf("warnings = %+v, want the stored peak_pricing warning still present", got.Warnings)
		}
	})
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

// getStats issues GET /api/stats with query and returns the decoded body,
// failing on any non-200.
func getStats(t *testing.T, handler http.Handler, query string) statsResponse {
	t.Helper()
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/stats"+query, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/stats%s: status = %d, want 200: %s", query, rr.Code, rr.Body.String())
	}
	return decodeJSON[statsResponse](t, rr.Body)
}

// TestStatsGranularityParam covers br-GI-21-03's granularity contract: each
// valid value selects a bucket format, and the response echoes the one that
// was actually applied rather than leaving the client to infer it.
func TestStatsGranularityParam(t *testing.T) {
	st := newTestStore(t)
	// One row at a fixed instant, so each granularity's bucket label is
	// deterministic: 2026-03-01T00:00Z is a Sunday, which %W numbers as week
	// 08 of 2026.
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedRequest(t, st, func(r *store.Request) { r.StartedAt = at })
	handler, _, _, _ := newTestAPI(t, st)

	cases := []struct {
		granularity string
		wantLabel   func(string) bool
		labelDesc   string
	}{
		{"hour", func(s string) bool { return s == "2026-03-01T00:00" }, `"2026-03-01T00:00"`},
		{"day", func(s string) bool { return s == "2026-03-01" }, `"2026-03-01"`},
		{"week", func(s string) bool { return len(s) == 8 && strings.HasPrefix(s, "2026-W") }, `"2026-Www"`},
		{"month", func(s string) bool { return s == "2026-03" }, `"2026-03"`},
	}
	for _, tc := range cases {
		got := getStats(t, handler, "?granularity="+tc.granularity)
		if got.Granularity != tc.granularity {
			t.Errorf("granularity=%s: echoed %q, want %q", tc.granularity, got.Granularity, tc.granularity)
		}
		if len(got.ByPeriod) != 1 {
			t.Fatalf("granularity=%s: got %d buckets, want 1 (%+v)", tc.granularity, len(got.ByPeriod), got.ByPeriod)
		}
		if label := got.ByPeriod[0].Period; !tc.wantLabel(label) {
			t.Errorf("granularity=%s: bucket label %q, want %s", tc.granularity, label, tc.labelDesc)
		}
		if got.ByPeriod[0].RequestCount != 1 {
			t.Errorf("granularity=%s: bucket RequestCount = %d, want 1", tc.granularity, got.ByPeriod[0].RequestCount)
		}
	}

	// Absent defaults to day, and says so in the response.
	got := getStats(t, handler, "")
	if got.Granularity != "day" {
		t.Errorf("absent granularity: echoed %q, want %q", got.Granularity, "day")
	}
}

// TestStatsInvalidGranularityIs400 pins the API-layer half of the two-layer
// granularity validation: an unknown value is rejected before any store call,
// so the store's own whitelist never has to be the first line of defense.
func TestStatsInvalidGranularityIs400(t *testing.T) {
	st := newTestStore(t)
	seedRequest(t, st, func(r *store.Request) { r.InputTokens = 10; r.OutputTokens = 20 })
	handler, _, _, _ := newTestAPI(t, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/stats?granularity=fortnight", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	body := decodeJSON[map[string]string](t, rr.Body)
	if body["error"] == "" {
		t.Errorf("body = %+v, want an error message", body)
	}

	// The seeded row is still there and still reported: the rejected request
	// did not consume or partially execute anything.
	got := getStats(t, handler, "")
	if got.Summary.RequestCount != 1 {
		t.Errorf("after rejected request: RequestCount = %d, want 1", got.Summary.RequestCount)
	}
}

func TestStatsTzOffset(t *testing.T) {
	st := newTestStore(t)
	a := time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC)
	b := time.Date(2026, 1, 2, 2, 0, 0, 0, time.UTC)
	seedRequest(t, st, func(r *store.Request) { r.StartedAt = a })
	seedRequest(t, st, func(r *store.Request) { r.StartedAt = b })
	handler, _, _, _ := newTestAPI(t, st)

	for _, q := range []string{"?tz_offset=abc", "?tz_offset=900", "?tz_offset=-900"} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/stats"+q, nil))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", q, rr.Code)
		}
	}
	utc := getStats(t, handler, "")
	if len(utc.ByPeriod) != 2 {
		t.Fatalf("default buckets = %d, want 2", len(utc.ByPeriod))
	}
	ist := getStats(t, handler, "?tz_offset=330")
	if len(ist.ByPeriod) != 1 {
		t.Fatalf("tz_offset=330 buckets = %+v, want 1", ist.ByPeriod)
	}
}

func TestStatsMalformedUntilIs400(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/stats?until=notatime", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
}

// TestStatsUntilNarrowsEveryField is the cross-field guard: until has to bound
// all four aggregates, not just the period breakdown. If only StatsByPeriod
// learned about it, the summary beside the chart would describe a wider window
// than the chart — the "two numbers that cannot be read as agreeing" failure,
// which looks like arithmetic that does not add up rather than a crash.
func TestStatsUntilNarrowsEveryField(t *testing.T) {
	st := newTestStore(t)
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, off := range []time.Duration{time.Hour, 2 * time.Hour, 5 * time.Hour} {
		seedRequest(t, st, func(r *store.Request) { r.StartedAt = base.Add(off) })
	}
	handler, _, _, _ := newTestAPI(t, st)

	cutoff := base.Add(3 * time.Hour)
	since := base.Format(time.RFC3339)
	until := cutoff.Format(time.RFC3339)

	got := getStats(t, handler, "?since="+since+"&until="+until)
	if got.Since.Format(time.RFC3339) != since {
		t.Errorf("echoed since = %s, want %s", got.Since.Format(time.RFC3339), since)
	}
	if got.Until.Format(time.RFC3339) != until {
		t.Errorf("echoed until = %s, want %s", got.Until.Format(time.RFC3339), until)
	}
	if got.Summary.RequestCount != 2 {
		t.Errorf("summary RequestCount = %d, want 2", got.Summary.RequestCount)
	}
	if n := sumCounts(got.ByModel, func(m store.ModelStat) int { return m.RequestCount }); n != 2 {
		t.Errorf("by_model totals %d calls, want 2", n)
	}
	if n := sumCounts(got.CostSources, func(c store.CostSourceStat) int { return c.RequestCount }); n != 2 {
		t.Errorf("cost_sources totals %d calls, want 2", n)
	}
	if n := sumCounts(got.ByPeriod, func(p store.PeriodStat) int { return p.RequestCount }); n != 2 {
		t.Errorf("by_period totals %d calls, want 2", n)
	}
}

// sumCounts totals a count field across rows, for comparing two breakdowns
// that should describe the same window.
func sumCounts[T any](rows []T, count func(T) int) int {
	n := 0
	for _, r := range rows {
		n += count(r)
	}
	return n
}

// TestStatsSinceAfterUntilIsEmpty pins that an inverted window is a legitimate
// empty answer, not a rejection: the bounds are independently settable by the
// client, and a range with nothing in it is a normal thing to ask for.
func TestStatsSinceAfterUntilIsEmpty(t *testing.T) {
	st := newTestStore(t)
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedRequest(t, st, func(r *store.Request) { r.StartedAt = base })
	handler, _, _, _ := newTestAPI(t, st)

	since := base.Add(time.Hour).Format(time.RFC3339)
	until := base.Format(time.RFC3339)
	got := getStats(t, handler, "?since="+since+"&until="+until)

	if got.Summary == nil || got.Summary.RequestCount != 0 {
		t.Errorf("summary = %+v, want RequestCount 0", got.Summary)
	}
	if len(got.ByPeriod) != 0 {
		t.Errorf("by_period = %+v, want empty", got.ByPeriod)
	}
	if len(got.ByModel) != 0 {
		t.Errorf("by_model = %+v, want empty", got.ByModel)
	}
	if len(got.CostSources) != 0 {
		t.Errorf("cost_sources = %+v, want empty", got.CostSources)
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

func TestPostShutdown(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	stopped := false
	handler.SetStop(func() { stopped = true })

	remote := httptest.NewRequest(http.MethodPost, "/api/shutdown", nil)
	remote.RemoteAddr = "192.0.2.1:9"
	remote.Host = "127.0.0.1"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, remote)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("remote caller status = %d, want 403", rr.Code)
	}

	cross := httptest.NewRequest(http.MethodPost, "/api/shutdown", nil)
	cross.RemoteAddr = "127.0.0.1:9"
	cross.Host = "127.0.0.1"
	cross.Header.Set("Origin", "http://evil.example")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, cross)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403", rr.Code)
	}

	ok := httptest.NewRequest(http.MethodPost, "/api/shutdown", nil)
	ok.RemoteAddr = "127.0.0.1:9"
	ok.Host = "127.0.0.1"
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, ok)
	if rr.Code != http.StatusOK {
		t.Fatalf("loopback status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if !stopped {
		t.Fatal("stop was not called")
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
		"/api/requests?until=notatime",
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

// GI-15 (br-GI-15-04): GET /api/warnings/summary — the groups the warning
// inbox renders, counted in SQL over the whole matching set.

// seedWarningsAt hangs warnings off one fresh request, so each call to it
// gets its own row to attach them to.
func seedWarningsAt(t *testing.T, st *store.Store, warnings []store.Warning) {
	t.Helper()
	r := seedRequest(t, st, nil)
	if err := st.InsertWarnings(context.Background(), r.ID, warnings); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}
}

// groupByKey indexes a summary by "kind|severity", failing on a duplicate —
// one entry per pair is the whole contract.
func groupByKey(t *testing.T, groups []store.WarningGroup) map[string]store.WarningGroup {
	t.Helper()
	m := map[string]store.WarningGroup{}
	for _, g := range groups {
		key := g.Kind + "|" + g.Severity
		if _, dup := m[key]; dup {
			t.Errorf("group %s appears twice — the summary must be one entry per (kind, severity)", key)
		}
		m[key] = g
	}
	return m
}

func summaryGroups(t *testing.T, h http.Handler, query string) []store.WarningGroup {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/warnings/summary"+query, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/warnings/summary%s: status = %d, want 200: %s", query, rr.Code, rr.Body.String())
	}
	return decodeJSON[[]store.WarningGroup](t, rr.Body)
}

// TestWarningsSummaryCountsPastTheListCap is the regression test this bead
// exists for. The dashboard used to group the rows it had fetched, which is
// at most store.DefaultLimit of them, so a kind that fired more often than
// that reported a count that was simply wrong — no error, no signal to the
// user, just a number that was too low. Counting in SQL over the whole table
// is what fixes it.
//
// The seed is DefaultLimit+1 rather than a round number: below the cap, a
// correct count and a truncated one are indistinguishable, so a smaller
// fixture would pass against the very behavior this guards against.
func TestWarningsSummaryCountsPastTheListCap(t *testing.T) {
	st := newTestStore(t)
	base := time.Now().Add(-time.Hour)
	warnings := make([]store.Warning, store.DefaultLimit+1)
	for i := range warnings {
		warnings[i] = store.Warning{
			Kind: "cache_control_ignored", Severity: "warn",
			Detail:    fmt.Sprintf("dropped %d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}
	}
	seedWarningsAt(t, st, warnings)
	handler, _, _, _ := newTestAPI(t, st)

	got := summaryGroups(t, handler, "")
	if len(got) != 1 {
		t.Fatalf("got %d groups, want 1: %+v", len(got), got)
	}
	if got[0].Count != store.DefaultLimit+1 {
		t.Errorf("group count = %d, want the true seeded total %d — the summary is being "+
			"computed over a capped row set rather than the whole table", got[0].Count, store.DefaultLimit+1)
	}
	if want := base.Add(time.Duration(store.DefaultLimit) * time.Second); !got[0].LastSeen.Equal(want) {
		t.Errorf("LastSeen = %v, want %v (the group's max created_at)", got[0].LastSeen, want)
	}
}

func TestWarningsSummaryGroupingShape(t *testing.T) {
	st := newTestStore(t)
	base := time.Now().Add(-time.Hour)
	seedWarningsAt(t, st, []store.Warning{
		{Kind: "alpha", Severity: "warn", Detail: "a1", CreatedAt: base},
		{Kind: "alpha", Severity: "warn", Detail: "a2", CreatedAt: base.Add(time.Second)},
		{Kind: "alpha", Severity: "error", Detail: "a3", CreatedAt: base.Add(2 * time.Second)},
		{Kind: "beta", Severity: "warn", Detail: "b1", CreatedAt: base.Add(3 * time.Second)},
	})
	handler, _, _, _ := newTestAPI(t, st)

	got := groupByKey(t, summaryGroups(t, handler, ""))
	if len(got) != 3 {
		t.Fatalf("got %d groups, want 3 (one per distinct kind/severity pair): %v", len(got), got)
	}
	for key, want := range map[string]struct {
		count    int
		lastSeen time.Time
	}{
		"alpha|warn":  {2, base.Add(time.Second)},
		"alpha|error": {1, base.Add(2 * time.Second)},
		"beta|warn":   {1, base.Add(3 * time.Second)},
	} {
		g, ok := got[key]
		if !ok {
			t.Errorf("no group for %s", key)
			continue
		}
		if g.Count != want.count {
			t.Errorf("%s: count = %d, want %d", key, g.Count, want.count)
		}
		if !g.LastSeen.Equal(want.lastSeen) {
			t.Errorf("%s: LastSeen = %v, want %v", key, g.LastSeen, want.lastSeen)
		}
	}
}

func TestWarningsSummaryOrdering(t *testing.T) {
	st := newTestStore(t)
	now := time.Now()
	seedWarningsAt(t, st, []store.Warning{
		// "rare" fires once; the other three tie at two, so the tiebreak
		// (kind, then severity) is what decides their order.
		{Kind: "rare", Severity: "warn", CreatedAt: now},
		{Kind: "zulu", Severity: "warn", CreatedAt: now},
		{Kind: "zulu", Severity: "warn", CreatedAt: now},
		{Kind: "bravo", Severity: "error", CreatedAt: now},
		{Kind: "bravo", Severity: "error", CreatedAt: now},
		{Kind: "bravo", Severity: "warn", CreatedAt: now},
		{Kind: "bravo", Severity: "warn", CreatedAt: now},
	})
	handler, _, _, _ := newTestAPI(t, st)

	got := summaryGroups(t, handler, "")
	want := []string{"bravo|error", "bravo|warn", "zulu|warn", "rare|warn"}
	if len(got) != len(want) {
		t.Fatalf("got %d groups, want %d: %+v", len(got), len(want), got)
	}
	for i, key := range want {
		if gotKey := got[i].Kind + "|" + got[i].Severity; gotKey != key {
			t.Errorf("group %d = %s, want %s — groups must come back highest count first, "+
				"ties broken by kind then severity", i, gotKey, key)
		}
	}
}

func TestWarningsSummaryFiltersNarrow(t *testing.T) {
	st := newTestStore(t)
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-time.Minute)
	seedWarningsAt(t, st, []store.Warning{
		{Kind: "alpha", Severity: "warn", CreatedAt: old},
		{Kind: "alpha", Severity: "warn", CreatedAt: old},
		{Kind: "alpha", Severity: "warn", CreatedAt: recent},
	})
	seedWarningsAt(t, st, []store.Warning{
		{Kind: "beta", Severity: "warn", CreatedAt: recent},
		{Kind: "beta", Severity: "error", CreatedAt: recent},
	})
	handler, _, _, _ := newTestAPI(t, st)

	// ?kind= narrows to alpha, and the count still covers the whole filtered
	// set — 3, not just however many rows a page would have held.
	byKind := groupByKey(t, summaryGroups(t, handler, "?kind=alpha"))
	if len(byKind) != 1 {
		t.Fatalf("?kind=alpha returned %d groups, want 1: %v", len(byKind), byKind)
	}
	if g := byKind["alpha|warn"]; g.Count != 3 {
		t.Errorf("?kind=alpha: count = %d, want 3", g.Count)
	}

	bySeverity := groupByKey(t, summaryGroups(t, handler, "?severity=error"))
	if len(bySeverity) != 1 {
		t.Fatalf("?severity=error returned %d groups, want 1: %v", len(bySeverity), bySeverity)
	}
	if g := bySeverity["beta|error"]; g.Count != 1 {
		t.Errorf("?severity=error: count = %d, want 1", g.Count)
	}

	bySince := groupByKey(t, summaryGroups(t, handler, "?since=1h"))
	g, ok := bySince["alpha|warn"]
	if !ok {
		t.Fatalf("?since=1h dropped alpha entirely, want its one in-window warning: %v", bySince)
	}
	if g.Count != 1 {
		t.Errorf("?since=1h: alpha count = %d, want 1 — the window must exclude the two older warnings", g.Count)
	}
}

// TestWarningsSummaryRouteCoexistence asserts on what each route returns, not
// on the mux's route table. /api/warnings is a prefix pattern that also
// matches /api/warnings/summary, so the failure worth guarding against is the
// summary URL quietly falling through and serving a bare warning array where
// the dashboard expects groups.
func TestWarningsSummaryRouteCoexistence(t *testing.T) {
	st := newTestStore(t)
	now := time.Now()
	seedWarningsAt(t, st, []store.Warning{
		{Kind: "alpha", Severity: "warn", Detail: "a1", CreatedAt: now},
		{Kind: "alpha", Severity: "warn", Detail: "a2", CreatedAt: now},
	})
	handler, _, _, _ := newTestAPI(t, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/warnings/summary", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("summary status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var asGroups []store.WarningGroup
	if err := json.Unmarshal(rr.Body.Bytes(), &asGroups); err != nil {
		t.Fatalf("summary body does not decode as []WarningGroup (%v): %s", err, rr.Body.String())
	}
	if len(asGroups) != 1 || asGroups[0].Kind != "alpha" || asGroups[0].Count != 2 {
		t.Errorf("summary returned %+v, want one alpha group of 2", asGroups)
	}

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/warnings", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var asWarnings []*store.Warning
	if err := json.Unmarshal(rr.Body.Bytes(), &asWarnings); err != nil {
		t.Fatalf("/api/warnings no longer decodes as []*Warning (%v): %s", err, rr.Body.String())
	}
	if len(asWarnings) != 2 {
		t.Errorf("/api/warnings returned %d rows, want the 2 raw warnings", len(asWarnings))
	} else if asWarnings[0].Detail == "" {
		t.Errorf("/api/warnings returned a group-shaped row — the summary route shadowed the list route")
	}
}

// TestWarningsSummaryHasNoPaginationHeaders: the summary is deliberately
// unpaginated, and br-GI-15-05's fetchPage keys off exactly these headers. If
// they ever show up here, the dashboard starts paging a route that cannot
// honor an offset.
func TestWarningsSummaryHasNoPaginationHeaders(t *testing.T) {
	st := newTestStore(t)
	seedWarningsAt(t, st, []store.Warning{{Kind: "alpha", Severity: "warn", CreatedAt: time.Now()}})
	handler, _, _, _ := newTestAPI(t, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/warnings/summary", nil))
	for _, name := range []string{"X-Total-Count", "X-Limit", "X-Offset"} {
		if got := rr.Header().Get(name); got != "" {
			t.Errorf("summary response carries %s: %q — this route must stay unpaginated", name, got)
		}
	}
}

// TestReplayOriginRejectNamesTheCallingAction covers br-GI-17-02: the guard
// is now shared by three write routes (replay, prices, purge), so its
// rejection message must name the caller's action rather than always
// saying "replay" — a browser turned away from POST /api/prices reading a
// message about replay would look like the wrong endpoint had been hit.
// Both rejection arms are covered: non-loopback Host and cross-origin
// Origin.
func TestReplayOriginRejectNamesTheCallingAction(t *testing.T) {
	nonLoopback := httptest.NewRequest(http.MethodPost, "/api/prices", nil)
	nonLoopback.Host = "evil.example.com"
	if reason := replayOriginReject(nonLoopback, "prices"); !strings.Contains(reason, "prices") || strings.Contains(reason, "replay") {
		t.Errorf("non-loopback Host reason = %q, want it to name %q and not %q", reason, "prices", "replay")
	}

	crossOrigin := httptest.NewRequest(http.MethodPost, "/api/purge", nil)
	crossOrigin.Host = "localhost:9999"
	crossOrigin.Header.Set("Origin", "http://attacker.example:1234")
	if reason := replayOriginReject(crossOrigin, "purge"); !strings.Contains(reason, "purge") || strings.Contains(reason, "replay") {
		t.Errorf("cross-origin reason = %q, want it to name %q and not %q", reason, "purge", "replay")
	}
}
