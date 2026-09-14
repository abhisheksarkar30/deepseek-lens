package parse

import (
	"bytes"
	"strings"
	"testing"
)

// buildStream joins pre-built "event: ...\ndata: ...\n" blocks (or
// "\r\n"-terminated blocks, per nl) into a full SSE body, inserting the
// blank-line terminator between and after every event.
func buildStream(nl string, events ...string) []byte {
	return []byte(strings.Join(events, nl) + nl)
}

func sseEventBlock(nl, eventType, data string) string {
	return "event: " + eventType + nl + "data: " + data + nl
}

// simpleSSEEvents returns the four raw event blocks used by most of the
// tests below: message_start (with input + cache tokens and a model),
// two message_delta events whose output_tokens are cumulative (5, then
// 42 — the summing bug would yield 47), and message_stop.
func simpleSSEEvents(nl string) []string {
	return []string{
		sseEventBlock(nl, "message_start",
			`{"type":"message_start","message":{"model":"deepseek-chat","usage":{"input_tokens":10,"cache_creation_input_tokens":2,"cache_read_input_tokens":3}}}`),
		sseEventBlock(nl, "message_delta",
			`{"type":"message_delta","delta":{},"usage":{"output_tokens":5}}`),
		sseEventBlock(nl, "message_delta",
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`),
		sseEventBlock(nl, "message_stop", `{"type":"message_stop"}`),
	}
}

func wantSimpleUsage() Usage {
	return Usage{
		InputTokens:         10,
		OutputTokens:        42, // last delta, never the sum (5+42=47 would be the bug)
		CacheCreationTokens: 2,
		CacheReadTokens:     3,
		StopReason:          "end_turn",
		Model:               "deepseek-chat",
		IsStream:            true,
		Events:              4,
	}
}

func feedBytes(t *testing.T, p *Parser, data []byte) {
	t.Helper()
	for i := range data {
		if err := p.Feed(data[i : i+1]); err != nil {
			t.Fatalf("Feed byte %d (%q): unexpected error: %v", i, data[i], err)
		}
	}
}

func TestExtractUsage_NonStreaming(t *testing.T) {
	body := []byte(`{"id":"msg_1","model":"deepseek-chat","stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":34,"cache_creation_input_tokens":5,"cache_read_input_tokens":6}}`)
	got, err := ExtractUsage(body, "application/json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Usage{
		InputTokens:         12,
		OutputTokens:        34,
		CacheCreationTokens: 5,
		CacheReadTokens:     6,
		StopReason:          "end_turn",
		Model:               "deepseek-chat",
		Events:              1,
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestExtractUsage_NonStreamingMissingUsage(t *testing.T) {
	body := []byte(`{"id":"msg_2","model":"deepseek-chat","stop_reason":"end_turn"}`)
	got, err := ExtractUsage(body, "application/json; charset=utf-8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.InputTokens != 0 || got.OutputTokens != 0 || got.CacheCreationTokens != 0 || got.CacheReadTokens != 0 {
		t.Fatalf("expected zero tokens, got %+v", got)
	}
}

func TestSimpleSSE_LastDeltaWinsNotSum(t *testing.T) {
	body := buildStream("\n", simpleSSEEvents("\n")...)
	got, err := ExtractUsage(body, "text/event-stream")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantSimpleUsage() {
		t.Fatalf("got %+v, want %+v", got, wantSimpleUsage())
	}
}

func TestMessageStartCacheTokens(t *testing.T) {
	body := buildStream("\n", simpleSSEEvents("\n")...)
	got, err := ExtractUsage(body, "text/event-stream")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CacheCreationTokens != 2 || got.CacheReadTokens != 3 {
		t.Fatalf("cache tokens not populated: got %+v", got)
	}
	if got.Model != "deepseek-chat" {
		t.Fatalf("message_start model not read: got %q", got.Model)
	}
}

func TestSplitAcrossChunks_ByteAtATime(t *testing.T) {
	body := buildStream("\n", simpleSSEEvents("\n")...)

	whole, err := ExtractUsage(body, "text/event-stream")
	if err != nil {
		t.Fatalf("whole-body: unexpected error: %v", err)
	}

	p := NewParser()
	feedBytes(t, p, body)
	got, err := p.Finish()
	if err != nil {
		t.Fatalf("byte-at-a-time: unexpected error: %v", err)
	}
	if got != whole {
		t.Fatalf("byte-at-a-time result %+v differs from whole-body result %+v", got, whole)
	}
	if got != wantSimpleUsage() {
		t.Fatalf("got %+v, want %+v", got, wantSimpleUsage())
	}
}

func TestSplitExactlyAtBlankLineBoundary(t *testing.T) {
	body := buildStream("\n", simpleSSEEvents("\n")...)
	idx := bytes.Index(body, []byte("\n\n"))
	if idx < 0 {
		t.Fatal("fixture has no \\n\\n terminator")
	}
	chunk1 := body[:idx+2]
	chunk2 := body[idx+2:]

	p := NewParser()
	if err := p.Feed(chunk1); err != nil {
		t.Fatalf("Feed chunk1: unexpected error: %v", err)
	}
	if err := p.Feed(chunk2); err != nil {
		t.Fatalf("Feed chunk2: unexpected error: %v", err)
	}
	got, err := p.Finish()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantSimpleUsage() {
		t.Fatalf("got %+v, want %+v", got, wantSimpleUsage())
	}
}

func TestCRLFTerminatorSplitAcrossChunks(t *testing.T) {
	body := buildStream("\r\n", simpleSSEEvents("\r\n")...)
	idx := bytes.Index(body, []byte("\r\n\r\n"))
	if idx < 0 {
		t.Fatal("fixture has no \\r\\n\\r\\n terminator")
	}
	// Split inside the terminator itself: chunk1 gets "\r\n\r", chunk2
	// gets the final "\n". The carry buffer must retain the partial
	// terminator rather than dropping it or treating it as complete.
	chunk1 := body[:idx+3]
	chunk2 := body[idx+3:]

	p := NewParser()
	if err := p.Feed(chunk1); err != nil {
		t.Fatalf("Feed chunk1: unexpected error: %v", err)
	}
	if err := p.Feed(chunk2); err != nil {
		t.Fatalf("Feed chunk2: unexpected error: %v", err)
	}
	got, err := p.Finish()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantSimpleUsage() {
		t.Fatalf("got %+v, want %+v", got, wantSimpleUsage())
	}
}

func TestChunkBoundaryMidJSONStringWithBrace(t *testing.T) {
	block := sseEventBlock("\n", "message_start",
		`{"type":"message_start","message":{"model":"a}b","usage":{"input_tokens":1}}}`)
	body := buildStream("\n", block)

	marker := []byte("a}b")
	idx := bytes.Index(body, marker)
	if idx < 0 {
		t.Fatal("fixture missing marker string")
	}
	// Split between 'a' and '}', inside the JSON string value.
	chunk1 := body[:idx+1]
	chunk2 := body[idx+1:]

	p := NewParser()
	if err := p.Feed(chunk1); err != nil {
		t.Fatalf("Feed chunk1: unexpected error: %v", err)
	}
	if err := p.Feed(chunk2); err != nil {
		t.Fatalf("Feed chunk2: unexpected error: %v", err)
	}
	got, err := p.Finish()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Model != "a}b" {
		t.Fatalf("got model %q, want %q (truncated early?)", got.Model, "a}b")
	}
	if got.InputTokens != 1 {
		t.Fatalf("got input tokens %d, want 1", got.InputTokens)
	}
}

func TestMalformedEventMidStream_EarlierUsagePreserved(t *testing.T) {
	events := []string{
		sseEventBlock("\n", "message_start",
			`{"type":"message_start","message":{"model":"deepseek-chat","usage":{"input_tokens":7}}}`),
		sseEventBlock("\n", "message_delta", `{"type": "message_delta", "usage": {not valid json`),
		sseEventBlock("\n", "message_delta",
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`),
	}
	body := buildStream("\n", events...)

	p := NewParser()
	if err := p.Feed(body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := p.Finish()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.InputTokens != 7 {
		t.Fatalf("earlier usage not preserved: got input tokens %d, want 7", got.InputTokens)
	}
	if got.OutputTokens != 9 {
		t.Fatalf("got output tokens %d, want 9", got.OutputTokens)
	}
	if got.Events != 3 {
		t.Fatalf("got Events %d, want 3 (malformed event still counted)", got.Events)
	}
}

func TestEmptyBody(t *testing.T) {
	p := NewParser()
	if err := p.Feed(nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := p.Finish()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.InputTokens != 0 || got.OutputTokens != 0 || got.Events != 0 {
		t.Fatalf("expected zero usage, got %+v", got)
	}

	gotJSON, err := ExtractUsage(nil, "application/json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotJSON != (Usage{}) {
		t.Fatalf("expected zero Usage for empty JSON body, got %+v", gotJSON)
	}
}

func TestErrorEvent(t *testing.T) {
	block := sseEventBlock("\n", "error",
		`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	body := buildStream("\n", block)

	_, err := ExtractUsage(body, "text/event-stream")
	if err == nil {
		t.Fatal("expected an error for an error event, got nil")
	}
}

func TestUnrecognizedContentType(t *testing.T) {
	body := []byte("whatever, not json or sse")
	got, err := ExtractUsage(body, "text/plain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != (Usage{}) {
		t.Fatalf("expected zero Usage, got %+v", got)
	}
}

func TestCRLFLineEndingsThroughout(t *testing.T) {
	body := buildStream("\r\n", simpleSSEEvents("\r\n")...)
	got, err := ExtractUsage(body, "text/event-stream")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantSimpleUsage() {
		t.Fatalf("got %+v, want %+v", got, wantSimpleUsage())
	}
}
