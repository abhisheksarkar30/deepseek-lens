# Bead br-GI-1-04: SSE and response parsing

**Plan Reference**: `docs/planning/GI-1-deepseek-lens-v1.md` §Bead sequence

- **Priority**: P0 (critical)
- **Dependencies**: br-GI-1-01
- **Blocks**: br-GI-1-07, br-GI-1-11

## Description

All response parsing lives off the hot path, operating on a fully-captured `RespBody []byte`. This
bead extracts token usage, stop reason, and the resolved model from both response shapes.

**Non-streaming** (`Content-Type: application/json`): unmarshal into a minimal struct —
`usage.input_tokens`, `usage.output_tokens`, `usage.cache_creation_input_tokens`,
`usage.cache_read_input_tokens`, `stop_reason`, `model`. Unmarshal into a *narrow* struct with only
these fields, never a full API model, so an unexpected field cannot break parsing.

**Streaming** (`Content-Type: text/event-stream`): a scanner over the captured bytes that extracts:

- From `message_start` → `message.usage.input_tokens`, `usage.cache_*`, and `message.model`
  (the **upstream-resolved** model, which is where the remap becomes visible).
- From each `message_delta` → `usage.output_tokens` (**cumulative** — take the last value seen, do
  not sum across deltas; summing is the obvious bug and must be explicitly tested).
- From `message_delta` → `delta.stop_reason`.
- From `error` → the error payload, surfaced as a parse error.
- `[DONE]` / `message_stop` → clean termination.

**The carry-buffer requirement.** The captured `RespBody` is a contiguous byte slice, so in
*this* bead events are always whole. But the same parser is fed in br-GI-1-07 from the streaming
accumulator, and the parser is written from the start to accept arbitrary chunk boundaries:

`Parser.Feed(chunk []byte)` appends to an internal buffer, extracts complete events (delimited by a
**blank line**), and **retains the incomplete tail** for the next call. `Parser.Finish()` flushes any
remainder. This is the API from the first commit, so br-GI-1-07 does not have to change it.

**Blank-line detection.** An event ends at a blank line, which per the SSE spec may be `\n\n`,
`\r\n\r\n`, or `\r\r`. The parser scans for **all three** forms rather than a literal `\n\n`
substring — a literal `\n\n` scan never matches `\r\n\r\n` (no adjacent `\n\n` exists there), so a
CRLF stream would never yield a complete event and the CRLF test below could not pass. Line endings
are not rewritten; the three terminator forms are matched directly. A trailing `\r` at the very end
of a `Feed` chunk is **retained in the carry buffer** (not consumed as a terminator) so a
`\r\n\r\n` / `\r\r` blank line split across two `Feed` calls is still recognised once the next chunk
arrives.

`Usage` is the return struct: `InputTokens`, `OutputTokens`, `CacheCreationTokens`,
`CacheReadTokens`, `StopReason`, `Model string`, `IsStream bool`, `Events int`.

Also expose `ExtractUsage(respBody []byte, contentType string) (Usage, error)` as the
single-entry convenience over a complete body.

Robustness rules: an unparseable event is skipped and counted in `Usage.Events` mismatch rather
than failing the whole call; a completely unrecognised content type returns
`Usage{IsStream: <url-suffix guess>}` with zero tokens and no error, so an upstream change degrades
to "no tokens recorded" rather than "capture dropped".

## Rationale

Token accounting is a headline requirement, and SSE parsing is the single most likely place for a
subtle defect in this codebase — plan risk 1. Two specific traps are designed out rather than
tested for after the fact: (a) a `message_delta`'s `output_tokens` is cumulative, so summing is
wrong; (b) events can straddle chunk boundaries once fed incrementally. Fixing the API shape now
(`Feed`/`Finish` with a carry buffer) means br-GI-1-07 wires it without rework.

## Outcome Definition

- `go test ./internal/parse/...` passes.
- A non-streaming body yields correct input/output tokens and stop reason.
- A multi-event SSE stream yields `output_tokens` equal to the **last** `message_delta` value.
- An event split across two `Feed` calls parses correctly and produces the same `Usage` as the
  unsplit input.
- `ExtractUsage` on a complete stream and `Feed`-in-arbitrary-chunks produce identical results.
- An unrecognised content type returns zero usage and no error.

## Test Specifications

- Unit Tests (`internal/parse/sse_test.go`):
  - **Non-streaming JSON**: known body → exact input/output tokens, stop reason, model.
  - **Non-streaming with missing `usage`**: zero tokens, no error.
  - **Simple SSE**: `message_start` + two `message_delta` + `message_stop` → output tokens equal the
    **second** delta's value, not the sum. (Explicitly asserts against the summing bug.)
  - **Split event across chunks**: the same stream fed one byte at a time → identical `Usage` to the
    whole-body case. (The classic bug; dedicated test.)
  - **Split exactly at `\n\n` boundary** → identical result.
  - **CRLF terminator split across chunks** → a `\r\n\r\n` blank line split between two `Feed`
    calls parses identically (the carry buffer retains the partial terminator rather than dropping
    the `\r`).
  - **Chunk boundary mid-JSON-string containing `}`** → parses, does not truncate early.
  - **`message_start` carries model**: assert the `message.model` field (upstream-resolved) is read.
  - **Cache token fields** on `message_start` → `cache_creation_input_tokens` /
    `cache_read_input_tokens` populated.
  - **Malformed event** mid-stream → earlier usage preserved, no error returned.
  - **Empty body** → zero usage, no error.
  - **`error` event** → returned as an error.
  - **Unrecognised content type** (`text/plain`) → zero usage, nil error.
  - **CRLF line endings** → parses identically.
- Integration Tests: none (pure function over bytes).

## Files to Touch

- `internal/parse/sse.go` (create)
- `internal/parse/sse_test.go` (create)
- `internal/parse/types.go` (create — `Usage`)
