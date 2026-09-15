package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/parse"
	"github.com/abhisheksarkar30/deepseek-lens/internal/pricing"
	"github.com/abhisheksarkar30/deepseek-lens/internal/session"
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

// seedRequest inserts a reasonable default *store.Request, optionally
// tweaked by opts, and returns it with ID set.
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

func dataLines(out string) []string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) <= 1 {
		return nil
	}
	return lines[1:] // drop the header row
}

// --- ls ---

func TestLSRendersRowCountAndLimit(t *testing.T) {
	st := newTestStore(t)
	for i := 0; i < 3; i++ {
		seedRequest(t, st, nil)
	}

	var buf bytes.Buffer
	if err := runLS(nil, &buf, st); err != nil {
		t.Fatalf("runLS: %v", err)
	}
	if got := len(dataLines(buf.String())); got != 3 {
		t.Fatalf("got %d rows, want 3:\n%s", got, buf.String())
	}

	buf.Reset()
	if err := runLS([]string{"--limit", "2"}, &buf, st); err != nil {
		t.Fatalf("runLS --limit 2: %v", err)
	}
	if got := len(dataLines(buf.String())); got != 2 {
		t.Fatalf("got %d rows, want 2:\n%s", got, buf.String())
	}
}

func TestLSWarnFilter(t *testing.T) {
	st := newTestStore(t)
	warned := seedRequest(t, st, nil)
	seedRequest(t, st, nil) // unwarned
	if err := st.InsertWarnings(context.Background(), warned.ID, []store.Warning{
		{Kind: "dropped_param", Severity: "warn", Detail: "top_k dropped", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}

	var buf bytes.Buffer
	if err := runLS([]string{"--warn"}, &buf, st); err != nil {
		t.Fatalf("runLS --warn: %v", err)
	}
	rows := dataLines(buf.String())
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (only the warned request):\n%s", len(rows), buf.String())
	}
}

func TestLSJSONParsesWithExpectedFields(t *testing.T) {
	st := newTestStore(t)
	seedRequest(t, st, nil)

	var buf bytes.Buffer
	if err := runLS([]string{"--json"}, &buf, st); err != nil {
		t.Fatalf("runLS --json: %v", err)
	}

	dec := json.NewDecoder(&buf)
	var rows []map[string]interface{}
	for dec.More() {
		var m map[string]interface{}
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		rows = append(rows, m)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d JSON rows, want 1", len(rows))
	}
	for _, field := range []string{"id", "started_at", "duration_ms", "model", "input_tokens", "output_tokens", "cost_usd", "status", "warned"} {
		if _, ok := rows[0][field]; !ok {
			t.Errorf("ls --json row missing field %q: %v", field, rows[0])
		}
	}
}

// --- show ---

func TestShowIncludesWarningsAndBodies(t *testing.T) {
	st := newTestStore(t)
	r := seedRequest(t, st, nil)
	if err := st.InsertWarnings(context.Background(), r.ID, []store.Warning{
		{Kind: "dropped_param", Severity: "warn", Detail: "top_k dropped", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}

	var buf bytes.Buffer
	if err := runShow([]string{"1"}, &buf, st); err != nil {
		t.Fatalf("runShow: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "dropped_param") || !strings.Contains(out, "top_k dropped") {
		t.Errorf("show output missing the warning:\n%s", out)
	}
	if !strings.Contains(out, "deepseek-chat") {
		t.Errorf("show output missing request body content:\n%s", out)
	}
	if !strings.Contains(out, "msg_1") {
		t.Errorf("show output missing response body content:\n%s", out)
	}
}

func TestShowMissingExitsNonZero(t *testing.T) {
	st := newTestStore(t)
	var buf bytes.Buffer
	if err := runShow([]string{"99999"}, &buf, st); err == nil {
		t.Fatal("runShow on a missing id should return a non-nil error")
	}
}

func TestShowTruncatesLargeBodyWithoutFull(t *testing.T) {
	st := newTestStore(t)
	bigLine := strings.Repeat("line\n", 100) // not valid JSON -> printed raw
	seedRequest(t, st, func(r *store.Request) { r.RespBody = []byte(bigLine) })

	var buf bytes.Buffer
	if err := runShow([]string{"1"}, &buf, st); err != nil {
		t.Fatalf("runShow: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "truncated, use --full") {
		t.Errorf("expected truncation notice without --full:\n%s", out)
	}

	buf.Reset()
	if err := runShow([]string{"--full", "1"}, &buf, st); err != nil {
		t.Fatalf("runShow --full: %v", err)
	}
	out = buf.String()
	if strings.Contains(out, "truncated, use --full") {
		t.Errorf("did not expect a truncation notice with --full:\n%s", out)
	}
	if got := strings.Count(out, "line"); got < 100 {
		t.Errorf("--full should dump all 100 lines, only found %d occurrences of \"line\"", got)
	}
}

// --- stats ---

func TestStatsTotalsMatchFixture(t *testing.T) {
	st := newTestStore(t)
	c1, c2 := 0.01, 0.02
	seedRequest(t, st, func(r *store.Request) {
		r.InputTokens, r.OutputTokens = 100, 200
		r.CostUSD = &c1
	})
	seedRequest(t, st, func(r *store.Request) {
		r.InputTokens, r.OutputTokens = 200, 300
		r.CostUSD = &c2
	})

	var buf bytes.Buffer
	if err := runStats([]string{"--since", "24h"}, &buf, st); err != nil {
		t.Fatalf("runStats: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "calls:         2 (0 errors)") {
		t.Errorf("stats output missing expected call count:\n%s", out)
	}
	if !strings.Contains(out, "in=300") || !strings.Contains(out, "out=500") {
		t.Errorf("stats output missing expected token totals:\n%s", out)
	}
	if !strings.Contains(out, "$0.0300") {
		t.Errorf("stats output missing expected cost total:\n%s", out)
	}
}

// TestStatsMixedPricingNamesTheUnpricedCount is the bead's core rule: a total
// over calls that includes unpriced ones says how many, so a partial sum is
// never read as the whole bill.
func TestStatsMixedPricingNamesTheUnpricedCount(t *testing.T) {
	st := newTestStore(t)
	priced := 0.01
	seedRequest(t, st, func(r *store.Request) { r.CostUSD = &priced })
	seedRequest(t, st, func(r *store.Request) { r.CostUSD = nil })

	var buf bytes.Buffer
	if err := runStats([]string{"--since", "24h"}, &buf, st); err != nil {
		t.Fatalf("runStats: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "$0.0100 + 1 unpriced") {
		t.Errorf("stats output does not name the unpriced call:\n%s", out)
	}
}

func TestStatsAllUnpricedShowsNoPartialDollarTotal(t *testing.T) {
	st := newTestStore(t)
	seedRequest(t, st, func(r *store.Request) { r.CostUSD = nil })

	var buf bytes.Buffer
	if err := runStats([]string{"--since", "24h"}, &buf, st); err != nil {
		t.Fatalf("runStats: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "— + 1 unpriced") {
		t.Errorf("stats output should show no dollar total at all:\n%s", out)
	}
	if strings.Contains(out, "$0.0000") {
		t.Errorf("stats claimed a $0 total for calls it never priced:\n%s", out)
	}
}

func TestStatsByDayBreakdown(t *testing.T) {
	st := newTestStore(t)
	seedRequest(t, st, nil)

	var buf bytes.Buffer
	if err := runStats([]string{"--since", "24h", "--by", "day"}, &buf, st); err != nil {
		t.Fatalf("runStats --by day: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "day split:") || !strings.Contains(out, "COST") {
		t.Errorf("runStats --by day did not render a day breakdown:\n%s", out)
	}
}

func TestStatsRejectsUnknownByValue(t *testing.T) {
	st := newTestStore(t)
	var buf bytes.Buffer
	if err := runStats([]string{"--by", "hour"}, &buf, st); err == nil {
		t.Fatal("runStats --by hour should be rejected")
	}
}

// TestLSCostColumnNeverClaimsZero pins the presentation rule for ls: an
// unpriced call renders "—" plus the reason, never "$0.00".
func TestLSCostColumnNeverClaimsZero(t *testing.T) {
	st := newTestStore(t)
	unpriced := "unpriced"
	seedRequest(t, st, func(r *store.Request) { r.CostUSD = nil; r.CostSource = &unpriced })
	seedRequest(t, st, nil) // priced

	var buf bytes.Buffer
	if err := runLS(nil, &buf, st); err != nil {
		t.Fatalf("runLS: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "$0.00") {
		t.Errorf("ls rendered a $0.00 cost for an unpriced call:\n%s", out)
	}
	if !strings.Contains(out, "(unpriced)") {
		t.Errorf("ls did not name the cost source:\n%s", out)
	}
}

// --- sessions ---

// seedSession runs three calls with the same opening prompt through the real
// resolver, the way the consumer does, and returns the session id they share
// — plus the three stored requests.
func seedSession(t *testing.T, st *store.Store) (string, []*store.Request) {
	t.Helper()
	res := session.New(st, 30)
	ctx := context.Background()
	const prefix = "aaaaaaaaaaaaaaa1"

	var sid string
	var reqs []*store.Request
	for i := 0; i < 3; i++ {
		at := time.Now().Add(-time.Minute).Add(time.Duration(i) * time.Second)
		sid = res.Resolve(parse.Meta{PrefixHash: prefix}, at)
		s := sid
		r := seedRequest(t, st, func(r *store.Request) {
			r.StartedAt = at
			r.PrefixHash = prefix
			r.SessionID = &s
		})
		if err := res.RecordCall(ctx, sid, r, 0); err != nil {
			t.Fatalf("RecordCall: %v", err)
		}
		reqs = append(reqs, r)
	}
	return sid, reqs
}

// TestSessionsListsTurnsAndTotals is the bead's CLI integration case: the
// three calls are one session with three turns and their summed totals.
func TestSessionsListsTurnsAndTotals(t *testing.T) {
	st := newTestStore(t)
	sid, _ := seedSession(t, st)

	sessions, err := st.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].RequestCount != 3 {
		t.Fatalf("sessions = %+v, want exactly one with 3 turns", sessions)
	}

	var buf bytes.Buffer
	if err := runSessions(nil, &buf, st); err != nil {
		t.Fatalf("runSessions: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "TURNS") || !strings.Contains(out, "COST") {
		t.Errorf("sessions output missing its header columns:\n%s", out)
	}
	rows := dataLines(out)
	if len(rows) != 1 {
		t.Fatalf("got %d session rows, want 1:\n%s", len(rows), out)
	}
	if !strings.Contains(rows[0], sid) {
		t.Errorf("session row does not name %q:\n%s", sid, rows[0])
	}
	// seedRequest makes each call 100 in / 200 out at $0.01.
	if !strings.Contains(rows[0], "900") {
		t.Errorf("session row does not show the 900-token total:\n%s", rows[0])
	}
	if !strings.Contains(rows[0], "$0.0300") {
		t.Errorf("session row does not show the $0.03 total:\n%s", rows[0])
	}
}

// TestLSSessionFilterReturnsExactlyThatSession covers `lens ls --session`.
func TestLSSessionFilterReturnsExactlyThatSession(t *testing.T) {
	st := newTestStore(t)
	sid, reqs := seedSession(t, st)
	seedRequest(t, st, nil) // a call in no session at all

	var buf bytes.Buffer
	if err := runLS([]string{"--session", sid}, &buf, st); err != nil {
		t.Fatalf("runLS --session: %v", err)
	}
	if got := len(dataLines(buf.String())); got != len(reqs) {
		t.Fatalf("got %d rows, want %d:\n%s", got, len(reqs), buf.String())
	}

	buf.Reset()
	if err := runLS([]string{"--session", "s_1_deadbeef"}, &buf, st); err != nil {
		t.Fatalf("runLS --session (unknown): %v", err)
	}
	if got := len(dataLines(buf.String())); got != 0 {
		t.Errorf("got %d rows for an unknown session, want 0:\n%s", got, buf.String())
	}
}

// TestShowPrintsTheSessionHeaderForASessionId covers `lens show <session>`:
// the session lookup is tried first, and a numeric argument falls through to
// the request it names (TestShowIncludesWarningsAndBodies covers that half).
func TestShowPrintsTheSessionHeaderForASessionId(t *testing.T) {
	st := newTestStore(t)
	sid, _ := seedSession(t, st)

	var buf bytes.Buffer
	if err := runShow([]string{sid}, &buf, st); err != nil {
		t.Fatalf("runShow %s: %v", sid, err)
	}
	out := buf.String()
	if !strings.Contains(out, "id:              "+sid) {
		t.Errorf("show output does not name the session:\n%s", out)
	}
	if !strings.Contains(out, "turns:           3") {
		t.Errorf("show output does not report the session's turns:\n%s", out)
	}
	if !strings.Contains(out, "$0.0300") {
		t.Errorf("show output does not report the session's cost:\n%s", out)
	}
	if !strings.Contains(out, "lens ls --session "+sid) {
		t.Errorf("show output does not point at the session's calls:\n%s", out)
	}
}

// TestStatsBySessionRanksByCost pins the --by session split and its
// mixed-pricing labelling: a session whose total covers only some of its
// calls says how many it does not.
func TestStatsBySessionRanksByCost(t *testing.T) {
	st := newTestStore(t)
	sid, _ := seedSession(t, st)
	// A second session whose unpriced call is worth more than the first's
	// total; a bare "$" would claim the ranking covered it.
	unpriced := seedRequest(t, st, func(r *store.Request) {
		r.CostUSD = nil
		r.PrefixHash = "bbbbbbbbbbbbbbb2"
	})
	if err := st.UpsertSession(context.Background(), &store.Session{
		ID: "s_2_bbbbbbbb", FirstSeen: unpriced.StartedAt, LastSeen: unpriced.StartedAt, RequestCount: 1,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	var buf bytes.Buffer
	if err := runStats([]string{"--since", "24h", "--by", "session"}, &buf, st); err != nil {
		t.Fatalf("runStats --by session: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "session split:") || !strings.Contains(out, "SESSION") {
		t.Fatalf("runStats --by session did not render a session breakdown:\n%s", out)
	}
	if !strings.Contains(out, sid) {
		t.Errorf("session split does not list %q:\n%s", sid, out)
	}
	if !strings.Contains(out, "1 unpriced") {
		t.Errorf("session split does not name the unpriced call:\n%s", out)
	}
}

// --- prices ---

func TestPricesSetWritesTheFileAndPrintsTheUnit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")

	var buf bytes.Buffer
	if err := runPrices([]string{
		"--set", "deepseek-flash.input=0.28",
		"--set", "deepseek-flash.output=0.42",
	}, &buf, path); err != nil {
		t.Fatalf("runPrices --set: %v", err)
	}
	// A bare "0.28" beside a model name is ambiguous between per-token and
	// per-million-token, so the unit has to be in the header.
	if !strings.Contains(buf.String(), "per 1,000,000 tokens") {
		t.Errorf("price table header does not name the unit:\n%s", buf.String())
	}

	tbl, err := pricing.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := tbl["deepseek-flash"]
	if r.Input == nil || *r.Input != 0.28 || r.Output == nil || *r.Output != 0.42 {
		t.Errorf("flash rates = %+v, want input 0.28 and output 0.42", r)
	}
	if r.Source() != pricing.SourceConfigured {
		t.Errorf("source = %q, want %q", r.Source(), pricing.SourceConfigured)
	}
	// The shipped models survive an edit to one of them.
	if _, ok := tbl["deepseek-v4-pro"]; !ok {
		t.Error("deepseek-v4-pro vanished from the table after editing flash")
	}
}

func TestPricesUnsetRevertsToUnpriced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	var buf bytes.Buffer

	if err := runPrices([]string{"--set", "deepseek-flash.input=0.28"}, &buf, path); err != nil {
		t.Fatalf("runPrices --set: %v", err)
	}
	buf.Reset()
	if err := runPrices([]string{"--unset", "deepseek-flash"}, &buf, path); err != nil {
		t.Fatalf("runPrices --unset: %v", err)
	}

	tbl, err := pricing.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := tbl["deepseek-flash"].Source(); got != pricing.SourceUnpriced {
		t.Errorf("source after --unset = %q, want %q", got, pricing.SourceUnpriced)
	}
	if !strings.Contains(buf.String(), "unpriced") {
		t.Errorf("printed table does not show the model as unpriced:\n%s", buf.String())
	}
}

func TestPricesRejectsBadRateWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	for _, arg := range []string{
		"deepseek-flash.input=-1",     // negative
		"deepseek-flash.thinking=0.1", // unknown field
		"deepseek-flash",              // no field
		"deepseek-flash.input=abc",    // not a number
	} {
		var buf bytes.Buffer
		if err := runPrices([]string{"--set", arg}, &buf, path); err == nil {
			t.Errorf("--set %q was accepted", arg)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a rejected --set still wrote %s (stat err = %v)", path, err)
	}
}

func TestPricesPrintsTheShippedTableWithNoFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml") // never written

	var buf bytes.Buffer
	if err := runPrices(nil, &buf, path); err != nil {
		t.Fatalf("runPrices: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "deepseek-v4-pro") || !strings.Contains(out, "deepseek-flash") {
		t.Errorf("shipped table missing a known model:\n%s", out)
	}
	if !strings.Contains(out, "unpriced") {
		t.Errorf("shipped table should report both models as unpriced:\n%s", out)
	}
}

// --- warnings ---

func TestWarningsGroupsByKindAndDetail(t *testing.T) {
	st := newTestStore(t)
	r1 := seedRequest(t, st, nil)
	r2 := seedRequest(t, st, nil)
	ctx := context.Background()
	if err := st.InsertWarnings(ctx, r1.ID, []store.Warning{
		{Kind: "dropped_param", Severity: "warn", Detail: "top_k dropped", CreatedAt: time.Now()},
		{Kind: "dropped_param", Severity: "warn", Detail: "top_p dropped", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("InsertWarnings r1: %v", err)
	}
	if err := st.InsertWarnings(ctx, r2.ID, []store.Warning{
		{Kind: "unsupported_block", Severity: "error", Detail: "document block", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("InsertWarnings r2: %v", err)
	}

	var buf bytes.Buffer
	if err := runWarnings(nil, &buf, st); err != nil {
		t.Fatalf("runWarnings: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "dropped_param") || !strings.Contains(out, "unsupported_block") {
		t.Fatalf("warnings output missing a kind:\n%s", out)
	}
	// dropped_param has count 2 on its row.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "dropped_param") && !strings.Contains(line, "2") {
			t.Errorf("dropped_param row missing count 2: %q", line)
		}
	}

	buf.Reset()
	if err := runWarnings([]string{"--detail"}, &buf, st); err != nil {
		t.Fatalf("runWarnings --detail: %v", err)
	}
	out = buf.String()
	if !strings.Contains(out, "top_k dropped") || !strings.Contains(out, "top_p dropped") || !strings.Contains(out, "document block") {
		t.Errorf("--detail output missing individual occurrences:\n%s", out)
	}
}

// --- export ---

func TestExportEmitsValidJSONL(t *testing.T) {
	st := newTestStore(t)
	seedRequest(t, st, nil)
	seedRequest(t, st, nil)

	var buf bytes.Buffer
	if err := runExport(nil, &buf, st); err != nil {
		t.Fatalf("runExport: %v", err)
	}

	dec := json.NewDecoder(&buf)
	var count int
	for dec.More() {
		var r store.Request
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("decode line %d: %v", count, err)
		}
		if r.ID == 0 {
			t.Errorf("decoded request has zero ID")
		}
		count++
	}
	if count != 2 {
		t.Fatalf("got %d JSONL objects, want 2", count)
	}
}

// --- doctor ---

func TestDoctorHealthyStorePasses(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath}, &buf); err != nil {
		t.Fatalf("runDoctor on a healthy store returned an error: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "PASS") {
		t.Errorf("doctor output missing PASS lines:\n%s", buf.String())
	}
}

func TestDoctorInjectedFailureExitsNonZero(t *testing.T) {
	// Make the db directory uncreatable: point it under a path component
	// that is a plain file, not a directory, so os.MkdirAll fails.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dbPath := filepath.Join(blocker, "sub", "lens.db")

	var buf bytes.Buffer
	err := runDoctor([]string{"--db-path", dbPath}, &buf)
	if err == nil {
		t.Fatal("expected a non-nil error when a check fails")
	}
	if !strings.Contains(buf.String(), "FAIL") {
		t.Errorf("doctor output missing a FAIL line:\n%s", buf.String())
	}
}

func TestDoctorReportsReplayPosture(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")

	var off bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath}, &off); err != nil {
		t.Fatalf("runDoctor: %v", err)
	}
	if !strings.Contains(off.String(), "off (default") {
		t.Errorf("doctor should report replay off by default:\n%s", off.String())
	}

	var on bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath, "--replay"}, &on); err != nil {
		t.Fatalf("runDoctor --replay: %v", err)
	}
	if !strings.Contains(on.String(), "on —") || !strings.Contains(on.String(), "Origin/Host allowlist") {
		t.Errorf("doctor should report replay on with the Origin/Host guard named:\n%s", on.String())
	}
}

// --- serve ---

func TestServeHelpListsFlags(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w

	err = Serve([]string{"--help"})

	w.Close()
	os.Stderr = orig
	var buf bytes.Buffer
	io.Copy(&buf, r)

	if err == nil {
		t.Fatal("Serve([--help]) should return an error (flag.ErrHelp)")
	}
	out := buf.String()
	for _, want := range []string{"-proxy-addr", "-dashboard-addr", "-db-path", "-capture", "-allow-remote", "-replay"} {
		if !strings.Contains(out, want) {
			t.Errorf("serve --help output missing %q; got:\n%s", want, out)
		}
	}
}

func TestServeBannerWarnsOnNoCapture(t *testing.T) {
	cfg := &config.Config{ProxyAddr: "127.0.0.1:8787", DashboardAddr: "127.0.0.1:8788", Capture: false}
	var buf bytes.Buffer
	printBanner(&buf, cfg)
	out := buf.String()
	if !strings.Contains(out, "export ANTHROPIC_BASE_URL=http://127.0.0.1:8787") {
		t.Errorf("banner missing the ANTHROPIC_BASE_URL line:\n%s", out)
	}
	if !strings.Contains(out, "WARNING") {
		t.Errorf("banner missing the capture-disabled warning:\n%s", out)
	}
}

func TestServeBannerSilentWhenCapturing(t *testing.T) {
	cfg := &config.Config{ProxyAddr: "127.0.0.1:8787", DashboardAddr: "127.0.0.1:8788", Capture: true}
	var buf bytes.Buffer
	printBanner(&buf, cfg)
	if strings.Contains(buf.String(), "WARNING") {
		t.Errorf("banner should not warn when capture is enabled:\n%s", buf.String())
	}
}

func TestTranslateNoCapture(t *testing.T) {
	got := translateNoCapture([]string{"--no-capture", "--proxy-addr", "x"})
	want := []string{"--capture=false", "--proxy-addr", "x"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- tail ---

func TestTailPlainOutputWhenNotTTY(t *testing.T) {
	st := newTestStore(t)
	seedRequest(t, st, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var buf bytes.Buffer // never a TTY
	if err := runTail(ctx, nil, &buf, st); err != nil {
		t.Fatalf("runTail: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("tail emitted an ANSI escape sequence to a non-TTY writer:\n%q", out)
	}
	if !strings.Contains(out, "deepseek-chat") {
		t.Errorf("tail did not print the seeded request:\n%s", out)
	}
}
