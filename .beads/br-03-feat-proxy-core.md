# Bead 3: Transparent proxy core

- **Priority**: P0 (critical)
- **Dependencies**: 1, 2
- **Blocks**: 7, 8, 9 (and 13, directly)

## Description

The hot path. An `http.Handler` that forwards any request to the configured upstream and returns the
upstream response to the client **byte-identically**, while teeing a copy into the sink.

Built on `httputil.ReverseProxy` with:

- `FlushInterval: -1` — flush immediately after every write. This is what makes SSE stream through
  rather than accumulating in a buffer. Non-negotiable.
- `Director` (or `Rewrite`) setting the upstream scheme/host and path prefix, and preserving the
  inbound `x-api-key` untouched. Hop-by-hop headers handled by the stdlib.
- A shared `http.Transport` with `MaxIdleConnsPerHost` raised (default is 2, which serialises
  connection reuse under agentic load) and HTTP/2 enabled.
- Custom `ErrorHandler` that passes upstream failures through faithfully as 502 with the upstream
  body when one exists — never swallows, never rewrites into a success.

**Teeing, without buffering:**

- Request body: wrap `r.Body` in an `io.TeeReader` feeding a `*bytes.Buffer` before proxying, so the
  original stream still flows to the upstream untouched. If the request exceeds `BodyCapBytes`,
  keep reading (the request must not be truncated) but stop appending past the cap, recording
  `truncated bool`.
- Response body: in `ModifyResponse`, replace `res.Body` with a tee reader that writes to an
  accumulator as the proxy copies it to the client. The accumulator is only inspected in
  `ModifyResponse`'s returned wrapper's `Close`, i.e. after the stream has finished — so the hot
  path never seeks or rewinds.
- Header redaction: `x-api-key`, `authorization`, and `cookie` are replaced with `[redacted]` on
  **both** request and response copies before the `CapturedCall` is submitted.

**The submit call site must be off the critical path.** It happens in the response body's `Close`,
after the final byte has already reached the client. A slow or full sink therefore cannot delay a
response — the trailing submit is fire-and-forget against `Sink.Submit`.

`--no-capture` (config `Capture: false`) bypasses teeing entirely and installs a plain reverse
proxy. This is the escape hatch for risk 4.

An `http.Server` is constructed with `ReadHeaderTimeout`, no `WriteTimeout` (streaming responses can
legitimately run for minutes), and `IdleTimeout`.

## Rationale

This bead is the product. Everything else observes or displays what this produces. The two
requirements — least latency, and capture everything — are in direct tension, and the resolution is
that **the hot path copies bytes and does literally nothing else**: no parsing, no JSON, no
allocation per chunk beyond the accumulator's append. Teeing request and response independently
(rather than buffering either) is what lets a multi-minute streaming response be captured without
the client waiting for it to finish.

## Outcome Definition

- `go test ./internal/proxy/...` passes, including the TTFB test and `-race`.
- A canned SSE stream is delivered byte-identical to the client.
- The first SSE event reaches the client before the fake upstream sends its last event.
- A `CapturedCall` is submitted with status, timings, and bodies populated.
- `x-api-key` appears as `[redacted]` in the captured headers.
- With `Capture: false`, no `CapturedCall` is submitted and the body is still byte-identical.
- A request body larger than `BodyCapBytes` is forwarded in full but captured truncated.

## Test Specifications

- Unit/Integration Tests (`internal/proxy/proxy_test.go`, using `httptest.NewServer`):
  - **Byte identity, non-streaming**: fake upstream returns a JSON body; assert the client receives
    exactly those bytes and the same status and headers.
  - **Byte identity, streaming**: fake upstream returns a 3-event SSE stream; assert the client
    receives the exact concatenation byte-for-byte.
  - **THE MONEY TEST — no buffering**: fake upstream writes event 1, sleeps 200ms, writes event 2,
    sleeps 200ms, writes event 3. The client reads with a deadline. Assert event 1 arrives more than
    150ms before event 3 is written upstream. A buffering implementation fails this; it is the
    regression guard for latency invariant 3.
  - **Tee populates capture**: after the stream completes, assert the submitted `CapturedCall` has
    `Status`, `TTFB > 0`, `Duration > 0`, and `RespBody` equal to the upstream's full stream.
  - **Request body captured and forwarded**: POST a known body; assert the upstream received it
    verbatim and the capture holds it.
  - **Oversized request body**: POST `BodyCapBytes + 1KB`; assert the upstream received all of it
    and the captured copy is capped.
  - **Redaction**: send a request whose `x-api-key` header carries a known sentinel value; assert
    the captured `ReqHeaders` holds `[redacted]` for that header, and the upstream received the
    sentinel unchanged.
  - **Upstream failure passes through**: upstream returns 500 with a body; assert client sees 500
    and that body.
  - **Upstream unreachable**: assert the client gets 502, not a hang, and a `CapturedCall` with
    `Err != nil` is submitted.
  - **Capture disabled**: with `Capture: false`, assert zero submissions and byte-identical output.
  - **Full sink does not delay the client**: fill the sink to capacity, issue a request, assert it
    returns within a tight deadline and `dropped` incremented.
- E2E: deferred to bead 13's opt-in real-endpoint test.

## Files to Touch

- `internal/proxy/proxy.go` (create)
- `internal/proxy/proxy_test.go` (create)
- `internal/proxy/redact.go` (create)
- `internal/proxy/redact_test.go` (create)
