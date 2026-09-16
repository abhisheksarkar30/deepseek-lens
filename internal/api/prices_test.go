package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/abhisheksarkar30/deepseek-lens/internal/pricing"
)

func f64(v float64) *float64 { return &v }

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
