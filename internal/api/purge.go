package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// retentionResponse is GET /api/retention's response: both previews in one
// round trip, since the Settings tab shows both actions at once (D11).
//
// Cutoff/Oldest/Newest are pointers so an unconfigured or empty eligible set
// marshals as JSON null rather than a zero time.Time — the same "unset"
// convention D3 uses for a rate. Cutoff is nil exactly when days <= 0
// ("keep forever" — retention is off, nothing is eligible, and there is no
// instant to name); it is non-nil whenever days > 0, even if the eligible
// set is empty, which is what tells the two states apart on the wire.
type retentionResponse struct {
	Days             int        `json:"days"`
	Cutoff           *time.Time `json:"cutoff"`
	EligibleRequests int        `json:"eligible_requests"`
	EligibleBytes    int64      `json:"eligible_bytes"`
	Oldest           *time.Time `json:"oldest"`
	Newest           *time.Time `json:"newest"`
	UnpricedRequests int        `json:"unpriced_requests"`
	UnpricedBytes    int64      `json:"unpriced_bytes"`
}

// getRetention is GET /api/retention: a read that deletes nothing, so the
// confirmation a user clicks in the Settings tab is informed by a real
// count rather than an estimate. eligible_* is never computed from
// cutoff = now — that would present the whole table as eligible under the
// safest ("keep forever") setting.
func (a *api) getRetention(w http.ResponseWriter, r *http.Request) {
	if a.purge == nil {
		writeError(w, http.StatusServiceUnavailable, "retention is unavailable: no purge seam is wired")
		return
	}

	resp := retentionResponse{Days: a.retentionDays}

	if a.retentionDays > 0 {
		cutoff := time.Now().Add(-time.Duration(a.retentionDays) * 24 * time.Hour)
		resp.Cutoff = &cutoff

		count, oldest, newest, err := a.purge.CountPurgeable(r.Context(), cutoff)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp.EligibleRequests = count
		resp.Oldest = oldest
		resp.Newest = newest

		eligibleBytes, err := a.purge.PurgeableBytes(r.Context(), cutoff)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp.EligibleBytes = eligibleBytes
	}

	// Independent of days (D10): the unpriced action stays available even
	// when retention is off.
	unpriced, err := a.purge.CountUnpriced(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp.UnpricedRequests = unpriced

	unpricedBytes, err := a.purge.UnpricedBytes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp.UnpricedBytes = unpricedBytes

	writeJSON(w, http.StatusOK, resp)
}

// purgeRequest is POST /api/purge's body. Days is ignored for
// mode "unpriced".
type purgeRequest struct {
	Mode string `json:"mode"`
	Days int    `json:"days"`
}

// purgeResponse reports what PurgeResult (D6) says was actually deleted,
// for either mode.
type purgeResponse struct {
	Mode               string `json:"mode"`
	Deleted            int64  `json:"deleted"`
	SessionsReconciled int    `json:"sessions_reconciled"`
}

// postPurge is POST /api/purge: the delete half of the preview/delete pair
// (D10). Two modes on one route because the guard, the confirmation and
// the "report what was deleted" response are identical between them — only
// the WHERE clause differs, and that lives in internal/store. Named
// postPurge rather than purge to avoid colliding with the api struct's own
// purge field (the RetentionPurger seam).
func (a *api) postPurge(w http.ResponseWriter, r *http.Request) {
	if a.purge == nil {
		writeError(w, http.StatusServiceUnavailable, "retention is unavailable: no purge seam is wired")
		return
	}
	if reason := replayOriginReject(r, "purge"); reason != "" {
		writeError(w, http.StatusForbidden, reason)
		return
	}

	var req purgeRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("malformed request body: %v", err))
		return
	}

	var res struct {
		Deleted            int64
		SessionsReconciled int
	}
	switch req.Mode {
	case "older_than":
		if req.Days <= 0 {
			writeError(w, http.StatusBadRequest, "days must be positive for mode \"older_than\"")
			return
		}
		cutoff := time.Now().Add(-time.Duration(req.Days) * 24 * time.Hour)
		r2, err := a.purge.PurgeOlderThan(r.Context(), cutoff)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		res.Deleted, res.SessionsReconciled = r2.Deleted, r2.SessionsReconciled
	case "unpriced":
		r2, err := a.purge.PurgeUnpriced(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		res.Deleted, res.SessionsReconciled = r2.Deleted, r2.SessionsReconciled
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown mode %q", req.Mode))
		return
	}

	writeJSON(w, http.StatusOK, purgeResponse{Mode: req.Mode, Deleted: res.Deleted, SessionsReconciled: res.SessionsReconciled})
}
