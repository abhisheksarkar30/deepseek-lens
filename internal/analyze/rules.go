package analyze

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/abhisheksarkar30/deepseek-lens/internal/parse"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// ruleInput is everything a rule reads: the request's extracted values plus
// the config-resolved tables.
type ruleInput struct {
	meta  parse.Meta
	usage parse.Usage
	req   *store.Request
	opts  Rules
}

// ruleFunc is one entry in the rule table. Rules are independent — none
// reads another's output — so the table's order only decides the order
// warnings come back in. Each rule returns zero or one warning, except
// ruleUnsupportedContentBlocks, which returns one per offending block.
type ruleFunc func(ruleInput) []store.Warning

// rules is the compatibility matrix: every way DeepSeek's Anthropic endpoint
// diverges from what a Claude Code client believes it is asking for. A new
// divergence is one entry here and one case in analyze_test.go.
var rules = []ruleFunc{
	ruleCacheControlIgnored,
	ruleBudgetTokensIgnored,
	ruleTopPClamped,
	ruleTopPBelowFloor,
	ruleParallelToolUseIgnored,
	ruleModelRemapped,
	ruleModelMappingDrift,
	ruleUnsupportedContentBlocks,
	ruleTopKIgnored,
	ruleServiceTierIgnored,
	ruleContainerIgnored,
	ruleMCPServersIgnored,
	ruleMaxTokensExceedsCeiling,
	ruleHeaderIgnored,
	ruleUpstreamError,
	rulePeakPricing,
}

// ruleCacheControlIgnored: DeepSeek ignores cache_control outright, so a
// client that placed cache breakpoints gets no caching at all. It must name
// every site, or the user cannot tell which parts of their prompt setup were
// wasted effort.
func ruleCacheControlIgnored(in ruleInput) []store.Warning {
	if !in.meta.HasCacheControl {
		return nil
	}
	// Path carries a single site only when there is one; with several, the
	// Detail enumerates them and no one path is the right answer.
	path := ""
	if len(in.meta.CacheControlSites) == 1 {
		path = in.meta.CacheControlSites[0]
	}
	return []store.Warning{warn(KindCacheControlIgnored, sevWarn,
		fmt.Sprintf("client requested prompt caching at %s; DeepSeek ignores cache_control, so no caching occurs",
			strings.Join(in.meta.CacheControlSites, ", ")), path)}
}

// ruleBudgetTokensIgnored: the thinking block is honored, its budget is not.
func ruleBudgetTokensIgnored(in ruleInput) []store.Warning {
	if in.meta.ThinkingBudget == nil {
		return nil
	}
	return []store.Warning{warn(KindBudgetTokensIgnored, sevWarn,
		fmt.Sprintf("thinking.budget_tokens=%d is disregarded", *in.meta.ThinkingBudget),
		"thinking.budget_tokens")}
}

// ruleTopPClamped: outside thinking mode DeepSeek forces sampling to 1.0, so
// a client's top_p silently stops constraining anything.
func ruleTopPClamped(in ruleInput) []store.Warning {
	if in.meta.HasThinking || in.meta.TopP == nil || *in.meta.TopP == 1.0 {
		return nil
	}
	return []store.Warning{warn(KindTopPClamped, sevWarn,
		fmt.Sprintf("top_p=%g is ignored outside thinking mode; sampling is forced to 1.0", *in.meta.TopP),
		"top_p")}
}

// ruleTopPBelowFloor: the other half of the top_p story, and mutually
// exclusive with ruleTopPClamped by construction (one requires thinking, the
// other requires its absence). Inside thinking mode anything below 0.95 is
// silently raised to the floor, so the client's value is not what ran.
func ruleTopPBelowFloor(in ruleInput) []store.Warning {
	if !in.meta.HasThinking || in.meta.TopP == nil || *in.meta.TopP >= 0.95 {
		return nil
	}
	return []store.Warning{warn(KindTopPBelowFloor, sevWarn,
		fmt.Sprintf("top_p=%g is below DeepSeek's 0.95 floor in thinking mode", *in.meta.TopP),
		"top_p")}
}

// ruleParallelToolUseIgnored: a client that asked for serial tool calls gets
// no such guarantee.
func ruleParallelToolUseIgnored(in ruleInput) []store.Warning {
	if !in.meta.DisableParallelToolUse {
		return nil
	}
	return []store.Warning{warn(KindParallelToolUseIgnored, sevInfo,
		"tool_choice.disable_parallel_tool_use is ignored; DeepSeek decides tool-call parallelism itself",
		"tool_choice.disable_parallel_tool_use")}
}

// ruleModelRemapped reports what DeepSeek will actually serve for the model
// the client asked for. A recognized model is informational — the client's
// model does not exist upstream and a known substitute is expected — while an
// unrecognized one warns, because the substitute is then a guess and both
// cost and quality can land somewhere the client did not intend.
func ruleModelRemapped(in ruleInput) []store.Warning {
	if in.meta.ModelRequested == "" {
		return nil
	}
	target, recognized := in.opts.matchModel(in.meta.ModelRequested)
	if !recognized {
		return []store.Warning{warn(KindModelRemapped, sevWarn,
			fmt.Sprintf("unrecognized model %s falls back to %s — quality and cost may differ from expectation",
				in.meta.ModelRequested, target), "model")}
	}
	if strings.EqualFold(target, in.meta.ModelRequested) {
		return nil // already a DeepSeek model: nothing is remapped
	}
	return []store.Warning{warn(KindModelRemapped, sevInfo,
		fmt.Sprintf("%s → %s (billed at %s rates)", in.meta.ModelRequested, target, target), "model")}
}

// ruleModelMappingDrift is how this tool notices DeepSeek changing its
// mapping without anyone reading release notes: usage.Model is what upstream
// actually reported, so comparing it against what config predicted turns a
// stale map into a visible warning. It is checked only for a model the map
// recognized — an unrecognized model's warning already says the expectation
// is a guess, and a "drift" off a guess would be noise.
func ruleModelMappingDrift(in ruleInput) []store.Warning {
	if in.meta.ModelRequested == "" || in.usage.Model == "" {
		return nil
	}
	target, recognized := in.opts.matchModel(in.meta.ModelRequested)
	if !recognized || strings.EqualFold(target, in.usage.Model) {
		return nil
	}
	return []store.Warning{warn(KindModelMappingDrift, sevWarn,
		fmt.Sprintf("config maps %s to %s but the response reports %s — the model map may be stale",
			in.meta.ModelRequested, target, in.usage.Model), "model")}
}

// ruleUnsupportedContentBlocks is the one rule that can fire more than once
// for a request: each unsupported block type is its own error, because each
// names a different piece of the request that may be silently dropped. Path
// is empty — parse records the block *types* it found, not their sites.
func ruleUnsupportedContentBlocks(in ruleInput) []store.Warning {
	var out []store.Warning
	for _, typ := range in.meta.UnsupportedBlocks {
		out = append(out, warn(KindUnsupportedContentBlock, sevError,
			fmt.Sprintf("content block type '%s' is not supported by DeepSeek's Anthropic endpoint; this request may fail or the block may be dropped",
				typ), ""))
	}
	return out
}

// ruleTopKIgnored: top_k parses and then does nothing.
func ruleTopKIgnored(in ruleInput) []store.Warning {
	if in.meta.TopK == nil {
		return nil
	}
	return []store.Warning{warn(KindParamIgnored, sevInfo,
		fmt.Sprintf("top_k=%d is accepted and ignored by DeepSeek", *in.meta.TopK), "top_k")}
}

// ruleServiceTierIgnored: the value is named because a client that asked for
// a tier needs to know which one was dropped.
func ruleServiceTierIgnored(in ruleInput) []store.Warning {
	if in.meta.ServiceTier == "" {
		return nil
	}
	return []store.Warning{warn(KindParamIgnored, sevInfo,
		fmt.Sprintf("service_tier=%s is accepted and ignored by DeepSeek", in.meta.ServiceTier), "service_tier")}
}

// ruleContainerIgnored: an Anthropic container (code execution / file
// context) has no DeepSeek counterpart.
func ruleContainerIgnored(in ruleInput) []store.Warning {
	if !in.meta.ContainerPresent {
		return nil
	}
	return []store.Warning{warn(KindParamIgnored, sevInfo,
		"container is accepted and ignored by DeepSeek", "container")}
}

// ruleMCPServersIgnored: likewise for MCP server declarations.
func ruleMCPServersIgnored(in ruleInput) []store.Warning {
	if !in.meta.MCPServersPresent {
		return nil
	}
	return []store.Warning{warn(KindParamIgnored, sevInfo,
		"mcp_servers is accepted and ignored by DeepSeek", "mcp_servers")}
}

// ruleMaxTokensExceedsCeiling fires only for the model the request resolves
// to and only when config knows that model's ceiling; an unknown model skips
// the rule silently rather than warning off a guess.
func ruleMaxTokensExceedsCeiling(in ruleInput) []store.Warning {
	if in.meta.MaxTokens <= 0 || in.meta.ModelRequested == "" {
		return nil
	}
	target, _ := in.opts.matchModel(in.meta.ModelRequested)
	ceiling, known := in.opts.maxTokens[target]
	if !known || in.meta.MaxTokens <= ceiling {
		return nil
	}
	return []store.Warning{warn(KindParamIgnored, sevInfo,
		fmt.Sprintf("max_tokens=%d exceeds %s's %d-token ceiling; the request may be rejected or truncated",
			in.meta.MaxTokens, target, ceiling), "max_tokens")}
}

// ruleHeaderIgnored: the anthropic-beta header is a /messages feature
// negotiation, and DeepSeek's endpoint ignores it.
func ruleHeaderIgnored(in ruleInput) []store.Warning {
	if !in.meta.HasAnthropicBeta {
		return nil
	}
	return []store.Warning{warn(KindHeaderIgnored, sevInfo,
		"anthropic-beta is ignored for /messages", "anthropic-beta")}
}

// ruleUpstreamError surfaces an application-level error carried in the
// response body — an "error" object inside a 200, which is how the
// Anthropic-compatible endpoint reports overload and invalid requests once a
// stream has begun.
//
// This is not the same trigger as the consumer's own upstream_error warning
// (a transport-level failure from call.Err): both share the Kind because
// both mean the same thing to the user, but neither is a duplicate of the
// other and both can legitimately appear on one row.
func ruleUpstreamError(in ruleInput) []store.Warning {
	msg, ok := findAPIError(in.req.RespBody)
	if !ok {
		return nil
	}
	return []store.Warning{warn(KindUpstreamError, sevError, "upstream returned an error: "+msg, "error")}
}

// rulePeakPricing names the calls DeepSeek billed at its 2x peak rate, so a
// doubled cost_usd has an explanation attached rather than looking like
// unexplained drift.
//
// It asks Calendar.PeakPriced — the same predicate the per-session rollup
// calls — so the badge on a row and the header above it cannot restate the
// gate and drift apart. That predicate also carries the "only when the call
// was actually priced and carried spend" test this rule used to open with: a
// nil CostUSD is an unpriced call, and a configured model with zero tokens
// prices to a real, non-nil 0 — neither is something this rule can honestly
// claim was "billed at peak".
//
// The sentence names a working day rather than Mon-Fri. A 调休 make-up day is
// a Saturday that the calendar calls a work day, so the rule fires on it; a
// Mon-Fri window would then be false on the one day it was shown.
func rulePeakPricing(in ruleInput) []store.Warning {
	if !in.opts.calendar.PeakPriced(in.req.StartedAt, in.req.CostUSD) {
		return nil
	}
	return []store.Warning{warn(KindPeakPricing, sevWarn,
		"this call landed inside DeepSeek's peak-pricing window (01:00-04:00 or 06:00-10:00 UTC, on a working day); cost_usd reflects the 2x peak rate", "")}
}

// apiError is the error object's shape on the Anthropic-compatible wire:
// {"error":{"type":"overloaded_error","message":"Overloaded"}}.
type apiError struct {
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// findAPIError pulls the upstream error message out of a response body,
// whether it arrived whole (non-streaming JSON) or wrapped in an SSE "data:"
// line. Streaming is the normal case for a coding agent, so scanning only
// the whole-body form would miss most real upstream errors.
func findAPIError(body []byte) (string, bool) {
	if msg, ok := errorMessage(body); ok {
		return msg, true
	}
	for _, line := range bytes.Split(body, []byte("\n")) {
		data, found := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
		if !found {
			continue
		}
		if msg, ok := errorMessage(bytes.TrimSpace(data)); ok {
			return msg, true
		}
	}
	return "", false
}

// errorMessage decodes one candidate JSON body into an error message,
// preferring the human message but falling back to the error type so a
// message-less error is still surfaced rather than swallowed.
func errorMessage(raw []byte) (string, bool) {
	var e apiError
	if err := json.Unmarshal(raw, &e); err != nil || e.Error == nil {
		return "", false
	}
	switch {
	case e.Error.Message != "":
		return e.Error.Message, true
	case e.Error.Type != "":
		return e.Error.Type, true
	default:
		return "unknown upstream error", true
	}
}
