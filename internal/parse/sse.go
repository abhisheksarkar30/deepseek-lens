package parse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// usageJSON is the narrow shape of an Anthropic "usage" object. Unmarshaling
// into this rather than a full API model means an unexpected upstream field
// can never break parsing.
type usageJSON struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// nonStreamBody is the narrow shape of a non-streaming response body.
type nonStreamBody struct {
	Usage      usageJSON `json:"usage"`
	StopReason string    `json:"stop_reason"`
	Model      string    `json:"model"`
}

// sseEvent is the narrow shape of one SSE event's `data:` payload, covering
// message_start, message_delta, and error — the only event types that carry
// usage, stop reason, model, or an error.
type sseEvent struct {
	Type    string `json:"type"`
	Message *struct {
		Model string    `json:"model"`
		Usage usageJSON `json:"usage"`
	} `json:"message"`
	Delta *struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage *usageJSON      `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// ExtractUsage is the single-entry convenience over a complete, captured
// response body. An unrecognised content type returns a zero Usage and a
// nil error — an upstream shape change must degrade to "no tokens
// recorded", never to broken capture.
// ExtractUsageWithTail recovers usage from a capped SSE head plus the
// overflow tail. For text/event-stream with a non-empty tail it feeds the
// head, a blank line that closes the head's torn final event, then the tail
// — the tail's torn first event fails to parse and is skipped.
//
// ponytail: JSON over the cap still loses usage; the upgrade path is
// tail-based JSON extraction.
func ExtractUsageWithTail(head, tail []byte, contentType string) (Usage, error) {
	ct := contentType
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "text/event-stream" && len(tail) > 0 {
		p := NewParser()
		if err := p.Feed(head); err != nil {
			return p.usage, err
		}
		if err := p.Feed([]byte("\n\n")); err != nil {
			return p.usage, err
		}
		if err := p.Feed(tail); err != nil {
			return p.usage, err
		}
		return p.Finish()
	}
	return ExtractUsage(head, contentType)
}

func ExtractUsage(respBody []byte, contentType string) (Usage, error) {
	ct := contentType
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.ToLower(strings.TrimSpace(ct))

	switch ct {
	case "application/json":
		return parseNonStream(respBody)
	case "text/event-stream":
		p := NewParser()
		if err := p.Feed(respBody); err != nil {
			return p.usage, err
		}
		return p.Finish()
	default:
		return Usage{}, nil
	}
}

func parseNonStream(body []byte) (Usage, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return Usage{}, nil
	}
	var r nonStreamBody
	if err := json.Unmarshal(body, &r); err != nil {
		return Usage{}, err
	}
	return Usage{
		InputTokens:         r.Usage.InputTokens,
		OutputTokens:        r.Usage.OutputTokens,
		CacheCreationTokens: r.Usage.CacheCreationInputTokens,
		CacheReadTokens:     r.Usage.CacheReadInputTokens,
		StopReason:          r.StopReason,
		Model:               r.Model,
		Events:              1,
	}, nil
}

// Parser incrementally extracts Usage from an SSE byte stream. Feed may be
// called any number of times with chunks of any size — including a single
// byte — and Finish flushes whatever remains. This shape exists so a later
// bead can feed it from a live streaming accumulator without changing the
// API: the carry buffer that makes arbitrary chunking safe is not an
// afterthought.
type Parser struct {
	buf    []byte
	usage  Usage
	events int
}

// NewParser returns an empty Parser ready for Feed.
func NewParser() *Parser {
	return &Parser{}
}

// Feed appends chunk to the internal carry buffer and processes every
// complete event (one terminated by a blank line) it finds. Any incomplete
// tail — including a lone trailing '\r' that might be the start of a
// \r\n\r\n or \r\r terminator split across calls — is retained for the next
// Feed or for Finish. It returns non-nil only when an `error` SSE event was
// parsed; a malformed event is skipped, not fatal.
func (p *Parser) Feed(chunk []byte) error {
	p.buf = append(p.buf, chunk...)
	for {
		event, rest, found := findEvent(p.buf)
		if !found {
			return nil
		}
		p.buf = rest
		if err := p.processEvent(event); err != nil {
			return err
		}
	}
}

// Finish flushes any buffered remainder (an event with no trailing blank
// line, e.g. a body that ends right after message_stop) and returns the
// accumulated Usage.
func (p *Parser) Finish() (Usage, error) {
	var err error
	if len(bytes.TrimSpace(p.buf)) > 0 {
		err = p.processEvent(p.buf)
	}
	p.buf = nil
	u := p.usage
	u.IsStream = true
	u.Events = p.events
	return u, err
}

// findEvent locates the first complete SSE event in buf, delimited by a
// blank line: \n\n, \r\n\r\n, or \r\r (SSE permits all three; a literal
// "\n\n" substring search alone would never match a CRLF stream). found is
// false when buf's tail cannot yet be told apart from an in-progress
// terminator — the whole of buf is then the carry, unchanged, for the next
// call.
func findEvent(buf []byte) (event, rest []byte, found bool) {
	n := len(buf)
	for i := 0; i < n; i++ {
		switch buf[i] {
		case '\n':
			if i+1 >= n {
				return nil, buf, false
			}
			if buf[i+1] == '\n' {
				return buf[:i], buf[i+2:], true
			}
		case '\r':
			if i+1 >= n {
				return nil, buf, false
			}
			if buf[i+1] == '\r' {
				return buf[:i], buf[i+2:], true
			}
			if buf[i+1] == '\n' {
				if i+2 >= n {
					return nil, buf, false
				}
				if buf[i+2] != '\r' {
					continue // ordinary CRLF line ending, not a terminator
				}
				if i+3 >= n {
					return nil, buf, false
				}
				if buf[i+3] == '\n' {
					return buf[:i], buf[i+4:], true
				}
			}
		}
	}
	return nil, buf, false
}

// ponytail: findEvent rescans buf from 0 on every Feed call, so a stream
// fed one byte at a time is O(n^2) over its own length. Response bodies are
// capped (256KB) and real upstream chunks aren't 1 byte, so this is not a
// live concern; if a future bead feeds this from many tiny writes, track a
// resume offset (rescan only the last 3 bytes of the previous buffer) to
// bring it back to O(n).

// processEvent splits one event block into its `data:` lines (joined with
// "\n" per the SSE spec when there are several), skips it if there is no
// data at all or it is the "[DONE]" sentinel, and otherwise hands the JSON
// payload to applyEvent. It always counts the event, even one whose JSON
// fails to parse, so Usage.Events can reveal a parse mismatch to a caller
// that cares.
func (p *Parser) processEvent(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var dataParts []string
	for _, line := range splitLines(raw) {
		if strings.HasPrefix(line, "data:") {
			d := strings.TrimPrefix(line, "data:")
			d = strings.TrimPrefix(d, " ")
			dataParts = append(dataParts, d)
		}
	}
	if len(dataParts) == 0 {
		return nil
	}
	p.events++
	data := strings.Join(dataParts, "\n")
	if data == "[DONE]" {
		return nil
	}
	return p.applyEvent([]byte(data))
}

// applyEvent unmarshals one event's JSON data and folds it into p.usage.
// An unparseable event is skipped, not fatal — the earlier usage stands.
func (p *Parser) applyEvent(data []byte) error {
	var ev sseEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			p.usage.Model = ev.Message.Model
			p.usage.InputTokens = ev.Message.Usage.InputTokens
			p.usage.CacheCreationTokens = ev.Message.Usage.CacheCreationInputTokens
			p.usage.CacheReadTokens = ev.Message.Usage.CacheReadInputTokens
		}
	case "message_delta":
		if ev.Usage != nil {
			// Cumulative, not incremental: each message_delta's
			// output_tokens is the running total, so the last value
			// seen wins. Summing here is the classic bug.
			p.usage.OutputTokens = ev.Usage.OutputTokens
		}
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			p.usage.StopReason = ev.Delta.StopReason
		}
	case "error":
		return fmt.Errorf("parse: sse error event: %s", ev.Error)
	}
	return nil
}

// splitLines splits an SSE event block into lines, recognising "\n",
// "\r\n", and a lone "\r" as line endings.
func splitLines(b []byte) []string {
	var lines []string
	start := 0
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '\n':
			line := b[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, string(line))
			start = i + 1
		case '\r':
			if i+1 < len(b) && b[i+1] == '\n' {
				continue // handled by the '\n' case above
			}
			lines = append(lines, string(b[start:i]))
			start = i + 1
		}
	}
	if start < len(b) {
		lines = append(lines, string(b[start:]))
	}
	return lines
}
