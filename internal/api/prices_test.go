package api

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abhisheksarkar30/deepseek-lens/internal/pricing"
)

func f64(v float64) *float64 { return &v }

// postPrices issues a same-origin, loopback POST /api/prices with body as
// the raw JSON request body — real requests through this handler go
// through httptest.NewRequest's default non-loopback Host ("example.com"),
// which replayOriginReject would reject before the test ever reaches the
// behaviour under test, so every POST test needs a loopback Host.
func postPrices(handler http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/prices", bytes.NewBufferString(body))
	req.Host = "localhost"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestGetPricesChartShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	if err := pricing.Save(path, pricing.Table{
		"deepseek-flash":  {Input: f64(0.28), Output: f64(1.1)},
		"deepseek-v4-pro": {},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetPricing(path)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/prices", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	got := decodeJSON[pricesResponse](t, rr.Body)
	if got.Path != path {
		t.Errorf("path = %q, want %q", got.Path, path)
	}
	if got.PeakMultiplier != pricing.PeakMultiplier {
		t.Errorf("peak_multiplier = %v, want %v", got.PeakMultiplier, pricing.PeakMultiplier)
	}
	if len(got.Models) != 2 {
		t.Fatalf("got %d models, want 2", len(got.Models))
	}
}

func TestGetPricesNullVsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	if err := pricing.Save(path, pricing.Table{
		"deepseek-flash":  {Input: f64(0)},
		"deepseek-v4-pro": {},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetPricing(path)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/prices", nil))
	got := decodeJSON[pricesResponse](t, rr.Body)

	byModel := map[string]priceModel{}
	for _, m := range got.Models {
		byModel[m.Model] = m
	}

	flash := byModel["deepseek-flash"]
	if flash.Input == nil || *flash.Input != 0 {
		t.Errorf("deepseek-flash.input = %v, want a non-nil 0", flash.Input)
	}
	if flash.Output != nil {
		t.Errorf("deepseek-flash.output = %v, want nil (unset)", flash.Output)
	}

	proModel := byModel["deepseek-v4-pro"]
	if proModel.Input != nil || proModel.Output != nil || proModel.CacheRead != nil || proModel.CacheWrite != nil {
		t.Errorf("deepseek-v4-pro rates = %+v, want all nil (fully unset)", proModel)
	}
}

func TestGetPricesSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	if err := pricing.Save(path, pricing.Table{
		"deepseek-flash": {Input: f64(0.28)},
		"deepseek-lite":  {}, // bare model line, present but no rate
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetPricing(path)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/prices", nil))
	got := decodeJSON[pricesResponse](t, rr.Body)

	byModel := map[string]priceModel{}
	for _, m := range got.Models {
		byModel[m.Model] = m
	}
	if got := byModel["deepseek-flash"].Source; got != pricing.SourceConfigured {
		t.Errorf("deepseek-flash source = %q, want %q", got, pricing.SourceConfigured)
	}
	if got := byModel["deepseek-lite"].Source; got != pricing.SourceUnpriced {
		t.Errorf("deepseek-lite source = %q, want %q", got, pricing.SourceUnpriced)
	}
	// deepseek-v4-pro is absent from the saved file but present in
	// Default(), so pricing.Load merges it in — it must be reported as
	// unpriced, not silently dropped from the response.
	proModel, ok := byModel["deepseek-v4-pro"]
	if !ok {
		t.Fatal("deepseek-v4-pro is missing from the response — Default() models must still be reported")
	}
	if proModel.Source != pricing.SourceUnpriced {
		t.Errorf("deepseek-v4-pro source = %q, want %q", proModel.Source, pricing.SourceUnpriced)
	}
}

func TestGetPricesSortedAndStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	if err := pricing.Save(path, pricing.Table{
		"zeta":  {Input: f64(1)},
		"alpha": {Input: f64(2)},
		"mid":   {Input: f64(3)},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetPricing(path)

	var lastOrder []string
	for i := 0; i < 3; i++ {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/prices", nil))
		got := decodeJSON[pricesResponse](t, rr.Body)
		order := make([]string, len(got.Models))
		for j, m := range got.Models {
			order[j] = m.Model
		}
		if i == 0 {
			lastOrder = order
			want := []string{"alpha", "deepseek-flash", "deepseek-v4-pro", "mid", "zeta"}
			if len(order) != len(want) {
				t.Fatalf("call %d: order = %v, want %v", i, order, want)
			}
			for j := range want {
				if order[j] != want[j] {
					t.Fatalf("call %d: order = %v, want sorted %v", i, order, want)
				}
			}
			continue
		}
		for j := range lastOrder {
			if order[j] != lastOrder[j] {
				t.Errorf("call %d: order changed: got %v, previously %v", i, order, lastOrder)
			}
		}
	}
}

func TestGetPricesUnwiredIs503(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st) // no SetPricing

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/prices", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (pricing unwired)", rr.Code)
	}
}

func TestGetPricesIsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	if err := pricing.Save(path, pricing.Table{"deepseek-flash": {Input: f64(0.28)}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}
	fiBefore, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat before: %v", err)
	}

	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetPricing(path)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/prices", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after: %v", err)
	}
	fiAfter, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("GET /api/prices changed the file's bytes")
	}
	if !fiBefore.ModTime().Equal(fiAfter.ModTime()) {
		t.Errorf("GET /api/prices changed the file's mtime: %v -> %v", fiBefore.ModTime(), fiAfter.ModTime())
	}
}

func TestSetPricesSetsARate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetPricing(path)

	rr := postPrices(handler, `{"model":"deepseek-flash","rates":{"input":0.28}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	tbl, err := pricing.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if in := tbl["deepseek-flash"].Input; in == nil || *in != 0.28 {
		t.Errorf("input = %v, want 0.28", in)
	}
}

func TestSetPricesUnsetsWithNull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	if err := pricing.Save(path, pricing.Table{
		"deepseek-flash": {Input: f64(0.28), Output: f64(0.42), CacheRead: f64(0.1)},
	}); err != nil {
		t.Fatalf("seeding Save: %v", err)
	}

	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetPricing(path)

	rr := postPrices(handler, `{"model":"deepseek-flash","rates":{"input":0.28,"output":null,"cache_read":0.1}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	tbl, err := pricing.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := tbl["deepseek-flash"]
	if r.Output != nil {
		t.Errorf("output = %v, want nil (unset by explicit null)", r.Output)
	}
	if r.Input == nil || *r.Input != 0.28 {
		t.Errorf("input = %v, want 0.28 (untouched)", r.Input)
	}
	if r.CacheRead == nil || *r.CacheRead != 0.1 {
		t.Errorf("cache_read = %v, want 0.1 (untouched)", r.CacheRead)
	}
}

func TestSetPricesAddsUnknownModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetPricing(path)

	rr := postPrices(handler, `{"model":"gpt-9","rates":{"input":5}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	got := decodeJSON[pricesResponse](t, rr.Body)
	found := false
	for _, m := range got.Models {
		if m.Model == "gpt-9" {
			found = true
			if m.Input == nil || *m.Input != 5 {
				t.Errorf("gpt-9.input = %v, want 5", m.Input)
			}
		}
	}
	if !found {
		t.Error("gpt-9 missing from the response after being added")
	}

	tbl, err := pricing.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := tbl["gpt-9"]; !ok {
		t.Error("gpt-9 missing from the saved table")
	}
}

// TestSetPricesRejectionLeavesFileUntouched is the assertion that separates
// "refused" from "refused after corrupting": each bad request must answer
// 400 and leave the price file byte-for-byte identical to before.
func TestSetPricesRejectionLeavesFileUntouched(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"newline in model", `{"model":"evil\nx","rates":{"input":1}}`},
		{"equals in model", `{"model":"evil=x","rates":{"input":1}}`},
		{"dot in model", `{"model":"evil.x","rates":{"input":1}}`},
		{"space in model", `{"model":"evil x","rates":{"input":1}}`},
		{"negative rate", `{"model":"deepseek-flash","rates":{"input":-1}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "prices.toml")
			if err := pricing.Save(path, pricing.Table{"deepseek-flash": {Input: f64(0.28)}}); err != nil {
				t.Fatalf("seeding Save: %v", err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile before: %v", err)
			}

			st := newTestStore(t)
			handler, _, _, _ := newTestAPI(t, st)
			handler.SetPricing(path)

			rr := postPrices(handler, tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", rr.Code, rr.Body.String())
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile after: %v", err)
			}
			if string(before) != string(after) {
				t.Errorf("rejected POST changed the file:\n before %q\n after  %q", before, after)
			}
		})
	}
}

// TestSetPricesUnknownRateFieldIs400 pins the decode-time rejection (D4):
// an unknown rate field never reaches Rates.Set, because DisallowUnknownFields
// rejects the body before any validation or write.
func TestSetPricesUnknownRateFieldIs400(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetPricing(path)

	rr := postPrices(handler, `{"model":"deepseek-flash","rates":{"inpt":1}}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("an unknown-field rejection still wrote %s (stat err = %v)", path, err)
	}
}

// TestPriceStatusMapsErrors is the platform-independent replacement for
// forcing a real Save I/O failure (GOOS-dependent, and the failure might
// surface in Load, which the contract doesn't map). It asserts the mapping
// directly: the two sentinels (bare or %w-wrapped) map to 400, anything
// else maps to 500.
func TestPriceStatusMapsErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"ErrInvalidName", pricing.ErrInvalidName, http.StatusBadRequest},
		{"ErrInvalidRate", pricing.ErrInvalidRate, http.StatusBadRequest},
		{"wrapped ErrInvalidName", fmt.Errorf("pricing: save: %w", pricing.ErrInvalidName), http.StatusBadRequest},
		{"wrapped ErrInvalidRate", fmt.Errorf("pricing: save: %w", pricing.ErrInvalidRate), http.StatusBadRequest},
		{"plain disk error", fmt.Errorf("pricing: save: writing temp file: %w", errors.New("no space left on device")), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := priceStatus(tc.err); got != tc.want {
				t.Errorf("priceStatus(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestSetPricesGuardNamesPricesAction mirrors replay's guard test: both
// rejection arms (non-loopback Host, cross-origin Origin) must answer 403
// with a body naming the "prices" action, not "replay" — a second write
// route with a weaker or mislabeled guard is the failure this story exists
// to avoid.
func TestSetPricesGuardNamesPricesAction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")

	t.Run("non-loopback Host", func(t *testing.T) {
		st := newTestStore(t)
		handler, _, _, _ := newTestAPI(t, st)
		handler.SetPricing(path)

		req := httptest.NewRequest(http.MethodPost, "/api/prices", bytes.NewBufferString(`{"model":"x","rates":{}}`))
		req.Host = "evil.example.com"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", rr.Code, rr.Body.String())
		}
		if body := rr.Body.String(); !strings.Contains(body, "prices") || strings.Contains(body, "replay") {
			t.Errorf("body = %q, want it to name %q and not %q", body, "prices", "replay")
		}
	})

	t.Run("cross-origin Origin", func(t *testing.T) {
		st := newTestStore(t)
		handler, _, _, _ := newTestAPI(t, st)
		handler.SetPricing(path)

		req := httptest.NewRequest(http.MethodPost, "/api/prices", bytes.NewBufferString(`{"model":"x","rates":{}}`))
		req.Host = "localhost"
		req.Header.Set("Origin", "http://attacker.example:1234")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", rr.Code, rr.Body.String())
		}
		if body := rr.Body.String(); !strings.Contains(body, "prices") || strings.Contains(body, "replay") {
			t.Errorf("body = %q, want it to name %q and not %q", body, "prices", "replay")
		}
	})
}

func TestSetPricesUnwiredIs503(t *testing.T) {
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st) // no SetPricing

	rr := postPrices(handler, `{"model":"deepseek-flash","rates":{"input":0.28}}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (pricing unwired)", rr.Code)
	}
}

// TestSetPricesResponseShapeMatchesGet decodes the POST response with the
// exact struct GET uses, so a shape drift between the two routes is a
// compile error rather than a client bug.
func TestSetPricesResponseShapeMatchesGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetPricing(path)

	rr := postPrices(handler, `{"model":"deepseek-flash","rates":{"input":0.28}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	got := decodeJSON[pricesResponse](t, rr.Body)
	if got.Path != path {
		t.Errorf("path = %q, want %q", got.Path, path)
	}
	if len(got.Models) == 0 {
		t.Error("models is empty")
	}
}

// TestSetPricesTakesEffectWithoutRestart is D1's central premise,
// automated: a real pricing.NewLoader primed with a Table() call BEFORE
// the POST must see the new rate on its NEXT Table() call, with no
// restart. Asserting through Loader.Table() rather than pricing.Load
// matters — Load re-reads unconditionally and would pass even if the
// file's mtime/size never changed, which is exactly the mechanism a naive
// in-memory-only implementation (or a non-atomic Save that fails to bump
// mtime/size) would fail to preserve.
func TestSetPricesTakesEffectWithoutRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	if err := pricing.Save(path, pricing.Table{"deepseek-flash": {Input: f64(0.28)}}); err != nil {
		t.Fatalf("seeding Save: %v", err)
	}

	loader := pricing.NewLoader(path)
	if in := loader.Table()["deepseek-flash"].Input; in == nil || *in != 0.28 {
		t.Fatalf("priming read: input = %v, want 0.28", in)
	}

	st := newTestStore(t)
	handler, _, _, _ := newTestAPI(t, st)
	handler.SetPricing(path)

	rr := postPrices(handler, `{"model":"deepseek-flash","rates":{"input":0.55}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	if in := loader.Table()["deepseek-flash"].Input; in == nil || *in != 0.55 {
		t.Errorf("after POST: Loader.Table() input = %v, want 0.55 (live without restart)", in)
	}
}
