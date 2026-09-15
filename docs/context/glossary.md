[← INDEX](INDEX.md)

# Glossary

| Term | Meaning | First seen in |
|---|---|---|
| **Lens / `lens`** | The project's binary and CLI name (deepseek-lens) | [cmd/lens/main.go](../../cmd/lens/main.go) |
| **Capture** | Recording a proxied call's bytes into the sink/store pipeline; toggled by `Capture`/`--capture` | [internal/config/config.go](../../internal/config/config.go) |
| **Sink** | The bounded, non-blocking channel handoff between the proxy (hot path) and the consumer (cold path) | [internal/sink/sink.go](../../internal/sink/sink.go) |
| **Consumer** | The single goroutine that drains the sink, parses, resolves session/cost, inserts rows, and runs analyzers | [internal/consumer/consumer.go](../../internal/consumer/consumer.go) |
| **Hot path** | The proxy request/response path — must never buffer or delay a stream | [CLAUDE.md](../../CLAUDE.md) |
| **Cold path** | The consumer's asynchronous parse-and-store path, off the client's critical path | [CLAUDE.md](../../CLAUDE.md) |
| **TTFB** | Time-to-first-byte; the latency metric the hot-path no-buffering guarantee is tested against | [internal/proxy/proxy_test.go](../../internal/proxy/proxy_test.go) |
| **Session** | A group of correlated calls treated as one agentic "run"; resolved by header or prefix-hash + inactivity gap | [internal/session/session.go](../../internal/session/session.go) |
| **PrefixHash** | Hash of a request body's opening prompt, used as the session correlation key absent an explicit header | [internal/parse/types.go](../../internal/parse/types.go) |
| **`x-lens-session`** | Request header that pins a call to an exact session id, overriding the prefix-hash heuristic | [internal/session/session.go:66-70](../../internal/session/session.go) |
| **Warning** | One analyzer finding (a dropped/rewritten parameter, or an upstream error) attached to a request row | [internal/store/types.go](../../internal/store/types.go) |
| **Kind** | The machine-readable name of a warning type (e.g. `cache_control_ignored`) | [internal/analyze/kinds.go](../../internal/analyze/kinds.go) |
| **Analyzer** | A post-insert rule that inspects a stored request and emits `Warning`s | [internal/consumer/analyzer.go](../../internal/consumer/analyzer.go) |
| **Cost source** | How a request's `cost_usd` was determined (priced / unpriced / unknown-model / ...) | [internal/store/types.go](../../internal/store/types.go) |
| **Replay** | Re-issuing a previously captured request's body (optionally edited) through the live proxy | [internal/replay/](../../internal/replay/) |
| **Outcome** | The compact comparable summary of one captured or replayed call, used to diff a replay against its original | [internal/replay/replay.go](../../internal/replay/replay.go) |
| **Broker** | The in-process SSE fan-out that pushes live events to the dashboard | [internal/api/broker.go](../../internal/api/broker.go) |
| **Bead** | An internal work-item id, `br-GI-<n>-<NN>`, referenced in code comments to explain *why* a piece of code exists | [.beads/](../../.beads/), [CLAUDE.md](../../CLAUDE.md) |
| **Fail open** | The project-wide rule that a broken observer must never break the user's coding session | [CLAUDE.md](../../CLAUDE.md) |
