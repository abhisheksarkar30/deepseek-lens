// Package consumer is the cold-path bridge that turns captured calls into
// rows: one goroutine, owned by Consumer.Run, that drains a *sink.Sink,
// parses each call with internal/parse, and writes it through internal/store.
// See CLAUDE.md's "cold-path pipeline order is fixed" — session resolution
// and cost accounting run before InsertRequest because they populate columns
// on the row; analyzers run after, because their warnings attach to the row
// by id. Cost accounting (br-GI-1-11) is a plain step, not an Analyzer: it
// produces column values, no warnings.
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
	"github.com/abhisheksarkar30/deepseek-lens/internal/pricing"
	"github.com/abhisheksarkar30/deepseek-lens/internal/sink"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// microPerDollar is the micro-dollar scale pricing.Cost.Amount is denominated
// in. It exists so the conversion to the dollars `requests.cost_usd` stores
// has exactly one site: here, in the pre-insert cost step.
const microPerDollar = 1e6

// PriceTable is the price source the pre-insert cost step prices against.
// *pricing.Loader is the production implementation — it re-reads
// prices.toml when the file changes, which is what makes `lens prices --set`
// take effect without restarting `lens serve`. pricing.Table satisfies it
// directly (its Table method returns itself), so a test can inject fixed
// rates with no wrapper.
type PriceTable interface {
	Table() pricing.Table
}

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
	sink       *sink.Sink
	store      Store
	resolver   SessionResolver
	aggregator SessionAggregator
	analyzers  []Analyzer
	prices     PriceTable

	processed     atomic.Uint64
	failed        atomic.Uint64
	flushes       atomic.Uint64
	lastWriteAtNs atomic.Int64
}

// New builds a Consumer that drains sk and writes through st. resolver may
// be nil, in which case Request.SessionID stays unset for every call.
// analyzers run, in order, after every insert; the slice is empty by
// default. A price table and a session aggregator are installed separately
// with SetPriceTable and SetSessionAggregator, for the same reason: both are
// separate pipeline steps rather than analyzers, and the beads that
// introduced them could not have them baked into New without rewriting the
// tests of the beads before them.
func New(sk *sink.Sink, st Store, resolver SessionResolver, analyzers ...Analyzer) *Consumer {
	return &Consumer{sink: sk, store: st, resolver: resolver, analyzers: analyzers}
}

// SetPriceTable installs the price table the pre-insert cost step uses.
// Leaving it unset is legal and leaves CostUSD/CostSource NULL on every row,
// which is what br-GI-1-07's tests do — cost is a separate pre-insert step,
// not an Analyzer, so it is not part of New's variadic.
func (c *Consumer) SetPriceTable(pt PriceTable) { c.prices = pt }

// SetSessionAggregator installs the post-insert step that folds each call's
// tokens, cost, and warning count into its session's totals (br-GI-1-12).
// Leaving it unset is legal and leaves the sessions table untouched, which
// is what every pre-bead test does. One *session.Resolver satisfies both
// this and SessionResolver, so `lens serve` installs the same object twice.
func (c *Consumer) SetSessionAggregator(sa SessionAggregator) { c.aggregator = sa }

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

// doProcessCall runs the bead's fixed pipeline for one call: ExtractMeta ->
// ExtractUsage -> build store.Request -> resolve session -> cost step ->
// InsertRequest -> run analyzers -> synthesize an upstream_error warning if
// call.Err != nil -> InsertWarnings -> fold the call into its session's
// totals.
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
	if c.resolver != nil {
		if sid := c.resolver.Resolve(meta, time.Now()); sid != "" {
			req.SessionID = &sid
		}
	}

	// Cost step (br-GI-1-11): priced *before* the insert because CostUSD and
	// CostSource are columns on the row. It is not an analyzer — it returns
	// no warnings — and it never fails the call: an unreadable or mid-edit
	// price file degrades to "unpriced", per CLAUDE.md's "fail open". The
	// model keyed is the upstream-resolved one, which is what the table is
	// keyed by; an unresolved model is priced as "unknown-model".
	if c.prices != nil {
		cost := pricing.Compute(req.ModelResolved, usage, c.prices.Table())
		if cost.Amount != nil {
			dollars := float64(*cost.Amount) / microPerDollar
			req.CostUSD = &dollars
		}
		src := cost.Source
		req.CostSource = &src
	}

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
	if len(warnings) > 0 {
		if err := c.store.InsertWarnings(ctx, id, warnings); err != nil {
			c.failed.Add(1)
			log.Printf("consumer: insert warnings failed for request %d: %v", id, err)
		}
	}

	// Session aggregates go last, and go through a step that is not the
	// Store: warningCount is only final here, and the tokens/cost it also
	// folds in are on req by now. A failure is logged without counting a
	// failed call — the row this call was about is already committed, so
	// calling the call itself failed would be a lie.
	if c.aggregator != nil && req.SessionID != nil {
		if err := c.aggregator.RecordCall(ctx, *req.SessionID, req, len(warnings)); err != nil {
			log.Printf("consumer: record session aggregate failed for call %s: %v", call.ID, err)
		}
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
