# Bead br-GI-1-02: Non-blocking capture sink

**Plan Reference**: `docs/planning/GI-1-deepseek-lens-v1.md` §Bead sequence

- **Priority**: P0 (critical)
- **Dependencies**: br-GI-1-01
- **Blocks**: br-GI-1-03, br-GI-1-07

## Description

`internal/sink` is the component that makes latency invariant 2 structurally true rather than
merely intended: **the observer can never block the client.**

A `Sink` wraps a buffered channel of `*CapturedCall`. Its `Submit` method performs a non-blocking
send:

```
select {
case s.ch <- call:
    atomic.AddUint64(&s.accepted, 1)
    return true
default:
    atomic.AddUint64(&s.dropped, 1)
    return false
}
```

It never blocks, never spawns a goroutine per call, and never returns an error the caller must
handle. It returns `bool` so callers *may* log a drop, but correct callers ignore it — dropping is a
normal, expected outcome under load, not a failure.

`CapturedCall` is the transport struct between hot and cold path. It holds only cheap references and
already-read byte slices:

- `ID string` — assigned at submit time (monotonic counter + start timestamp)
- `StartedAt time.Time`, `TTFB time.Duration`, `Duration time.Duration`
- `Method`, `Path string`, `RemoteAddr string`
- `Status int`
- `ReqHeaders http.Header`, `RespHeaders http.Header` — **already redacted by the caller**
- `ReqBody []byte` — body policy applied by the caller
- `RespBody []byte` — nil when streaming; the stream tee fills this via a separate accumulator
- `Err error`

`Stats()` returns `(accepted, dropped uint64)` via atomic loads, for `doctor` and the dashboard.

`Drain(ctx)` yields the receive channel so the consumer owns the read side. `Close()` closes the
channel after the producer has stopped.

Capacity is a config-derived constant (default 4096), not a package global.

## Rationale

This is the single most important file in the hot path and the smallest. Invariant 2 says a
logging path able to stall a request is how seconds get added to an agentic loop. A `select/default`
send makes stalling impossible by construction — there is no code path in which `Submit` waits on
anything. Isolating it in its own package means it can be tested with the consumer deliberately
stopped, which is exactly the failure mode that matters.

## Outcome Definition

- `go test ./internal/sink/...` passes, including the with-consumer-stopped case.
- With the consumer stopped, submitting more than capacity completes in bounded time, `drops`
  equals the overflow count, and no goroutine leaks.
- `Submit` makes no allocation beyond the `CapturedCall` the caller already built (verified by
  `testing.AllocsPerRun`).
- `go test -race` passes.

## Test Specifications

- Unit Tests (`internal/sink/sink_test.go`):
  - Submit to an empty sink returns true and `accepted` increments.
  - With the consumer stopped, submitting `capacity` items returns true for all.
  - With the consumer stopped, submitting `capacity + 500` items returns true for the first
    `capacity` and false for the rest; `dropped` == 500; total wall time under 1s.
  - `Stats()` reflects accepted and dropped counts accurately after concurrent submissions from
    100 goroutines.
  - `Submit` never blocks: a single-goroutine loop of `capacity * 10` submissions completes.
  - After `Close()`, `Drain`'s channel closes and a `range` over it terminates.
  - `go test -race` clean with 100 concurrent submitters and one concurrent consumer.
- Integration Tests: none (this package has no external dependencies by design).

## Files to Touch

- `internal/sink/sink.go` (create)
- `internal/sink/sink_test.go` (create)
