package cli

import (
	"context"
	"encoding/json"
	"os"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/analyze"
	"github.com/abhisheksarkar30/deepseek-lens/internal/api"
	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/consumer"
	"github.com/abhisheksarkar30/deepseek-lens/internal/pricing"
	"github.com/abhisheksarkar30/deepseek-lens/internal/sink"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
	"github.com/abhisheksarkar30/deepseek-lens/internal/web"
)

// serveRig is the slice of `lens serve`'s wiring that wireCalendar operates
// on: one sink, one consumer, one dashboard handler, one store. Serve itself
// cannot be driven from a test (two real listeners, a blocking signal
// context), which is exactly why wireCalendar was split out of it.
type serveRig struct {
	store   *store.Store
	sink    *sink.Sink
	cons    *consumer.Consumer
	dashAPI interface{ SetCalendar(pricing.Calendar) }
	handler http.Handler
	cfg     *config.Config
}

func newServeRig(t *testing.T) *serveRig {
	t.Helper()
	cfg := config.Default()

	dbPath := filepath.Join(t.TempDir(), "lens.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	sk := sink.New(16)
	// Price one model at a round rate so a 1,000,000-token call is exactly
	// the rate off-peak and exactly twice it at peak — the two answers the
	// seam tests tell apart.
	cons := consumer.New(sk, st, nil, analyze.NewRules(cfg.ModelMap, cfg.ModelMaxTokens, pricing.Calendar{}))
	cons.SetPriceTable(pricing.Table{"deepseek-chat": {Input: ptrTo(0.28)}})

	// nil proxy handler and replay off: this rig drives the read routes and
	// the consumer only, and the replay route needs a live upstream.
	var proxyHandler http.Handler
	dashAPI := api.New(st, sk, cons, api.NewBroker(), web.Files, proxyHandler, false)
	dashAPI.SetPricing(pricing.DefaultPath())

	return &serveRig{store: st, sink: sk, cons: cons, dashAPI: dashAPI, handler: dashAPI, cfg: cfg}
}

// calendar mirrors Serve's own construction, from the same two config keys —
// so these tests exercise the shipped 2026 calendar rather than a fixture
// that could agree with the code by accident.
func (r *serveRig) calendar(t *testing.T) pricing.Calendar {
	t.Helper()
	cal, err := pricing.NewCalendar(r.cfg.OffPeakDates, r.cfg.WorkDates)
	if err != nil {
		t.Fatalf("pricing.NewCalendar: %v", err)
	}
	return cal
}

// callAt is one captured /v1/messages call that parses to 1,000,000 input
// tokens on deepseek-chat, starting at at.
func callAt(at time.Time) *sink.CapturedCall {
	return &sink.CapturedCall{
		StartedAt:   at,
		TTFB:        2 * time.Millisecond,
		Duration:    9 * time.Millisecond,
		Method:      "POST",
		Path:        "/v1/messages",
		RemoteAddr:  "127.0.0.1:54321",
		Status:      200,
		ReqHeaders:  http.Header{"Content-Type": {"application/json"}},
		RespHeaders: http.Header{"Content-Type": {"application/json"}},
		ReqBody:     []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`),
		RespBody: []byte(`{"model":"deepseek-chat","stop_reason":"end_turn",` +
			`"usage":{"input_tokens":1000000,"output_tokens":0}}`),
	}
}

// runClosed submits calls to the rig's sink, closes it, and runs the consumer
// to completion — so every row is stored by the time this returns, with no
// polling and no deadline beyond the safety net.
func (r *serveRig) runClosed(t *testing.T, calls ...*sink.CapturedCall) {
	t.Helper()
	for i, call := range calls {
		if !r.sink.Submit(call) {
			t.Fatalf("submit #%d dropped", i)
		}
	}
	r.sink.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := r.cons.Run(ctx); err != nil {
		t.Fatalf("consumer.Run: %v", err)
	}
}

// pricesBody GETs /api/prices and decodes it as the raw fields, so the
// assertion is on what the route actually serialises.
func (r *serveRig) pricesBody(t *testing.T) map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	r.handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/prices", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/prices: status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode /api/prices: %v", err)
	}
	return got
}

// TestWireCalendarReachesBothSeams is br-GI-24-06's case. The calendar has
// three install points and only one of them is compile-enforced; the two
// setters are silent when forgotten, because the zero calendar is
// indistinguishable from today's rule in every test that does not look for
// it. So this asserts both seams, and a wireCalendar that wired only one
// would fail exactly one half.
func TestWireCalendarReachesBothSeams(t *testing.T) {
	rig := newServeRig(t)
	cal := rig.calendar(t)
	wireCalendar(cal, rig.cons, rig.dashAPI)

	t.Run("the API seam echoes the calendar", func(t *testing.T) {
		got := rig.pricesBody(t)
		if got["off_peak_dates"] != rig.cfg.OffPeakDates || got["work_dates"] != rig.cfg.WorkDates {
			t.Errorf("off_peak_dates/work_dates = %v/%v, want the configured %q/%q",
				got["off_peak_dates"], got["work_dates"], rig.cfg.OffPeakDates, rig.cfg.WorkDates)
		}
	})

	t.Run("the consumer seam prices the shipped calendar", func(t *testing.T) {
		holiday := callAt(time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC))  // National Day, a rest day, inside the window
		ordinary := callAt(time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)) // the next Thursday, the same clock hour
		makeup := callAt(time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC))  // 调休: a Saturday the calendar calls a work day

		rig.runClosed(t, holiday, ordinary, makeup)

		costs := map[time.Time]*float64{}
		reqs, err := rig.store.ListRequests(context.Background(), store.Filter{Limit: 10})
		if err != nil {
			t.Fatalf("ListRequests: %v", err)
		}
		for _, r := range reqs {
			costs[r.StartedAt.UTC()] = r.CostUSD
		}
		for _, tc := range []struct {
			name string
			at   time.Time
			want float64
		}{
			{"a National Day call", holiday.StartedAt, 0.28},
			{"the next ordinary Thursday", ordinary.StartedAt, 0.56},
			{"a 调休 Saturday", makeup.StartedAt, 0.56},
		} {
			got, ok := costs[tc.at.UTC()]
			if !ok || got == nil {
				t.Fatalf("%s: no priced row at %s", tc.name, tc.at)
			}
			if *got != tc.want {
				t.Errorf("%s CostUSD = %v, want %v", tc.name, *got, tc.want)
			}
		}
	})
}

// TestUnwiredCalendarIsTheSupportedState: with no wireCalendar call the two
// seams keep their documented unset behaviour — the date fields are empty and
// the consumer prices on the window-and-weekend rule alone. This is the
// negative control for the test above: it is what a forgotten setter looks
// like, and it must be legal (not a panic, not a 503).
func TestUnwiredCalendarIsTheSupportedState(t *testing.T) {
	rig := newServeRig(t)

	got := rig.pricesBody(t)
	if got["off_peak_dates"] != "" || got["work_dates"] != "" {
		t.Errorf("off_peak_dates/work_dates = %v/%v, want \"\"/\"\" with no calendar wired",
			got["off_peak_dates"], got["work_dates"])
	}

	// The same holiday instant the wired rig prices at 1x: unwired, the
	// window rule sees a plain Thursday inside the window and charges 2x.
	holiday := callAt(time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC))
	rig.runClosed(t, holiday)

	reqs, err := rig.store.ListRequests(context.Background(), store.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("got %d rows, want 1", len(reqs))
	}
	if reqs[0].CostUSD == nil {
		t.Fatal("CostUSD = nil, want the window rule's 2x price")
	}
	if *reqs[0].CostUSD != 0.56 {
		t.Errorf("CostUSD = %v, want 0.56 — the zero calendar is the pre-GI-24 window rule", *reqs[0].CostUSD)
	}
}

// TestWireCalendarNamesBothSeams is the cheap structural guard behind the
// two behavioural cases above: if a later refactor drops one of the two
// calls, this fails even when the caller's config happens to make the two
// calendars agree.
func TestWireCalendarNamesBothSeams(t *testing.T) {
	rig := newServeRig(t)
	cal, err := pricing.NewCalendar("2026-10-01", "2026-10-10")
	if err != nil {
		t.Fatalf("NewCalendar: %v", err)
	}
	rec := &recordingDash{}
	wireCalendar(cal, rig.cons, rec)

	wantOff, wantWork := cal.DateSets()
	if gotOff, gotWork := rec.got.DateSets(); gotOff != wantOff || gotWork != wantWork {
		t.Errorf("dash calendar = %q/%q, want %q/%q", gotOff, gotWork, wantOff, wantWork)
	}
}

// recordingDash captures what wireCalendar handed the dashboard without
// standing up the HTTP route.
type recordingDash struct{ got pricing.Calendar }

func (d *recordingDash) SetCalendar(cal pricing.Calendar) { d.got = cal }

func TestMaintenancePassPurgesThenArchives(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "lens.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	seedRequest(t, st, func(r *store.Request) {
		r.StartedAt = time.Now().Add(-40 * 24 * time.Hour)
		r.ReqBody = []byte("purge-me")
	})
	seedRequest(t, st, func(r *store.Request) {
		r.StartedAt = time.Now().Add(-10 * 24 * time.Hour)
		r.ReqBody = []byte("archive-me")
	})
	runMaintenancePass(ctx, st, func() int { return 30 }, func() int { return 7 })
	left, err := st.ListRequests(ctx, store.Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 {
		t.Fatalf("rows = %d, want the warm row only", len(left))
	}
	entries, err := os.ReadDir(filepath.Join(dir, "archive"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("archive dir: %v entries=%d", err, len(entries))
	}
	got, err := st.GetRequest(ctx, left[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.ReqBody) != "archive-me" {
		t.Fatalf("hydrated body = %q", got.ReqBody)
	}
}

func TestStateFileStaysUntilMaintenanceReturns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.state.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	joined := make(chan struct{})
	go func() {
		<-release
		close(joined)
	}()
	if waitJoined(joined, 30*time.Millisecond) {
		t.Fatal("joined before the batch returned")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("state file removed while the batch was in flight")
	}
	close(release)
	if !waitJoined(joined, time.Second) {
		t.Fatal("did not join after the batch returned")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
