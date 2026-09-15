package analyze

import (
	"strings"
	"testing"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/parse"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// An almost-empty parse.Meta is already a clean baseline — no thinking, no
// top_p, no dropped parameters — so each case below sets only the fields its
// rule cares about. The clean cases name a DeepSeek model so that nothing is
// remapped for them.
func fp(f float64) *float64 { return &f }
func ip(i int) *int         { return &i }

// warning is one expected store.Warning, matched field-for-field. Detail is
// the product, so it is compared exactly rather than by substring.
type warning struct {
	kind     string
	severity string
	path     string
	detail   string
}

type testCase struct {
	name     string
	meta     parse.Meta
	usage    parse.Usage
	respBody string
	want     []warning // nil means "expect no warnings at all"
}

func (tc testCase) run(t *testing.T) {
	t.Helper()
	req := &store.Request{RespBody: []byte(tc.respBody)}
	got := Analyze(tc.meta, tc.usage, req)

	if len(tc.want) == 0 {
		if got != nil {
			t.Fatalf("Analyze() = %+v, want nil (not an empty slice)", got)
		}
		return
	}
	if len(got) != len(tc.want) {
		t.Fatalf("Analyze() returned %d warnings, want %d:\n got %s\nwant %s",
			len(got), len(tc.want), format(got), formatWant(tc.want))
	}
	for i, w := range tc.want {
		g := got[i]
		if g.Kind != w.kind || g.Severity != w.severity || g.Path != w.path || g.Detail != w.detail {
			t.Errorf("warning[%d]:\n got kind=%q severity=%q path=%q detail=%q\nwant kind=%q severity=%q path=%q detail=%q",
				i, g.Kind, g.Severity, g.Path, g.Detail, w.kind, w.severity, w.path, w.detail)
		}
	}
}

func format(ws []store.Warning) string {
	var b strings.Builder
	for _, w := range ws {
		b.WriteString("  [" + w.Severity + "] " + w.Kind + " @ " + w.Path + ": " + w.Detail + "\n")
	}
	return b.String()
}

func formatWant(ws []warning) string {
	var b strings.Builder
	for _, w := range ws {
		b.WriteString("  [" + w.severity + "] " + w.kind + " @ " + w.path + ": " + w.detail + "\n")
	}
	return b.String()
}

// TestRules is the bead's unit-test list: every rule with at least one
// positive and one negative case, including the boundaries.
func TestRules(t *testing.T) {
	cases := []testCase{
		// --- clean request: the most important negative case ---
		{
			name:     "clean request produces no warnings",
			meta:     parse.Meta{ModelRequested: "deepseek-v4-pro", MaxTokens: 4096, TopP: fp(1.0)},
			respBody: `{"model":"deepseek-v4-pro","stop_reason":"end_turn","usage":{"input_tokens":3}}`,
		},
		{
			name: "clean request with empty model produces no warnings",
			meta: parse.Meta{MaxTokens: 4096},
		},

		// --- cache_control ---
		{
			name: "cache_control at one site names it",
			meta: parse.Meta{
				ModelRequested:    "deepseek-v4-pro",
				HasCacheControl:   true,
				CacheControlSites: []string{"tools[0]"},
			},
			want: []warning{{
				"cache_control_ignored", "warn", "tools[0]",
				"client requested prompt caching at tools[0]; DeepSeek ignores cache_control, so no caching occurs",
			}},
		},
		{
			name: "cache_control at three sites lists all three",
			meta: parse.Meta{
				ModelRequested:    "deepseek-v4-pro",
				HasCacheControl:   true,
				CacheControlSites: []string{"tools[0]", "system[0]", "messages[3].content[1]"},
			},
			want: []warning{{
				"cache_control_ignored", "warn", "",
				"client requested prompt caching at tools[0], system[0], messages[3].content[1]; DeepSeek ignores cache_control, so no caching occurs",
			}},
		},
		{
			name: "cache_control absent produces no warning",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro"},
		},

		// --- thinking budget ---
		{
			name: "thinking budget present is disregarded",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", ThinkingBudget: ip(8000)},
			want: []warning{{
				"budget_tokens_ignored", "warn", "thinking.budget_tokens",
				"thinking.budget_tokens=8000 is disregarded",
			}},
		},
		{
			name: "thinking without a budget produces no warning",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", HasThinking: true, TopP: fp(0.95)},
		},

		// --- top_p, outside thinking mode ---
		{
			name: "top_p below 1.0 without thinking is clamped",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", TopP: fp(0.8)},
			want: []warning{{
				"top_p_clamped", "warn", "top_p",
				"top_p=0.8 is ignored outside thinking mode; sampling is forced to 1.0",
			}},
		},
		{
			name: "top_p exactly 1.0 without thinking produces no warning",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", TopP: fp(1.0)},
		},

		// --- top_p, inside thinking mode ---
		{
			name: "top_p below the floor with thinking is raised",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", HasThinking: true, TopP: fp(0.9)},
			want: []warning{{
				"top_p_below_floor", "warn", "top_p",
				"top_p=0.9 is below DeepSeek's 0.95 floor in thinking mode",
			}},
		},
		{
			name: "top_p exactly at the floor with thinking produces no warning",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", HasThinking: true, TopP: fp(0.95)},
		},

		// --- parallel tool use ---
		{
			name: "disable_parallel_tool_use true is ignored",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", DisableParallelToolUse: true},
			want: []warning{{
				"parallel_tool_use_ignored", "info", "tool_choice.disable_parallel_tool_use",
				"tool_choice.disable_parallel_tool_use is ignored; DeepSeek decides tool-call parallelism itself",
			}},
		},
		{
			name: "disable_parallel_tool_use false produces no warning",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro"},
		},

		// --- model remapping ---
		{
			name: "claude-opus-5 maps to deepseek-v4-pro",
			meta: parse.Meta{ModelRequested: "claude-opus-5"},
			want: []warning{{
				"model_remapped", "info", "model",
				"claude-opus-5 → deepseek-v4-pro (billed at deepseek-v4-pro rates)",
			}},
		},
		{
			name: "claude-sonnet-5 maps to deepseek-flash",
			meta: parse.Meta{ModelRequested: "claude-sonnet-5"},
			want: []warning{{
				"model_remapped", "info", "model",
				"claude-sonnet-5 → deepseek-flash (billed at deepseek-flash rates)",
			}},
		},
		{
			name: "claude-haiku-4-5 maps to deepseek-flash",
			meta: parse.Meta{ModelRequested: "claude-haiku-4-5"},
			want: []warning{{
				"model_remapped", "info", "model",
				"claude-haiku-4-5 → deepseek-flash (billed at deepseek-flash rates)",
			}},
		},
		{
			name: "unrecognized model warns about the fallback",
			meta: parse.Meta{ModelRequested: "gpt-4o"},
			want: []warning{{
				"model_remapped", "warn", "model",
				"unrecognized model gpt-4o falls back to deepseek-flash — quality and cost may differ from expectation",
			}},
		},
		{
			name: "model prefix matches case-insensitively",
			meta: parse.Meta{ModelRequested: "CLAUDE-OPUS-5"},
			want: []warning{{
				"model_remapped", "info", "model",
				"CLAUDE-OPUS-5 → deepseek-v4-pro (billed at deepseek-v4-pro rates)",
			}},
		},
		{
			name: "a model that is already a DeepSeek model is not remapped",
			meta: parse.Meta{ModelRequested: "deepseek-flash"},
		},

		// --- unsupported content blocks ---
		{
			name: "an unsupported block type is an error",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", UnsupportedBlocks: []string{"document"}},
			want: []warning{{
				"unsupported_content_block", "error", "",
				"content block type 'document' is not supported by DeepSeek's Anthropic endpoint; this request may fail or the block may be dropped",
			}},
		},
		{
			name: "two unsupported block types are two warnings, not one",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", UnsupportedBlocks: []string{"document", "mcp_tool_use"}},
			want: []warning{
				{
					"unsupported_content_block", "error", "",
					"content block type 'document' is not supported by DeepSeek's Anthropic endpoint; this request may fail or the block may be dropped",
				},
				{
					"unsupported_content_block", "error", "",
					"content block type 'mcp_tool_use' is not supported by DeepSeek's Anthropic endpoint; this request may fail or the block may be dropped",
				},
			},
		},

		// --- accepted-and-ignored parameters ---
		{
			name: "top_k is accepted and ignored",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", TopK: ip(5)},
			want: []warning{{
				"param_ignored", "info", "top_k",
				"top_k=5 is accepted and ignored by DeepSeek",
			}},
		},
		{
			name: "top_k absent produces no warning",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro"},
		},
		{
			name: "service_tier is accepted and ignored, naming the value",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", ServiceTier: "priority"},
			want: []warning{{
				"param_ignored", "info", "service_tier",
				"service_tier=priority is accepted and ignored by DeepSeek",
			}},
		},
		{
			name: "service_tier absent produces no warning",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro"},
		},
		{
			name: "container is accepted and ignored",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", ContainerPresent: true},
			want: []warning{{
				"param_ignored", "info", "container",
				"container is accepted and ignored by DeepSeek",
			}},
		},
		{
			name: "container absent produces no warning",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro"},
		},
		{
			name: "mcp_servers is accepted and ignored",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", MCPServersPresent: true},
			want: []warning{{
				"param_ignored", "info", "mcp_servers",
				"mcp_servers is accepted and ignored by DeepSeek",
			}},
		},
		{
			name: "mcp_servers absent produces no warning",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro"},
		},
		{
			name: "max_tokens above the model's ceiling is capped",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", MaxTokens: 999999},
			want: []warning{{
				"param_ignored", "info", "max_tokens",
				"max_tokens=999999 exceeds deepseek-v4-pro's 64000-token ceiling; the request may be rejected or truncated",
			}},
		},
		{
			name: "max_tokens at the model's ceiling produces no warning",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", MaxTokens: 64000},
		},
		{
			name: "max_tokens is judged against the fallback model's ceiling when unrecognized",
			meta: parse.Meta{ModelRequested: "deepseek-v9-unknown", MaxTokens: 999999},
			want: []warning{
				{
					"model_remapped", "warn", "model",
					"unrecognized model deepseek-v9-unknown falls back to deepseek-flash — quality and cost may differ from expectation",
				},
				{
					"param_ignored", "info", "max_tokens",
					"max_tokens=999999 exceeds deepseek-flash's 32000-token ceiling; the request may be rejected or truncated",
				},
			},
		},

		// --- header ---
		{
			name: "anthropic-beta is ignored",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro", HasAnthropicBeta: true},
			want: []warning{{
				"header_ignored", "info", "anthropic-beta",
				"anthropic-beta is ignored for /messages",
			}},
		},
		{
			name: "anthropic-beta absent produces no warning",
			meta: parse.Meta{ModelRequested: "deepseek-v4-pro"},
		},

		// --- upstream error ---
		{
			name:     "an error object in a 200 response body is an error",
			meta:     parse.Meta{ModelRequested: "deepseek-v4-pro"},
			respBody: `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			want: []warning{{
				"upstream_error", "error", "error",
				"upstream returned an error: Overloaded",
			}},
		},
		{
			name:     "an error object inside an SSE stream is an error too",
			meta:     parse.Meta{ModelRequested: "deepseek-v4-pro"},
			respBody: "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n",
			want: []warning{{
				"upstream_error", "error", "error",
				"upstream returned an error: Overloaded",
			}},
		},
		{
			name:     "a successful response body produces no upstream_error",
			meta:     parse.Meta{ModelRequested: "deepseek-v4-pro"},
			respBody: `{"model":"deepseek-v4-pro","content":[{"type":"text","text":"hi"}]}`,
		},

		// --- mapping drift ---
		{
			name:  "a response reporting a different model than config predicted drifts",
			meta:  parse.Meta{ModelRequested: "claude-opus-5"},
			usage: parse.Usage{Model: "deepseek-v5"},
			want: []warning{
				{
					"model_remapped", "info", "model",
					"claude-opus-5 → deepseek-v4-pro (billed at deepseek-v4-pro rates)",
				},
				{
					"model_mapping_drift", "warn", "model",
					"config maps claude-opus-5 to deepseek-v4-pro but the response reports deepseek-v5 — the model map may be stale",
				},
			},
		},
		{
			name:  "a response confirming the predicted model does not drift",
			meta:  parse.Meta{ModelRequested: "claude-opus-5"},
			usage: parse.Usage{Model: "deepseek-v4-pro"},
			want: []warning{{
				"model_remapped", "info", "model",
				"claude-opus-5 → deepseek-v4-pro (billed at deepseek-v4-pro rates)",
			}},
		},

		// --- multi-rule ---
		{
			name: "a request violating five rules yields exactly five warnings",
			meta: parse.Meta{
				ModelRequested:    "claude-opus-5",
				HasThinking:       true,
				ThinkingBudget:    ip(8000),
				TopP:              fp(0.8),
				TopK:              ip(5),
				HasCacheControl:   true,
				CacheControlSites: []string{"tools[0]"},
			},
			want: []warning{
				{
					"cache_control_ignored", "warn", "tools[0]",
					"client requested prompt caching at tools[0]; DeepSeek ignores cache_control, so no caching occurs",
				},
				{
					"budget_tokens_ignored", "warn", "thinking.budget_tokens",
					"thinking.budget_tokens=8000 is disregarded",
				},
				{
					"top_p_below_floor", "warn", "top_p",
					"top_p=0.8 is below DeepSeek's 0.95 floor in thinking mode",
				},
				{
					"model_remapped", "info", "model",
					"claude-opus-5 → deepseek-v4-pro (billed at deepseek-v4-pro rates)",
				},
				{
					"param_ignored", "info", "top_k",
					"top_k=5 is accepted and ignored by DeepSeek",
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t) })
	}
}

// TestNoDuplicateWarnings guards the multi-rule case from the other side:
// overlapping rules must not report the same finding twice.
func TestNoDuplicateWarnings(t *testing.T) {
	meta := parse.Meta{
		ModelRequested:    "claude-opus-5",
		HasThinking:       true,
		ThinkingBudget:    ip(8000),
		TopP:              fp(0.8),
		TopK:              ip(5),
		ServiceTier:       "priority",
		ContainerPresent:  true,
		MCPServersPresent: true,
		HasCacheControl:   true,
		CacheControlSites: []string{"tools[0]"},
		HasAnthropicBeta:  true,
		UnsupportedBlocks: []string{"document"},
		MaxTokens:         999999,
	}
	got := Analyze(meta, parse.Usage{Model: "deepseek-v4-pro"}, &store.Request{})

	seen := map[string]bool{}
	for _, w := range got {
		key := w.Kind + "|" + w.Path
		if seen[key] {
			t.Errorf("duplicate warning %q", key)
		}
		seen[key] = true
	}
	if len(got) != len(seen) {
		t.Errorf("got %d warnings but %d distinct (kind, path) pairs", len(got), len(seen))
	}
	// Every rule that should fire here has fired.
	for _, kind := range []string{
		"cache_control_ignored", "budget_tokens_ignored", "top_p_below_floor",
		"model_remapped", "unsupported_content_block", "param_ignored", "header_ignored",
	} {
		found := false
		for _, w := range got {
			if w.Kind == kind {
				found = true
			}
		}
		if !found {
			t.Errorf("no %s warning in %s", kind, format(got))
		}
	}
}

// TestUnsupportedBlockTypes covers every type parse.scanBlock can report, so
// a type added there without a Detail sentence here fails loudly.
func TestUnsupportedBlockTypes(t *testing.T) {
	// Mirrors parse's unexported unsupportedBlockTypes.
	types := []string{
		"document", "search_result", "redacted_thinking", "mcp_tool_use",
		"mcp_tool_result", "container_upload", "code_execution_tool_result", "server_tool_use",
	}
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			got := Analyze(parse.Meta{ModelRequested: "deepseek-v4-pro", UnsupportedBlocks: []string{typ}}, parse.Usage{}, &store.Request{})
			if len(got) != 1 {
				t.Fatalf("got %d warnings, want 1: %s", len(got), format(got))
			}
			w := got[0]
			if w.Kind != "unsupported_content_block" || w.Severity != "error" {
				t.Errorf("got %s/%s, want unsupported_content_block/error", w.Kind, w.Severity)
			}
			if !strings.Contains(w.Detail, "'"+typ+"'") {
				t.Errorf("Detail = %q, want it to name %q", w.Detail, typ)
			}
		})
	}
}

// TestAnalyzeStampsRowIDAndTime: the consumer hands these straight to
// InsertWarnings, which reads CreatedAt off each warning — an unstamped one
// would be recorded as 1970 and sort to the bottom of every dashboard list.
func TestAnalyzeStampsRowIDAndTime(t *testing.T) {
	before := time.Now()
	got := Analyze(parse.Meta{ModelRequested: "claude-opus-5"}, parse.Usage{}, &store.Request{ID: 42})
	if len(got) != 1 {
		t.Fatalf("got %d warnings, want 1", len(got))
	}
	if got[0].RequestID != 42 {
		t.Errorf("RequestID = %d, want 42", got[0].RequestID)
	}
	if got[0].CreatedAt.Before(before) || got[0].CreatedAt.After(time.Now()) {
		t.Errorf("CreatedAt = %v, want it stamped at Analyze time", got[0].CreatedAt)
	}
}

// TestRulesWithConfiguredModelMap drives the rules through a caller-supplied
// table rather than the built-in one — the path `lens serve` takes with
// whatever the user put in their config file.
func TestRulesWithConfiguredModelMap(t *testing.T) {
	rules := NewRules("fable:deepseek-v4-pro,*:deepseek-v4-pro", "deepseek-v4-pro:100")
	meta := parse.Meta{ModelRequested: "FABLE-9", MaxTokens: 200}

	got := rules.Analyze(meta, parse.Usage{}, &store.Request{})
	if len(got) != 2 {
		t.Fatalf("got %d warnings, want 2: %s", len(got), format(got))
	}
	if got[0].Detail != "FABLE-9 → deepseek-v4-pro (billed at deepseek-v4-pro rates)" {
		t.Errorf("detail = %q, want the configured map's target", got[0].Detail)
	}
	if got[1].Kind != "param_ignored" || !strings.Contains(got[1].Detail, "100-token ceiling") {
		t.Errorf("second warning = %q, want the configured 100-token ceiling", got[1].Detail)
	}
}

// TestMaxTokensCeilingUnknownIsSkipped: a model config has no ceiling for
// must skip the rule silently rather than warn off a guess.
func TestMaxTokensCeilingUnknownIsSkipped(t *testing.T) {
	r := NewRules("opus:deepseek-v4-pro", "deepseek-flash:1000")
	got := r.Analyze(parse.Meta{ModelRequested: "claude-opus-5", MaxTokens: 999999}, parse.Usage{}, &store.Request{})
	if len(got) != 1 || got[0].Kind != "model_remapped" {
		t.Fatalf("got %s, want only the model_remapped warning", format(got))
	}
}

// TestNewRulesFallsBackToDefaults: an empty or unparseable table must not
// leave the model rules inert — that would silently turn every request into
// an unrecognized-model warning and hide the real ones.
func TestNewRulesFallsBackToDefaults(t *testing.T) {
	for _, in := range []string{"", "   ", "no-colon-here", "opus:"} {
		t.Run(in, func(t *testing.T) {
			got := NewRules(in, in).Analyze(parse.Meta{ModelRequested: "claude-opus-5"}, parse.Usage{}, &store.Request{})
			if len(got) != 1 || got[0].Severity != "info" {
				t.Fatalf("got %s, want the built-in defaults' info-severity remap", format(got))
			}
		})
	}
}

// friPeak and friOff share the fixture date internal/pricing's own boundary
// tests use (Friday 2026-09-11) — see that package's pricing_test.go comment
// on why sharing the date is deliberate but detects no drift on its own.
var (
	friPeak = time.Date(2026, 9, 11, 7, 0, 0, 0, time.UTC)  // inside the window
	friOff  = time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC) // outside it
	friWknd = time.Date(2026, 9, 12, 7, 0, 0, 0, time.UTC)  // Saturday, same clock hour as friPeak
)

// TestRulePeakPricing is br-GI-4-03's case list: the rule fires only for a
// call that was both inside the peak window and actually priced with real
// spend.
func TestRulePeakPricing(t *testing.T) {
	cleanMeta := parse.Meta{ModelRequested: "deepseek-v4-pro"}

	t.Run("peak + priced", func(t *testing.T) {
		req := &store.Request{StartedAt: friPeak, CostUSD: fp(0.28)}
		got := Analyze(cleanMeta, parse.Usage{}, req)
		if len(got) != 1 || got[0].Kind != string(KindPeakPricing) || got[0].Severity != sevWarn {
			t.Fatalf("got %s, want exactly one warn-severity peak_pricing warning", format(got))
		}
	})

	t.Run("peak + unpriced", func(t *testing.T) {
		req := &store.Request{StartedAt: friPeak, CostUSD: nil}
		if got := Analyze(cleanMeta, parse.Usage{}, req); len(got) != 0 {
			t.Errorf("got %s, want no warnings for an unpriced call", format(got))
		}
	})

	t.Run("peak + zero-token configured", func(t *testing.T) {
		req := &store.Request{StartedAt: friPeak, CostUSD: fp(0)}
		if got := Analyze(cleanMeta, parse.Usage{}, req); len(got) != 0 {
			t.Errorf("got %s, want no warnings for a real, non-nil $0 cost", format(got))
		}
	})

	t.Run("off-peak + priced", func(t *testing.T) {
		req := &store.Request{StartedAt: friOff, CostUSD: fp(0.28)}
		if got := Analyze(cleanMeta, parse.Usage{}, req); len(got) != 0 {
			t.Errorf("got %s, want no warnings off-peak", format(got))
		}
	})

	t.Run("weekend + priced", func(t *testing.T) {
		req := &store.Request{StartedAt: friWknd, CostUSD: fp(0.28)}
		if got := Analyze(cleanMeta, parse.Usage{}, req); len(got) != 0 {
			t.Errorf("got %s, want no warnings on a weekend (the window is a weekday rule)", format(got))
		}
	})

	t.Run("composes with an existing rule", func(t *testing.T) {
		meta := parse.Meta{ModelRequested: "deepseek-v4-pro", TopP: fp(0.5)}
		req := &store.Request{StartedAt: friPeak, CostUSD: fp(0.5)}
		got := Analyze(meta, parse.Usage{}, req)
		if len(got) != 2 {
			t.Fatalf("got %d warnings, want 2:\n%s", len(got), format(got))
		}
		if got[0].Kind != string(KindTopPClamped) || got[1].Kind != string(KindPeakPricing) {
			t.Errorf("got kinds [%q, %q], want [%q, %q] — the rule table's order",
				got[0].Kind, got[1].Kind, KindTopPClamped, KindPeakPricing)
		}
	})
}
