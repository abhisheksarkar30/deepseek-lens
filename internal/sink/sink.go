// Package sink provides the non-blocking handoff between the hot proxy path
// and the cold consumer path. Submit performs a bounded, non-blocking send:
// under load it drops rather than waits, which is what keeps a stalled or
// absent consumer from ever adding latency to a client request.
package sink

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// DefaultCapacity is the channel capacity used when the caller has no
// config-derived value to pass to New.
const DefaultCapacity = 4096

// CapturedCall is the transport struct between the hot proxy path and the
// cold consumer path. It holds only cheap references and already-read byte
// slices — nothing here requires further I/O to produce.
type CapturedCall struct {
	ID         string
	StartedAt  time.Time
	TTFB       time.Duration
	Duration   time.Duration
	Method     string
	Path       string
	RemoteAddr string
	Status     int

	ReqHeaders  http.Header // already redacted by the caller
	RespHeaders http.Header // already redacted by the caller
	ReqBody     []byte      // body policy already applied by the caller
	RespBody    []byte      // nil when streaming; filled by a separate accumulator
	// RespTail is the last bytes past the body cap. Nil unless RespBody was
	// truncated. Not stored in the DB.
	RespTail []byte

	// ReplayOf and ReplayEdits link a re-issued call to the capture it came
	// from (br-GI-1-13): the original request's row id, and the serialized
	// [{path, old, new}] array of the edits applied to its body. Both are nil
	// for every ordinary proxied call, which is every call but a replay. Only
	// already-produced values are carried here — the serialized edits text is
	// built by the sender, so the capture path still does no I/O.
	ReplayOf    *int64
	ReplayEdits *string

	Err error
}

// Sink is a bounded, non-blocking handoff of *CapturedCall between producer
// (proxy) and consumer (analyzer) goroutines.
type Sink struct {
	ch       chan *CapturedCall
	seq      uint64
	accepted uint64
	dropped  uint64
}

// New returns a Sink whose channel has the given capacity. Pass
// DefaultCapacity when there is no config-derived value.
func New(capacity int) *Sink {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Sink{ch: make(chan *CapturedCall, capacity)}
}

// Submit assigns call.ID and attempts a non-blocking send. It returns true
// if the call was accepted, false if the sink was full and the call was
// dropped. Submit never blocks and never spawns a goroutine — callers may
// inspect the return value to log a drop, but ignoring it is correct:
// dropping under load is expected, not a failure.
func (s *Sink) Submit(call *CapturedCall) bool {
	n := atomic.AddUint64(&s.seq, 1)
	var buf [32]byte
	b := strconv.AppendInt(buf[:0], call.StartedAt.UnixNano(), 36)
	b = append(b, '-')
	b = strconv.AppendUint(b, n, 36)
	call.ID = string(b)

	select {
	case s.ch <- call:
		atomic.AddUint64(&s.accepted, 1)
		return true
	default:
		atomic.AddUint64(&s.dropped, 1)
		return false
	}
}

// Stats returns the accepted and dropped counts observed so far.
func (s *Sink) Stats() (accepted, dropped uint64) {
	return atomic.LoadUint64(&s.accepted), atomic.LoadUint64(&s.dropped)
}

// Drain returns the receive side of the channel for the consumer to range
// over. Drain itself does no waiting or cancellation — the caller owns
// reading and is the one that should stop selecting on this channel when
// its own context is done (consumer.Run does, alongside this channel).
func (s *Sink) Drain() <-chan *CapturedCall {
	return s.ch
}

// Close closes the channel, causing a consumer ranging over Drain's channel
// to terminate once it has drained any buffered calls. Callers must ensure
// Submit is not called concurrently with or after Close.
func (s *Sink) Close() {
	close(s.ch)
}
