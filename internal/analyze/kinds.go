package analyze

import "slices"

// Kind is the machine-readable name of one finding. It is a distinct type
// rather than a bare string so the rule table can only name a kind declared
// here: a new rule that invents one does not compile, which is what keeps
// AllKinds' list from going stale.
type Kind string

// Every kind this package's rules can emit. A new DeepSeek divergence adds a
// constant here, an entry in allKinds below, and a rule in rules.go — the
// README's warning table is checked against this list, so those three edits
// are what keep the user-facing reference true.
const (
	KindCacheControlIgnored     Kind = "cache_control_ignored"
	KindBudgetTokensIgnored     Kind = "budget_tokens_ignored"
	KindTopPClamped             Kind = "top_p_clamped"
	KindTopPBelowFloor          Kind = "top_p_below_floor"
	KindParallelToolUseIgnored  Kind = "parallel_tool_use_ignored"
	KindModelRemapped           Kind = "model_remapped"
	KindModelMappingDrift       Kind = "model_mapping_drift"
	KindUnsupportedContentBlock Kind = "unsupported_content_block"
	KindParamIgnored            Kind = "param_ignored"
	KindHeaderIgnored           Kind = "header_ignored"
	KindUpstreamError           Kind = "upstream_error"
	KindPeakPricing             Kind = "peak_pricing"
)

// KindInfo pairs a Kind with the one sentence the README's warning table
// shows for it. The sentence is the product — a kind name alone teaches a
// reader nothing — so it lives beside the kind it describes rather than in
// the markdown, where nothing would notice it going stale.
//
// Descriptions must render inside a markdown table cell: no '|', no leading
// '>', and no newlines. readme_test.go asserts each one appears verbatim in
// the README.
type KindInfo struct {
	Kind        Kind
	Description string
}

// allKinds is the single source of truth for what this package can emit, in
// the order the README lists them. Both the warning table's completeness and
// the rule table's legitimacy are checked against it.
var allKinds = []KindInfo{
	{
		KindCacheControlIgnored,
		"The client put a `cache_control` breakpoint in the prompt; DeepSeek ignores it, so nothing is cached and the whole prompt is billed at the input rate on every call. `Path` names the site when there is one, and the detail lists every site when there are several.",
	},
	{
		KindBudgetTokensIgnored,
		"`thinking.budget_tokens` is disregarded. The thinking block itself is honored — only its budget is dropped.",
	},
	{
		KindTopPClamped,
		"`top_p` below 1.0 outside thinking mode: DeepSeek forces sampling to 1.0, so the value constrains nothing that runs.",
	},
	{
		KindTopPBelowFloor,
		"`top_p` below 0.95 inside thinking mode: DeepSeek raises it to its 0.95 floor, so the value that ran is not the one sent.",
	},
	{
		KindParallelToolUseIgnored,
		"`tool_choice.disable_parallel_tool_use` is ignored; DeepSeek decides tool-call parallelism itself, so serial tool calls are not guaranteed.",
	},
	{
		KindModelRemapped,
		"The model the client asked for is not one DeepSeek serves, so the model map's substitute serves it. `info` when the name was recognized (a known substitute is expected), `warn` when it was not — the substitute is then a guess.",
	},
	{
		KindModelMappingDrift,
		"The response reports a different resolved model than the model map predicted — the upstream mapping moved and the map is stale.",
	},
	{
		KindUnsupportedContentBlock,
		"A content block type DeepSeek's Anthropic endpoint does not support; the request may fail or the block may be dropped silently. One warning per offending block, so two unsupported blocks are two warnings.",
	},
	{
		KindParamIgnored,
		"A parameter DeepSeek accepts and then does nothing with. `Path` names which: `top_k`, `service_tier`, `container`, `mcp_servers`, or a `max_tokens` above the resolved model's known ceiling.",
	},
	{
		KindHeaderIgnored,
		"The `anthropic-beta` request header is ignored for `/messages`.",
	},
	{
		KindUpstreamError,
		"The upstream returned an error. Raised here when the error object arrives inside a 200 body (how the Anthropic wire reports overload and invalid requests once a stream has begun), and by the capture pipeline for a transport-level failure — the same kind, because it means the same thing to the reader.",
	},
	{
		KindPeakPricing,
		"The call landed inside DeepSeek's 01:00-04:00 or 06:00-10:00 UTC peak-pricing window on a working day, so `cost_usd` reflects DeepSeek's 2x peak rate rather than the configured off-peak one.",
	},
}

// AllKinds returns every warning kind this package can emit, in README order.
// A copy, so a caller cannot reorder the source of truth.
func AllKinds() []KindInfo { return slices.Clone(allKinds) }
