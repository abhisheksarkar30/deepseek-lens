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

// WarningGroup is one row of WarningSummary's result: how many warnings of
// this (kind, severity) the matching set holds, and when the newest of them
// was raised.
//
// No json tags, same convention as Warning and Request above — the served
// keys are the capitalized Go field names ("Kind", "Count", "LastSeen"), so
// internal/web/app.js must read them capitalized too.
type WarningGroup struct {
	Kind     string
	Severity string
	Count    int
	LastSeen time.Time
}

// Session groups requests correlated by SessionHeader/PrefixHash and an
// inactivity gap (br-GI-1-07/12). ID is the session's correlation key, not a
// surrogate — it's what Request.SessionID points at.
//
// The totals from TotalInputTokens down are maintained incrementally by
// UpsertSession, one call at a time (br-GI-1-12), so the session list is
// O(1) per call rather than an aggregate over the requests table on every
// read. UnpricedCount is the session's share of br-GI-1-11's mixed-pricing
// rule: a session whose total covers only some of its calls must say so.
type Session struct {
	ID string
	// PrefixHash is the correlation key this session was resolved from, and
	// its nil-ness distinguishes the three ways a session can be keyed: nil =
	// an explicit x-lens-session header, "" = the null-prefix bucket for
	// unparseable bodies, anything else = a parse.PrefixHash. See
	// schema.sql's sessions table for why that matters.
	PrefixHash *string
	FirstSeen  time.Time
	LastSeen   time.Time

	RequestCount      int
	TotalInputTokens  int64
	TotalOutputTokens int64
	TotalCostUSD      float64
	PricedCount       int
	UnpricedCount     int
	ModelSet          string // comma-joined distinct upstream (model_resolved) names
	WarningCount      int
}

// Filter narrows ListRequests, ListWarnings, ListSessions, WarningSummary,
// and the CountRequests/CountWarnings counts. Only the fields relevant to
// the call being made are read — ListRequests ignores Kind/Severity,
// ListWarnings ignores SessionID/Model/OnlyWarned/OnlyErrors, ListSessions
// ignores everything but Limit/Offset, WarningSummary ignores
// Limit/Offset/SessionID/Model/OnlyWarned/OnlyErrors/ReplayOf, and
// CountRequests/CountWarnings honor their List twin's predicates while
// ignoring Limit/Offset (a total that shrank to the page size would defeat
// the count). CountSessions takes no Filter at all — sessions have no
// filterable column, so it reads none.
type Filter struct {
	Limit      int // 0 means DefaultLimit, never unbounded
	Offset     int // 0 means start at the top; a negative offset is clamped to 0
	Since      time.Time
	Until      time.Time // zero means unbounded; applied as started_at < ? (half-open)
	SessionID  string
	Model      string
	OnlyWarned bool
	OnlyErrors bool

	Kind     string
	Severity string

	// ReplayOf selects a capture's replays: the requests that were re-issued
	// from the given request id (br-GI-1-13). It is how the replay endpoint
	// finds the row its own send produced — the id it must report is assigned
	// by the writer, asynchronously, so replay_of is the only name for that row
	// that the sender can know in advance.
	ReplayOf *int64
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

// PeriodStat is one row of StatsByPeriod's result. Period is formatted per
// the requested granularity, all UTC: hour "2006-01-02T15:00", day
// "2006-01-02", week "2006-W02" (SQLite's %W — Monday-first week-of-year, not
// ISO-8601 numbering), month "2006-01". UnpricedCount is that period's share
// of Summary.UnpricedCount.
type PeriodStat struct {
	Period        string
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

// PurgeResult is what PurgeOlderThan and PurgeUnpriced both return —
// exported because it crosses into internal/api, where POST /api/purge
// reports it for either mode. No json tags, same convention as the rest of
// this file: internal/api owns the tagged wire shape.
type PurgeResult struct {
	Deleted            int64 // requests removed
	SessionsReconciled int   // distinct sessions touched
}
