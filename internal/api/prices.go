package api

import (
	"net/http"
	"sort"

	"github.com/abhisheksarkar30/deepseek-lens/internal/pricing"
)

// priceModel is one model's row in GET /api/prices's response. Rate fields
// are pointers so an unset rate marshals as JSON null — never an implicit
// 0, which would claim the model is free rather than unconfigured (the same
// distinction pricing.Rates' own pointer fields carry).
type priceModel struct {
	Model      string   `json:"model"`
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
	Source     string   `json:"source"`
}

// pricesResponse is what both GET and POST /api/prices return — POST
// re-renders the same shape from what was actually written, so the UI
// re-renders from the server's own record rather than echoing its request.
type pricesResponse struct {
	Path           string       `json:"path"`
	PeakMultiplier float64      `json:"peak_multiplier"`
	Models         []priceModel `json:"models"`
}

// getPrices is GET /api/prices: the effective price table, resolved fresh
// from disk on every call. There is no cache — the file is the interface
// (D1), so this route always agrees with what the consumer's Loader is
// actually pricing from.
func (a *api) getPrices(w http.ResponseWriter, r *http.Request) {
	if a.pricePath == "" {
		writeError(w, http.StatusServiceUnavailable, "pricing is unavailable: no price path is wired")
		return
	}

	tbl, err := pricing.Load(a.pricePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, renderPrices(a.pricePath, tbl))
}

// renderPrices builds the shared GET/POST response shape from a loaded
// table: sorted models, so the array does not reshuffle between renders as
// the underlying map iterates.
func renderPrices(path string, tbl pricing.Table) pricesResponse {
	names := make([]string, 0, len(tbl))
	for name := range tbl {
		names = append(names, name)
	}
	sort.Strings(names)

	models := make([]priceModel, 0, len(tbl))
	for _, name := range names {
		r := tbl[name]
		models = append(models, priceModel{
			Model: name, Input: r.Input, Output: r.Output,
			CacheRead: r.CacheRead, CacheWrite: r.CacheWrite,
			Source: r.Source(),
		})
	}
	return pricesResponse{Path: path, PeakMultiplier: pricing.PeakMultiplier, Models: models}
}
