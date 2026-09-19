package api

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// OffPeakDates and WorkDates are the installed calendar's date sets,
	// echoed verbatim — a range stays a range, because the UI shows the
	// user's own config text back rather than a re-serialisation of the
	// parsed days. Both are "" when no calendar is installed.
	OffPeakDates string `json:"off_peak_dates"`
	WorkDates    string `json:"work_dates"`
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

	writeJSON(w, http.StatusOK, renderPrices(a.pricePath, tbl, a.calendar))
}

// ratesFields is the JSON shape of a POST /api/prices body's "rates"
// object — the four known fields only. Decoding into this (rather than
// straight into pricing.Rates) is what lets setPrices reject an unknown
// field ({"rates":{"inpt":1}}) at decode time with DisallowUnknownFields,
// before any validation or write.
type ratesFields struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
}

// setPricesRequest is POST /api/prices's body: one model, whole-row
// rates (D3). An omitted field and an explicit null both decode to a nil
// pointer, so both mean "unset" with no separate syntax for either.
type setPricesRequest struct {
	Model string      `json:"model"`
	Rates ratesFields `json:"rates"`
}

// setPrices is POST /api/prices: replaces one model's rates wholesale and
// returns the same shape GET does, so the UI re-renders from what was
// actually written rather than from what it sent.
//
// The write path is D1's, deliberately three lines: Load, replace one
// model's Rates, Save — no in-memory state and no invalidation protocol,
// so a rate set here prices the consumer's very next request once its
// Loader next stats the file.
func (a *api) setPrices(w http.ResponseWriter, r *http.Request) {
	if a.pricePath == "" {
		writeError(w, http.StatusServiceUnavailable, "pricing is unavailable: no price path is wired")
		return
	}
	if reason := replayOriginReject(r, "prices"); reason != "" {
		writeError(w, http.StatusForbidden, reason)
		return
	}

	var req setPricesRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("malformed request body: %v", err))
		return
	}

	tbl, err := pricing.Load(a.pricePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tbl[req.Model] = pricing.Rates{
		Input: req.Rates.Input, Output: req.Rates.Output,
		CacheRead: req.Rates.CacheRead, CacheWrite: req.Rates.CacheWrite,
	}
	if err := pricing.Save(a.pricePath, tbl); err != nil {
		writeError(w, priceStatus(err), err.Error())
		return
	}

	tbl, err = pricing.Load(a.pricePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, renderPrices(a.pricePath, tbl, a.calendar))
}

// priceStatus maps a pricing.Save error to the status POST /api/prices
// answers: 400 for the two typed rejections (matched with errors.Is, so a
// %w-wrapped sentinel still maps correctly), 500 for anything else — a
// disk/write failure is not a bad request.
func priceStatus(err error) int {
	if errors.Is(err, pricing.ErrInvalidName) || errors.Is(err, pricing.ErrInvalidRate) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// renderPrices builds the shared GET/POST response shape from a loaded
// table and the installed calendar: sorted models, so the array does not
// reshuffle between renders as the underlying map iterates.
//
// The calendar comes in as an argument rather than being read off the
// receiver so both callers render it through one shape — POST re-renders
// through the same function GET does, so neither can start omitting the
// dates.
func renderPrices(path string, tbl pricing.Table, cal pricing.Calendar) pricesResponse {
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
	offPeak, work := cal.DateSets()
	return pricesResponse{
		Path: path, PeakMultiplier: pricing.PeakMultiplier, Models: models,
		OffPeakDates: offPeak, WorkDates: work,
	}
}
