# Bead 10: Silently-dropped-parameter detection

- **Priority**: P0 (critical) — this is the flagship feature
- **Dependencies**: 5, 7, 9
- **Blocks**: none

## Description

The reason this project exists. A pure, table-driven rule engine that reports every client setting
DeepSeek accepts and then silently ignores, rewrites, or rejects.

`internal/analyze` exposes exactly one entry point:

```
func Analyze(meta parse.Meta, usage parse.Usage, respBody []byte) []store.Warning
```

Pure function over already-extracted values — no I/O, no store, no config mutation. Registered into
the consumer's `Analyzer` slice (bead 7), so no existing pipeline code changes.

```
store.Warning{Kind, Severity, Detail string, Path string}
// Severity: "error" | "warn" | "info"
```

**Rules** (each its own function in `rules.go`, each with a `Detail` string written for a human
reading the dashboard, not a log parser):

| Kind | Trigger | Severity | Detail |
|---|---|---|---|
| `cache_control_ignored` | `meta.HasCacheControl` | warn | Names each site from `CacheControlSites` — "client requested prompt caching at tools[0], system[0]; DeepSeek ignores cache_control, so no caching occurs" |
| `budget_tokens_ignored` | `meta.ThinkingBudget != nil` | warn | "thinking.budget_tokens=N is disregarded" |
| `top_p_clamped` | `meta.TopP != nil && *meta.TopP != 1.0 && !meta.HasThinking` | warn | "top_p=0.8 is ignored outside thinking mode; sampling is forced to 1.0" |
| `top_p_below_floor` | `meta.HasThinking && meta.TopP != nil && *meta.TopP < 0.95` | warn | "top_p=X is below DeepSeek's 0.95 floor in thinking mode" |
| `parallel_tool_use_ignored` | `meta.DisableParallelToolUse` | info | |
| `model_remapped` | `opus*` | info | "claude-opus-5 → deepseek-v4-pro (billed at V4 Pro rates)" |
| `model_remapped` | `sonnet*` / `haiku*` | info | "claude-sonnet-5 → deepseek-flash" |
| `model_unmapped_fallback` | neither pattern | warn | "unrecognized model X falls back to deepseek-flash — quality and cost may differ from expectation" |
| `unsupported_content_block` | any of `meta.UnsupportedBlocks` | **error** | "content block type 'document' is not supported by DeepSeek's Anthropic endpoint; this request may fail or the block may be dropped" |
| `param_ignored` | `top_k` set | info | |
| `param_ignored` | `max_tokens` exceeds the model's ceiling, if known from config | info | |
| `header_ignored` | `anthropic-beta` present in headers | info | "anthropic-beta is ignored for /messages" |
| `upstream_error` | `respBody` carries an `error` object | error | Surfaces the upstream message; emitted even when the HTTP status was 200 |

The model-mapping table (`opus`→`deepseek-v4-pro`, `sonnet`/`haiku`→`deepseek-flash`) lives in
config, not code — plan risk 6 — with the built-in defaults above as a fallback. Matching is
case-insensitive on a prefix.

`usage.Model` (the upstream-resolved model from `message_start`) is used to **confirm** the mapping
rather than assume it: when the response reports a different model than the config predicted, emit
`model_mapping_drift` at warn severity. This is how the tool notices DeepSeek changing its mapping
without anyone reading release notes.

**Severity is meaningful, not decorative**: `error` means the request may have failed or lost
content; `warn` means the client's belief and reality diverge in a way that affects cost, quality,
or safety; `info` means a setting was dropped with no practical consequence.

The `Detail` strings are the product. A warning that says `cache_control_ignored` teaches nothing; a
warning that says *which* sites and *what it means* is the whole value proposition.

## Rationale

The spec's opening problem statement is that the compatibility is *partial and silent*, and there is
no signal anywhere on either side. This bead is the only place that can generate that signal, since
the proxy is the sole vantage point that sees both the client's intent and the upstream's actual
behaviour.

Built pure and table-driven because the rule set is the part most likely to change as DeepSeek's
compatibility evolves — new rules should be one table entry and one test case, not a code change in
a pipeline. The `model_mapping_drift` check is included because a config-driven mapping is only
useful if something notices when the config goes stale.

## Outcome Definition

- `go test ./internal/analyze/...` passes.
- A request containing `cache_control` produces a `cache_control_ignored` warning naming every site.
- Every rule in the table has at least one positive and one negative test.
- `Analyze` returns `nil` (not an empty non-nil slice) for a clean request.
- A clean, fully-compatible request produces zero warnings — no false positives.
- Warnings reach the `warnings` table with the correct `request_id` via the consumer.
- `lens warnings` groups them by kind with correct counts.
- The dashboard warning inbox lists them with their detail text.

## Test Specifications

- Unit Tests (`internal/analyze/analyze_test.go`), table-driven over synthetic `parse.Meta` values:
  - **Clean request** → zero warnings. (The most important negative case.)
  - `cache_control` at one site → one warning naming it.
  - `cache_control` at three sites → one warning listing all three.
  - `cache_control` absent → no warning.
  - `thinking.budget_tokens` present → `budget_tokens_ignored`.
  - `thinking` present, budget absent → no warning.
  - `top_p=0.8`, no thinking → `top_p_clamped`.
  - `top_p=1.0`, no thinking → no warning.
  - `top_p=0.9`, with thinking → `top_p_below_floor`.
  - `top_p=0.95`, with thinking → no warning (boundary inclusive).
  - `disable_parallel_tool_use` true → warning; false → none.
  - Model `claude-opus-5` → `model_remapped` to `deepseek-v4-pro`.
  - Model `claude-sonnet-5` and `claude-haiku-4-5` → `deepseek-flash`.
  - Model `gpt-4o` → `model_unmapped_fallback` at warn.
  - Model matching case-insensitively (`CLAUDE-OPUS-5`) → same result.
  - Each unsupported block type → `unsupported_content_block` at error severity.
  - Multiple unsupported blocks → one warning each (not collapsed).
  - `top_k` set → `param_ignored`.
  - `anthropic-beta` header present → `header_ignored`.
  - Response body containing an `error` object → `upstream_error` at error severity.
  - `usage.Model` differing from the config-predicted model → `model_mapping_drift`.
  - `usage.Model` matching → no drift warning.
  - **Multi-rule**: a request violating five rules → exactly five warnings, no duplicates.
- Integration Tests (`internal/consumer` extension):
  - A proxied request carrying `cache_control` produces rows in `warnings` linked to the request.
  - `lens warnings --detail` lists it with a readable detail string.
- E2E (opt-in): a real request carrying `cache_control` → a `cache_control_ignored` warning appears.

## Files to Touch

- `internal/analyze/analyze.go` (create)
- `internal/analyze/rules.go` (create)
- `internal/analyze/analyze_test.go` (create)
- `internal/config/config.go` (modify — model map + severity overrides)
- `internal/consumer/consumer.go` (modify — register the analyzer)
- `internal/store/types.go` (modify — `Warning` if not already defined there)
