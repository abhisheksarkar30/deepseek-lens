package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// GI-15 (br-GI-15-01): offset/limit windows, the counts that must not drift
// from their lists, and the warnings summary that sees past the list cap.

// insertRequestAt inserts the fullRequest fixture at startedAt after letting
// opts tweak it, and returns the id the store assigned.
func insertRequestAt(t *testing.T, s *Store, ctx context.Context, startedAt time.Time, opts func(*Request)) int64 {
	t.Helper()
	r := fullRequest()
	r.StartedAt = startedAt
	if opts != nil {
		opts(r)
	}
	id, err := s.InsertRequest(ctx, r)
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	return id
}

// pageFixture10 seeds ten requests one second apart, so "page 2" is a
// well-defined thing to ask for.
func pageFixture10(t *testing.T, s *Store, ctx context.Context) {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		insertRequestAt(t, s, ctx, base.Add(time.Duration(i)*time.Second), nil)
	}
}

// TestListRequestsOffsetWindows asserts page 2 is *disjoint* from page 1 and
// that together they cover everything — not merely that each has 5 rows.
// Equal-length overlapping pages is the failure this guards, and a
// length-only assertion cannot see it.
func TestListRequestsOffsetWindows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	pageFixture10(t, s, ctx)

	page1, err := s.ListRequests(ctx, Filter{Limit: 5, Offset: 0})
	if err != nil {
		t.Fatalf("ListRequests{Limit:5,Offset:0}: %v", err)
	}
	page2, err := s.ListRequests(ctx, Filter{Limit: 5, Offset: 5})
	if err != nil {
		t.Fatalf("ListRequests{Limit:5,Offset:5}: %v", err)
	}
	if len(page1) != 5 || len(page2) != 5 {
		t.Fatalf("page sizes: got %d and %d, want 5 and 5", len(page1), len(page2))
	}

	seen := map[int64]bool{}
	for _, r := range page1 {
		seen[r.ID] = true
	}
	for _, r := range page2 {
		if seen[r.ID] {
			t.Errorf("request %d appears on both page 1 and page 2 — the pages are not disjoint", r.ID)
		}
		seen[r.ID] = true
	}
	if len(seen) != 10 {
		t.Errorf("the two pages cover %d distinct requests, want all 10", len(seen))
	}
}

func TestListRequestsOffsetEdges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	pageFixture10(t, s, ctx)

	// Last partial page: 10 rows at 4 per page means the third page holds 2.
	last, err := s.ListRequests(ctx, Filter{Limit: 4, Offset: 8})
	if err != nil {
		t.Fatalf("ListRequests{Limit:4,Offset:8}: %v", err)
	}
	if len(last) != 2 {
		t.Errorf("last partial page: got %d rows, want 2", len(last))
	}

	// An offset past the end is an empty page, not an error.
	past, err := s.ListRequests(ctx, Filter{Limit: 10, Offset: 100})
	if err != nil {
		t.Fatalf("ListRequests past the end: %v", err)
	}
	if len(past) != 0 {
		t.Errorf("offset past the end: got %d rows, want 0", len(past))
	}

	// A negative offset clamps to 0 rather than erroring: a caller that
	// computed its offset arithmetically must not be able to break the query.
	neg, err := s.ListRequests(ctx, Filter{Limit: 10, Offset: -5})
	if err != nil {
		t.Fatalf("ListRequests{Offset:-5}: %v", err)
	}
	zero, err := s.ListRequests(ctx, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRequests{Offset:0}: %v", err)
	}
	if len(neg) != len(zero) {
		t.Fatalf("negative offset returned %d rows, offset 0 returned %d", len(neg), len(zero))
	}
	for i := range neg {
		if neg[i].ID != zero[i].ID {
			t.Fatalf("negative offset differs from offset 0 at row %d: %d vs %d", i, neg[i].ID, zero[i].ID)
		}
	}
}

func TestCountRequestsIgnoresWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	pageFixture10(t, s, ctx)

	for _, f := range []Filter{
		{Limit: 1},
		{Limit: DefaultLimit},
		{Offset: 50},
		{Limit: 3, Offset: 4},
	} {
		n, err := s.CountRequests(ctx, f)
		if err != nil {
			t.Fatalf("CountRequests(%+v): %v", f, err)
		}
		if n != 10 {
			t.Errorf("CountRequests(%+v) = %d, want 10 — the page window must not shrink the total", f, n)
		}
	}
}

// TestCountRequestsHonorsPredicate is the drift guard: for every narrowing
// filter, the count must equal the length of the list that filter produces.
// A limit far above the seed keeps the list itself untruncated, so the two
// numbers answer the same question.
func TestCountRequestsHonorsPredicate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	chat := func(r *Request) {
		r.ModelRequested, r.ModelResolved = "deepseek-chat", "deepseek-chat"
		r.ErrorText = nil
	}
	reasoner := func(r *Request) {
		r.ModelRequested, r.ModelResolved = "deepseek-reasoner", "deepseek-reasoner"
		r.ErrorText = nil
	}
	errText := "upstream 502"

	insertRequestAt(t, s, ctx, base.Add(1*time.Second), chat)
	insertRequestAt(t, s, ctx, base.Add(2*time.Second), reasoner)
	insertRequestAt(t, s, ctx, base.Add(3*time.Second), reasoner)
	insertRequestAt(t, s, ctx, base.Add(4*time.Second), func(r *Request) {
		reasoner(r)
		r.ErrorText = &errText
	})
	warnedID := insertRequestAt(t, s, ctx, base.Add(5*time.Second), reasoner)
	if err := s.InsertWarnings(ctx, warnedID, []Warning{
		{Kind: "dropped_param", Severity: "warn", Detail: "top_k dropped", CreatedAt: base},
	}); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}

	for _, f := range []Filter{
		{},
		{Model: "deepseek-chat"},
		{Model: "deepseek-reasoner"},
		{OnlyErrors: true},
		{OnlyWarned: true},
		{Model: "deepseek-chat", OnlyErrors: true},
	} {
		f.Limit = 100
		got, err := s.ListRequests(ctx, f)
		if err != nil {
			t.Fatalf("ListRequests(%+v): %v", f, err)
		}
		n, err := s.CountRequests(ctx, f)
		if err != nil {
			t.Fatalf("CountRequests(%+v): %v", f, err)
		}
		if n != len(got) {
			t.Errorf("CountRequests(%+v) = %d but ListRequests returned %d — the count has drifted from the list", f, n, len(got))
		}
	}
}

func TestCountWarningsHonorsPredicate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	id := insertRequestAt(t, s, ctx, base, nil)

	seed := []Warning{
		{Kind: "dropped_param", Severity: "warn", Detail: "a", CreatedAt: base},
		{Kind: "dropped_param", Severity: "warn", Detail: "b", CreatedAt: base.Add(time.Second)},
		{Kind: "dropped_param", Severity: "error", Detail: "c", CreatedAt: base.Add(2 * time.Second)},
		{Kind: "unsupported_block", Severity: "error", Detail: "d", CreatedAt: base.Add(3 * time.Second)},
	}
	if err := s.InsertWarnings(ctx, id, seed); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}

	for _, f := range []Filter{
		{},
		{Kind: "dropped_param"},
		{Kind: "dropped_param", Severity: "error"},
		{Severity: "error"},
		{Since: base.Add(2500 * time.Millisecond)},
		{Kind: "nonexistent"},
	} {
		f.Limit = 100
		got, err := s.ListWarnings(ctx, f)
		if err != nil {
			t.Fatalf("ListWarnings(%+v): %v", f, err)
		}
		n, err := s.CountWarnings(ctx, f)
		if err != nil {
			t.Fatalf("CountWarnings(%+v): %v", f, err)
		}
		if n != len(got) {
			t.Errorf("CountWarnings(%+v) = %d but ListWarnings returned %d", f, n, len(got))
		}
	}
}

// TestListRequestsPagingIsTotal asserts the id DESC tiebreaker earns its
// place: every row here shares one started_at, which is what the consumer's
// batch insert produces in practice. Without a total order the LIMIT/OFFSET
// window can serve a row twice or skip it. With distinct timestamps the same
// assertions would pass against tiebreaker-less code, so the fixture is the
// test.
func TestListRequestsPagingIsTotal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	same := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	const total = 7
	for i := 0; i < total; i++ {
		insertRequestAt(t, s, ctx, same, nil)
	}

	seen := map[int64]int{}
	for offset := 0; offset < total; offset += 2 {
		page, err := s.ListRequests(ctx, Filter{Limit: 2, Offset: offset})
		if err != nil {
			t.Fatalf("ListRequests{Limit:2,Offset:%d}: %v", offset, err)
		}
		for _, r := range page {
			seen[r.ID]++
		}
	}
	if len(seen) != total {
		t.Errorf("paging over %d identical-timestamp rows yielded %d distinct rows — rows were skipped across a boundary", total, len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("request %d appeared on %d pages, want exactly 1", id, n)
		}
	}
}

// TestListWarningsPagingIsTotal is TestListRequestsPagingIsTotal's warning
// twin — same identical-timestamp fixture, over created_at.
func TestListWarningsPagingIsTotal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	id := insertRequestAt(t, s, ctx, base, nil)

	const total = 7
	warnings := make([]Warning, 0, total)
	for i := 0; i < total; i++ {
		warnings = append(warnings, Warning{Kind: "dropped_param", Severity: "warn", Detail: "d", CreatedAt: base})
	}
	if err := s.InsertWarnings(ctx, id, warnings); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}

	seen := map[int64]int{}
	for offset := 0; offset < total; offset += 2 {
		page, err := s.ListWarnings(ctx, Filter{Limit: 2, Offset: offset})
		if err != nil {
			t.Fatalf("ListWarnings{Limit:2,Offset:%d}: %v", offset, err)
		}
		for _, w := range page {
			seen[w.ID]++
		}
	}
	if len(seen) != total {
		t.Errorf("paging over %d identical-timestamp warnings yielded %d distinct rows", total, len(seen))
	}
	for wid, n := range seen {
		if n != 1 {
			t.Errorf("warning %d appeared on %d pages, want exactly 1", wid, n)
		}
	}
}

// TestWarningSummaryCountsPastTheListCap is the regression test for the
// undercount this story fixes: seed more warnings of one kind than
// ListWarnings would ever return, then assert the group's count is the true
// total rather than the cap. The list assertion at the end is what gives the
// test teeth — it proves the fixture really does exceed the cap.
func TestWarningSummaryCountsPastTheListCap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	id := insertRequestAt(t, s, ctx, base, nil)

	const total = DefaultLimit + 1
	warnings := make([]Warning, 0, total)
	for i := 0; i < total; i++ {
		warnings = append(warnings, Warning{
			Kind: "cache_control_ignored", Severity: "warn", Detail: "x",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	if err := s.InsertWarnings(ctx, id, warnings); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}

	groups, err := s.WarningSummary(ctx, Filter{})
	if err != nil {
		t.Fatalf("WarningSummary: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if groups[0].Count != total {
		t.Errorf("group count = %d, want the true total %d (the %d list cap is the bug this replaces)",
			groups[0].Count, total, DefaultLimit)
	}
	wantLastSeen := base.Add(time.Duration(total-1) * time.Second)
	if !groups[0].LastSeen.Equal(wantLastSeen) {
		t.Errorf("LastSeen = %v, want %v (the group's max created_at)", groups[0].LastSeen, wantLastSeen)
	}

	listed, err := s.ListWarnings(ctx, Filter{Kind: "cache_control_ignored"})
	if err != nil {
		t.Fatalf("ListWarnings: %v", err)
	}
	if len(listed) != DefaultLimit {
		t.Fatalf("ListWarnings returned %d rows, want the DefaultLimit cap %d — without that gap this test proves nothing",
			len(listed), DefaultLimit)
	}
}

func TestWarningSummaryOrdering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	id := insertRequestAt(t, s, ctx, base, nil)

	var warnings []Warning
	add := func(kind, severity string, n int) {
		for i := 0; i < n; i++ {
			warnings = append(warnings, Warning{Kind: kind, Severity: severity, Detail: "d", CreatedAt: base})
		}
	}
	add("zebra", "warn", 3) // highest count, so it sorts first
	add("alpha", "warn", 1) // then a three-way tie on count, broken by kind then severity
	add("beta", "error", 1)
	add("beta", "warn", 1)
	if err := s.InsertWarnings(ctx, id, warnings); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}

	got, err := s.WarningSummary(ctx, Filter{})
	if err != nil {
		t.Fatalf("WarningSummary: %v", err)
	}
	want := []WarningGroup{
		{Kind: "zebra", Severity: "warn", Count: 3},
		{Kind: "alpha", Severity: "warn", Count: 1},
		{Kind: "beta", Severity: "error", Count: 1},
		{Kind: "beta", Severity: "warn", Count: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d groups, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Kind != want[i].Kind || got[i].Severity != want[i].Severity || got[i].Count != want[i].Count {
			t.Errorf("group %d = %s/%s count %d, want %s/%s count %d",
				i, got[i].Kind, got[i].Severity, got[i].Count, want[i].Kind, want[i].Severity, want[i].Count)
		}
	}
}

// TestWarningSummaryHonorsPredicates checks the narrowing filters, and that
// the windowing ones are deliberately inert: a GROUP BY has to count the
// whole matching set, so Limit/Offset on a summary must change nothing.
func TestWarningSummaryHonorsPredicates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	id := insertRequestAt(t, s, ctx, base, nil)

	seed := []Warning{
		{Kind: "a", Severity: "warn", Detail: "d", CreatedAt: base},
		{Kind: "a", Severity: "error", Detail: "d", CreatedAt: base.Add(time.Second)},
		{Kind: "b", Severity: "error", Detail: "d", CreatedAt: base.Add(2 * time.Second)},
		{Kind: "c", Severity: "error", Detail: "d", CreatedAt: base.Add(3 * time.Second)},
	}
	if err := s.InsertWarnings(ctx, id, seed); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}

	byKind, err := s.WarningSummary(ctx, Filter{Kind: "a"})
	if err != nil {
		t.Fatalf("WarningSummary{Kind}: %v", err)
	}
	if len(byKind) != 2 {
		t.Errorf("Filter{Kind: a}: got %d groups, want 2 (a/warn and a/error)", len(byKind))
	}
	for _, g := range byKind {
		if g.Kind != "a" {
			t.Errorf("Filter{Kind: a} returned kind %q", g.Kind)
		}
	}

	bySev, err := s.WarningSummary(ctx, Filter{Severity: "error"})
	if err != nil {
		t.Fatalf("WarningSummary{Severity}: %v", err)
	}
	if len(bySev) != 3 {
		t.Errorf("Filter{Severity: error}: got %d groups, want 3 distinct kinds", len(bySev))
	}

	bySince, err := s.WarningSummary(ctx, Filter{Since: base.Add(2500 * time.Millisecond)})
	if err != nil {
		t.Fatalf("WarningSummary{Since}: %v", err)
	}
	if len(bySince) != 1 || bySince[0].Kind != "c" {
		t.Errorf("Filter{Since}: got %+v, want just kind c", bySince)
	}

	all, err := s.WarningSummary(ctx, Filter{})
	if err != nil {
		t.Fatalf("WarningSummary{}: %v", err)
	}
	byWindow, err := s.WarningSummary(ctx, Filter{Limit: 1, Offset: 5})
	if err != nil {
		t.Fatalf("WarningSummary{Limit,Offset}: %v", err)
	}
	if len(byWindow) != len(all) {
		t.Errorf("Filter{Limit:1,Offset:5} returned %d groups, unfiltered returned %d — a GROUP BY must ignore the page window",
			len(byWindow), len(all))
	}
}

// TestWarningGroupWireKeysAreCapitalized pins the wire contract the dashboard
// depends on. store.WarningGroup carries no json tags by convention, so the
// served keys are the Go field names and internal/web/app.js reads them
// capitalized. If someone adds tags later, this fails here rather than
// rendering undefined cells in the warnings tab.
func TestWarningGroupWireKeysAreCapitalized(t *testing.T) {
	b, err := json.Marshal(WarningGroup{
		Kind: "k", Severity: "s", Count: 2, LastSeen: time.Unix(0, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"Kind", "Severity", "Count", "LastSeen"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("marshaled WarningGroup has no %q key — got %s", key, b)
		}
	}
}
