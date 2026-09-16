package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// GI-15 (br-GI-15-03): the page headers and ?offset on the three list
// endpoints. Response bodies stay bare JSON arrays — the headers are what
// carry the pagination metadata, which is what keeps every existing consumer
// working unchanged.

// listRoutes are the three paginated endpoints, so a header or parameter
// rule is asserted on all of them rather than on whichever one the author
// happened to be editing.
var listRoutes = []string{"/api/requests", "/api/warnings", "/api/sessions"}

func getOK(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200: %s", path, rr.Code, rr.Body.String())
	}
	return rr
}

func wantHeader(t *testing.T, rr *httptest.ResponseRecorder, name, want string) {
	t.Helper()
	if got := rr.Header().Get(name); got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func seedRequestsAt(t *testing.T, st *store.Store, n int) {
	t.Helper()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < n; i++ {
		offset := time.Duration(i) * time.Second
		seedRequest(t, st, func(r *store.Request) { r.StartedAt = base.Add(offset) })
	}
}

func TestPageHeadersPresentOnEveryListRoute(t *testing.T) {
	st := newTestStore(t)
	seedRequestsAt(t, st, 5)
	handler, _, _, _ := newTestAPI(t, st)

	for _, path := range listRoutes {
		rr := getOK(t, handler, path)
		for _, name := range []string{"X-Total-Count", "X-Limit", "X-Offset"} {
			if rr.Header().Get(name) == "" {
				t.Errorf("GET %s: %s header is missing", path, name)
			}
		}
	}
}

// TestPageHeaderLimitIsEffective is the regression guard for the X-Limit: 0
// bug. An absent ?limit parses to 0, which the store clamps to DefaultLimit,
// so the header must report the *applied* page size. A header-driven consumer
// computing nextOffset = offset + X-Limit would otherwise be stuck on page 1
// forever, with no error to explain why.
func TestPageHeaderLimitIsEffective(t *testing.T) {
	st := newTestStore(t)
	seedRequestsAt(t, st, 5)
	handler, _, _, _ := newTestAPI(t, st)

	for _, path := range listRoutes {
		rr := getOK(t, handler, path)
		wantHeader(t, rr, "X-Limit", strconv.Itoa(store.DefaultLimit))

		rr = getOK(t, handler, path+"?limit=25")
		wantHeader(t, rr, "X-Limit", "25")
	}
}

func TestListRequestsOffsetWindow(t *testing.T) {
	st := newTestStore(t)
	seedRequestsAt(t, st, 10)
	handler, _, _, _ := newTestAPI(t, st)

	page1 := decodeJSON[[]*store.Request](t, getOK(t, handler, "/api/requests?limit=5&offset=0").Body)
	page2 := decodeJSON[[]*store.Request](t, getOK(t, handler, "/api/requests?limit=5&offset=5").Body)
	if len(page1) != 5 || len(page2) != 5 {
		t.Fatalf("page sizes: got %d and %d, want 5 and 5", len(page1), len(page2))
	}

	onPage1 := map[int64]bool{}
	for _, r := range page1 {
		onPage1[r.ID] = true
	}
	for _, r := range page2 {
		if onPage1[r.ID] {
			t.Errorf("request %d appears on both offset=0 and offset=5 — the windows are not disjoint", r.ID)
		}
	}
}

func TestPageHeaderOffset(t *testing.T) {
	st := newTestStore(t)
	seedRequestsAt(t, st, 10)
	handler, _, _, _ := newTestAPI(t, st)

	wantHeader(t, getOK(t, handler, "/api/requests?limit=5"), "X-Offset", "0")
	wantHeader(t, getOK(t, handler, "/api/requests?limit=5&offset=7"), "X-Offset", "7")
}

// TestPageHeaderTotalCountIsWindowIndependent: the total describes the whole
// filtered set, so the page window must not move it. X-Total-Count shrinking
// to the page size is the failure that would make Next/Prev meaningless.
func TestPageHeaderTotalCountIsWindowIndependent(t *testing.T) {
	st := newTestStore(t)
	seedRequestsAt(t, st, 10)
	handler, _, _, _ := newTestAPI(t, st)

	wantHeader(t, getOK(t, handler, "/api/requests?limit=1"), "X-Total-Count", "10")
	wantHeader(t, getOK(t, handler, "/api/requests?limit=1000"), "X-Total-Count", "10")
	wantHeader(t, getOK(t, handler, "/api/requests?offset=50"), "X-Total-Count", "10")
}

func TestPageHeaderTotalCountHonorsPredicate(t *testing.T) {
	st := newTestStore(t)
	seedRequestsAt(t, st, 10)
	seedRequest(t, st, func(r *store.Request) {
		r.ModelRequested, r.ModelResolved = "deepseek-reasoner", "deepseek-reasoner"
	})
	handler, _, _, _ := newTestAPI(t, st)

	rr := getOK(t, handler, "/api/requests?model=deepseek-reasoner")
	wantHeader(t, rr, "X-Total-Count", "1")
	got := decodeJSON[[]*store.Request](t, rr.Body)
	if len(got) != 1 {
		t.Errorf("got %d rows, want 1 — the total and the body must describe the same set", len(got))
	}
}

func TestWarningsTotalCountHonorsKind(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	req := seedRequest(t, st, nil)
	warnings := []store.Warning{
		{Kind: "alpha", Severity: "warn", Detail: "a", CreatedAt: time.Now().Add(-3 * time.Second)},
		{Kind: "alpha", Severity: "warn", Detail: "b", CreatedAt: time.Now().Add(-2 * time.Second)},
		{Kind: "alpha", Severity: "error", Detail: "c", CreatedAt: time.Now().Add(-time.Second)},
		{Kind: "beta", Severity: "warn", Detail: "d", CreatedAt: time.Now()},
	}
	if err := st.InsertWarnings(ctx, req.ID, warnings); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}
	handler, _, _, _ := newTestAPI(t, st)

	rr := getOK(t, handler, "/api/warnings?kind=alpha")
	wantHeader(t, rr, "X-Total-Count", "3")

	// The default limit is DefaultLimit, far above the seed, so the body is
	// the untruncated filtered set and the two numbers must agree.
	got := decodeJSON[[]*store.Warning](t, rr.Body)
	if len(got) != 3 {
		t.Errorf("got %d warnings of kind alpha, want 3", len(got))
	}
	for _, w := range got {
		if w.Kind != "alpha" {
			t.Errorf("kind filter returned a %q warning", w.Kind)
		}
	}
}

func TestNegativeOffsetRejected(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)

	for _, path := range listRoutes {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path+"?offset=-1", nil))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("GET %s?offset=-1: status = %d, want 400", path, rr.Code)
		}
	}
}

// TestSessionsPageBodyStaysABareArray is the proof that the headers approach
// did not quietly become a response envelope: the paged body still decodes as
// []store.Session, with the metadata alongside it in the headers.
func TestSessionsPageBodyStaysABareArray(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if err := st.UpsertSession(ctx, &store.Session{
			ID:           fmt.Sprintf("s_%d", i),
			FirstSeen:    base.Add(time.Duration(i) * time.Second),
			LastSeen:     base.Add(time.Duration(i) * time.Second),
			RequestCount: 1,
		}); err != nil {
			t.Fatalf("UpsertSession: %v", err)
		}
	}
	handler, _, _, _ := newTestAPI(t, st)

	rr := getOK(t, handler, "/api/sessions?limit=2")
	wantHeader(t, rr, "X-Total-Count", "4")
	wantHeader(t, rr, "X-Limit", "2")
	wantHeader(t, rr, "X-Offset", "0")

	got := decodeJSON[[]*store.Session](t, rr.Body)
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2 — the paged body must still be a bare array", len(got))
	}
}
