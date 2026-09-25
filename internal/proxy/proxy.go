// Package proxy is the hot path: a transparent, streaming reverse proxy to
// the configured DeepSeek upstream that tees a copy of every request and
// response into the sink for later analysis, without ever buffering the
// stream itself. See CLAUDE.md's "Architecture essentials" for the
// data-flow and latency invariants this package exists to uphold: proxy
// depends only on sink and config, and never on store or analyze.
package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/sink"
)

// New builds the reverse proxy handler for cfg.UpstreamURL. With
// cfg.Capture true, every request and response is teed into sk — bodies
// capped at cfg.BodyCapBytes, sensitive headers redacted — without ever
// delaying the client: the hot path copies bytes and does nothing else.
// With cfg.Capture false, this is a plain reverse proxy: no teeing, no sink
// calls, the escape hatch for risk 4.
func New(cfg *config.Config, sk *sink.Sink) (http.Handler, error) {
	target, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("proxy: invalid upstream URL %q: %w", cfg.UpstreamURL, err)
	}

	// Default is 2, which serialises connection reuse under agentic load.
	// http.DefaultTransport already has ForceAttemptHTTP2 set.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 100

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target) // rewrites scheme/host and joins target's path as a prefix
		},
		Transport: transport,
		// Flush after every write. This is what makes SSE stream through
		// rather than accumulate in a buffer. Non-negotiable.
		FlushInterval: -1,
		// Never swallow, never rewrite into a success: pass upstream
		// failures through faithfully as 502 and record them.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if st, ok := r.Context().Value(stateKey{}).(*captureState); ok {
				st.submit(0, nil, nil, nil, err)
			}
			w.WriteHeader(http.StatusBadGateway)
		},
	}

	if !cfg.Capture {
		return rp, nil
	}

	bodyCap := cfg.BodyCapBytes

	rp.ModifyResponse = func(res *http.Response) error {
		st, ok := res.Request.Context().Value(stateKey{}).(*captureState)
		if !ok {
			return nil
		}
		st.ttfb = time.Since(st.start)

		status := res.StatusCode
		respHeaders := redactHeaders(res.Header)
		respBuf := newBoundedBuffer(bodyCap)
		orig := res.Body
		res.Body = &teeCloser{
			r: io.TeeReader(orig, respBuf),
			c: orig,
			onClose: func() {
				st.submit(status, respHeaders, respBuf.Bytes(), respBuf.Tail(), nil)
			},
		}
		return nil
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBuf := newBoundedBuffer(bodyCap)
		r.Body = &teeCloser{r: io.TeeReader(r.Body, reqBuf), c: r.Body}

		st := &captureState{
			start:      time.Now(),
			method:     r.Method,
			path:       r.URL.Path,
			remoteAddr: r.RemoteAddr,
			reqHeaders: redactHeaders(r.Header),
			reqBody:    reqBuf,
			sk:         sk,
		}
		if rm, ok := r.Context().Value(replayKey{}).(ReplayMeta); ok {
			of := rm.Of
			st.replayOf = &of
			if rm.Edits != "" {
				edits := rm.Edits
				st.replayEdits = &edits
			}
			st.noCapture = rm.NoCapture
		}
		r = r.WithContext(context.WithValue(r.Context(), stateKey{}, st))
		rp.ServeHTTP(w, r)
	}), nil
}

// ReplayMeta carries a replay's linkage from the handler that re-issues a
// captured request (internal/api's replay endpoint) to the capture path that
// records it. It exists so a replay produces the *same kind* of row an ordinary
// proxied call does — same transport, same tee, same single writer — with
// replay_of and replay_edits filled in, instead of a second send path that
// would have to reproduce all of it.
type ReplayMeta struct {
	// Of is the original request's row id. Requests ids start at 1, so it is
	// always a real row when WithReplay is used at all.
	Of int64
	// Edits is the serialized [{path, old, new}] array of edits applied to the
	// replayed body, or "" when the replay has no edits.
	Edits string
	// NoCapture is --no-capture: the request is still sent, but the capture
	// path skips the sink entirely, so no row is written at all. That is
	// deliberately not "submit a call flagged as uncaptured" — a submitted
	// call is a row whatever flags it carries.
	NoCapture bool
}

// replayKey is the context key a replay's metadata is stashed under, for the
// same reason stateKey{} exists: New's outer closure sets the captureState, but
// the metadata arrives on the request the *caller* constructed.
type replayKey struct{}

// WithReplay returns r with meta attached for the proxy's capture path to pick
// up. It is how the replay endpoint hands a re-issued request to the very
// Handler that serves live traffic, so the replay is rewritten, streamed,
// teed, size-capped and redacted by the code that already does all of that.
func WithReplay(r *http.Request, meta ReplayMeta) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), replayKey{}, meta))
}

// NewServer builds the http.Server that fronts New's handler, with the
// timeouts the hot path requires: a bounded ReadHeaderTimeout, no
// WriteTimeout (a streaming response can legitimately run for minutes), and
// an IdleTimeout to reclaim idle keep-alive connections.
func NewServer(cfg *config.Config, sk *sink.Sink) (*http.Server, error) {
	h, err := New(cfg, sk)
	if err != nil {
		return nil, err
	}
	return &http.Server{
		Addr:              cfg.ProxyAddr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}, nil
}

// stateKey is the context key under which a request's captureState is
// stashed so both ModifyResponse and ErrorHandler — which only ever see the
// http.Request, not any value New's outer closure held — can reach it.
type stateKey struct{}

// captureState carries everything needed to build a sink.CapturedCall for
// one in-flight request, gathered before the request is proxied so that
// neither ModifyResponse nor ErrorHandler need to re-derive it.
type captureState struct {
	start      time.Time
	ttfb       time.Duration
	method     string
	path       string
	remoteAddr string
	reqHeaders http.Header
	reqBody    *boundedBuffer
	sk         *sink.Sink

	// Replay linkage, copied off the request's ReplayMeta (see WithReplay).
	// All three are zero for ordinary traffic.
	replayOf    *int64
	replayEdits *string
	noCapture   bool
}

// submit assembles and submits the CapturedCall. It is called at most once per
// request, from whichever of ModifyResponse's response-body Close or
// ErrorHandler fires — the two are mutually exclusive. Submit itself never
// blocks, so this never delays anything: by the time it runs, either the last
// byte has already reached the client or the request has already failed.
//
// A --no-capture replay returns before submitting (see ReplayMeta.NoCapture):
// the send still happens, but nothing reaches the sink, so the consumer writes
// no row. Skipping the submit is the only honest way to express that — a
// submitted call becomes a row regardless of any flag on it.
func (st *captureState) submit(status int, respHeaders http.Header, respBody, respTail []byte, callErr error) {
	if st.noCapture {
		return
	}
	st.sk.Submit(&sink.CapturedCall{
		StartedAt:   st.start,
		TTFB:        st.ttfb,
		Duration:    time.Since(st.start),
		Method:      st.method,
		Path:        st.path,
		RemoteAddr:  st.remoteAddr,
		Status:      status,
		ReqHeaders:  st.reqHeaders,
		RespHeaders: respHeaders,
		ReqBody:     st.reqBody.Bytes(),
		RespBody:    respBody,
		RespTail:    respTail,
		ReplayOf:    st.replayOf,
		ReplayEdits: st.replayEdits,
		Err:         callErr,
	})
}

// usageTailBytes is the trailing window kept once the head cap is full.
// Past the cap each Write appends then memmoves this window; that cost is
// bounded and post-cap, so it does not move TTFB.
const usageTailBytes = 16 * 1024

// boundedBuffer accumulates up to capacity bytes; writes past that are
// dropped rather than appended, so it caps only what lens stores. Once the
// head is full, a response buffer also keeps the most recent usageTailBytes
// of the overflow. Write always reports the full length written and never
// errors, so wrapping it in an io.TeeReader never affects the stream being teed.
type boundedBuffer struct {
	buf     bytes.Buffer
	cap     int
	tailCap int
	tail    []byte
}

func newBoundedBuffer(capacity int) *boundedBuffer {
	return &boundedBuffer{cap: capacity, tailCap: usageTailBytes}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := b.cap - b.buf.Len(); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		b.buf.Write(p[:room])
		p = p[room:]
	}
	if len(p) > 0 && b.tailCap > 0 {
		b.tail = append(b.tail, p...)
		if extra := len(b.tail) - b.tailCap; extra > 0 {
			b.tail = append([]byte(nil), b.tail[extra:]...)
		}
	}
	return n, nil
}

func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }

// Tail returns the most recent overflow bytes, or nil when the head has not
// overflowed.
func (b *boundedBuffer) Tail() []byte {
	if len(b.tail) == 0 {
		return nil
	}
	return b.tail
}

// teeCloser wraps a tee'd reader with the original body's Close, running
// onClose (when set) after that Close returns. For a response body this is
// the fire-and-forget capture submit — it runs only after the copy loop
// that streams to the client has finished, i.e. strictly after the last
// byte reached the client. The hot path never seeks or rewinds this data;
// it is only ever inspected here, in Close, once the stream is done.
type teeCloser struct {
	r       io.Reader
	c       io.Closer
	onClose func()
}

func (t *teeCloser) Read(p []byte) (int, error) { return t.r.Read(p) }

func (t *teeCloser) Close() error {
	err := t.c.Close()
	if t.onClose != nil {
		t.onClose()
	}
	return err
}
