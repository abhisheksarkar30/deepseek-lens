// Package analyze is the flagship rule engine (br-GI-1-10): given a
// request's extracted Meta, its upstream Usage, and the stored Request, it
// reports every client setting DeepSeek accepts and then silently drops,
// rewrites, or rejects as store.Warning rows.
//
// It is pure — no I/O, no store writes, no config mutation — so the whole
// compatibility matrix is testable from synthetic values. The matrix itself
// is the `rules` table in rules.go: a new DeepSeek divergence should be one
// entry there and one case in analyze_test.go, never a change to the
// pipeline. The consumer registers a Rules value after InsertRequest and
// attaches what Analyze returns to the row by id (see consumer.Analyzer).
package analyze

import (
	"strconv"
	"strings"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/parse"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// Severities a warning can carry. They are meaningful, not decorative: error
// means the request may have failed or lost content; warn means the client's
// belief and reality diverge in a way that affects cost, quality, or safety;
// info means a setting was dropped with no practical consequence.
const (
	sevError = "error"
	sevWarn  = "warn"
	sevInfo  = "info"
)

// fallbackPrefix is the model-map entry that catches every client model the
// table does not otherwise recognize.
const fallbackPrefix = "*"

// unknownModel is what a Detail names when the map has no catch-all entry,
// so the sentence still reads as a sentence.
const unknownModel = "a model DeepSeek chooses"

// modelMapping is one client-model prefix and the DeepSeek model that will
// serve it.
type modelMapping struct {
	Prefix string
	Target string
}

// Rules is the rule engine with its config-resolved tables baked in. Build
// one with NewRules; the zero value has no model map and so treats every
// model as unrecognized.
type Rules struct {
	modelMap  []modelMapping
	fallback  string
	maxTokens map[string]int
}

// NewRules resolves the engine's tables from config's raw delimited strings
// (Config.ModelMap and Config.ModelMaxTokens). A table that is unset, empty,
// or unparseable falls back to the built-in defaults rather than leaving the
// rules inert — an empty map would otherwise turn every request into an
// "unrecognized model" warning.
//
// Parsing is deliberately lenient: an entry without a ':' or with a
// non-numeric ceiling is skipped, not fatal. There is no per-entry format
// for Config.Validate to check, and a skipped entry degrades to the default
// or to "ceiling unknown" instead of refusing to start.
func NewRules(modelMap, maxTokens string) Rules {
	r := Rules{
		modelMap:  parseMappings(modelMap),
		maxTokens: parseCeilings(maxTokens),
	}
	if len(r.modelMap) == 0 {
		r.modelMap = parseMappings(config.DefaultModelMap)
	}
	if len(r.maxTokens) == 0 {
		r.maxTokens = parseCeilings(config.DefaultModelMaxTokens)
	}
	for _, m := range r.modelMap {
		if m.Prefix == fallbackPrefix {
			r.fallback = m.Target
		}
	}
	return r
}

// defaultRules is NewRules over the built-in defaults, resolved once so
// Analyze costs nothing per call.
var defaultRules = NewRules(config.DefaultModelMap, config.DefaultModelMaxTokens)

// Analyze runs the rule table against the built-in default model map. It is
// the bead's entry point and the config-free one; the running pipeline calls
// Rules.Analyze with the tables config actually resolved.
func Analyze(meta parse.Meta, usage parse.Usage, req *store.Request) []store.Warning {
	return defaultRules.Analyze(meta, usage, req)
}

// Analyze runs the rule table for one request. It returns nil — not an empty
// slice — when nothing fires, which is the common case for a request that
// asks for nothing DeepSeek ignores.
func (r Rules) Analyze(meta parse.Meta, usage parse.Usage, req *store.Request) []store.Warning {
	if req == nil {
		return nil
	}
	in := ruleInput{meta: meta, usage: usage, req: req, opts: r}

	var out []store.Warning
	for _, rule := range rules {
		out = append(out, rule(in)...)
	}
	if len(out) == 0 {
		return nil
	}

	// Stamp the row id and time once for the whole batch. InsertWarnings
	// keys off its own reqID argument, but it reads CreatedAt straight off
	// each warning, and the dashboard reads RequestID off the SSE event.
	now := time.Now()
	for i := range out {
		out[i].RequestID = req.ID
		out[i].CreatedAt = now
	}
	return out
}

// warn builds one warning. Severity and Kind come from the rule table, Path
// names the offending site (a dotted/bracket path into the request body, or
// the header name for a header-sourced rule), and Detail is the human
// sentence — the product, since a Kind alone teaches nothing.
//
// kind is a Kind rather than a string so the rule table can only name a kind
// declared in kinds.go; that is what keeps AllKinds — and the README table
// checked against it — from missing a kind some rule emits.
func warn(kind Kind, severity, detail, path string) store.Warning {
	return store.Warning{Kind: string(kind), Severity: severity, Detail: detail, Path: path}
}

// matchModel resolves a client model name against the map, returning the
// DeepSeek model that will serve it and whether the client's name was
// recognized at all. A name already equal to one of the map's targets is
// recognized as itself: nothing is remapped there, and warning that a model
// "falls back" to the very model it names would be a false positive.
//
// Matching is case-insensitive and on a prefix, per the bead — but on the
// prefix of the name or of any '-' separated segment of it, so the table's
// documented `opus` also covers the id a client actually sends,
// `claude-opus-5`.
func (r Rules) matchModel(requested string) (target string, recognized bool) {
	for _, m := range r.modelMap {
		if m.Prefix == fallbackPrefix {
			continue
		}
		if matchesPrefix(requested, m.Prefix) {
			return m.Target, true
		}
	}
	for _, m := range r.modelMap {
		if strings.EqualFold(m.Target, requested) {
			return m.Target, true
		}
	}
	if r.fallback == "" {
		return unknownModel, false
	}
	return r.fallback, false
}

// matchesPrefix reports whether model starts with prefix, either whole or at
// the start of one of its '-' separated segments.
func matchesPrefix(model, prefix string) bool {
	model, prefix = strings.ToLower(model), strings.ToLower(prefix)
	if strings.HasPrefix(model, prefix) {
		return true
	}
	for _, seg := range strings.Split(model, "-") {
		if strings.HasPrefix(seg, prefix) {
			return true
		}
	}
	return false
}

// parseMappings parses a comma-separated "prefix:target" table, skipping
// entries missing either half.
func parseMappings(s string) []modelMapping {
	var out []modelMapping
	for _, entry := range strings.Split(s, ",") {
		prefix, target, ok := strings.Cut(entry, ":")
		prefix, target = strings.TrimSpace(prefix), strings.TrimSpace(target)
		if !ok || prefix == "" || target == "" {
			continue
		}
		out = append(out, modelMapping{Prefix: prefix, Target: target})
	}
	return out
}

// parseCeilings parses a comma-separated "model:ceiling" table, skipping
// entries whose ceiling is not a positive integer.
func parseCeilings(s string) map[string]int {
	out := map[string]int{}
	for _, entry := range strings.Split(s, ",") {
		model, raw, ok := strings.Cut(entry, ":")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if model = strings.TrimSpace(model); model == "" || err != nil || n <= 0 {
			continue
		}
		out[model] = n
	}
	return out
}
