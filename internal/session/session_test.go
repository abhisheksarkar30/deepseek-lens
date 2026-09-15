package session

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/parse"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// Two 16-hex prefix hashes of the shape parse.ExtractMeta produces, and the
// eight characters each contributes to a session id.
const (
	hashA = "aaaaaaaaaaaaaaa1"
	hashB = "bbbbbbbbbbbbbbb2"
)

// emptyPrefix8 is SHA-256 of the empty string, truncated — what an
// unparseable body's session id carries (see newID).
const emptyPrefix8 = "e3b0c442"

var idFormat = regexp.MustCompile(`^s_\d+_[0-9a-f]{8}$`)

// base is a fixed instant, so every gap in these tests is exact rather than
// wall-clock dependent.
var base = time.Unix(1700000000, 0).UTC()

func newResolver(t *testing.T, gapMinutes int) (*Resolver, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, gapMinutes), st
}

// callOpts describes one call: where it sits in time, what identifies it, and
// what it cost.
type callOpts struct {
	prefix   string
	header   string
	at       time.Time
	warnings int
	tweak    func(*store.Request)
}

// runCall performs the pairing the consumer performs — Resolve before the
// insert, RecordCall after the analyzers have run — and returns the session
// id the call landed in.
func runCall(t *testing.T, r *Resolver, o callOpts) string {
	t.Helper()
	meta := parse.Meta{PrefixHash: o.prefix, SessionHeader: o.header}
	sid := r.Resolve(meta, o.at)
	if sid == "" {
		t.Fatalf("Resolve returned no session id for prefix=%q header=%q", o.prefix, o.header)
	}
	req := &store.Request{
		StartedAt:      o.at,
		PrefixHash:     o.prefix,
		ModelResolved:  "deepseek-flash",
		InputTokens:    10,
		OutputTokens:   20,
		ModelRequested: "claude-sonnet-5",
	}
	if o.header != "" {
		h := o.header
		req.SessionHeader = &h
	}
	if o.tweak != nil {
		o.tweak(req)
	}
	if err := r.RecordCall(context.Background(), sid, req, o.warnings); err != nil {
		t.Fatalf("RecordCall: %v", err)
	}
	return sid
}

func getSession(t *testing.T, st *store.Store, id string) *store.Session {
	t.Helper()
	sess, err := st.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSession(%q): %v", id, err)
	}
	return sess
}

// suffix8 is the session id's 8-hex characters — the prefix hash's share of it.
func suffix8(id string) string { return id[len(id)-8:] }

// --- resolution rules ---

func TestSamePrefixWithinWindowIsOneSession(t *testing.T) {
	r, _ := newResolver(t, 30)

	first := runCall(t, r, callOpts{prefix: hashA, at: base})
	second := runCall(t, r, callOpts{prefix: hashA, at: base.Add(2 * time.Minute)})

	if first != second {
		t.Errorf("2 minutes apart, same prefix: ids %q and %q, want one session", first, second)
	}
}

func TestSamePrefixBeyondWindowIsANewSessionWithTheSamePrefixHash(t *testing.T) {
	r, _ := newResolver(t, 30)

	first := runCall(t, r, callOpts{prefix: hashA, at: base})
	second := runCall(t, r, callOpts{prefix: hashA, at: base.Add(45 * time.Minute)})

	if first == second {
		t.Fatalf("45 minutes apart (gap 30): one id %q, want two sessions", first)
	}
	if suffix8(first) != suffix8(second) {
		t.Errorf("prefix hashes differ: %q vs %q, want the same 8 hex characters", suffix8(first), suffix8(second))
	}
	if suffix8(first) != hashA[:8] {
		t.Errorf("id suffix = %q, want the prefix hash's first 8 (%q)", suffix8(first), hashA[:8])
	}
}

func TestDifferentPrefixIsANewSession(t *testing.T) {
	r, _ := newResolver(t, 30)

	first := runCall(t, r, callOpts{prefix: hashA, at: base})
	second := runCall(t, r, callOpts{prefix: hashB, at: base.Add(time.Minute)})

	if first == second {
		t.Fatalf("different prefixes: one id %q, want two sessions", first)
	}
	if suffix8(first) == suffix8(second) {
		t.Errorf("different prefixes produced the same id suffix %q", suffix8(first))
	}
}

func TestExplicitHeaderWinsOverPrefixAndGap(t *testing.T) {
	r, _ := newResolver(t, 30)

	first := runCall(t, r, callOpts{header: "run-42", prefix: hashA, at: base})
	second := runCall(t, r, callOpts{header: "run-42", prefix: hashB, at: base.Add(90 * time.Minute)})

	if first != "run-42" || second != "run-42" {
		t.Errorf("ids = %q and %q, want the header value verbatim on both", first, second)
	}
}

func TestHeaderOnOneCallOnlyDoesNotGroup(t *testing.T) {
	r, _ := newResolver(t, 30)

	withHeader := runCall(t, r, callOpts{header: "run-42", prefix: hashA, at: base})
	withoutHeader := runCall(t, r, callOpts{prefix: hashA, at: base.Add(time.Minute)})

	if withHeader == withoutHeader {
		t.Fatalf("a header-derived id and a prefix-derived one merged into %q", withHeader)
	}
	if !idFormat.MatchString(withoutHeader) {
		t.Errorf("header-less call got id %q, want a generated one", withoutHeader)
	}
}

func TestGapBoundaryIsInclusive(t *testing.T) {
	r, _ := newResolver(t, 30)
	first := runCall(t, r, callOpts{prefix: hashA, at: base})
	atBoundary := runCall(t, r, callOpts{prefix: hashA, at: base.Add(30 * time.Minute)})
	if first != atBoundary {
		t.Errorf("a gap exactly equal to the 30-minute window split the session: %q vs %q", first, atBoundary)
	}

	r2, _ := newResolver(t, 30)
	first2 := runCall(t, r2, callOpts{prefix: hashA, at: base})
	pastBoundary := runCall(t, r2, callOpts{prefix: hashA, at: base.Add(30*time.Minute + time.Nanosecond)})
	if first2 == pastBoundary {
		t.Errorf("a gap one nanosecond past the window stayed in session %q", first2)
	}
}

// TestOlderCallDoesNotStartANewSession pins the directional rule: an
// out-of-order call joins the session it belongs to. first_seen is the MIN of
// observed call times and last_seen the MAX, so an older call legitimately
// extends the session's start backwards while never dragging its most recent
// activity backwards — both bounds are order-independent.
func TestOlderCallDoesNotStartANewSession(t *testing.T) {
	r, st := newResolver(t, 30)

	first := runCall(t, r, callOpts{prefix: hashA, at: base})
	late := runCall(t, r, callOpts{prefix: hashA, at: base.Add(2 * time.Minute)})
	if first != late {
		t.Fatalf("setup: ids %q and %q should be one session", first, late)
	}

	older := runCall(t, r, callOpts{prefix: hashA, at: base.Add(-10 * time.Minute)})
	if older != first {
		t.Fatalf("a call 10 minutes before last_seen created session %q, want it in %q", older, first)
	}

	sess := getSession(t, st, first)
	if !sess.LastSeen.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("LastSeen = %v, want it still at the latest call (%v)", sess.LastSeen, base.Add(2*time.Minute))
	}
	if !sess.FirstSeen.Equal(base.Add(-10 * time.Minute)) {
		t.Errorf("FirstSeen = %v, want the earliest call (%v)", sess.FirstSeen, base.Add(-10*time.Minute))
	}

	// And the session stays open from there, not reopened fresh.
	next := runCall(t, r, callOpts{prefix: hashA, at: base.Add(3 * time.Minute)})
	if next != first {
		t.Errorf("the call after the out-of-order one landed in %q, want %q", next, first)
	}
}

func TestNullPrefixGroupsWithinFiveMinutesOnly(t *testing.T) {
	r, _ := newResolver(t, 30)
	first := runCall(t, r, callOpts{at: base})
	second := runCall(t, r, callOpts{at: base.Add(time.Minute)})
	if first != second {
		t.Errorf("two unparseable calls 1 minute apart: %q vs %q, want one session", first, second)
	}

	r2, _ := newResolver(t, 30)
	third := runCall(t, r2, callOpts{at: base})
	fourth := runCall(t, r2, callOpts{at: base.Add(10 * time.Minute)})
	if third == fourth {
		t.Errorf("two unparseable calls 10 minutes apart collapsed into %q", third)
	}

	// The 5-minute window is the bucket's, not the configured 30: a call 10
	// minutes out starts a new session even though it is well inside the gap.
	if !strings.HasSuffix(third, emptyPrefix8) || !strings.HasSuffix(fourth, emptyPrefix8) {
		t.Errorf("null-prefix ids %q and %q should carry the empty-prefix placeholder %q",
			third, fourth, emptyPrefix8)
	}
}

func TestIDFormat(t *testing.T) {
	r, _ := newResolver(t, 30)

	for _, id := range []string{
		runCall(t, r, callOpts{prefix: hashA, at: base}),
		runCall(t, r, callOpts{prefix: hashB, at: base}),
		runCall(t, r, callOpts{at: base}),
		runCall(t, r, callOpts{prefix: hashA[:3], at: base}), // a short, hand-built hash
	} {
		if !idFormat.MatchString(id) {
			t.Errorf("session id %q does not match %s", id, idFormat)
		}
	}
}

// --- aggregates ---

func TestAggregatesAccumulateAcrossCalls(t *testing.T) {
	r, st := newResolver(t, 30)

	var id string
	for i := 0; i < 3; i++ {
		id = runCall(t, r, callOpts{prefix: hashA, at: base.Add(time.Duration(i) * time.Minute)})
	}

	sess := getSession(t, st, id)
	if sess.RequestCount != 3 {
		t.Errorf("RequestCount = %d, want 3", sess.RequestCount)
	}
	if sess.TotalInputTokens != 30 || sess.TotalOutputTokens != 60 {
		t.Errorf("tokens = (%d, %d), want (30, 60)", sess.TotalInputTokens, sess.TotalOutputTokens)
	}
	if !sess.FirstSeen.Equal(base) || !sess.LastSeen.Equal(base.Add(2*time.Minute)) {
		t.Errorf("bounds = %v..%v, want %v..%v", sess.FirstSeen, sess.LastSeen, base, base.Add(2*time.Minute))
	}
	if sess.PrefixHash == nil || *sess.PrefixHash != hashA {
		t.Errorf("PrefixHash = %v, want %q", sess.PrefixHash, hashA)
	}
}

// TestAggregatesCountPricedAndUnpriced is br-GI-1-11's mixed-pricing rule at
// session granularity: the priced subtotal and the number of calls it does
// not cover are tracked separately, so a session total is never presented as
// the whole bill.
func TestAggregatesCountPricedAndUnpriced(t *testing.T) {
	r, st := newResolver(t, 30)
	price := func(v float64) func(*store.Request) {
		return func(req *store.Request) { req.CostUSD = &v }
	}

	id := runCall(t, r, callOpts{prefix: hashA, at: base, tweak: price(0.01)})
	id = runCall(t, r, callOpts{prefix: hashA, at: base.Add(time.Minute), tweak: price(0.02)})
	id = runCall(t, r, callOpts{prefix: hashA, at: base.Add(2 * time.Minute)}) // unpriced: CostUSD nil

	sess := getSession(t, st, id)
	if sess.PricedCount != 2 || sess.UnpricedCount != 1 {
		t.Errorf("priced/unpriced = %d/%d, want 2/1", sess.PricedCount, sess.UnpricedCount)
	}
	if sess.TotalCostUSD < 0.0299 || sess.TotalCostUSD > 0.0301 {
		t.Errorf("TotalCostUSD = %v, want 0.03 (the priced calls only)", sess.TotalCostUSD)
	}
	if sess.RequestCount != 3 {
		t.Errorf("RequestCount = %d, want 3", sess.RequestCount)
	}
}

func TestWarningCountAccumulates(t *testing.T) {
	r, st := newResolver(t, 30)

	var id string
	for i, n := range []int{2, 0, 3} {
		id = runCall(t, r, callOpts{prefix: hashA, at: base.Add(time.Duration(i) * time.Minute), warnings: n})
	}

	if got := getSession(t, st, id).WarningCount; got != 5 {
		t.Errorf("WarningCount = %d, want 5", got)
	}
}

func TestModelSetKeepsDistinctModelsOnly(t *testing.T) {
	r, st := newResolver(t, 30)
	model := func(m string) func(*store.Request) {
		return func(req *store.Request) { req.ModelResolved = m }
	}

	id := runCall(t, r, callOpts{prefix: hashA, at: base, tweak: model("deepseek-flash")})
	id = runCall(t, r, callOpts{prefix: hashA, at: base.Add(time.Minute), tweak: model("deepseek-flash")})
	id = runCall(t, r, callOpts{prefix: hashA, at: base.Add(2 * time.Minute), tweak: model("deepseek-v4-pro")})
	id = runCall(t, r, callOpts{prefix: hashA, at: base.Add(3 * time.Minute), tweak: model("")}) // unresolved

	if got := getSession(t, st, id).ModelSet; got != "deepseek-flash,deepseek-v4-pro" {
		t.Errorf("ModelSet = %q, want %q", got, "deepseek-flash,deepseek-v4-pro")
	}
}

// TestHeaderSessionIsNotVisibleToThePrefixLookup is the reason prefix_hash is
// nullable: a header-keyed session must never be reachable by rule 2, or the
// "header on one call, absent on the other" rule would quietly break one hop
// later.
func TestHeaderSessionIsNotVisibleToThePrefixLookup(t *testing.T) {
	r, st := newResolver(t, 30)

	runCall(t, r, callOpts{header: "run-42", prefix: hashA, at: base})
	other := runCall(t, r, callOpts{prefix: hashA, at: base.Add(time.Minute)})
	if other == "run-42" {
		t.Fatalf("the header-less call joined the header session %q", other)
	}

	plain := getSession(t, st, "run-42")
	if plain.PrefixHash != nil {
		t.Errorf("header session PrefixHash = %q, want NULL", *plain.PrefixHash)
	}
	if got := getSession(t, st, other); got.PrefixHash == nil || *got.PrefixHash != hashA {
		t.Errorf("prefix session PrefixHash = %v, want %q", got.PrefixHash, hashA)
	}

	if _, err := st.LatestSessionByPrefix(context.Background(), "run-42"); err != sql.ErrNoRows {
		t.Errorf("LatestSessionByPrefix(%q) = %v, want sql.ErrNoRows — it is an id, not a prefix", "run-42", err)
	}
}
