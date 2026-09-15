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

// Meta is everything the system needs to know about a request before it is
// stored, extracted from the captured request body and headers. It feeds
// three downstream features without any of them re-parsing the body:
// dropped-parameter detection, cost accounting, and session grouping.
type Meta struct {
	ModelRequested         string // as the client sent it, e.g. "claude-sonnet-5"
	Stream                 bool   // request-level "stream": true
	MaxTokens              int
	Temperature            *float64 // pointer: absent != 0
	TopP                   *float64
	TopK                   *int
	HasThinking            bool
	ThinkingBudget         *int     // thinking.budget_tokens (nil if absent)
	HasCacheControl        bool     // cache_control appears anywhere in the body
	CacheControlSites      []string // "tools[0]", "system[0]", "messages[3].content[1]"
	ToolCount              int
	ToolNames              []string
	ToolChoice             string // raw tool_choice type, e.g. "auto"
	DisableParallelToolUse bool
	ServiceTier            string   // raw service_tier value; "" if absent
	ContainerPresent       bool     // a container object is present
	MCPServersPresent      bool     // mcp_servers is present
	UnsupportedBlocks      []string // e.g. "document", "mcp_tool_use"
	MetadataUserID         string   // metadata.user_id, if present
	SessionHeader          string   // x-lens-session, if present
	HasAnthropicBeta       bool     // anthropic-beta request header present
	BodyBytes              int
	MessageCount           int
	PrefixHash             string // see ExtractMeta doc comment
}
