package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/consumer"
	"github.com/abhisheksarkar30/deepseek-lens/internal/proxy"
	"github.com/abhisheksarkar30/deepseek-lens/internal/replay"
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
	ListSessions(ctx context.Context, f store.Filter) ([]*store.Session, error)
	GetSession(ctx context.Context, id string) (*store.Session, error)
	ListWarnings(ctx context.Context, f store.Filter) ([]*store.Warning, error)
	CountRequests(ctx context.Context, f store.Filter) (int, error)
	CountWarnings(ctx context.Context, f store.Filter) (int, error)
	CountSessions(ctx context.Context) (int, error)
	WarningSummary(ctx context.Context, f store.Filter) ([]store.WarningGroup, error)
}

// RetentionPurger is the write surface the retention/purge routes call —
// deliberately separate from Store (which stays the narrow read slice
// documented above), so a purge enters through this seam rather than by
// widening Store into something that can delete rows.
//
// CountPurgeable and CountUnpriced are named for their own predicate each
// (older-than and the D5 unpriced predicate respectively), because the two
// preview handlers serve two different WHERE clauses — one Count/Bytes pair
// cannot produce both eligible_* and unpriced_*. CountPurgeable's oldest/newest
// are nil when the eligible count is 0 (MIN/MAX over an empty set is SQL
// NULL). (*store.Store).Vacuum is deliberately not here: it takes an
// exclusive lock and is CLI-only, never a dashboard action.
type RetentionPurger interface {
	PurgeOlderThan(ctx context.Context, cutoff time.Time) (store.PurgeResult, error)
	PurgeUnpriced(ctx context.Context) (store.PurgeResult, error)
	CountPurgeable(ctx context.Context, cutoff time.Time) (count int, oldest, newest *time.Time, err error)
	PurgeableBytes(ctx context.Context, cutoff time.Time) (int64, error)
	CountUnpriced(ctx context.Context) (int, error)
	UnpricedBytes(ctx context.Context) (int64, error)
}

type api struct {
	store    Store
	sink     *sink.Sink
	consumer *consumer.Consumer
	broker   *Broker
	mux      *http.ServeMux

	// proxyHandler is the live proxy's own Handler. The replay endpoint sends
	// through it rather than through a transport of its own, and replayEnabled
	// is config.ReplayEnabled — the endpoint's opt-in control (br-GI-1-13).
	proxyHandler  http.Handler
	replayEnabled bool

	// replayRejected counts every replay request turned away by either guard
	// in replay() — disabled-by-default or the Origin/Host allowlist. Without
	// this, a rejected probe against the one billable route leaves no
	// server-side trace at all. Surfaced on /api/health alongside the other
	// counters.
	replayRejected atomic.Uint64

	// pricePath is the price-file path GET/POST /api/prices load from and
	// save to (D1) — a path, not an in-memory table, so the handler cannot
	// bypass the file the consumer's Loader re-reads. Empty means unwired:
	// both routes answer 503.
	pricePath string

	// retentionDays and purge back GET /api/retention and POST /api/purge.
	// purge == nil means unwired: both routes answer 503, independent of
	// retentionDays (0 is itself a meaningful "keep forever" value, so it
	// cannot double as the unwired sentinel).
	retentionDays int
	purge         RetentionPurger
}

// SetPricing wires GET/POST /api/prices to the price file at path. Called
// from serve.go alongside the other optional-capability setters
// (SetSessionAggregator, SetPriceTable, SetBodyDecoding); leaving it unset
// is a supported state (both routes answer 503), not a nil dereference.
func (a *api) SetPricing(path string) { a.pricePath = path }

// SetRetention wires GET /api/retention and POST /api/purge to purge, with
// days as the configured retention threshold the preview and the CLI-implied
// default read. Leaving it unset is a supported state: both routes answer
// 503 rather than panicking on a nil purge.
func (a *api) SetRetention(days int, purge RetentionPurger) {
	a.retentionDays = days
	a.purge = purge
}

// ServeHTTP delegates to the stored mux, so *api satisfies http.Handler and
// Handler: api.New(...) in serve.go keeps compiling with no change at that
// call site.
func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.mux.ServeHTTP(w, r) }

// New builds the dashboard's http.Handler: the read-only JSON API under
// /api/* (including the /api/stream SSE endpoint) plus assets served from
// the embedded internal/web filesystem at every other path. Handlers are
// thin per the bead: parse query params into a store.Filter, call st, encode
// JSON — no business logic here. Grouping that has to be correct over the
// whole table (the warning inbox's per-kind totals) therefore lives in SQL,
// in store.WarningSummary, and is served by /api/warnings/summary.
//
// proxyHandler is the one write path this package owns: POST
// /api/requests/{id}/replay re-issues a captured request through it, so the
// replay is proxied, teed and recorded by exactly the code that handles live
// traffic. Passing nil disables the route entirely, which is what the read-only
// tests of beads before this one do.
func New(st Store, sk *sink.Sink, cons *consumer.Consumer, broker *Broker, assets fs.FS, proxyHandler http.Handler, replayEnabled bool) *api {
	a := &api{store: st, sink: sk, consumer: cons, broker: broker, proxyHandler: proxyHandler, replayEnabled: replayEnabled}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/requests", methodGet(a.listRequests))
	mux.HandleFunc("/api/requests/{id}", methodGet(a.getRequest))
	mux.HandleFunc("POST /api/requests/{id}/replay", a.replay)
	mux.HandleFunc("/api/stats", methodGet(a.stats))
	// Registered before /api/warnings, which as a prefix pattern would
	// otherwise also match this path. Go's ServeMux prefers the more
	// specific pattern either way; the order here is for the reader.
	mux.HandleFunc("/api/warnings/summary", methodGet(a.warningsSummary))
	mux.HandleFunc("/api/warnings", methodGet(a.listWarnings))
	mux.HandleFunc("/api/sessions", methodGet(a.listSessions))
	mux.HandleFunc("/api/sessions/{id}", methodGet(a.getSession))
	mux.HandleFunc("/api/stream", methodGet(a.stream))
	mux.HandleFunc("/api/health", methodGet(a.health))
	mux.HandleFunc("/api/prices", methodGet(a.getPrices))
	mux.Handle("/", http.FileServer(http.FS(assets)))
	a.mux = mux
	return a
}

// methodGet rejects every method but GET with a JSON 405 before h runs. The
// dashboard's read endpoints are otherwise unauthenticated on the strength of
// being read-only and loopback-bound; the one route that is neither is
// registered separately, with its own guard and its own method — see replay.
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

// parseOffset reads ?offset, defaulting to 0. A negative offset is rejected
// rather than clamped, mirroring parseLimit — so no handler ever needs to
// resolve an effective offset: absent → 0 *is* the effective offset.
func parseOffset(r *http.Request) (int, error) {
	s := r.URL.Query().Get("offset")
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid offset %q: want a non-negative integer", s)
	}
	return n, nil
}

// effectiveLimit resolves the page size the store will actually apply, so a
// handler can report it while the store's own clamp is out of reach. The
// duplication of store's Limit <= 0 → DefaultLimit rule is deliberate: a
// header that disagrees with the rows on screen is worse than one line of
// arithmetic in two places.
func effectiveLimit(limit int) int {
	if limit <= 0 {
		return store.DefaultLimit
	}
	return limit
}

// writePageHeaders sets the pagination metadata that rides on response
// headers rather than in the body, so list responses stay the bare JSON
// arrays every existing consumer already decodes.
//
// It must run before writeJSON, which calls WriteHeader — headers set after
// that are silently dropped, and the bug would surface only as three missing
// headers in a browser.
//
// limit is the EFFECTIVE page size, not the requested one. The two differ
// whenever ?limit is absent: that parses to 0, which the store clamps to
// DefaultLimit, so the response must say 1000 rather than 0. A header-driven
// consumer computes nextOffset = offset + X-Limit, and the raw 0 would leave
// it stuck on page 1 forever with no error to explain why. Callers pass
// effectiveLimit(limit); offsets need no such resolution.
func writePageHeaders(w http.ResponseWriter, total, limit, offset int) {
	h := w.Header()
	h.Set("X-Total-Count", strconv.Itoa(total))
	h.Set("X-Limit", strconv.Itoa(limit))
	h.Set("X-Offset", strconv.Itoa(offset))
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
	offset, err := parseOffset(r)
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
		Offset:     offset,
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
	// The same filter, so the total counts exactly the set the page was
	// drawn from — CountRequests ignores Limit/Offset itself.
	total, err := a.store.CountRequests(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writePageHeaders(w, total, effectiveLimit(limit), offset)
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

	warnings, err := a.warningsFor(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, requestDetail{Request: req, Warnings: warnings})
}

// warningsFor returns the warnings attached to one request. store.Filter has no
// per-request warning lookup (ListWarnings filters by Kind/Severity/Since only)
// — the same one-query-then-filter-in-Go trade internal/cli's show/ls commands
// already take, at the same DefaultLimit cap, rather than adding a store.go
// method for it.
func (a *api) warningsFor(ctx context.Context, id int64) ([]*store.Warning, error) {
	all, err := a.store.ListWarnings(ctx, store.Filter{Limit: store.DefaultLimit})
	if err != nil {
		return nil, err
	}
	var warnings []*store.Warning
	for _, wn := range all {
		if wn.RequestID == id {
			warnings = append(warnings, wn)
		}
	}
	return warnings, nil
}

// replayWait bounds how long the replay endpoint waits for the consumer to
// commit the row for the call it just sent, and replayPollInterval is how often
// it looks. The wait is inherent to the design: the send goes through the sink
// to the consumer's single writer, and the consumer batches — a lone call lands
// within one batchQuietWait (~250ms), so this is a very generous ceiling for
// the "did the row land yet" question rather than a timeout anyone should hit.
const (
	replayWait         = 2 * time.Second
	replayPollInterval = 25 * time.Millisecond
)

// replay is POST /api/requests/{id}/replay: the project's only billable,
// state-changing route, and therefore the only one not covered by the read-only
// dashboard's "no auth on loopback" rationale (br-GI-1-13, plan §security
// self-review).
//
// Its guard is two controls, neither a credential, and both are applied before
// anything is sent — a rejected request is rejected without a single byte
// reaching the upstream API. That ordering is the whole point of the guard, so
// it is the first thing this function does and it does not depend on reading
// anything:
//
//  1. replayEnabled (config.ReplayEnabled, `lens serve --replay`): off by
//     default, and when off this route does nothing at all.
//  2. Origin/Host allowlist (replayOriginReject): an accidental or malicious
//     browser page cannot make this endpoint spend money. See that function for
//     what "the dashboard's own origin" means and why a request with no Origin
//     at all passes — that is the CLI's path, deliberately, not a hole.
//
// On success the stored body is edited, re-sent through the proxy's own Handler
// (so it is teed, capped and recorded by the live path, and reaches SQLite
// through the consumer's single writer), and the row the consumer just wrote is
// returned along with the compact Outcome `lens replay` diffs.
func (a *api) replay(w http.ResponseWriter, r *http.Request) {
	if !a.replayEnabled {
		a.replayRejected.Add(1)
		log.Printf("api: replay rejected from %s: replay is disabled", r.RemoteAddr)
		writeError(w, http.StatusForbidden, "replay is disabled: start `lens serve --replay` to enable it")
		return
	}
	if reason := replayOriginReject(r, "replay"); reason != "" {
		a.replayRejected.Add(1)
		log.Printf("api: replay rejected from %s: %s", r.RemoteAddr, reason)
		writeError(w, http.StatusForbidden, reason)
		return
	}
	if a.proxyHandler == nil {
		writeError(w, http.StatusServiceUnavailable, "replay is unavailable: no proxy handler is wired")
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request id")
		return
	}
	orig, err := a.store.GetRequest(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "request not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(orig.ReqBody) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("request %d has no stored body to replay", id))
		return
	}

	edits, err := replay.ParseSets(r.URL.Query()["set"])
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	edited, err := replay.ApplyEdits(orig.ReqBody, edits)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	editsJSON, err := replay.MarshalEdits(edits)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	noCapture, err := parseBoolParam(r, "no_capture")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Snapshot the newest existing replay of this capture before sending, so
	// the row this request produces can be told apart from one a concurrent
	// replay of the same id may already have written.
	before, err := a.newestReplay(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	status, err := a.sendReplay(r, orig, edited, proxy.ReplayMeta{Of: id, Edits: editsJSON, NoCapture: noCapture})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if noCapture {
		// Sent, deliberately not recorded: there is no row and no id to report.
		writeJSON(w, http.StatusOK, replay.Result{Captured: false, Status: status})
		return
	}

	row, err := a.awaitReplayRow(r.Context(), id, before)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	warnings, err := a.warningsFor(r.Context(), row.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	outcome := replay.OutcomeOf(row, warnings)
	writeJSON(w, http.StatusOK, replay.Result{ID: row.ID, Captured: true, Status: row.Status, Outcome: &outcome})
}

// sendReplay re-issues orig's stored body through the proxy's own Handler and
// returns the upstream status.
//
// Routing the replay through proxy.Handler rather than through an HTTP client
// of its own is the design: the request picks up the same transport, the same
// streaming rewrite, the same tee, the same body cap and the same header
// redaction as live traffic, and the call it produces reaches SQLite through
// the consumer's single writer, which CLAUDE.md requires ("SQLite has exactly
// one writer: the consumer goroutine"). proxy.WithReplay is what carries the
// replay_of/replay_edits linkage across, since the Handler sees only the
// request.
//
// The response is written to a statusRecorder, which records the status and
// discards the body — the proxy's own tee already captured it, so keeping a
// second copy here would only duplicate a stream that can be large.
//
// Header note: the stored request headers are what the capture saved, which
// means the credential in them is the redaction placeholder, not a key. Lens
// neither persists nor injects one (design spec §Secrets: "Lens never persists
// a key and never injects one"), so a replay against an upstream that requires
// the captured credential is answered 401 and that 401 is recorded faithfully.
func (a *api) sendReplay(parent *http.Request, orig *store.Request, body []byte, meta proxy.ReplayMeta) (int, error) {
	req, err := http.NewRequestWithContext(parent.Context(), orig.Method, orig.Path, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("replay: build outbound request: %w", err)
	}
	if orig.ReqHeaders != "" {
		// Copied so a malformed header blob degrades to "no headers" rather
		// than failing the send: replay must still be able to try.
		headers := http.Header{}
		if err := json.Unmarshal([]byte(orig.ReqHeaders), &headers); err == nil {
			req.Header = headers
		}
	}
	// The row's remote_addr says where the call came from; a replayed call did
	// not come from the original client, and naming that plainly keeps a
	// replay distinguishable from the traffic it was re-issued from.
	req.RemoteAddr = "replay"

	rec := &statusRecorder{}
	a.proxyHandler.ServeHTTP(rec, proxy.WithReplay(req, meta))
	return rec.status(), nil
}

// statusRecorder is the http.ResponseWriter a replay's upstream response is
// written to. It records the status code and drops the body (see sendReplay).
// Flush is a no-op that keeps the proxy's FlushInterval: -1 path from
// degrading — the proxy flushes after every write so an SSE response reaches
// the client immediately, and there is no client here to reach.
type statusRecorder struct {
	code   int
	header http.Header
}

func (s *statusRecorder) Header() http.Header {
	if s.header == nil {
		s.header = http.Header{}
	}
	return s.header
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	s.WriteHeader(http.StatusOK)
	return len(p), nil
}

func (s *statusRecorder) Flush() {}

func (s *statusRecorder) status() int {
	if s.code == 0 {
		return http.StatusOK // an empty body still means the upstream answered
	}
	return s.code
}

// newestReplay returns the id of the newest recorded replay of origID, or 0
// when that capture has never been replayed.
func (a *api) newestReplay(ctx context.Context, origID int64) (int64, error) {
	rows, err := a.store.ListRequests(ctx, store.Filter{ReplayOf: &origID, Limit: 1})
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].ID, nil
}

// awaitReplayRow waits for the consumer to commit the row for the replay that
// was just sent and returns it.
//
// Polling is how this endpoint can name that row at all: its id is assigned by
// the writer, asynchronously, so the sender has no other handle on it. The
// retry is what makes that a non-issue rather than a race — see replayWait.
//
// ponytail: a bounded poll, not a completion hook on the consumer. Giving
// br-GI-1-07's write path a signalling channel for this one caller would be more
// machinery than a 2s deadline, and the deadline is already ~8× the consumer's
// own batch quiet window. Revisit if the write path ever gains a queue depth
// that makes a flat wait wrong.
func (a *api) awaitReplayRow(ctx context.Context, origID, afterID int64) (*store.Request, error) {
	deadline := time.Now().Add(replayWait)
	for {
		rows, err := a.store.ListRequests(ctx, store.Filter{ReplayOf: &origID, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 && rows[0].ID > afterID {
			return rows[0], nil
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("replay was sent but its row did not appear within %s; check `lens ls`", replayWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(replayPollInterval):
		}
	}
}

// replayOriginReject applies the Origin/Host allowlist shared by every
// write route (replay, POST /api/prices, POST /api/purge). It returns ""
// when the request may proceed, or the reason to report as a 403. It reads
// only headers — it never touches the store, the upstream, or the body — so
// a rejection costs nothing and can send nothing.
//
// action names the calling route in the rejection message ("replay",
// "prices", "purge"): the three write routes share this one guard, and
// without a parameter every rejection would talk about "replay" even when a
// browser had just hit POST /api/prices — a debugging trap for whoever gets
// turned away by a guard they don't recognize the name of.
//
// Two rejections, both about who can reach this route from a browser:
//
//   - Host must be loopback. A DNS-rebinding page resolves its own hostname to
//     127.0.0.1 and then POSTs with its own Host header, so a non-loopback Host
//     is the rebinding case even though the connection itself arrived over
//     loopback. r.Host (not RemoteAddr) is what a browser cannot forge into
//     loopback while still being a foreign origin.
//
//   - Origin, when present, must be this request's own origin. A browser sets
//     Origin on every cross-origin request and on same-origin POSTs, so
//     Origin == our own origin is the same-origin case and anything else is
//     another page driving this endpoint. "The dashboard's own origin" is
//     compared as the Origin's host:port against the request's own Host rather
//     than against the configured DashboardAddr, because the dashboard answers
//     on whichever loopback alias the user browsed to (localhost, 127.0.0.1,
//     [::1]) and the browser's same-origin rule is exactly this comparison. A
//     page served from a *different* loopback port is a different origin and is
//     rejected — correct, and the reason the comparison is not merely "is
//     Origin loopback too".
//
// A missing Origin passes. Browsers always send it, so its absence means a
// non-browser client, which for this endpoint means `lens replay` — the
// deliberate credentialless design in the bead's "Why no secret" (a local
// process that could forge past this could already read the SQLite file).
func replayOriginReject(r *http.Request, action string) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if !loopbackHost(host) {
		return fmt.Sprintf("%s requires a loopback Host, got %q", action, r.Host)
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		return ""
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return fmt.Sprintf("%s rejected origin %q: not a usable origin", action, origin)
	}
	if !strings.EqualFold(u.Host, r.Host) {
		return fmt.Sprintf("%s rejected cross-origin request: origin %q is not this dashboard's own origin %q", action, origin, r.Host)
	}
	return ""
}

// loopbackHost reports whether host — any port already stripped — is a loopback
// name or address. "localhost" is accepted by name because that is what a
// browser's Origin carries when the user browsed to localhost; the address
// forms (127.0.0.0/8, ::1) are accepted by net.IP.IsLoopback.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

// warningsSummary serves the warning inbox's groups: one entry per distinct
// (kind, severity), with a count taken over the entire matching set.
//
// The count has to come from SQL and not from a page of rows, because
// grouping and pagination do not compose — a page of N rows cannot yield a
// correct global per-kind total, however large N is. This is what fixes the
// latent undercount: the dashboard used to group at most DefaultLimit raw
// rows, so any kind with more occurrences than that reported a wrong count.
//
// There is deliberately no limit or offset, and none should be added. A
// GROUP BY result set is bounded by the number of distinct (kind, severity)
// pairs, not by row count, so it cannot grow with the table — and a
// paginated summary would put the UI right back where this ticket found it,
// unable to state a true group total. The *query* is unbounded, since
// counting correctly means scanning every warning; br-GI-15-01's covering
// index is what keeps that scan cheap.
func (a *api) warningsSummary(w http.ResponseWriter, r *http.Request) {
	since, err := parseSinceParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	// No Limit/Offset: the request carries none and the store reads none.
	groups, err := a.store.WarningSummary(r.Context(), store.Filter{
		Since: since, Kind: q.Get("kind"), Severity: q.Get("severity"),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (a *api) listWarnings(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	offset, err := parseOffset(r)
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
	f := store.Filter{
		Limit: limit, Offset: offset, Since: since,
		Kind: q.Get("kind"), Severity: q.Get("severity"),
	}

	warnings, err := a.store.ListWarnings(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, err := a.store.CountWarnings(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writePageHeaders(w, total, effectiveLimit(limit), offset)
	writeJSON(w, http.StatusOK, warnings)
}

func (a *api) listSessions(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	offset, err := parseOffset(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Every row store.ListSessions returns is maintained incrementally by
	// store.UpsertSession, which br-GI-1-12's consumer step calls once per
	// captured call. The aggregates on each row therefore need no work here.
	sessions, err := a.store.ListSessions(r.Context(), store.Filter{Limit: limit, Offset: offset})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// CountSessions takes no Filter: sessions have no filterable column, so
	// the total is simply the whole table.
	total, err := a.store.CountSessions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writePageHeaders(w, total, effectiveLimit(limit), offset)
	writeJSON(w, http.StatusOK, sessions)
}

// sessionDetail is /api/sessions/{id}'s response shape: the session's own
// fields (promoted from the embedded pointer, matching requestDetail) plus
// the calls that make it up and every warning any of them raised.
type sessionDetail struct {
	*store.Session
	// Calls is the session's calls in chronological order — the order the run
	// actually happened in. A session is one activity with N turns in it, so
	// the dashboard's running total only means anything read in that
	// direction. store.ListRequests returns newest first, so this is reversed
	// here rather than growing an ORDER BY parameter the store does not have.
	Calls []*store.Request `json:"calls"`
	// Warnings is the union of every warning raised across the session's
	// calls, one entry per occurrence. Grouping them into one line per kind
	// is the dashboard's job — but the union has to be assembled here,
	// because only the server knows which requests belong to the session.
	Warnings []*store.Warning `json:"warnings"`
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

	// One query for the session's calls, then one for the newest
	// store.DefaultLimit warnings filtered down to those calls' ids — the
	// same trade getRequest and internal/cli's show/ls already take, rather
	// than adding a per-session warning lookup to store.go.
	calls, err := a.store.ListRequests(r.Context(), store.Filter{SessionID: id})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	reversed := make([]*store.Request, len(calls))
	for i, c := range calls {
		reversed[len(calls)-1-i] = c
	}

	inSession := make(map[int64]bool, len(calls))
	for _, c := range calls {
		inSession[c.ID] = true
	}
	all, err := a.store.ListWarnings(r.Context(), store.Filter{Limit: store.DefaultLimit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var warnings []*store.Warning
	for _, wn := range all {
		if inSession[wn.RequestID] {
			warnings = append(warnings, wn)
		}
	}

	writeJSON(w, http.StatusOK, sessionDetail{Session: sess, Calls: reversed, Warnings: warnings})
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
	// ReplayEnabled is how the dashboard learns whether to offer the replay
	// editor at all: the button must be inert when the endpoint it would call
	// answers 403 (br-GI-1-13). It rides on /api/health, which is already the
	// dashboard's one view of server state.
	ReplayEnabled  bool   `json:"replay_enabled"`
	ReplayRejected uint64 `json:"replay_rejected"`
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
		ReplayEnabled:     a.replayEnabled,
		ReplayRejected:    a.replayRejected.Load(),
	}
	if !cs.LastWriteAt.IsZero() {
		t := cs.LastWriteAt
		resp.LastWriteAt = &t
		resp.LastWriteAgeMs = time.Since(t).Milliseconds()
	}
	writeJSON(w, http.StatusOK, resp)
}
