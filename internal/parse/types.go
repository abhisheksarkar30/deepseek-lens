// Package parse turns a captured response body into the token usage and
// metadata the rest of the system records. It is a pure-function package:
// no network I/O, no dependency on the proxy or sink — everything here
// operates on bytes already captured elsewhere. A body arrives either
// whole (non-streaming JSON) or incrementally via Parser.Feed (SSE).
package parse

// Usage is the token/metadata summary extracted from a response body,
// whether it arrived as a single non-streaming JSON body or as an SSE
// event stream.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	StopReason          string
	Model               string
	IsStream            bool
	Events              int
}
