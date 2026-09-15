package consumer

import (
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
// empty return means "no session" — Request.SessionID stays nil. This bead
// injects nil: the consumer nil-checks before calling, so SessionID is
// simply never set.
type SessionResolver interface {
	Resolve(meta parse.Meta, now time.Time) (sessionID string)
}
