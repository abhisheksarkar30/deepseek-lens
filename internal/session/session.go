// Package session groups individual calls into the agentic runs they belong
// to (br-GI-1-12), so the dashboard and CLI answer "this run cost $X across
// N turns" instead of showing an undifferentiated call log.
//
// One Resolver is both halves of the grouping: Resolve (a
// consumer.SessionResolver) runs before store.InsertRequest and decides which
// session a call belongs to; RecordCall (a consumer.SessionAggregator) runs
// after the analyzers and folds the call's tokens, cost, and warning count
// into that session's running totals. `lens serve` installs the same object
// for both.
//
// # ponytail: prefix-hash + time-window heuristic
//
// Resolution is approximate on purpose. Two distinct runs that happen to
// share an identical opening prompt (two "fix the build" sessions started
// within half an hour of each other, say) will be merged into one session,
// and a single run that pauses for longer than the gap will be split in two.
// Exact correlation needs either a client-supplied id, which no current tool
// sends, or full conversation-state tracking — expensive and still ambiguous.
// The explicit `x-lens-session` request header is the upgrade path and
// already works: it overrides both heuristics verbatim and is the answer for
// anyone who needs exactness. The next step, if prefix-plus-window proves too
// coarse, is a content-aware correlation that matches the growing
// conversation prefix rather than just its head.
//
// This ceiling is documented in the README as well; both statements are
// deliberate, because a heuristic that is hidden is a wrong answer rather
// than an approximation.
package session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/parse"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// nullPrefixWindow is the inactivity window applied to calls whose body did
// not parse (an empty PrefixHash). It is deliberately much shorter than the
// configured session gap: a null-prefix session is keyed *only* by timing, so
// a long window would let unrelated broken requests accumulate in one
// ever-growing bucket, which is exactly what rule 3 exists to prevent.
// Fixed rather than configurable — it is a damage limiter, not a preference.
const nullPrefixWindow = 5 * time.Minute

// Resolver resolves and maintains sessions against a store. Construct one
// with New; it is safe for use by the single consumer goroutine that owns
// the store's writer.
type Resolver struct {
	store *store.Store
	gap   time.Duration
}

// New returns a Resolver whose inactivity gap is gapMinutes — config's
// SessionGapMinutes (default 30, validated positive).
func New(st *store.Store, gapMinutes int) *Resolver {
	return &Resolver{store: st, gap: time.Duration(gapMinutes) * time.Minute}
}

// Resolve implements consumer.SessionResolver. First match wins:
//
//  1. A non-empty meta.SessionHeader is the session id verbatim. It always
//     wins, regardless of prefix or gap.
//  2. Otherwise the most recently active session with the same
//     meta.PrefixHash, if its last_seen is within the configured gap
//     (inclusive), takes the call; otherwise a fresh id is minted with the
//     same prefix hash.
//  3. A call with an empty PrefixHash (unparseable body) looks up the
//     null-prefix bucket under nullPrefixWindow instead.
//
// Resolve is read-only: the row it names is inserted or updated by
// RecordCall, once the call's warning count is known. That leaves one write
// per call instead of two, and keeps this side a pure lookup.
//
// The gap comparison is one-sided: a call *older* than the session's
// last_seen has a negative gap, which is within any window, so an
// out-of-order call attaches to the session rather than starting a new one.
// last_seen itself is only ever raised (store.UpsertSession MAXes it), so it
// keeps meaning "the session's most recent activity" whatever order calls
// arrive in.
//
// An empty return means "no session" and is the failure mode too: a store
// error while looking up the previous session degrades to no grouping for
// that call rather than to a wrong grouping or a failed call (CLAUDE.md's
// "fail open"). The interface carries no context, so lookups run on
// context.Background() — their only bound is the store's busy timeout.
func (r *Resolver) Resolve(meta parse.Meta, now time.Time) string {
	if meta.SessionHeader != "" {
		return meta.SessionHeader
	}

	window := r.gap
	if meta.PrefixHash == "" {
		window = nullPrefixWindow
	}

	prev, err := r.store.LatestSessionByPrefix(context.Background(), meta.PrefixHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if prev != nil && now.Sub(prev.LastSeen) <= window {
		return prev.ID
	}
	return newID(now, meta.PrefixHash)
}

// RecordCall implements consumer.SessionAggregator: it folds one stored call
// into sessionID's running totals. req.StartedAt, not the resolution time,
// is what FirstSeen/LastSeen are updated with — it is the call's own
// timestamp, and the store MINes/MAXes them, so the session's bounds are
// independent of the order calls happen to arrive in.
//
// The row's prefix_hash is nil for a header-keyed session and req.PrefixHash
// otherwise. That single decision is what keeps a header-bearing call and a
// header-less call with the same body prefix out of each other's sessions:
// only prefix-hash-keyed rows are visible to the rule-2 lookup.
func (r *Resolver) RecordCall(ctx context.Context, sessionID string, req *store.Request, warningCount int) error {
	var prefix *string
	if req.SessionHeader == nil || *req.SessionHeader != sessionID {
		p := req.PrefixHash
		prefix = &p
	}

	priced, cost := 0, 0.0
	if req.CostUSD != nil {
		priced, cost = 1, *req.CostUSD
	}

	return r.store.UpsertSession(ctx, &store.Session{
		ID:         sessionID,
		PrefixHash: prefix,
		FirstSeen:  req.StartedAt,
		LastSeen:   req.StartedAt,

		RequestCount:      1,
		TotalInputTokens:  int64(req.InputTokens),
		TotalOutputTokens: int64(req.OutputTokens),
		TotalCostUSD:      cost,
		PricedCount:       priced,
		UnpricedCount:     1 - priced,
		ModelSet:          req.ModelResolved,
		WarningCount:      warningCount,
	})
}

// newID mints a session id in the bead's format, s_<unix-ms>_<8 hex chars of
// prefix hash>: sortable by time, greppable by prefix, and collision-resistant
// enough for a local tool. An unparseable body has no prefix hash to name, so
// the eight hex characters are the first eight of SHA-256 over the empty
// string ("e3b0c442") — a fixed, documented placeholder, which keeps null-
// prefix ids matching the same format as every other one. Such sessions are
// told apart by their timestamp, which is what nullPrefixWindow is sized for.
func newID(now time.Time, prefixHash string) string {
	return fmt.Sprintf("s_%d_%s", now.UnixMilli(), prefix8(prefixHash))
}

// prefix8 returns the eight hex characters of a prefix hash that go into a
// session id. parse always produces 16, but a hand-built Meta (or a future
// cheaper hash) need not, so anything shorter is hashed to a deterministic
// eight rather than panicking on a slice.
func prefix8(prefixHash string) string {
	if len(prefixHash) >= 8 {
		return prefixHash[:8]
	}
	sum := sha256.Sum256([]byte(prefixHash))
	return hex.EncodeToString(sum[:])[:8]
}
