// Package consumer is the cold-path bridge that turns captured calls into
// rows: one goroutine, owned by Consumer.Run, that drains a *sink.Sink,
// parses each call with internal/parse, and writes it through internal/store.
// See CLAUDE.md's "cold-path pipeline order is fixed" — session resolution
// and cost accounting (the latter not wired until br-GI-1-11) run before
// InsertRequest because they populate columns on the row; analyzers run
// after, because their warnings attach to the row by id.
//
// Error containment is the point of this package: a bad body, a store
// error, or a panicking analyzer is logged once and the loop moves on. If
// Run ever returns early because of a single call's failure, capture
// silently stops for the rest of the process — the worst failure mode in
// the project, because it is invisible. See CLAUDE.md's "fail open".
package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/parse"
	"github.com/abhisheksarkar30/deepseek-lens/internal/sink"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

const (
	// batchSize is the call count that forces a flush regardless of quiet time.
	batchSize = 50
	// batchQuietWait is how long a buffered-but-unflushed batch waits for
	// another call before flushing anyway. A single call submitted with
	// nothing following it becomes readable at most this long after arrival.
	batchQuietWait = 250 * time.Millisecond

	// shutdownBound is the hard cap on how long Run keeps draining the sink
	// after ctx is cancelled. Losing the last few calls on Ctrl-C is
	// acceptable; hanging on exit is not.
	shutdownBound = 2 * time.Second
	// shutdownIdle lets shutdown return well before shutdownBound once the
	// sink has genuinely gone quiet, instead of always paying the full cap.
	shutdownIdle = 100 * time.Millisecond
)

// Store is the narrow slice of *store.Store the consumer writes through.
// *store.Store satisfies it automatically (structural typing) — tests
// inject a fake to cover a failing-store scenario without a real database.
type Store interface {
	InsertRequest(ctx context.Context, r *store.Request) (int64, error)
	InsertWarnings(ctx context.Context, reqID int64, warnings []store.Warning) error
}

// Stats is a snapshot of Consumer's counters, safe to read concurrently
// with Run — doctor and the dashboard poll it from another goroutine.
type Stats struct {
	Processed   uint64
	Failed      uint64
	Flushes     uint64 // number of batch flushes issued; « Processed under load is how batching is verified.
	LastWriteAt time.Time
}

// Consumer drains a *sink.Sink and writes each call to Store. Build one with
// New and run it with Run; there is exactly one Run goroutine per Consumer,
// matching the store's single-writer discipline (CLAUDE.md).
type Consumer struct {
	sink      *sink.Sink
	store     Store
	resolver  SessionResolver
	analyzers []Analyzer

	processed     atomic.Uint64
	failed        atomic.Uint64
	flushes       atomic.Uint64
	lastWriteAtNs atomic.Int64
}

// New builds a Consumer that drains sk and writes through st. resolver may
// be nil (this bead injects nil — Request.SessionID stays unset). analyzers
// run, in order, after every insert; the slice is empty by default.
func New(sk *sink.Sink, st Store, resolver SessionResolver, analyzers ...Analyzer) *Consumer {
	return &Consumer{sink: sk, store: st, resolver: resolver, analyzers: analyzers}
}

// Stats returns a snapshot of the consumer's counters.
func (c *Consumer) Stats() Stats {
	var lastWrite time.Time
	if ns := c.lastWriteAtNs.Load(); ns != 0 {
		lastWrite = time.Unix(0, ns).UTC()
	}
	return Stats{
		Processed:   c.processed.Load(),
		Failed:      c.failed.Load(),
		Flushes:     c.flushes.Load(),
		LastWriteAt: lastWrite,
	}
}

// Run drains the sink until ctx is cancelled or the sink is closed and
// drained, batching writes: a flush fires at batchSize calls or
// batchQuietWait of inactivity since the first unflushed call, whichever
// comes first. On ctx cancellation it drains whatever is already sitting in
// the sink for up to shutdownBound, flushes once more, and returns — it
// never blocks past that bound. Run itself never returns a non-nil error;
// every per-call failure is contained and counted in Stats instead (see the
// package doc).
func (c *Consumer) Run(ctx context.Context) error {
	ch := c.sink.Drain(ctx)
	batch := make([]*sink.CapturedCall, 0, batchSize)

	var timer *time.Timer
	var timerC <-chan time.Time
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
			timerC = nil
		}
	}
	flush := func(writeCtx context.Context) {
		if len(batch) == 0 {
			return
		}
		c.flushBatch(writeCtx, batch)
		batch = batch[:0]
		stopTimer()
	}

	for {
		select {
		case call, ok := <-ch:
			if !ok {
				flush(ctx)
				return nil
			}
			batch = append(batch, call)
			if len(batch) >= batchSize {
				flush(ctx)
				continue
			}
			if timer == nil {
				timer = time.NewTimer(batchQuietWait)
				timerC = timer.C
			}

		case <-timerC:
			timer = nil
			timerC = nil
			flush(ctx)

		case <-ctx.Done():
			// ctx is already cancelled here, so the drain-and-final-flush
			// writes below must not run on it: every store call would fail
			// instantly with context.Canceled, silently dropping the whole
			// final batch (that is exactly the "invisible failure" CLAUDE.md
			// warns about). Give them their own bounded, non-cancelled
			// context instead — shutdownBound covers both the drain and the
			// flush that follows it.
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownBound)
			c.drainRemaining(ch, &batch)
			flush(shutdownCtx)
			shutdownCancel()
			return nil
		}
	}
}

// drainRemaining reads whatever is left on ch into batch, stopping once
// either shutdownIdle passes with nothing new arriving or shutdownBound is
// reached overall — bounded either way, per Run's doc comment.
func (c *Consumer) drainRemaining(ch <-chan *sink.CapturedCall, batch *[]*sink.CapturedCall) {
	hardDeadline := time.NewTimer(shutdownBound)
	defer hardDeadline.Stop()
	idle := time.NewTimer(shutdownIdle)
	defer idle.Stop()

	for {
		select {
		case call, ok := <-ch:
			if !ok {
				return
			}
			*batch = append(*batch, call)
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(shutdownIdle)

		case <-idle.C:
			return
		case <-hardDeadline.C:
			return
		}
	}
}

// flushBatch processes every call in batch and counts the flush itself —
// the batching test asserts Stats().Flushes stays well below the call
// count, which is what actually demonstrates batching happened (row count
// must still equal call count 1:1, so it can't be measured by counting
// InsertRequest calls).
func (c *Consumer) flushBatch(ctx context.Context, batch []*sink.CapturedCall) {
	c.flushes.Add(1)
	for _, call := range batch {
		c.processCall(ctx, call)
	}
}

// processCall is the per-call boundary error containment applies at: a
// panic anywhere in the pipeline for one call (not just in an analyzer) is
// recovered, logged once, and counted as failed, so it can never take the
// whole consumer down.
func (c *Consumer) processCall(ctx context.Context, call *sink.CapturedCall) {
	defer func() {
		if r := recover(); r != nil {
			c.failed.Add(1)
			log.Printf("consumer: panic processing call %s: %v", call.ID, r)
		}
	}()
	c.doProcessCall(ctx, call)
}

// doProcessCall runs the bead's fixed 9-step pipeline for one call:
// ExtractMeta -> ExtractUsage -> build store.Request -> resolve session ->
// (cost step: absent in this bead) -> InsertRequest -> run analyzers ->
// synthesize an upstream_error warning if call.Err != nil -> InsertWarnings.
func (c *Consumer) doProcessCall(ctx context.Context, call *sink.CapturedCall) {
	meta := parse.ExtractMeta(call.ReqBody, call.ReqHeaders)
	usage, _ := parse.ExtractUsage(call.RespBody, call.RespHeaders.Get("Content-Type"))
	// ExtractUsage's error only ever reports a parsed SSE "error" event —
	// the Usage returned alongside it is still whatever was accumulated so
	// far, and per parse's own contract a shape mismatch degrades rather
	// than fails. Consistent with "fail open", it is not surfaced here.

	req := &store.Request{
		StartedAt:  call.StartedAt,
		TTFB:       call.TTFB,
		Duration:   call.Duration,
		Method:     call.Method,
		Path:       call.Path,
		RemoteAddr: call.RemoteAddr,
		Status:     call.Status,

		ReqHeaders:  headerJSON(call.ReqHeaders),
		RespHeaders: headerJSON(call.RespHeaders),
		ReqBody:     call.ReqBody,
		RespBody:    call.RespBody,

		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		CacheReadTokens:     usage.CacheReadTokens,

		ModelRequested: meta.ModelRequested,
		ModelResolved:  usage.Model,
		PrefixHash:     meta.PrefixHash,
	}
	if usage.StopReason != "" {
		sr := usage.StopReason
		req.StopReason = &sr
	}
	if meta.SessionHeader != "" {
		sh := meta.SessionHeader
		req.SessionHeader = &sh
	}
	if call.Err != nil {
		errText := call.Err.Error()
		req.ErrorText = &errText
	}

	// Resolve session before insert: SessionID is a column on the row.
	// This bead injects a nil resolver, so this is a no-op seam for now.
	if c.resolver != nil {
		if sid := c.resolver.Resolve(meta, time.Now()); sid != "" {
			req.SessionID = &sid
		}
	}

	// Cost step (br-GI-1-11) belongs here, before InsertRequest, writing
	// req.CostUSD/req.CostSource. Absent in this bead.

	id, err := c.store.InsertRequest(ctx, req)
	if err != nil {
		c.failed.Add(1)
		log.Printf("consumer: insert request failed for call %s: %v", call.ID, err)
		return
	}
	c.processed.Add(1)
	c.lastWriteAtNs.Store(time.Now().UnixNano())

	var warnings []store.Warning
	for _, a := range c.analyzers {
		warnings = append(warnings, c.runAnalyzer(a, meta, usage, req)...)
	}
	if call.Err != nil {
		warnings = append(warnings, store.Warning{
			RequestID: id,
			Kind:      "upstream_error",
			Severity:  "error",
			Detail:    call.Err.Error(),
			CreatedAt: time.Now(),
		})
	}
	if len(warnings) == 0 {
		return
	}
	if err := c.store.InsertWarnings(ctx, id, warnings); err != nil {
		c.failed.Add(1)
		log.Printf("consumer: insert warnings failed for request %d: %v", id, err)
	}
}

// runAnalyzer calls a.Analyze with its own recover, so one panicking
// analyzer neither kills the consumer nor silently drops the row's other
// warnings — it becomes a warning row of its own, naming the analyzer.
func (c *Consumer) runAnalyzer(a Analyzer, meta parse.Meta, usage parse.Usage, req *store.Request) (warnings []store.Warning) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("consumer: analyzer %T panicked: %v", a, r)
			warnings = []store.Warning{{
				RequestID: req.ID,
				Kind:      "analyzer_panic",
				Severity:  "error",
				Detail:    fmt.Sprintf("%T panicked: %v", a, r),
				CreatedAt: time.Now(),
			}}
		}
	}()
	return a.Analyze(meta, usage, req)
}

// headerJSON marshals h (already redacted by the caller, per sink.go) into
// the JSON text store.Request.ReqHeaders/RespHeaders expect. A nil/empty
// header set or a marshal failure (never expected for http.Header) both
// degrade to "", matching ExtractMeta's own never-error discipline.
func headerJSON(h http.Header) string {
	if len(h) == 0 {
		return ""
	}
	b, err := json.Marshal(h)
	if err != nil {
		return ""
	}
	return string(b)
}
