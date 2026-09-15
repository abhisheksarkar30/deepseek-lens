package consumer

import (
	"context"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/parse"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// Analyzer is a post-insert warning rule (br-GI-1-10 registers the first
// one). It runs after store.InsertRequest, so req.ID is already set —
// warnings returned here are attached to the row by that id. A panic inside
// Analyze is recovered by the caller and turned into its own warning row
// naming the analyzer; implementations do not need their own recover.
//
// ponytail: a slice of one-method interfaces, not a registry with
// priorities — upgrade if ordering between analyzers ever matters.
type Analyzer interface {
	Analyze(meta parse.Meta, usage parse.Usage, req *store.Request) []store.Warning
}

// SessionResolver resolves the session a call belongs to from the extracted
// request Meta (notably PrefixHash and SessionHeader) and the current time
// (br-GI-1-12 needs "now" to apply an inactivity-gap cutoff). It runs
// before store.InsertRequest, since SessionID is a column on the row. An
// empty return means "no session" — Request.SessionID stays nil.
//
// It is deliberately read-only: the aggregates a session carries (tokens,
// cost, warning count) are not all known at this point — warning_count is
// only final after the analyzers have run — so persisting them belongs on
// the other side of the insert. See SessionAggregator.
type SessionResolver interface {
	Resolve(meta parse.Meta, now time.Time) (sessionID string)
}

// SessionAggregator folds one already-stored call into its session's running
// totals (br-GI-1-12). It is the second half of session grouping and exists
// because Resolve's signature cannot carry what it needs: usage and cost are
// known only after the pre-insert cost step, and warningCount only after the
// analyzers have run and their warnings have been attached by row id. The
// consumer therefore calls this once per call, last, after InsertRequest and
// InsertWarnings.
//
// It is a separate, optional interface rather than part of the consumer's
// narrow Store because it is not on the per-call hot read path and the
// existing tests' failing-store fake has no business implementing it —
// SetSessionAggregator installs it, and a Consumer without one behaves
// exactly as it did before this bead. An implementation failure is logged,
// not counted as a failed call: the row itself is already committed.
type SessionAggregator interface {
	RecordCall(ctx context.Context, sessionID string, req *store.Request, warningCount int) error
}
