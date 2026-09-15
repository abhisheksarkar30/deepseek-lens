# Bead br-GI-1-05: Request metadata extraction

**Plan Reference**: `docs/planning/GI-1-deepseek-lens-v1.md` §Bead sequence

- **Priority**: P1 (high)
- **Dependencies**: br-GI-1-01
- **Blocks**: br-GI-1-07, br-GI-1-10, br-GI-1-11, br-GI-1-12

## Description

Everything the system needs to know about a request *before* it is stored, extracted from the
captured request body and headers. Depends only on `encoding/json` and `crypto/sha256`.

`internal/parse/meta.go` exposes `ExtractMeta(reqBody []byte, headers http.Header) Meta`:

```
type Meta struct {
    ModelRequested string   // as the client sent it, e.g. "claude-sonnet-5"
    Stream         bool     // request-level "stream": true
    MaxTokens      int
    Temperature    *float64 // pointer: absent != 0
    TopP           *float64
    TopK           *int
    HasThinking    bool
    ThinkingBudget *int     // thinking.budget_tokens (nil if absent)
    HasCacheControl bool    // cache_control appears anywhere in the body
    CacheControlSites []string // "tools[0]", "system[0]", "messages[3].content[1]"
    ToolCount      int
    ToolNames      []string
    ToolChoice     string   // raw, plus DisableParallelToolUse bool
    DisableParallelToolUse bool
    ServiceTier    string   // raw service_tier value; "" if absent
    ContainerPresent bool   // a container object is present
    MCPServersPresent bool  // mcp_servers is present
    UnsupportedBlocks []string // e.g. "document", "mcp_tool_use"
    MetadataUserID string   // metadata.user_id, if present
    SessionHeader  string   // x-lens-session, if present
    HasAnthropicBeta bool   // anthropic-beta request header present
    BodyBytes      int
    MessageCount   int
    PrefixHash     string   // see below
}
```

`HasCacheControl` and `CacheControlSites` walk: `system[]`, `tools[]`, and
`messages[].content[]` — recording a **path string** per site so the dashboard can point at exactly
where the client asked for caching. Paths are human-readable indices, not JSON pointers.

`UnsupportedBlocks` scans every content block's `type` against the known-unsupported set:
`document`, `search_result`, `redacted_thinking`, `mcp_tool_use`, `mcp_tool_result`,
`container_upload`, `code_execution_tool_result`, `server_tool_use` (flagged only when it is not a
`web_search_tool_result` pairing). The set is a package-level slice so br-GI-1-10's rules and this
scanner share one source of truth.

`ServiceTier` records the raw `service_tier` value (`""` if absent); `ContainerPresent` and
`MCPServersPresent` record the presence of `container` / `mcp_servers` in the body; `HasAnthropicBeta`
records the presence of the `anthropic-beta` request header. All four are accepted-and-ignored by
DeepSeek, so they are captured here purely so br-GI-1-10 can raise `param_ignored` / `header_ignored`
on them — extraction for detection, not interpretation.

`PrefixHash` is the session-correlation key: SHA-256 over the concatenated `system` text plus the
first two `messages` entries, hex-encoded, truncated to 16 chars. It is computed here (cold path)
rather than in br-GI-1-12 so the column is populated from the first request and br-GI-1-12 only has to
apply the time-window grouping.

Malformed JSON returns `Meta{BodyBytes: len(reqBody)}` with zero values and no error — a request the
tool cannot parse must still be proxied and stored.

All accessors are nil-safe: a non-object `thinking`, a string where an array is expected, or a
`null` `metadata` must not panic. This is untrusted input from a third-party client and gets parsed
defensively.

## Rationale

This is the extraction layer that feeds three separate features: dropped-parameter detection
(br-GI-1-10) needs `HasCacheControl`, `ThinkingBudget`, `TopP`, `TopK` and `UnsupportedBlocks`;
cost accounting (br-GI-1-11) needs `ModelRequested`; session grouping (br-GI-1-12) needs `PrefixHash` and
`SessionHeader`. Building it once, as a pure function over bytes with a full `Meta` return, means
each feature is a consumer rather than a re-parser.

Doing it as one wide extraction also concentrates the defensive-parsing burden in a single
well-tested file, rather than scattering nil guards across three features.

## Outcome Definition

- `go test ./internal/parse/...` passes, including the malformed-input and nil-safety tables.
- A canonical agentic request yields every `Meta` field correctly populated.
- `CacheControlSites` reports precise indices for a request with caching on both a tool and a
  message.
- `PrefixHash` is stable for two requests sharing a prefix and differs when the prefix differs.
- Malformed JSON, `null` bodies, and type-confused fields never panic.

## Test Specifications

- Unit Tests (`internal/parse/meta_test.go`):
  - **Full realistic request** (system + 3 tools + 4 messages + thinking) → every field asserted.
  - **`stream: false`** → `Stream` false. **absent** → `Stream` false (not an error).
  - **`temperature: 0`** → `*Temperature == 0` and non-nil (distinguishes absent from zero).
  - **`temperature` absent** → nil, not 0.
  - **cache_control on a tool** → `HasCacheControl` true, site `tools[0]`.
  - **cache_control on `system[0]`** → correct site path.
  - **cache_control nested in `messages[2].content[1]`** → correct site path.
  - **cache_control absent** → false, empty sites.
  - **thinking present with budget** → `HasThinking` true, `ThinkingBudget` correct.
  - **thinking present without budget_tokens** → `HasThinking` true, budget nil.
  - **unsupported blocks**: one test per type in the set, plus a negative for `text`/`tool_use`.
  - **`tool_choice: {"type":"auto","disable_parallel_tool_use":true}`** → both fields captured.
  - **`metadata.user_id`** → captured; **`metadata: null`** → no panic.
  - **`x-lens-session` header** → `SessionHeader` captured; absent → "".
  - **`service_tier`** present → `ServiceTier` set to the raw value; absent → "".
  - **`container`** present → `ContainerPresent` true; absent → false.
  - **`mcp_servers`** present → `MCPServersPresent` true; absent → false.
  - **`anthropic-beta` header** → `HasAnthropicBeta` true; absent → false.
  - **Malformed JSON** (`{"messages":`) → zero `Meta`, `BodyBytes` correct, no panic, no error.
  - **Type confusion**: `messages` as a string, `system` as an object, `thinking` as a number,
    `tools` as `null` → no panic, degrades sensibly.
  - **Empty body** → zero `Meta`, no panic.
  - **`PrefixHash` stability**: two bodies sharing system + first two messages → equal hashes;
    differing system → different hashes.
  - **Very large body** (1000 messages) → completes without pathological allocation.
- Integration Tests: none (pure function).

## Files to Touch

- `internal/parse/meta.go` (create)
- `internal/parse/meta_test.go` (create)
- `internal/parse/types.go` (modify — add `Meta`)
