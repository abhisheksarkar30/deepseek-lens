# Bead 7: Consumer wiring (sink → parse → store)

- **Priority**: P0 (critical)
- **Dependencies**: 2, 3, 4, 5, 6
- **Blocks**: 8, 9, 10, 11, 12

## Description

The cold-path bridge that turns captured calls into rows. One goroutine, owned by
`internal/consumer`, that drains the sink and writes to SQLite.

`Consumer.Run(ctx) error` loops over `sink.Drain(ctx)` and, per call, in order:

1. `parse.ExtractMeta(call.ReqBody, call.ReqHeaders)` → `Meta`
2. `parse.ExtractUsage(call.RespBody, call.RespHeaders.Get("Content-Type"))` → `Usage`
3. `proxy`-provided status/timings + `Meta` + `Usage` → `store.Request`
4. `store.InsertRequest(ctx, req)` → `id`
5. If `call.Err != nil`, record it as a warning row with kind `upstream_error`
6. Analytics hooks (beads 10–12) are invoked through a **plugin interface**, `Analyzer`:
   `Analyze(meta Meta, usage Usage, req *store.Request) []store.Warning`
   In this bead the registered set is empty; beads 10 and 11 register into it without touching
   this file. (`ponytail:` a slice of one-method interfaces, not a registry with priorities —
   upgrade if ordering between analyzers ever matters.)
7. `store.InsertWarnings(ctx, id, warnings)`
8. Session assignment is delegated to an injected `SessionResolver` interface, nil in this bead.

**Error containment is the point of this bead.** Any per-call failure — bad JSON, store error,
analyzer panic — is logged to stderr once and the loop continues. A panic in an analyzer is
recovered with `defer recover()`, recorded as a warning, and does not kill the consumer. If the
consumer dies, capture silently stops and the user has no idea — the worst failure mode in the
project, because it is invisible.

Batching: writes are grouped into a transaction flushed when either 50 calls or 250ms of quiet have
accumulated, whichever first. This keeps SQLite write amplification low under agentic load
(hundreds of calls per hour) while keeping the dashboard's freshness inside a quarter second.

`Consumer.Stats()` exposes processed, failed, and last-write-at for `doctor` and the dashboard.

Shutdown: on `ctx` cancel, drain whatever remains in the sink (bounded, e.g. 2s) then flush and
close. Losing the last few calls on Ctrl-C is acceptable; hanging on exit is not.

## Rationale

Every prior bead is a pure component; this is the only place they are wired, and therefore the only
place a cross-component mismatch can hide. Locating the wiring in one small file means the failure
is debuggable in one screen.

The batching trade is deliberate: per-call writes would be correct but churn SQLite; unbounded
batching would make the dashboard stale. 50-or-250ms is the cheap middle. The `Analyzer` and
`SessionResolver` interfaces exist so beads 10–12 are *additive* — they register, they do not
rewrite the pipeline.

## Outcome Definition

- `go test ./internal/consumer/...` passes, with `-race`.
- A captured call produces exactly one `requests` row with metadata, usage, and timings populated.
- A call whose body is unparseable still produces a row, with zero usage and no error surfaced.
- An analyzer that panics does not stop the consumer and yields a warning row.
- A store error on one call does not stop the consumer processing subsequent calls.
- 1000 submitted calls produce 1000 rows.
- Cancel mid-stream flushes and returns within the shutdown bound.

## Test Specifications

- Integration Tests (`internal/consumer/consumer_test.go`, real sink + real temp SQLite):
  - **Happy path**: submit a fully-formed call → one row with all fields correct.
  - **Streaming call**: `RespBody` containing a full SSE stream → correct token counts in the row.
  - **Unparseable request body** → row exists, `ModelRequested` empty, no panic, no error returned.
  - **Upstream error call** (`Err != nil`) → row exists, an `upstream_error` warning recorded.
  - **Panicking analyzer** → recovered, consumer survives, subsequent calls still processed, a
    warning row recorded naming the analyzer.
  - **Failing store** (injected error on the first call) → the consumer continues; the second call
    is written successfully.
  - **Batching**: submit 200 calls rapidly; assert rows appear and that write count is materially
    below 200 (assert the batching actually happens, not just that it works).
  - **Flush on quiet**: submit 1 call; assert it is readable within 500ms without waiting for 50.
  - **Throughput**: 1000 calls → 1000 rows.
  - **Shutdown**: cancel context with 100 calls queued; assert `Run` returns within the bound and
    the store is closed cleanly.
  - **Analyzer registration**: a registered analyzer's returned warnings land in the `warnings`
    table linked to the right request id.
  - `go test -race` clean with a concurrent producer.
- E2E: none.

## Files to Touch

- `internal/consumer/consumer.go` (create)
- `internal/consumer/consumer_test.go` (create)
- `internal/consumer/analyzer.go` (create — `Analyzer`, `SessionResolver` interfaces)
