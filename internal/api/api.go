package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/consumer"
	"github.com/abhisheksarkar30/deepseek-lens/internal/sink"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// Store is the narrow read slice of *store.Store the API needs. Satisfied
// structurally by *store.Store (and by *PublishingStore, since it embeds
// one) — kept as an interface only so api_test.go can seed a real temp
// store without any other indirection.
type Store interface {
	GetRequest(ctx context.Context, id int64) (*store.Request, error)
	ListRequests(ctx context.Context, f store.Filter) ([]*store.Request, error)
	StatsSummary(ctx context.Context, since time.Time) (*store.Summary, error)
	StatsByModel(ctx context.Context, since time.Time) ([]store.ModelStat, error)
	StatsByDay(ctx context.Context, since time.Time) ([]store.DayStat, error)
	StatsByCostSource(ctx context.Context, since time.Time) ([]store.CostSourceStat, error)
	ListSessions(ctx context.Context) ([]*store.Session, error)
	GetSession(ctx context.Context, id string) (*store.Session, error)
	ListWarnings(ctx context.Context, f store.Filter) ([]*store.Warning, error)
}

type api struct {
	store    Store
	sink     *sink.Sink
	consumer *consumer.Consumer
	broker   *Broker
}

// New builds the dashboard's http.Handler: the read-only JSON API under
// /api/* (including the /api/stream SSE endpoint) plus assets served from
// the embedded internal/web filesystem at every other path. Handlers are
// thin per the bead: parse query params into a store.Filter, call st, encode
// JSON — no business logic here (grouping, e.g. for the warning inbox, is
// the dashboard JS's job).
func New(st Store, sk *sink.Sink, cons *consumer.Consumer, broker *Broker, assets fs.FS) http.Handler {
	a := &api{store: st, sink: sk, consumer: cons, broker: broker}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/requests", methodGet(a.listRequests))
	mux.HandleFunc("/api/requests/{id}", methodGet(a.getRequest))
	mux.HandleFunc("/api/stats", methodGet(a.stats))
	mux.HandleFunc("/api/warnings", methodGet(a.listWarnings))
	mux.HandleFunc("/api/sessions", methodGet(a.listSessions))
	mux.HandleFunc("/api/sessions/{id}", methodGet(a.getSession))
	mux.HandleFunc("/api/stream", methodGet(a.stream))
	mux.HandleFunc("/api/health", methodGet(a.health))
	mux.Handle("/", http.FileServer(http.FS(assets)))
	return mux
}

// methodGet rejects every method but GET with a JSON 405 before h runs —
// the dashboard is entirely read-only (replay, POST /api/requests/{id}/replay,
// is br-GI-1-13's job and is not implemented here at all).
func methodGet(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// parseLimit reads ?limit, defaulting to 0 (which every store list method
// treats as its own capped default — never unbounded).
func parseLimit(r *http.Request) (int, error) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid limit %q: want a non-negative integer", s)
	}
	return n, nil
}

// parseSinceParam reads ?since as a Go duration ("24h") or an RFC3339
// timestamp, mirroring internal/cli's parseSince. Absent means the zero
// Time, which every store method already treats as "since the beginning of
// time" (Filter.Since.IsZero() / a very small UnixNano()).
func parseSinceParam(r *http.Request) (time.Time, error) {
	s := r.URL.Query().Get("since")
	if s == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid since %q: want a duration like 24h or an RFC3339 timestamp", s)
}

func parseBoolParam(r *http.Request, key string) (bool, error) {
	s := r.URL.Query().Get(key)
	if s == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return false, fmt.Errorf("invalid %s %q: want true or false", key, s)
	}
	return b, nil
}

func (a *api) listRequests(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	since, err := parseSinceParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	warn, err := parseBoolParam(r, "warn")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	errs, err := parseBoolParam(r, "errors")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	f := store.Filter{
		Limit:      limit,
		Since:      since,
		SessionID:  q.Get("session"),
		Model:      q.Get("model"),
		OnlyWarned: warn,
		OnlyErrors: errs,
	}

	reqs, err := a.store.ListRequests(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, reqs)
}

// requestDetail is /api/requests/{id}'s response shape: the request's own
// fields (promoted from the embedded pointer, matching every other bare
// store.Request the API returns) plus its warnings attached.
type requestDetail struct {
	*store.Request
	Warnings []*store.Warning `json:"warnings"`
}

func (a *api) getRequest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request id")
		return
	}

	req, err := a.store.GetRequest(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "request not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// store.Filter has no per-request lookup (ListWarnings only filters by
	// Kind/Severity/Since) — same one-query-then-filter-in-Go trade
	// internal/cli's show/ls commands already take, at the same DefaultLimit
	// cap, rather than adding a store.go method outside this bead's scope.
	all, err := a.store.ListWarnings(r.Context(), store.Filter{Limit: store.DefaultLimit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var warnings []*store.Warning
	for _, wn := range all {
		if wn.RequestID == id {
			warnings = append(warnings, wn)
		}
	}

	writeJSON(w, http.StatusOK, requestDetail{Request: req, Warnings: warnings})
}

type statsResponse struct {
	Since   time.Time         `json:"since"`
	Summary *store.Summary    `json:"summary"`
	ByModel []store.ModelStat `json:"by_model"`
	ByDay   []store.DayStat   `json:"by_day"`
	// CostSources is the cost-source breakdown: how many calls in the window
	// were priced, approximate, unpriced, or for a model the table does not
	// know. The dashboard needs it because a cost total alone cannot say
	// whether it covered every call.
	CostSources []store.CostSourceStat `json:"cost_sources"`
}

func (a *api) stats(w http.ResponseWriter, r *http.Request) {
	since, err := parseSinceParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	summary, err := a.store.StatsSummary(r.Context(), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	byModel, err := a.store.StatsByModel(r.Context(), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	byDay, err := a.store.StatsByDay(r.Context(), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	costSources, err := a.store.StatsByCostSource(r.Context(), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, statsResponse{
		Since: since, Summary: summary, ByModel: byModel, ByDay: byDay, CostSources: costSources,
	})
}

func (a *api) listWarnings(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	since, err := parseSinceParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	f := store.Filter{Limit: limit, Since: since, Kind: q.Get("kind"), Severity: q.Get("severity")}

	warnings, err := a.store.ListWarnings(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, warnings)
}

func (a *api) listSessions(w http.ResponseWriter, r *http.Request) {
	// store.ListSessions takes no Filter (no limit param to honor) — every
	// row it returns comes from UpsertSession, which nothing calls until
	// br-GI-1-12 wires session resolution, so this is a placeholder in
	// practice (always empty) today, exactly as the bead describes, without
	// a special-cased stub handler.
	sessions, err := a.store.ListSessions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (a *api) getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := a.store.GetSession(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// stream is the SSE endpoint: it subscribes to the broker and forwards every
// Event as a "data:" line until the client disconnects or the broker drops
// it for being slow.
func (a *api) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	ch, unsubscribe := a.broker.Subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return // dropped by the broker for being slow
			}
			b, err := json.Marshal(e)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

type healthResponse struct {
	SinkAccepted      uint64     `json:"sink_accepted"`
	SinkDropped       uint64     `json:"sink_dropped"`
	ConsumerProcessed uint64     `json:"consumer_processed"`
	ConsumerFailed    uint64     `json:"consumer_failed"`
	ConsumerFlushes   uint64     `json:"consumer_flushes"`
	LastWriteAt       *time.Time `json:"last_write_at,omitempty"`
	LastWriteAgeMs    int64      `json:"last_write_age_ms,omitempty"`
}

func (a *api) health(w http.ResponseWriter, r *http.Request) {
	accepted, dropped := a.sink.Stats()
	cs := a.consumer.Stats()

	resp := healthResponse{
		SinkAccepted:      accepted,
		SinkDropped:       dropped,
		ConsumerProcessed: cs.Processed,
		ConsumerFailed:    cs.Failed,
		ConsumerFlushes:   cs.Flushes,
	}
	if !cs.LastWriteAt.IsZero() {
		t := cs.LastWriteAt
		resp.LastWriteAt = &t
		resp.LastWriteAgeMs = time.Since(t).Milliseconds()
	}
	writeJSON(w, http.StatusOK, resp)
}
