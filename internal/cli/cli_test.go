package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
	return seedSessionAt(t, st, "aaaaaaaaaaaaaaa1", time.Now().Add(-time.Minute))
}

// seedSessionAt is seedSession parameterized on the prefix hash (so multiple
// sessions in one test don't collide) and the base time of its first call —
// used by the retention-purge tests, which need one session entirely before
// a cutoff and another entirely after it.
func seedSessionAt(t *testing.T, st *store.Store, prefix string, base time.Time) (string, []*store.Request) {
	t.Helper()
	res := session.New(st, 30)
	ctx := context.Background()

	var sid string
	var reqs []*store.Request
	for i := 0; i < 3; i++ {
		at := base.Add(time.Duration(i) * time.Second)
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

	sessions, err := st.ListSessions(context.Background(), store.Filter{})
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

// TestPricesRejectsInjectedModelName covers br-GI-17-01: a model name
// containing a newline is the CLI's pre-existing injection hole ($'evil\nx.input=1'
// writes two parseable lines) and must now be refused by the shared
// pricing.CheckModel guard, with the file left unwritten.
func TestPricesRejectsInjectedModelName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	var buf bytes.Buffer
	err := runPrices([]string{"--set", "evil\nx.input=1"}, &buf, path)
	if err == nil {
		t.Fatal("--set with a newline in the model name was accepted")
	}
	if !strings.Contains(err.Error(), "invalid model name") {
		t.Errorf("error = %q, want it to name the shared check's rejection", err)
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

func TestDoctorReportsLiveStatsFromRunningServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"sink_accepted":7,"sink_dropped":2,"last_write_at":"2026-09-15T12:00:00Z","last_write_age_ms":1500}`)
	}))
	defer srv.Close()

	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath, "--dashboard-addr", srv.Listener.Addr().String()}, &buf); err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "sink accepted=7 dropped=2") {
		t.Errorf("doctor did not report the live sink counters:\n%s", out)
	}
	if !strings.Contains(out, "consumer last write") {
		t.Errorf("doctor did not report the consumer last-write age:\n%s", out)
	}
}

func TestDoctorWarnsWhenServerNotRunning(t *testing.T) {
	// Bind then close a loopback port so nothing is listening on it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath, "--dashboard-addr", addr}, &buf); err != nil {
		t.Fatalf("doctor must not fail when serve is down: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "live_stats") || !strings.Contains(buf.String(), "WARN") {
		t.Errorf("doctor should WARN that live stats are unavailable with no server:\n%s", buf.String())
	}
}

// --- doctor: provider_hooks ---

// writeJSON writes v as JSON to path, creating any parent directory needed.
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

const lensProxyAddr = "127.0.0.1:8787"
const lensURL = "http://" + lensProxyAddr

func lensCfg() *config.Config {
	return &config.Config{ProxyAddr: lensProxyAddr}
}

// TestProviderHooksSubstringClauseIgnoresOverlayMismatch is the regression
// case: a direct-DeepSeek user recognized by the guard's substring clause
// must PASS even though their overlay names a different URL than
// cfg.ProxyAddr — the old overlay-vs-ProxyAddr check warned here.
func TestProviderHooksSubstringClauseIgnoresOverlayMismatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{
		"env": map[string]string{"ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic"},
	})
	writeJSON(t, filepath.Join(dir, ".deepseek-env.json"), map[string]string{
		"ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic",
	})

	c := providerHookCheck(lensCfg())
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS: %s", c.Status, c.Detail)
	}
}

func TestProviderHooksSubstringClauseFiresEvenWithDifferentOverlay(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{
		"env": map[string]string{"ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic"},
	})
	writeJSON(t, filepath.Join(dir, ".deepseek-env.json"), map[string]string{
		"ANTHROPIC_BASE_URL": lensURL,
	})

	c := providerHookCheck(lensCfg())
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS: %s", c.Status, c.Detail)
	}
}

func TestProviderHooksEqualityClauseRecognizesLensRoute(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{
		"env": map[string]string{"ANTHROPIC_BASE_URL": lensURL},
	})
	writeJSON(t, filepath.Join(dir, ".deepseek-env.json"), map[string]string{
		"ANTHROPIC_BASE_URL": lensURL,
	})

	c := providerHookCheck(lensCfg())
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS: %s", c.Status, c.Detail)
	}
}

func TestProviderHooksMissingOverlayShortCircuitsToPass(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{
		"env": map[string]string{"ANTHROPIC_BASE_URL": lensURL},
	})
	// No .deepseek-env.json written.

	c := providerHookCheck(lensCfg())
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS: %s", c.Status, c.Detail)
	}
}

// TestProviderHooksWarnsOnTheGenuineBlindSpot is the one WARN case: the
// lens route, declared differently than what's actually effective. It also
// exercises the scheme normalization (cfg.ProxyAddr is bare host:port).
func TestProviderHooksWarnsOnTheGenuineBlindSpot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{
		"env": map[string]string{"ANTHROPIC_BASE_URL": lensURL},
	})
	writeJSON(t, filepath.Join(dir, ".deepseek-env.json"), map[string]string{
		"ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic",
	})

	c := providerHookCheck(lensCfg())
	if c.Status != statusWarn {
		t.Fatalf("status = %s, want WARN: %s", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, lensURL) || !strings.Contains(c.Detail, "https://api.deepseek.com/anthropic") {
		t.Errorf("detail names neither URL: %s", c.Detail)
	}
}

func TestProviderHooksNoConfigDirAtAllPasses(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "does-not-exist"))
	c := providerHookCheck(lensCfg())
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS: %s", c.Status, c.Detail)
	}
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath}, &buf); err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, buf.String())
	}
}

func TestProviderHooksNoSettingsFilePasses(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	c := providerHookCheck(lensCfg())
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS: %s", c.Status, c.Detail)
	}
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath}, &buf); err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, buf.String())
	}
}

// TestProviderHooksUnreadableSettingsPasses uses the same best-effort chmod
// internal/store uses (os.Chmod's write-only effect on Windows means this is
// real coverage on Unix and degrades to the equally-required "readable but
// nothing to check" path on Windows — either way the check must PASS).
func TestProviderHooksUnreadableSettingsPasses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	settingsPath := filepath.Join(dir, "settings.json")
	writeJSON(t, settingsPath, map[string]any{
		"env": map[string]string{"ANTHROPIC_BASE_URL": lensURL},
	})
	_ = os.Chmod(settingsPath, 0o000)
	t.Cleanup(func() { os.Chmod(settingsPath, 0o644) })

	c := providerHookCheck(lensCfg())
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS: %s", c.Status, c.Detail)
	}
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath}, &buf); err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, buf.String())
	}
}

func TestProviderHooksSettingsIsDirectoryPasses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.Mkdir(filepath.Join(dir, "settings.json"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	c := providerHookCheck(lensCfg())
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS: %s", c.Status, c.Detail)
	}
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath}, &buf); err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, buf.String())
	}
}

func TestProviderHooksSettingsWithNoEnvBlockPasses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{"model": "x"})

	c := providerHookCheck(lensCfg())
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS: %s", c.Status, c.Detail)
	}
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath}, &buf); err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, buf.String())
	}
}

func TestProviderHooksEnvBlockWithNoBaseURLPasses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{
		"env": map[string]string{"SOME_OTHER_VAR": "x"},
	})

	c := providerHookCheck(lensCfg())
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS: %s", c.Status, c.Detail)
	}
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath}, &buf); err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, buf.String())
	}
}

func TestProviderHooksMalformedOverlayJSONPasses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{
		"env": map[string]string{"ANTHROPIC_BASE_URL": lensURL},
	})
	if err := os.WriteFile(filepath.Join(dir, ".deepseek-env.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	c := providerHookCheck(lensCfg())
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS: %s", c.Status, c.Detail)
	}
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath}, &buf); err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, buf.String())
	}
}

// TestProviderHooksUnreadableOverlayPasses: see
// TestProviderHooksUnreadableSettingsPasses for why the overlay is written
// with a value that still PASSes if chmod's write-only effect on Windows
// lets the read through anyway.
func TestProviderHooksUnreadableOverlayPasses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{
		"env": map[string]string{"ANTHROPIC_BASE_URL": lensURL},
	})
	overlayPath := filepath.Join(dir, ".deepseek-env.json")
	writeJSON(t, overlayPath, map[string]string{"ANTHROPIC_BASE_URL": lensURL})
	_ = os.Chmod(overlayPath, 0o000)
	t.Cleanup(func() { os.Chmod(overlayPath, 0o644) })

	c := providerHookCheck(lensCfg())
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS: %s", c.Status, c.Detail)
	}
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath}, &buf); err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, buf.String())
	}
}

func TestProviderHooksSectionedOverlayShapePasses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{
		"env": map[string]string{"ANTHROPIC_BASE_URL": lensURL},
	})
	writeJSON(t, filepath.Join(dir, ".deepseek-env.json"), map[string]any{
		"env":      map[string]string{"ANTHROPIC_BASE_URL": lensURL},
		"settings": map[string]string{"apiKeyHelper": "x"},
	})

	c := providerHookCheck(lensCfg())
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS: %s", c.Status, c.Detail)
	}
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath}, &buf); err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, buf.String())
	}
}

// TestProviderHooksResolverPrecedence pins claudeConfigDir's precedence:
// CLAUDE_CONFIG_DIR, then USERPROFILE, then HOME. A test that isolated only
// via HOME would leak on Windows, where USERPROFILE is set for real and
// outranks it.
func TestProviderHooksResolverPrecedence(t *testing.T) {
	writeLensSettings := func(dir string) {
		writeJSON(t, filepath.Join(dir, ".claude", "settings.json"), map[string]any{
			"env": map[string]string{"ANTHROPIC_BASE_URL": lensURL},
		})
	}

	t.Run("CLAUDE_CONFIG_DIR wins when set", func(t *testing.T) {
		want := t.TempDir()
		writeJSON(t, filepath.Join(want, "settings.json"), map[string]any{
			"env": map[string]string{"ANTHROPIC_BASE_URL": lensURL},
		})
		decoy := t.TempDir()
		writeLensSettings(decoy)

		t.Setenv("CLAUDE_CONFIG_DIR", want)
		t.Setenv("USERPROFILE", decoy)
		t.Setenv("HOME", decoy)

		if got := claudeConfigDir(); got != want {
			t.Fatalf("claudeConfigDir() = %q, want %q", got, want)
		}
	})

	t.Run("USERPROFILE used when CLAUDE_CONFIG_DIR unset", func(t *testing.T) {
		up := t.TempDir()
		home := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		t.Setenv("USERPROFILE", up)
		t.Setenv("HOME", home)

		want := filepath.Join(up, ".claude")
		if got := claudeConfigDir(); got != want {
			t.Fatalf("claudeConfigDir() = %q, want %q", got, want)
		}
	})

	t.Run("HOME used when CLAUDE_CONFIG_DIR and USERPROFILE unset", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		t.Setenv("USERPROFILE", "")
		t.Setenv("HOME", home)

		want := filepath.Join(home, ".claude")
		if got := claudeConfigDir(); got != want {
			t.Fatalf("claudeConfigDir() = %q, want %q", got, want)
		}
	})
}

// TestDoctorReportsProviderHooksRow proves runDoctor actually wires the
// check in, not just that providerHookCheck works standalone.
func TestDoctorReportsProviderHooksRow(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath}, &buf); err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "provider_hooks") {
		t.Errorf("doctor output missing provider_hooks row:\n%s", buf.String())
	}
}

// peakCheck finds runChecks' peak_calendar row, reporting whether it was
// produced at all — the malformed-date case deliberately appends none.
func peakCheck(t *testing.T, cfg *config.Config) (doctorCheck, bool) {
	t.Helper()
	for _, c := range runChecks(cfg) {
		if c.Name == "peak_calendar" {
			return c, true
		}
	}
	return doctorCheck{}, false
}

// doctorCfg is a config runChecks can be handed directly: the defaults with a
// throwaway database, so the checks that need a real store have one.
func doctorCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DBPath = filepath.Join(t.TempDir(), "lens.db")
	return cfg
}

func TestDoctorPeakCalendarPassesOnACoveredYear(t *testing.T) {
	cfg := doctorCfg(t)
	year := time.Now().Year()
	cfg.OffPeakDates = fmt.Sprintf("%d-10-01..%d-10-07", year, year)
	cfg.WorkDates = ""

	c, ok := peakCheck(t, cfg)
	if !ok {
		t.Fatal("no peak_calendar row in runChecks' output")
	}
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS for a set naming %d: %s", c.Status, year, c.Detail)
	}
}

// TestDoctorPeakCalendarWarnsOnAnUncoveredYear is the case the check exists
// for: the shipped set is a dated fact and 2027 is not in it.
func TestDoctorPeakCalendarWarnsOnAnUncoveredYear(t *testing.T) {
	cfg := doctorCfg(t)
	cfg.OffPeakDates = "1999-10-01..1999-10-07"
	cfg.WorkDates = ""

	c, ok := peakCheck(t, cfg)
	if !ok {
		t.Fatal("no peak_calendar row in runChecks' output")
	}
	if c.Status != statusWarn {
		t.Errorf("status = %s, want WARN when no set names %d: %s", c.Status, time.Now().Year(), c.Detail)
	}
	if !strings.Contains(c.Detail, strconv.Itoa(time.Now().Year())) {
		t.Errorf("detail = %q, want it to name the uncovered year", c.Detail)
	}
}

// TestDoctorPeakCalendarNeverFails is the D9 invariant: lens's correctness
// must not depend on a date list the user has not updated yet, and runDoctor
// turns a FAIL into a non-zero exit. An uncovered year therefore has to leave
// the exit code alone while still being visible in the output.
func TestDoctorPeakCalendarNeverFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	err := runDoctor([]string{"--db-path", dbPath, "--off-peak-dates", "1999-10-01..1999-10-07", "--work-dates", ""}, &buf)
	if err != nil {
		t.Fatalf("runDoctor = %v, want nil for an uncovered year — a WARN must not affect the exit\n%s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "peak_calendar") || !strings.Contains(out, "WARN") {
		t.Errorf("output = missing the peak_calendar WARN row:\n%s", out)
	}
}

// TestDoctorPeakCalendarSkipsMalformedDates: runChecks short-circuits on
// nothing, so a malformed date reaches the coverage check having already
// FAILed config_valid. The check must add no second failure — it is absent
// from the output entirely.
func TestDoctorPeakCalendarSkipsMalformedDates(t *testing.T) {
	cfg := doctorCfg(t)
	cfg.OffPeakDates = "2026-13-45"

	var fails []string
	for _, c := range runChecks(cfg) {
		if c.Name == "peak_calendar" {
			t.Errorf("peak_calendar row present for malformed dates: %+v", c)
		}
		if c.Status == statusFail {
			fails = append(fails, c.Name)
		}
	}
	if len(fails) != 1 || fails[0] != "config_valid" {
		t.Errorf("FAIL rows = %v, want exactly [config_valid] — the coverage check is incapable of FAIL", fails)
	}
}

// TestDoctorPrintsTheCalendar mirrors TestDoctorReportsRetentionDays: the
// values are asserted at a non-default setting, so a hardcoded row fails.
func TestDoctorPrintsTheCalendar(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"--db-path", dbPath,
		"--off-peak-dates", "2031-01-01..2031-01-03", "--work-dates", "2031-01-04"}, &buf); err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, buf.String())
	}
	out := buf.String()
	for _, want := range []string{"off_peak_dates", "2031-01-01..2031-01-03", "work_dates", "2031-01-04"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
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

func TestTailFiltersBySession(t *testing.T) {
	st := newTestStore(t)
	seedRequest(t, st, func(r *store.Request) {
		sid := "sess-a"
		r.SessionID = &sid
		r.ModelResolved = "deepseek-in-session"
	})
	seedRequest(t, st, func(r *store.Request) {
		sid := "sess-b"
		r.SessionID = &sid
		r.ModelResolved = "deepseek-other-session"
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var buf bytes.Buffer
	if err := runTail(ctx, []string{"--session", "sess-a"}, &buf, st); err != nil {
		t.Fatalf("runTail: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "deepseek-in-session") {
		t.Errorf("tail --session sess-a missing the matching request:\n%s", out)
	}
	if strings.Contains(out, "deepseek-other-session") {
		t.Errorf("tail --session sess-a leaked another session's request:\n%s", out)
	}
}

func TestTailFiltersByWarn(t *testing.T) {
	st := newTestStore(t)
	warned := seedRequest(t, st, func(r *store.Request) { r.ModelResolved = "deepseek-warned" })
	seedRequest(t, st, func(r *store.Request) { r.ModelResolved = "deepseek-unwarned" })
	if err := st.InsertWarnings(context.Background(), warned.ID, []store.Warning{
		{Kind: "k", Severity: "warn", Detail: "m", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var buf bytes.Buffer
	if err := runTail(ctx, []string{"--warn"}, &buf, st); err != nil {
		t.Fatalf("runTail: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "deepseek-warned") {
		t.Errorf("tail --warn missing the warned request:\n%s", out)
	}
	if strings.Contains(out, "deepseek-unwarned") {
		t.Errorf("tail --warn leaked the unwarned request:\n%s", out)
	}
}

// TestServeRedactionCheckReportsALeak proves the boot self-test is actually
// wired and notifies: a stored request whose header JSON still carries a
// reachable x-api-key must produce a logged line naming that request. This
// is the only test that exercises the call site in Serve, which is what
// keeps RedactCheck from quietly becoming dead code again.
func TestServeRedactionCheckReportsALeak(t *testing.T) {
	st := newTestStore(t)
	sentinel := "TESTSENTINEL-do-not-leak-1234567890"
	leaked := seedRequest(t, st, func(r *store.Request) {
		r.ReqHeaders = `{"X-Api-Key":["` + sentinel + `"],"Content-Type":["application/json"]}`
	})

	var lines []string
	checkRedaction(context.Background(), st, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	if len(lines) != 1 {
		t.Fatalf("expected exactly one logged line, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], sentinel) {
		t.Errorf("log line does not name the leaked value: %q", lines[0])
	}
	if !strings.Contains(lines[0], fmt.Sprintf("request %d", leaked.ID)) {
		t.Errorf("log line does not name request %d: %q", leaked.ID, lines[0])
	}
}

// TestServeRedactionCheckSilentOnCleanStore is the negative half: a store
// whose headers are already redacted must log nothing, so the check cannot
// become noise that gets ignored.
func TestServeRedactionCheckSilentOnCleanStore(t *testing.T) {
	st := newTestStore(t)
	seedRequest(t, st, func(r *store.Request) {
		r.ReqHeaders = `{"X-Api-Key":["[redacted]"],"Content-Type":["application/json"]}`
	})

	var lines []string
	checkRedaction(context.Background(), st, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	if len(lines) != 0 {
		t.Errorf("expected no log output for an already-redacted store, got: %v", lines)
	}
}

// TestPurgeOnStartupDeletesPastCutoffAndReconciles is the bead's "retention
// end to end" integration case, driven directly against purgeOnStartup since
// Serve itself cannot be driven from a test (two real listeners, a blocking
// signal context). One session is entirely older than the cutoff and must be
// fully purged; a second, untouched session must survive with its totals
// unchanged — the assertion that would catch a purge that deletes rows
// without reconciling sessions correctly.
func TestPurgeOnStartupDeletesPastCutoffAndReconciles(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	oldSid, oldReqs := seedSessionAt(t, st, "aaaaaaaaaaaaaaa1", time.Now().Add(-10*24*time.Hour))
	newSid, _ := seedSessionAt(t, st, "bbbbbbbbbbbbbbb2", time.Now().Add(-time.Hour))

	var lines []string
	purgeOnStartup(ctx, st, 5, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	if len(lines) != 1 {
		t.Fatalf("expected exactly one logged line, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], fmt.Sprintf("%d", len(oldReqs))) {
		t.Errorf("logged line does not name the deleted count %d: %q", len(oldReqs), lines[0])
	}

	for _, r := range oldReqs {
		if _, err := st.GetRequest(ctx, r.ID); err == nil {
			t.Errorf("request %d from the old session should have been purged", r.ID)
		}
	}

	sessions, err := st.ListSessions(ctx, store.Filter{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != newSid {
		t.Fatalf("sessions after purge = %+v, want exactly the untouched session %q", sessions, newSid)
	}
	if sessions[0].RequestCount != 3 {
		t.Errorf("surviving session %q has RequestCount=%d, want 3 (unchanged)", oldSid, sessions[0].RequestCount)
	}
}

// TestPurgeOnStartupZeroRetentionDeletesNothing pins retention_days<=0 as
// "keep forever": no rows are removed and nothing is logged.
func TestPurgeOnStartupZeroRetentionDeletesNothing(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_, oldReqs := seedSessionAt(t, st, "ccccccccccccccc3", time.Now().Add(-365*24*time.Hour))

	var lines []string
	purgeOnStartup(ctx, st, 0, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	if len(lines) != 0 {
		t.Errorf("retention_days=0 logged output: %v", lines)
	}
	for _, r := range oldReqs {
		if _, err := st.GetRequest(ctx, r.ID); err != nil {
			t.Errorf("request %d should survive retention_days=0: %v", r.ID, err)
		}
	}
}

// TestDoctorReportsRetentionDays asserts the resolved value appears in
// doctor's effective-config table for a non-default setting, so a hardcoded
// output line would fail.
func TestDoctorReportsRetentionDays(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	var buf bytes.Buffer
	if err := runDoctor([]string{"-db-path", dbPath, "-retention-days", "17"}, &buf); err != nil {
		t.Fatalf("runDoctor: %v", err)
	}
	if !strings.Contains(buf.String(), "17") {
		t.Errorf("doctor output does not report retention_days=17:\n%s", buf.String())
	}
}
