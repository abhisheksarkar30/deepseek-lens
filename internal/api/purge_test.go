package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// postPurge issues a same-origin, loopback POST /api/purge with body as the
// raw JSON request body — see postPrices in prices_test.go for why every
// POST test needs an explicit loopback Host.
func postPurge(handler http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/purge", bytes.NewBufferString(body))
	req.Host = "localhost"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func getRetention(handler http.Handler) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/retention", nil))
	return rr
}

// seedOldRequest inserts a priced, positive-token request older than cutoff
// — eligible for PurgeOlderThan but not for PurgeUnpriced.
func seedOldRequest(t *testing.T, st *store.Store, age time.Duration) *store.Request {
	t.Helper()
	return seedRequest(t, st, func(r *store.Request) {
		r.StartedAt = time.Now().Add(-age)
	})
}

// seedPricedRequest inserts a request explicitly marked priced (cost_source
// = "configured"). seedRequest's own default leaves CostSource nil, which
// purgeUnpricedWhere's COALESCE(cost_source,'unpriced') folds into the
// unpriced bucket regardless of CostUSD — real inserts always populate
// CostSource, so a "priced, ineligible" fixture needs to say so explicitly
// or it silently matches the unpriced predicate too.
func seedPricedRequest(t *testing.T, st *store.Store) *store.Request {
	t.Helper()
	configured := "configured"
	return seedRequest(t, st, func(r *store.Request) {
		r.CostSource = &configured
	})
}

// seedUnpricedRequest inserts a request matching the D5 unpriced predicate
// (COALESCE(cost_source,'unpriced')='unpriced' AND positive tokens) — see
// internal/store's purgeUnpricedWhere.
func seedUnpricedRequest(t *testing.T, st *store.Store) *store.Request {
	t.Helper()
	unpriced := "unpriced"
	return seedRequest(t, st, func(r *store.Request) {
		r.CostUSD = nil
		r.CostSource = &unpriced
	})
}

func TestPreviewEqualsDeleteBothModes(t *testing.T) {
	t.Run("older_than", func(t *testing.T) {
		st := newTestStore(t)
		handler, _, _, _ := newTestAPI(t, st)
		handler.SetRetention(5, st)

		seedOldRequest(t, st, 10*24*time.Hour)
		seedOldRequest(t, st, 20*24*time.Hour)
		seedRequest(t, st, nil) // recent, ineligible

		preview := decodeJSON[retentionResponse](t, getRetention(handler).Body)
		if preview.EligibleRequests != 2 {
			t.Fatalf("eligible_requests = %d, want 2", preview.EligibleRequests)
		}

		rr := postPurge(handler, `{"mode":"older_than","days":5}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
		}
		got := decodeJSON[purgeResponse](t, rr.Body)
		if got.Deleted != int64(preview.EligibleRequests) {
			t.Errorf("deleted = %d, want the previewed eligible_requests = %d", got.Deleted, preview.EligibleRequests)
		}
	})

	t.Run("unpriced", func(t *testing.T) {
		st := newTestStore(t)
		handler, _, _, _ := newTestAPI(t, st)
		handler.SetRetention(0, st)

		seedUnpricedRequest(t, st)
		seedUnpricedRequest(t, st)
		seedPricedRequest(t, st) // priced, ineligible

		preview := decodeJSON[retentionResponse](t, getRetention(handler).Body)
		if preview.UnpricedRequests != 2 {
			t.Fatalf("unpriced_requests = %d, want 2", preview.UnpricedRequests)
		}

		rr := postPurge(handler, `{"mode":"unpriced"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
		}
		got := decodeJSON[purgeResponse](t, rr.Body)
		if got.Deleted != int64(preview.UnpricedRequests) {
			t.Errorf("deleted = %d, want the previewed unpriced_requests = %d", got.Deleted, preview.UnpricedRequests)
		}
	})
}

func TestGetRetentionIsARead(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetRetention(5, st)
	seedOldRequest(t, st, 10*24*time.Hour)
	seedRequest(t, st, nil)

	before, err := st.CountRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("CountRequests before: %v", err)
	}

	rr := getRetention(handler)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	after, err := st.CountRequests(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("CountRequests after: %v", err)
	}
	if before != after {
		t.Errorf("GET /api/retention changed the row count: %d -> %d", before, after)
	}
}

func TestGetRetentionDaysZero(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetRetention(0, st)
	seedOldRequest(t, st, 365*24*time.Hour)

	got := decodeJSON[retentionResponse](t, getRetention(handler).Body)
	if got.Days != 0 {
		t.Errorf("days = %d, want 0", got.Days)
	}
	if got.Cutoff != nil {
		t.Errorf("cutoff = %v, want nil for days=0", got.Cutoff)
	}
	if got.EligibleRequests != 0 || got.EligibleBytes != 0 {
		t.Errorf("eligible = %d/%d, want 0/0 for days=0", got.EligibleRequests, got.EligibleBytes)
	}
	if got.Oldest != nil || got.Newest != nil {
		t.Errorf("oldest/newest = %v/%v, want nil/nil for days=0", got.Oldest, got.Newest)
	}
}

// TestGetRetentionEmptyEligibleSetStillNamesCutoff is the state that tells
// "retention off" apart from "retention on but nothing is old enough yet":
// both report zeros and nulls for oldest/newest, but only the latter
// includes a non-nil cutoff.
func TestGetRetentionEmptyEligibleSetStillNamesCutoff(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetRetention(3650, st) // far longer than any seeded row's age
	seedRequest(t, st, nil)

	got := decodeJSON[retentionResponse](t, getRetention(handler).Body)
	if got.Cutoff == nil {
		t.Fatal("cutoff = nil, want a set cutoff for days>0 even with zero eligible rows")
	}
	if got.EligibleRequests != 0 || got.EligibleBytes != 0 {
		t.Errorf("eligible = %d/%d, want 0/0", got.EligibleRequests, got.EligibleBytes)
	}
	if got.Oldest != nil || got.Newest != nil {
		t.Errorf("oldest/newest = %v/%v, want nil/nil for an empty eligible set", got.Oldest, got.Newest)
	}
}

func TestGetRetentionNoUnpricedRows(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetRetention(0, st)
	seedPricedRequest(t, st)

	got := decodeJSON[retentionResponse](t, getRetention(handler).Body)
	if got.UnpricedRequests != 0 || got.UnpricedBytes != 0 {
		t.Errorf("unpriced = %d/%d, want 0/0", got.UnpricedRequests, got.UnpricedBytes)
	}
}

// TestGetRetentionUnknownModelNotCountedAsUnpriced is the assertion that
// fails if unpriced_requests degrades to a bare cost_usd IS NULL — an
// unknown-model row also has a nil CostUSD but must not be swept into the
// unpriced count (internal/store's purgeUnpricedWhere, D5).
func TestGetRetentionUnknownModelNotCountedAsUnpriced(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetRetention(0, st)

	unknown := "unknown-model"
	seedRequest(t, st, func(r *store.Request) {
		r.CostUSD = nil
		r.CostSource = &unknown
	})

	got := decodeJSON[retentionResponse](t, getRetention(handler).Body)
	if got.UnpricedRequests != 0 {
		t.Errorf("unpriced_requests = %d, want 0 (unknown-model must not count as unpriced)", got.UnpricedRequests)
	}
}

// TestGetRetentionUnpricedIndependentOfDays pins that unpriced_* reports the
// same numbers whether or not retention is configured (D10) — the unpriced
// action stays available with retention off.
func TestGetRetentionUnpricedIndependentOfDays(t *testing.T) {
	st := newTestStore(t)
	seedUnpricedRequest(t, st)

	handlerOff, _, _, _ := newTestAPI(t, st)
	handlerOff.SetRetention(0, st)
	off := decodeJSON[retentionResponse](t, getRetention(handlerOff).Body)

	handlerOn, _, _, _ := newTestAPI(t, st)
	handlerOn.SetRetention(30, st)
	on := decodeJSON[retentionResponse](t, getRetention(handlerOn).Body)

	if off.UnpricedRequests != on.UnpricedRequests || off.UnpricedBytes != on.UnpricedBytes {
		t.Errorf("unpriced differs with days on/off: %+v vs %+v", off, on)
	}
}

func TestPostPurgeModes(t *testing.T) {
	t.Run("older_than valid days deletes", func(t *testing.T) {
		st := newTestStore(t)
		handler, _, _, _ := newTestAPI(t, st)
		handler.SetRetention(5, st)
		seedOldRequest(t, st, 10*24*time.Hour)

		rr := postPurge(handler, `{"mode":"older_than","days":5}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
		}
		got := decodeJSON[purgeResponse](t, rr.Body)
		if got.Deleted != 1 {
			t.Errorf("deleted = %d, want 1", got.Deleted)
		}
	})

	t.Run("unpriced deletes", func(t *testing.T) {
		st := newTestStore(t)
		handler, _, _, _ := newTestAPI(t, st)
		handler.SetRetention(0, st)
		seedUnpricedRequest(t, st)

		rr := postPurge(handler, `{"mode":"unpriced"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
		}
		got := decodeJSON[purgeResponse](t, rr.Body)
		if got.Deleted != 1 {
			t.Errorf("deleted = %d, want 1", got.Deleted)
		}
	})

	t.Run("missing mode is 400", func(t *testing.T) {
		st := newTestStore(t)
		handler, _, _, _ := newTestAPI(t, st)
		handler.SetRetention(5, st)

		rr := postPurge(handler, `{}`)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("unknown mode is 400", func(t *testing.T) {
		st := newTestStore(t)
		handler, _, _, _ := newTestAPI(t, st)
		handler.SetRetention(5, st)

		rr := postPurge(handler, `{"mode":"delete_everything"}`)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("older_than days=0 is 400 and deletes nothing", func(t *testing.T) {
		st := newTestStore(t)
		handler, _, _, _ := newTestAPI(t, st)
		handler.SetRetention(5, st)
		seedOldRequest(t, st, 10*24*time.Hour)

		rr := postPurge(handler, `{"mode":"older_than","days":0}`)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400: %s", rr.Code, rr.Body.String())
		}
		n, err := st.CountRequests(context.Background(), store.Filter{})
		if err != nil {
			t.Fatalf("CountRequests: %v", err)
		}
		if n != 1 {
			t.Errorf("row count = %d, want 1 (nothing deleted)", n)
		}
	})

	t.Run("older_than days=-1 is 400 and deletes nothing", func(t *testing.T) {
		st := newTestStore(t)
		handler, _, _, _ := newTestAPI(t, st)
		handler.SetRetention(5, st)
		seedOldRequest(t, st, 10*24*time.Hour)

		rr := postPurge(handler, `{"mode":"older_than","days":-1}`)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400: %s", rr.Code, rr.Body.String())
		}
		n, err := st.CountRequests(context.Background(), store.Filter{})
		if err != nil {
			t.Fatalf("CountRequests: %v", err)
		}
		if n != 1 {
			t.Errorf("row count = %d, want 1 (nothing deleted)", n)
		}
	})
}

// TestPostPurgeOlderThanBeyondDataAgeReturnsZero pins that a days value
// larger than the data's age is not an error — the same empty-set state the
// preview reports.
func TestPostPurgeOlderThanBeyondDataAgeReturnsZero(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetRetention(3650, st)
	seedRequest(t, st, nil)

	rr := postPurge(handler, `{"mode":"older_than","days":3650}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	got := decodeJSON[purgeResponse](t, rr.Body)
	if got.Deleted != 0 {
		t.Errorf("deleted = %d, want 0", got.Deleted)
	}
}

// TestPurgeGuardNamesPurgeAction mirrors the prices guard test: both
// rejection arms must answer 403 naming the "purge" action, not "replay" or
// "prices" — a third write route with a mislabeled guard is exactly the
// failure this ticket exists to avoid.
func TestPurgeGuardNamesPurgeAction(t *testing.T) {
	t.Run("non-loopback Host", func(t *testing.T) {
		st := newTestStore(t)
		handler, _, _, _ := newTestAPI(t, st)
		handler.SetRetention(5, st)

		req := httptest.NewRequest(http.MethodPost, "/api/purge", bytes.NewBufferString(`{"mode":"unpriced"}`))
		req.Host = "evil.example.com"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", rr.Code, rr.Body.String())
		}
		if body := rr.Body.String(); !strings.Contains(body, "purge") || strings.Contains(body, "replay") {
			t.Errorf("body = %q, want it to name %q and not %q", body, "purge", "replay")
		}
	})

	t.Run("cross-origin Origin", func(t *testing.T) {
		st := newTestStore(t)
		handler, _, _, _ := newTestAPI(t, st)
		handler.SetRetention(5, st)

		req := httptest.NewRequest(http.MethodPost, "/api/purge", bytes.NewBufferString(`{"mode":"unpriced"}`))
		req.Host = "localhost"
		req.Header.Set("Origin", "http://attacker.example:1234")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", rr.Code, rr.Body.String())
		}
		if body := rr.Body.String(); !strings.Contains(body, "purge") || strings.Contains(body, "replay") {
			t.Errorf("body = %q, want it to name %q and not %q", body, "purge", "replay")
		}
	})
}

func TestRetentionUnwiredIs503(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st) // no SetRetention

	rr := getRetention(handler)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /api/retention status = %d, want 503 (retention unwired)", rr.Code)
	}
}

func TestPurgeUnwiredIs503(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st) // no SetRetention

	rr := postPurge(handler, `{"mode":"unpriced"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("POST /api/purge status = %d, want 503 (retention unwired)", rr.Code)
	}
}

// TestGetPurgeIsNotARoute pins that /api/purge is reachable only by POST.
// With only "POST /api/purge" registered, a GET at that path doesn't match
// it and instead falls through to the "/" FileServer catch-all (the same
// fallthrough br-GI-17-04's review notes describe for an unregistered
// route) — a 404 for a nonexistent file, not the JSON purgeResponse a
// mismatched guard might otherwise leak.
func TestGetPurgeIsNotARoute(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetRetention(5, st)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/purge", nil))
	if rr.Code == http.StatusOK {
		t.Errorf("GET /api/purge status = 200, want anything but — the write route must not answer a GET")
	}
	if ct := rr.Header().Get("Content-Type"); strings.Contains(ct, "application/json") {
		t.Errorf("GET /api/purge answered JSON (Content-Type=%q) — the write handler ran on a GET", ct)
	}
}
