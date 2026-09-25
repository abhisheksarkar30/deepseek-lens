### Bead 3: `feat` usage tail — `boundedBuffer` tail, `RespTail`, `ExtractUsageWithTail`, consumer wiring

- **Bead ID**: br-GI-27-03
- **Priority**: P0 (critical)
- **Original Estimate**: 2h
- **Dependencies**: None
- **Blocks**: None (backstop for A.3's cap raise, but independent)

**Description**:

RC-1 / A.1. The proxy tees the response into a `boundedBuffer` that keeps the **first** `BodyCapBytes` (262,144) and drops the rest (`proxy.go:219-242`); the consumer runs `parse.ExtractUsage` on that body. An SSE stream reports input/cache tokens in `message_start` (head) but the **output-token count in the final `message_delta`** (tail), so a response over the cap prices with `output_tokens = 0`. Measured 2026-09-25: 1,487 of 18,325 rows (8%) are at the cap and **all** have `output_tokens = 0`. Keep a small trailing window of the bytes past the head cap.

**Precondition:** the working-tree A.1/A.2 prototype must already be stashed (bead 02 step 0, plan §13). This bead re-implements A.1 from the plan, not from the prototype; never `git stash pop`.

**1. `internal/proxy/proxy.go` — `boundedBuffer` gains a tail.** The struct is at `:233`, `Write` at `:244`, `Bytes()` at `:262`. Add `tailCap int` / `tail []byte`:
- While `len(buf) < cap`, write into the head as today.
- Once the head is full, bytes go into a **trailing window of `usageTailBytes` (16 KiB)**: append, then drop the oldest bytes so the window holds the most recent 16 KiB. A lazy ring is the upgrade path if profiling shows the append+memmove cost matters.
- Add `func (b *boundedBuffer) Tail() []byte` returning the window (nil when nothing overflowed). Add a const `usageTailBytes = 16 * 1024`.
- Response buffer only — the request body needs no tail.
- **No parsing in the hot path.** This is a byte copy, bounded and post-cap. Record the hot-path cost honestly (plan §9): past the head cap each `Write` appends then memmoves the 16 KiB window (~100x copy amplification per small chunk), bounded and post-cap; TTFB is unaffected. The TTFB test is the gate.
- `submit` (`:198`) already takes `respTail []byte` in the current prototype signature but the base tree's `submit` does not — at the base tree, thread the tail from the tee call at `:80` through to `submit`.

**2. `internal/sink/sink.go` — `CapturedCall.RespTail []byte`** (struct at `:21`, beside `RespBody` at `:34`). Document it: last bytes past the body cap; nil unless `RespBody` was truncated. **Not stored in the DB.**

**3. `internal/parse/sse.go` — `ExtractUsageWithTail(head, tail []byte, contentType string) (Usage, error)`** (beside `ExtractUsage` at `:47`, `applyEvent`). Behaviour: for `text/event-stream` with a non-empty tail, feed head, then `"\n\n"` (closes the head's torn final event), then tail — the tail's torn first event fails to parse and is skipped by `applyEvent`. Otherwise fall back to the plain `ExtractUsage` path (return `ExtractUsage(head, contentType)` semantics). Put a `ponytail:` comment at its top: **JSON over the cap still loses usage**; the upgrade path is tail-based JSON extraction.

**4. `internal/consumer/consumer.go` — use it, and drop the tail when the response was compressed.** The call site is the `parse.ExtractUsage` call in the cold path (the `respBody`/`respHeaders` block at `:357-371`). Pass `call.RespTail` (or the tail from the captured call). **Guard on the pre-decode headers**: drop the tail when `call.RespHeaders.Get("Content-Encoding")` is non-empty. **Test `call.RespHeaders`, not the post-decode `respHeaders`** — `decode.Body` strips `Content-Encoding` from its returned header clone on every successful decode, so testing the decoded headers never fires (this is F1.1, the bug the prototype carries).

**Rationale**:

RC-1/A.1. A response over the cap silently under-bills: the tail holds the output-token count the head dropped. The tail is a 16 KiB backstop (each response) and stays valuable even after bead 15 raises the cap to 8 MiB — at 8 MiB the overflow rate is near zero, but the tail turns "usage lost above the cap" from a silent under-bill into a non-event for any stream, however long. The compressed-response case is an accepted residual: a compressed stream over the cap decodes only to a prefix, so its usage is unrecoverable — hence dropping the tail there (the tail would be compressed bytes that do not decode).

**Outcome Definition**:

- `boundedBuffer.Tail()` returns nil until the head overflows, then the most recent up-to-16 KiB of the dropped bytes.
- `CapturedCall.RespTail` is populated from the proxy and is not persisted.
- `ExtractUsageWithTail` recovers output tokens/input/stop reason from a torn head + torn tail; the head alone loses them (no vacuous pass).
- The consumer uses the tail for uncompressed responses and ignores it when `call.RespHeaders` carries `Content-Encoding`.
- TTFB test stays green (`go test ./internal/proxy/...`).
- `go build ./... && go vet ./... && go test ./...` pass.

**Test Specifications**:

- `internal/proxy/proxy_test.go`: `Tail()` is nil before the head cap; a body past the cap yields a 16 KiB trailing window (the last bytes, not the first); the tail is empty/populated correctly at the boundary.
- `internal/parse/sse_test.go`: an SSE fixture with a head cut mid-event and a tail holding the final `message_delta` recovers output tokens / input / stop reason; the head alone yields `output_tokens = 0` (assert the two differ — the non-vacuous control); the tail's torn first event is skipped, not fatal.
- `internal/consumer/consumer_test.go`: a capped head plus a tail prices with the **tail's** output tokens; a call whose `call.RespHeaders` carries `Content-Encoding` ignores the tail (set the pre-decode headers **directly** on the `CapturedCall` — do not rely on a decode round-trip).
- The TTFB test in `internal/proxy/` (fake upstream, slow-streamed SSE, client sees first event before upstream's last) must stay green.

**Files to Touch**:
- `internal/proxy/proxy.go` (modify — `boundedBuffer` tail, `Tail()`, `usageTailBytes`, thread the tail through `submit`)
- `internal/proxy/proxy_test.go` (modify)
- `internal/sink/sink.go` (modify — add `RespTail`)
- `internal/parse/sse.go` (modify — add `ExtractUsageWithTail`, `ponytail:` note)
- `internal/parse/sse_test.go` (modify)
- `internal/consumer/consumer.go` (modify — use the tail, guard on `call.RespHeaders`)
- `internal/consumer/consumer_test.go` (modify)
