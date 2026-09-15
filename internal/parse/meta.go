package parse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
)

// unsupportedBlockTypes is the set of content-block "type" values DeepSeek
// silently drops or mishandles. It is a package-level slice so br-GI-1-10's
// warning rules and this scanner share one source of truth. server_tool_use
// is handled separately in scanBlock: it is unsupported unless its "name"
// is "web_search".
// maxSessionHeaderLen bounds x-lens-session before it is accepted as
// meta.SessionHeader, which session.Resolve uses verbatim as sessions.id.
// 200 is generous for a client-chosen id/label and small next to SQLite's
// own TEXT limits.
const maxSessionHeaderLen = 200

var unsupportedBlockTypes = []string{
	"document",
	"search_result",
	"redacted_thinking",
	"mcp_tool_use",
	"mcp_tool_result",
	"container_upload",
	"code_execution_tool_result",
	"server_tool_use",
}

// ExtractMeta pulls every request-level detail the rest of the system needs
// before storing a request out of the raw JSON body and headers. It is a
// pure function: no I/O, no error return. Malformed JSON or an empty body
// yields a zero Meta with only BodyBytes populated — a request this tool
// cannot parse must still be proxied and stored, so this never errors.
//
// Every accessor is nil-safe against type-confused input (a string where an
// array is expected, an object where a string is expected, a null field):
// this is untrusted third-party input, and a shape mismatch degrades to
// "field absent" rather than panicking.
//
// PrefixHash is the session-correlation key: SHA-256 over the JSON-encoded
// "system" value concatenated with the JSON encoding of the first two
// "messages" entries, hex-encoded and truncated to 16 chars. Re-encoding
// through encoding/json (rather than hashing the raw bytes) makes the hash
// depend only on content, not on incidental whitespace or key order.
func ExtractMeta(reqBody []byte, headers http.Header) Meta {
	m := Meta{BodyBytes: len(reqBody)}

	var body map[string]interface{}
	if err := json.Unmarshal(reqBody, &body); err != nil {
		return m
	}

	// An overlong x-lens-session is left empty rather than truncated:
	// unlike PrefixHash (a bounded 16-char hash), this value rides straight
	// into sessions.id verbatim (session.Resolve), so an unbounded client
	// header would mean an unbounded primary key. Silently truncating would
	// also risk colliding two different clients' ids onto the same session,
	// which is worse than falling back to the prefix/gap heuristic
	// session.Resolve already has for exactly this case.
	if sh := headers.Get("x-lens-session"); len(sh) <= maxSessionHeaderLen {
		m.SessionHeader = sh
	}
	m.HasAnthropicBeta = headers.Get("anthropic-beta") != ""

	m.ModelRequested = asString(body["model"])
	m.Stream = asBool(body["stream"])
	m.MaxTokens = asInt(body["max_tokens"])
	m.Temperature = asFloatPtr(body, "temperature")
	m.TopP = asFloatPtr(body, "top_p")
	m.TopK = asIntPtr(body, "top_k")
	m.ServiceTier = asString(body["service_tier"])

	if _, ok := body["container"]; ok {
		m.ContainerPresent = true
	}
	if _, ok := body["mcp_servers"]; ok {
		m.MCPServersPresent = true
	}

	if thinking, ok := asObject(body["thinking"]); ok {
		m.HasThinking = true
		m.ThinkingBudget = asIntPtr(thinking, "budget_tokens")
	}

	if tc, ok := body["tool_choice"]; ok {
		switch v := tc.(type) {
		case string:
			m.ToolChoice = v
		case map[string]interface{}:
			m.ToolChoice = asString(v["type"])
			m.DisableParallelToolUse = asBool(v["disable_parallel_tool_use"])
		}
	}

	if meta, ok := asObject(body["metadata"]); ok {
		m.MetadataUserID = asString(meta["user_id"])
	}

	var sites []string
	var unsupported []string

	if sysArr, ok := asArray(body["system"]); ok {
		for i, block := range sysArr {
			scanBlock(block, fmt.Sprintf("system[%d]", i), &sites, &unsupported)
		}
	}

	if toolArr, ok := asArray(body["tools"]); ok {
		m.ToolCount = len(toolArr)
		for i, tool := range toolArr {
			to, ok := asObject(tool)
			if !ok {
				continue
			}
			if name := asString(to["name"]); name != "" {
				m.ToolNames = append(m.ToolNames, name)
			}
			if _, has := to["cache_control"]; has {
				sites = append(sites, fmt.Sprintf("tools[%d]", i))
			}
		}
	}

	msgArr, _ := asArray(body["messages"])
	m.MessageCount = len(msgArr)
	for i, msg := range msgArr {
		msgObj, ok := asObject(msg)
		if !ok {
			continue
		}
		contentArr, ok := asArray(msgObj["content"])
		if !ok {
			continue
		}
		for j, block := range contentArr {
			scanBlock(block, fmt.Sprintf("messages[%d].content[%d]", i, j), &sites, &unsupported)
		}
	}

	m.CacheControlSites = sites
	m.HasCacheControl = len(sites) > 0
	m.UnsupportedBlocks = unsupported

	m.PrefixHash = prefixHash(body["system"], msgArr)

	return m
}

// scanBlock inspects one content block (a system[] entry or a
// messages[].content[] entry) for a cache_control marker and an
// unsupported "type", appending path to sites/unsupported as applicable.
// A non-object block (type confusion) is silently skipped.
func scanBlock(block interface{}, path string, sites, unsupported *[]string) {
	obj, ok := asObject(block)
	if !ok {
		return
	}
	if _, has := obj["cache_control"]; has {
		*sites = append(*sites, path)
	}
	t := asString(obj["type"])
	if t == "" {
		return
	}
	if t == "server_tool_use" {
		if asString(obj["name"]) == "web_search" {
			return
		}
		*unsupported = append(*unsupported, t)
		return
	}
	for _, u := range unsupportedBlockTypes {
		if u == t {
			*unsupported = append(*unsupported, t)
			return
		}
	}
}

// prefixHash computes the session-correlation hash described in
// ExtractMeta's doc comment. Encoding errors (none of these values can fail
// to marshal — they came from json.Unmarshal) are ignored defensively
// rather than propagated, consistent with ExtractMeta never erroring.
func prefixHash(system interface{}, messages []interface{}) string {
	h := sha256.New()
	sysBytes, _ := json.Marshal(system)
	h.Write(sysBytes)
	n := len(messages)
	if n > 2 {
		n = 2
	}
	for i := 0; i < n; i++ {
		b, _ := json.Marshal(messages[i])
		h.Write(b)
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:16]
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}

func asBool(v interface{}) bool {
	b, _ := v.(bool)
	return b
}

func asInt(v interface{}) int {
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return int(f)
}

func asObject(v interface{}) (map[string]interface{}, bool) {
	m, ok := v.(map[string]interface{})
	return m, ok
}

func asArray(v interface{}) ([]interface{}, bool) {
	a, ok := v.([]interface{})
	return a, ok
}

// asFloatPtr returns a pointer to obj[key] as a float64 if present and
// numeric, or nil otherwise. The pointer is what lets a caller distinguish
// "temperature: 0" (present, points at 0.0) from an absent field (nil) —
// a plain float64 return value could not make that distinction.
func asFloatPtr(obj map[string]interface{}, key string) *float64 {
	f, ok := obj[key].(float64)
	if !ok {
		return nil
	}
	return &f
}

// asIntPtr is asFloatPtr's integer counterpart: JSON numbers decode to
// float64 regardless of the source having an integer literal, so this
// truncates after confirming the value is numeric.
func asIntPtr(obj map[string]interface{}, key string) *int {
	f, ok := obj[key].(float64)
	if !ok {
		return nil
	}
	i := int(f)
	return &i
}
