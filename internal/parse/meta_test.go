package parse

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func headers(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func floatPtr(f float64) *float64 { return &f }
func intPtr(i int) *int           { return &i }

func TestExtractMeta_FullRealisticRequest(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-5",
		"stream": true,
		"max_tokens": 1024,
		"temperature": 0.7,
		"top_p": 0.9,
		"top_k": 40,
		"service_tier": "auto",
		"container": {"id": "c1"},
		"mcp_servers": [{"name": "s1"}],
		"thinking": {"type": "enabled", "budget_tokens": 2048},
		"tool_choice": {"type": "auto", "disable_parallel_tool_use": true},
		"metadata": {"user_id": "user_123"},
		"system": [
			{"type": "text", "text": "You are helpful.", "cache_control": {"type": "ephemeral"}}
		],
		"tools": [
			{"name": "get_weather"},
			{"name": "get_time", "cache_control": {"type": "ephemeral"}},
			{"name": "search"}
		],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hi"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "hello"}]},
			{"role": "user", "content": [{"type": "text", "text": "q"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "a"}]}
		]
	}`)
	h := headers("x-lens-session", "sess-1", "anthropic-beta", "some-beta-flag")

	m := ExtractMeta(body, h)

	if m.ModelRequested != "claude-sonnet-5" {
		t.Errorf("ModelRequested = %q, want claude-sonnet-5", m.ModelRequested)
	}
	if !m.Stream {
		t.Error("Stream = false, want true")
	}
	if m.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d, want 1024", m.MaxTokens)
	}
	if m.Temperature == nil || *m.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", m.Temperature)
	}
	if m.TopP == nil || *m.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", m.TopP)
	}
	if m.TopK == nil || *m.TopK != 40 {
		t.Errorf("TopK = %v, want 40", m.TopK)
	}
	if !m.HasThinking {
		t.Error("HasThinking = false, want true")
	}
	if m.ThinkingBudget == nil || *m.ThinkingBudget != 2048 {
		t.Errorf("ThinkingBudget = %v, want 2048", m.ThinkingBudget)
	}
	if !m.HasCacheControl {
		t.Error("HasCacheControl = false, want true")
	}
	wantSites := []string{"system[0]", "tools[1]"}
	if !equalStrings(m.CacheControlSites, wantSites) {
		t.Errorf("CacheControlSites = %v, want %v", m.CacheControlSites, wantSites)
	}
	if m.ToolCount != 3 {
		t.Errorf("ToolCount = %d, want 3", m.ToolCount)
	}
	wantNames := []string{"get_weather", "get_time", "search"}
	if !equalStrings(m.ToolNames, wantNames) {
		t.Errorf("ToolNames = %v, want %v", m.ToolNames, wantNames)
	}
	if m.ToolChoice != "auto" {
		t.Errorf("ToolChoice = %q, want auto", m.ToolChoice)
	}
	if !m.DisableParallelToolUse {
		t.Error("DisableParallelToolUse = false, want true")
	}
	if m.ServiceTier != "auto" {
		t.Errorf("ServiceTier = %q, want auto", m.ServiceTier)
	}
	if !m.ContainerPresent {
		t.Error("ContainerPresent = false, want true")
	}
	if !m.MCPServersPresent {
		t.Error("MCPServersPresent = false, want true")
	}
	if len(m.UnsupportedBlocks) != 0 {
		t.Errorf("UnsupportedBlocks = %v, want empty", m.UnsupportedBlocks)
	}
	if m.MetadataUserID != "user_123" {
		t.Errorf("MetadataUserID = %q, want user_123", m.MetadataUserID)
	}
	if m.SessionHeader != "sess-1" {
		t.Errorf("SessionHeader = %q, want sess-1", m.SessionHeader)
	}
	if !m.HasAnthropicBeta {
		t.Error("HasAnthropicBeta = false, want true")
	}
	if m.BodyBytes != len(body) {
		t.Errorf("BodyBytes = %d, want %d", m.BodyBytes, len(body))
	}
	if m.MessageCount != 4 {
		t.Errorf("MessageCount = %d, want 4", m.MessageCount)
	}
	if m.PrefixHash == "" || len(m.PrefixHash) != 16 {
		t.Errorf("PrefixHash = %q, want 16 hex chars", m.PrefixHash)
	}
}

func TestExtractMeta_Stream(t *testing.T) {
	got := ExtractMeta([]byte(`{"stream": false}`), nil)
	if got.Stream {
		t.Error("stream: false -> Stream true, want false")
	}
	got = ExtractMeta([]byte(`{}`), nil)
	if got.Stream {
		t.Error("stream absent -> Stream true, want false")
	}
}

func TestExtractMeta_TemperatureZeroVsAbsent(t *testing.T) {
	got := ExtractMeta([]byte(`{"temperature": 0}`), nil)
	if got.Temperature == nil {
		t.Fatal("temperature: 0 -> Temperature nil, want non-nil pointer to 0")
	}
	if *got.Temperature != 0 {
		t.Errorf("Temperature = %v, want 0", *got.Temperature)
	}

	got = ExtractMeta([]byte(`{}`), nil)
	if got.Temperature != nil {
		t.Errorf("temperature absent -> Temperature = %v, want nil", got.Temperature)
	}
}

func TestExtractMeta_CacheControlOnTool(t *testing.T) {
	body := []byte(`{"tools": [{"name": "a", "cache_control": {"type": "ephemeral"}}]}`)
	got := ExtractMeta(body, nil)
	if !got.HasCacheControl {
		t.Error("HasCacheControl = false, want true")
	}
	if !equalStrings(got.CacheControlSites, []string{"tools[0]"}) {
		t.Errorf("CacheControlSites = %v, want [tools[0]]", got.CacheControlSites)
	}
}

func TestExtractMeta_CacheControlOnSystem(t *testing.T) {
	body := []byte(`{"system": [{"type": "text", "text": "x", "cache_control": {"type": "ephemeral"}}]}`)
	got := ExtractMeta(body, nil)
	if !equalStrings(got.CacheControlSites, []string{"system[0]"}) {
		t.Errorf("CacheControlSites = %v, want [system[0]]", got.CacheControlSites)
	}
}

func TestExtractMeta_CacheControlNestedInMessage(t *testing.T) {
	body := []byte(`{"messages": [
		{"role": "user", "content": [{"type": "text", "text": "0"}]},
		{"role": "assistant", "content": [{"type": "text", "text": "1"}]},
		{"role": "user", "content": [
			{"type": "text", "text": "a"},
			{"type": "text", "text": "b", "cache_control": {"type": "ephemeral"}}
		]}
	]}`)
	got := ExtractMeta(body, nil)
	if !equalStrings(got.CacheControlSites, []string{"messages[2].content[1]"}) {
		t.Errorf("CacheControlSites = %v, want [messages[2].content[1]]", got.CacheControlSites)
	}
}

func TestExtractMeta_CacheControlAbsent(t *testing.T) {
	got := ExtractMeta([]byte(`{"messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}]}]}`), nil)
	if got.HasCacheControl {
		t.Error("HasCacheControl = true, want false")
	}
	if len(got.CacheControlSites) != 0 {
		t.Errorf("CacheControlSites = %v, want empty", got.CacheControlSites)
	}
}

func TestExtractMeta_ThinkingWithBudget(t *testing.T) {
	got := ExtractMeta([]byte(`{"thinking": {"type": "enabled", "budget_tokens": 4096}}`), nil)
	if !got.HasThinking {
		t.Error("HasThinking = false, want true")
	}
	if got.ThinkingBudget == nil || *got.ThinkingBudget != 4096 {
		t.Errorf("ThinkingBudget = %v, want 4096", got.ThinkingBudget)
	}
}

func TestExtractMeta_ThinkingWithoutBudget(t *testing.T) {
	got := ExtractMeta([]byte(`{"thinking": {"type": "enabled"}}`), nil)
	if !got.HasThinking {
		t.Error("HasThinking = false, want true")
	}
	if got.ThinkingBudget != nil {
		t.Errorf("ThinkingBudget = %v, want nil", got.ThinkingBudget)
	}
}

func TestExtractMeta_UnsupportedBlocks(t *testing.T) {
	for _, typ := range unsupportedBlockTypes {
		typ := typ
		t.Run(typ, func(t *testing.T) {
			body := []byte(fmt.Sprintf(
				`{"messages": [{"role": "user", "content": [{"type": %q}]}]}`, typ))
			got := ExtractMeta(body, nil)
			if !contains(got.UnsupportedBlocks, typ) {
				t.Errorf("UnsupportedBlocks = %v, want to contain %q", got.UnsupportedBlocks, typ)
			}
		})
	}

	t.Run("negative_text", func(t *testing.T) {
		body := []byte(`{"messages": [{"role": "user", "content": [{"type": "text", "text": "x"}]}]}`)
		got := ExtractMeta(body, nil)
		if len(got.UnsupportedBlocks) != 0 {
			t.Errorf("UnsupportedBlocks = %v, want empty for type text", got.UnsupportedBlocks)
		}
	})

	t.Run("negative_tool_use", func(t *testing.T) {
		body := []byte(`{"messages": [{"role": "assistant", "content": [{"type": "tool_use", "name": "x"}]}]}`)
		got := ExtractMeta(body, nil)
		if len(got.UnsupportedBlocks) != 0 {
			t.Errorf("UnsupportedBlocks = %v, want empty for type tool_use", got.UnsupportedBlocks)
		}
	})

	t.Run("server_tool_use_web_search_not_flagged", func(t *testing.T) {
		body := []byte(`{"messages": [{"role": "assistant", "content": [{"type": "server_tool_use", "name": "web_search"}]}]}`)
		got := ExtractMeta(body, nil)
		if contains(got.UnsupportedBlocks, "server_tool_use") {
			t.Errorf("UnsupportedBlocks = %v, want server_tool_use excluded for web_search", got.UnsupportedBlocks)
		}
	})

	t.Run("server_tool_use_other_name_flagged", func(t *testing.T) {
		body := []byte(`{"messages": [{"role": "assistant", "content": [{"type": "server_tool_use", "name": "code_exec"}]}]}`)
		got := ExtractMeta(body, nil)
		if !contains(got.UnsupportedBlocks, "server_tool_use") {
			t.Errorf("UnsupportedBlocks = %v, want server_tool_use flagged for non-web_search name", got.UnsupportedBlocks)
		}
	})
}

func TestExtractMeta_ToolChoice(t *testing.T) {
	body := []byte(`{"tool_choice": {"type": "auto", "disable_parallel_tool_use": true}}`)
	got := ExtractMeta(body, nil)
	if got.ToolChoice != "auto" {
		t.Errorf("ToolChoice = %q, want auto", got.ToolChoice)
	}
	if !got.DisableParallelToolUse {
		t.Error("DisableParallelToolUse = false, want true")
	}
}

func TestExtractMeta_Metadata(t *testing.T) {
	got := ExtractMeta([]byte(`{"metadata": {"user_id": "u1"}}`), nil)
	if got.MetadataUserID != "u1" {
		t.Errorf("MetadataUserID = %q, want u1", got.MetadataUserID)
	}

	got = ExtractMeta([]byte(`{"metadata": null}`), nil)
	if got.MetadataUserID != "" {
		t.Errorf("metadata: null -> MetadataUserID = %q, want empty", got.MetadataUserID)
	}
}

func TestExtractMeta_SessionHeader(t *testing.T) {
	got := ExtractMeta([]byte(`{}`), headers("x-lens-session", "abc"))
	if got.SessionHeader != "abc" {
		t.Errorf("SessionHeader = %q, want abc", got.SessionHeader)
	}
	got = ExtractMeta([]byte(`{}`), nil)
	if got.SessionHeader != "" {
		t.Errorf("SessionHeader = %q, want empty", got.SessionHeader)
	}
}

func TestExtractMeta_ServiceTier(t *testing.T) {
	got := ExtractMeta([]byte(`{"service_tier": "priority"}`), nil)
	if got.ServiceTier != "priority" {
		t.Errorf("ServiceTier = %q, want priority", got.ServiceTier)
	}
	got = ExtractMeta([]byte(`{}`), nil)
	if got.ServiceTier != "" {
		t.Errorf("ServiceTier = %q, want empty", got.ServiceTier)
	}
}

func TestExtractMeta_ContainerPresent(t *testing.T) {
	got := ExtractMeta([]byte(`{"container": {"id": "c"}}`), nil)
	if !got.ContainerPresent {
		t.Error("ContainerPresent = false, want true")
	}
	got = ExtractMeta([]byte(`{}`), nil)
	if got.ContainerPresent {
		t.Error("ContainerPresent = true, want false")
	}
}

func TestExtractMeta_MCPServersPresent(t *testing.T) {
	got := ExtractMeta([]byte(`{"mcp_servers": [{"name": "s"}]}`), nil)
	if !got.MCPServersPresent {
		t.Error("MCPServersPresent = false, want true")
	}
	got = ExtractMeta([]byte(`{}`), nil)
	if got.MCPServersPresent {
		t.Error("MCPServersPresent = true, want false")
	}
}

func TestExtractMeta_AnthropicBetaHeader(t *testing.T) {
	got := ExtractMeta([]byte(`{}`), headers("anthropic-beta", "flag-1"))
	if !got.HasAnthropicBeta {
		t.Error("HasAnthropicBeta = false, want true")
	}
	got = ExtractMeta([]byte(`{}`), nil)
	if got.HasAnthropicBeta {
		t.Error("HasAnthropicBeta = true, want false")
	}
}

func TestExtractMeta_MalformedJSON(t *testing.T) {
	body := []byte(`{"messages":`)
	got := ExtractMeta(body, nil)
	want := Meta{BodyBytes: len(body)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestExtractMeta_TypeConfusion(t *testing.T) {
	t.Run("messages_as_string", func(t *testing.T) {
		got := ExtractMeta([]byte(`{"messages": "not an array"}`), nil)
		if got.MessageCount != 0 {
			t.Errorf("MessageCount = %d, want 0", got.MessageCount)
		}
	})

	t.Run("system_as_object", func(t *testing.T) {
		got := ExtractMeta([]byte(`{"system": {"foo": "bar"}}`), nil)
		if got.HasCacheControl {
			t.Error("HasCacheControl = true, want false")
		}
	})

	t.Run("thinking_as_number", func(t *testing.T) {
		got := ExtractMeta([]byte(`{"thinking": 5}`), nil)
		if got.HasThinking {
			t.Error("HasThinking = true, want false")
		}
		if got.ThinkingBudget != nil {
			t.Errorf("ThinkingBudget = %v, want nil", got.ThinkingBudget)
		}
	})

	t.Run("tools_as_null", func(t *testing.T) {
		got := ExtractMeta([]byte(`{"tools": null}`), nil)
		if got.ToolCount != 0 {
			t.Errorf("ToolCount = %d, want 0", got.ToolCount)
		}
		if got.ToolNames != nil {
			t.Errorf("ToolNames = %v, want nil", got.ToolNames)
		}
	})
}

func TestExtractMeta_EmptyBody(t *testing.T) {
	got := ExtractMeta(nil, nil)
	want := Meta{BodyBytes: 0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	got = ExtractMeta([]byte{}, nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestExtractMeta_PrefixHashStability(t *testing.T) {
	bodyA := []byte(`{
		"system": [{"type": "text", "text": "same system"}],
		"messages": [
			{"role": "user", "content": "one"},
			{"role": "assistant", "content": "two"},
			{"role": "user", "content": "three"}
		]
	}`)
	// Same system + first two messages, different whitespace and a
	// different third message: hash must still match.
	bodyB := []byte(`{"system":[{"type":"text","text":"same system"}],"messages":[{"role":"user","content":"one"},{"role":"assistant","content":"two"},{"role":"user","content":"DIFFERENT"}]}`)
	bodyC := []byte(`{
		"system": [{"type": "text", "text": "different system"}],
		"messages": [
			{"role": "user", "content": "one"},
			{"role": "assistant", "content": "two"}
		]
	}`)

	hA := ExtractMeta(bodyA, nil).PrefixHash
	hB := ExtractMeta(bodyB, nil).PrefixHash
	hC := ExtractMeta(bodyC, nil).PrefixHash

	if hA != hB {
		t.Errorf("PrefixHash differs for shared system+first-two-messages: %q vs %q", hA, hB)
	}
	if hA == hC {
		t.Errorf("PrefixHash equal despite differing system: %q", hA)
	}
}

func TestExtractMeta_VeryLargeBody(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"messages": [`)
	for i := 0; i < 1000; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(fmt.Sprintf(`{"role": "user", "content": [{"type": "text", "text": "msg %d"}]}`, i))
	}
	sb.WriteString(`]}`)

	got := ExtractMeta([]byte(sb.String()), nil)
	if got.MessageCount != 1000 {
		t.Errorf("MessageCount = %d, want 1000", got.MessageCount)
	}
	if got.PrefixHash == "" {
		t.Error("PrefixHash empty for large body")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
