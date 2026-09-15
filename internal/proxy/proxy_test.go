package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/sink"
)

// testConfig returns a valid *config.Config pointed at upstreamURL, with a
// small body cap so oversized-body tests stay fast.
func testConfig(upstreamURL string) *config.Config {
	cfg := config.Default()
	cfg.UpstreamURL = upstreamURL
	cfg.Capture = true
	cfg.BodyCapBytes = 4096
	return cfg
}

// newProxyServer builds the proxy handler for cfg/sk and wraps it in an
// httptest.Server, registering its Close with t.Cleanup.
func newProxyServer(t *testing.T, cfg *config.Config, sk *sink.Sink) *httptest.Server {
	t.Helper()
	h, err := New(cfg, sk)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

// drainOne waits for a single CapturedCall to arrive at sk, failing the
// test if none arrives within the deadline.
func drainOne(t *testing.T, sk *sink.Sink) *sink.CapturedCall {
	t.Helper()
	select {
	case call := <-sk.Drain():
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("no CapturedCall submitted within deadline")
		return nil
	}
}

func TestByteIdentityNonStreaming(t *testing.T) {
	const body = `{"hello":"world"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom", "upstream-value")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, body)
	}))
	defer upstream.Close()

	sk := sink.New(16)
	ts := newProxyServer(t, testConfig(upstream.URL), sk)

	resp, err := ts.Client().Get(ts.URL + "/v1/chat")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if got := resp.Header.Get("X-Custom"); got != "upstream-value" {
		t.Errorf("X-Custom = %q, want %q", got, "upstream-value")
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
}

func sseEvents(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		fmt.Fprintf(&b, "data: %s\n\n", e)
	}
	return b.String()
}

func TestByteIdentityStreaming(t *testing.T) {
	want := sseEvents("one", "two", "three")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, e := range []string{"one", "two", "three"} {
			fmt.Fprintf(w, "data: %s\n\n", e)
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	sk := sink.New(16)
	ts := newProxyServer(t, testConfig(upstream.URL), sk)

	resp, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestNoBufferingSSE is the money test: it fails if the proxy ever buffers
// the response instead of streaming it. The fake upstream writes event 1,
// sleeps 200ms, writes event 2, sleeps 200ms, writes event 3. If the proxy
// buffers, the client sees nothing until upstream is done and this test
// fails; if it streams, event 1 arrives immediately, long before event 3 is
// even written upstream.
func TestNoBufferingSSE(t *testing.T) {
	event3WrittenAt := make(chan time.Time, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		fmt.Fprint(w, "data: one\n\n")
		flusher.Flush()
		time.Sleep(200 * time.Millisecond)

		fmt.Fprint(w, "data: two\n\n")
		flusher.Flush()
		time.Sleep(200 * time.Millisecond)

		event3WrittenAt <- time.Now()
		fmt.Fprint(w, "data: three\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	sk := sink.New(16)
	ts := newProxyServer(t, testConfig(upstream.URL), sk)

	resp, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	var got strings.Builder
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(got.String(), "one") {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for event 1; got so far: %q", got.String())
		}
		line, err := br.ReadString('\n')
		got.WriteString(line)
		if err != nil && err != io.EOF {
			t.Fatalf("reading event 1: %v", err)
		}
	}
	event1ArrivedAt := time.Now()

	// Drain the rest so the upstream handler (and this request) completes
	// cleanly.
	io.Copy(io.Discard, br)

	var t3 time.Time
	select {
	case t3 = <-event3WrittenAt:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never wrote event 3")
	}

	gap := t3.Sub(event1ArrivedAt)
	t.Logf("event 1 arrived %v before event 3 was written upstream", gap)
	if gap <= 150*time.Millisecond {
		t.Fatalf("event 1 arrived only %v before event 3 was written upstream, want > 150ms (buffered?)", gap)
	}
}

func TestTeePopulatesCapture(t *testing.T) {
	want := sseEvents("one", "two")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond) // keep TTFB/Duration comfortably > 0
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, want)
	}))
	defer upstream.Close()

	sk := sink.New(16)
	ts := newProxyServer(t, testConfig(upstream.URL), sk)

	resp, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	call := drainOne(t, sk)
	if call.Status != http.StatusOK {
		t.Errorf("Status = %d, want %d", call.Status, http.StatusOK)
	}
	if call.TTFB <= 0 {
		t.Errorf("TTFB = %v, want > 0", call.TTFB)
	}
	if call.Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", call.Duration)
	}
	if string(call.RespBody) != want {
		t.Errorf("RespBody = %q, want %q", call.RespBody, want)
	}
}

func TestRequestBodyCapturedAndForwarded(t *testing.T) {
	const reqBody = `{"prompt":"hello"}`
	var upstreamSaw []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamSaw, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	sk := sink.New(16)
	ts := newProxyServer(t, testConfig(upstream.URL), sk)

	resp, err := ts.Client().Post(ts.URL, "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if string(upstreamSaw) != reqBody {
		t.Errorf("upstream received %q, want %q", upstreamSaw, reqBody)
	}

	call := drainOne(t, sk)
	if string(call.ReqBody) != reqBody {
		t.Errorf("captured ReqBody = %q, want %q", call.ReqBody, reqBody)
	}
}

func TestOversizedRequestBody(t *testing.T) {
	cfg := testConfig("") // UpstreamURL filled in below
	payload := bytes.Repeat([]byte("a"), cfg.BodyCapBytes+1024)

	var upstreamLen int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		upstreamLen = len(got)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	cfg.UpstreamURL = upstream.URL

	sk := sink.New(16)
	ts := newProxyServer(t, cfg, sk)

	resp, err := ts.Client().Post(ts.URL, "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if upstreamLen != len(payload) {
		t.Errorf("upstream received %d bytes, want %d (must not be truncated)", upstreamLen, len(payload))
	}

	call := drainOne(t, sk)
	if len(call.ReqBody) != cfg.BodyCapBytes {
		t.Errorf("captured ReqBody len = %d, want exactly %d", len(call.ReqBody), cfg.BodyCapBytes)
	}
}

func TestOversizedResponseBody(t *testing.T) {
	cfg := testConfig("")
	payload := bytes.Repeat([]byte("b"), cfg.BodyCapBytes+1024)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(payload)
	}))
	defer upstream.Close()
	cfg.UpstreamURL = upstream.URL

	sk := sink.New(16)
	ts := newProxyServer(t, cfg, sk)

	resp, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("client received %d bytes, want %d byte-identical payload", len(got), len(payload))
	}

	call := drainOne(t, sk)
	if len(call.RespBody) != cfg.BodyCapBytes {
		t.Errorf("captured RespBody len = %d, want exactly %d", len(call.RespBody), cfg.BodyCapBytes)
	}
}

func TestRedactionIntegration(t *testing.T) {
	const sentinel = "sk-sentinel-value"
	var upstreamSaw string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamSaw = r.Header.Get("X-Api-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	sk := sink.New(16)
	ts := newProxyServer(t, testConfig(upstream.URL), sk)

	req, err := http.NewRequest(http.MethodGet, ts.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Api-Key", sentinel)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if upstreamSaw != sentinel {
		t.Errorf("upstream saw X-Api-Key = %q, want unchanged %q", upstreamSaw, sentinel)
	}

	call := drainOne(t, sk)
	if got := call.ReqHeaders.Get("X-Api-Key"); got != redactedValue {
		t.Errorf("captured ReqHeaders X-Api-Key = %q, want %q", got, redactedValue)
	}
}

func TestUpstreamFailurePassesThrough(t *testing.T) {
	const errBody = "internal error from upstream"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, errBody)
	}))
	defer upstream.Close()

	sk := sink.New(16)
	ts := newProxyServer(t, testConfig(upstream.URL), sk)

	resp, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
	if string(got) != errBody {
		t.Errorf("body = %q, want %q", got, errBody)
	}
}

func TestUpstreamUnreachable(t *testing.T) {
	// A server that is immediately closed leaves a dead loopback address:
	// connections to it are refused quickly and deterministically.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	sk := sink.New(16)
	ts := newProxyServer(t, testConfig(deadURL), sk)

	client := ts.Client()
	client.Timeout = 5 * time.Second

	start := time.Now()
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("request took %v, want a quick 502, not a hang", elapsed)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}

	call := drainOne(t, sk)
	if call.Err == nil {
		t.Error("captured call Err = nil, want non-nil for an unreachable upstream")
	}
}

func TestCaptureDisabled(t *testing.T) {
	const body = "plain body"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, body)
	}))
	defer upstream.Close()

	cfg := testConfig(upstream.URL)
	cfg.Capture = false
	sk := sink.New(16)
	ts := newProxyServer(t, cfg, sk)

	resp, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}

	accepted, dropped := sk.Stats()
	if accepted != 0 || dropped != 0 {
		t.Errorf("Stats() = (%d, %d), want (0, 0) with capture disabled", accepted, dropped)
	}
}

func TestFullSinkDoesNotDelayClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	sk := sink.New(1)
	// Fill the sink to capacity with no consumer draining it.
	if ok := sk.Submit(&sink.CapturedCall{StartedAt: time.Now()}); !ok {
		t.Fatal("failed to fill sink to capacity")
	}

	ts := newProxyServer(t, testConfig(upstream.URL), sk)

	start := time.Now()
	resp, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	elapsed := time.Since(start)

	const tightDeadline = 500 * time.Millisecond
	if elapsed > tightDeadline {
		t.Fatalf("request took %v with a full sink, want < %v", elapsed, tightDeadline)
	}

	_, dropped := sk.Stats()
	if dropped == 0 {
		t.Error("dropped = 0, want at least 1 (the sink was already full)")
	}
}
