package store

import "time"

// Request is one proxied call, row-for-row with the requests table. Nullable
// columns (nothing populated until a later bead runs, or genuinely absent on
// this request) are pointer fields so nil round-trips as SQL NULL rather
// than as a zero value — see store.go's scan/insert code for the discipline
// this requires.
type Request struct {
	ID         int64
	StartedAt  time.Time
	TTFB       time.Duration
	Duration   time.Duration
	Method     string
	Path       string
	RemoteAddr string
	Status     int

	// ReqHeaders/RespHeaders are pre-redacted JSON (caller's job, per
	// internal/proxy/redact.go) — store persists them as opaque text and
	// never parses them, except RedactCheck's belt-and-braces scan.
	ReqHeaders  string
	RespHeaders string
	ReqBody     []byte
	RespBody    []byte

	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	StopReason          *string

	ModelRequested string // as the client sent it
	ModelResolved  string // as usage.Model reported it back; "" if unknown

	ErrorText *string

	SessionHeader *string // raw x-lens-session header value, if any
	SessionID     *string // resolved session key (br-GI-1-07/12), joins Session.ID

	CostUSD    *float64
	CostSource *string

	ReplayOf    *int64
	ReplayEdits *string // JSON blob describing what a replay changed

	PrefixHash string
}

// Warning is one analyzer finding attached to a Request (e.g. a dropped
// parameter, per CLAUDE.md). Kind/Severity/Detail are free-form strings
// owned by whichever analyzer bead (br-GI-1-10) produces them; Path names
// the offending site inside the request (e.g. "thinking.budget_tokens",
// "tools[0]", "anthropic-beta"), or "" when there is no single natural one —
// same dotted/bracket style as parse.Meta.CacheControlSites.
//
// No json tags: this type crosses the API and the SSE broker with Go's
// default capitalized field names, matching store.Request's existing
// convention (so the wire field is "Detail", and internal/web/app.js reads
// it as such).
type Warning struct {
	ID        int64
	RequestID int64
	Kind      string
	Severity  string
	Detail    string
	Path      string
	CreatedAt time.Time
}

// Session groups requests correlated by SessionHeader/PrefixHash and an
// inactivity gap (br-GI-1-07). ID is the session's correlation key, not a
// surrogate — it's what Request.SessionID points at.
type Session struct {
	ID           string
	FirstSeen    time.Time
	LastSeen     time.Time
	RequestCount int
}

// Filter narrows ListRequests and ListWarnings. Only the fields relevant to
// the call being made are read — ListRequests ignores Kind/Severity,
// ListWarnings ignores SessionID/Model/OnlyWarned/OnlyErrors.
type Filter struct {
	Limit      int // 0 means DefaultLimit, never unbounded
	Since      time.Time
	SessionID  string
	Model      string
	OnlyWarned bool
	OnlyErrors bool

	Kind     string
	Severity string
}

// Summary is StatsSummary's result: totals over a time window.
//
// UnpricedCount is the number of requests with no cost at all (cost_usd IS
// NULL, which is what an unpriced model or an absent price table writes). It
// is separate from CostUSDTotal because COALESCE(SUM(cost_usd), 0) cannot
// tell "the total is 0" from "N calls were never priced" — and presenting
// the second as the first is the exact bug br-GI-1-11 exists to prevent. A
// real $0.00 (zero tokens, or all tokens free) is priced, so it is not
// counted here.
type Summary struct {
	RequestCount        int
	ErrorCount          int
	WarningCount        int
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	CostUSDTotal        float64
	UnpricedCount       int
	DurationP50Ms       float64
	DurationP95Ms       float64
}

// ModelStat is one row of StatsByModel's result. UnpricedCount is that
// model's share of Summary.UnpricedCount.
type ModelStat struct {
	Model         string
	RequestCount  int
	InputTokens   int64
	OutputTokens  int64
	CostUSDTotal  float64
	UnpricedCount int
}

// DayStat is one row of StatsByDay's result, Day formatted "2006-01-02" in
// UTC. UnpricedCount is that day's share of Summary.UnpricedCount.
type DayStat struct {
	Day           string
	RequestCount  int
	InputTokens   int64
	OutputTokens  int64
	CostUSDTotal  float64
	UnpricedCount int
}

// CostSourceStat is one row of StatsByCostSource's result: how many requests
// in the window carry each cost_source, and what they summed to. Source is
// one of pricing's four values.
type CostSourceStat struct {
	Source       string
	RequestCount int
	CostUSDTotal float64
}
