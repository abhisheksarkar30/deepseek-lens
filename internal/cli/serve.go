package cli

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/analyze"
	"github.com/abhisheksarkar30/deepseek-lens/internal/api"
	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/consumer"
	"github.com/abhisheksarkar30/deepseek-lens/internal/pricing"
	"github.com/abhisheksarkar30/deepseek-lens/internal/proxy"
	"github.com/abhisheksarkar30/deepseek-lens/internal/session"
	"github.com/abhisheksarkar30/deepseek-lens/internal/sink"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
	"github.com/abhisheksarkar30/deepseek-lens/internal/web"
)

// shutdownGrace bounds how long Serve waits for both http.Servers to
// finish in-flight requests during shutdown, on top of the consumer's own
// shutdownBound drain (internal/consumer).
const shutdownGrace = 5 * time.Second

// Serve runs `lens serve`: the proxy listener (br-GI-1-03), the dashboard
// listener (internal/api + internal/web, br-GI-1-09), and the consumer
// (br-GI-1-07), in one process. It blocks until SIGINT, then shuts down both
// servers with a bounded drain.
func Serve(args []string) error {
	cfg, err := config.Load(translateNoCapture(args))
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	// The peak calendar (br-GI-24), built once from the two config keys
	// cfg.Validate just proved parse. Constructed here, next to the check
	// that guarantees it, rather than at its first use: the error below is
	// unreachable for an already-validated config, and keeping the two lines
	// together is what makes that visible.
	cal, err := pricing.NewCalendar(cfg.OffPeakDates, cfg.WorkDates)
	if err != nil {
		return fmt.Errorf("serve: build calendar: %w", err)
	}

	statePath := serveStatePath(cfg.DBPath)
	early := newEarlyState()
	early.LogPath = filepath.Join(filepath.Dir(cfg.DBPath), "serve.log")
	existing, created, err := writeServeStateExclusive(statePath, early)
	if err != nil {
		return fmt.Errorf("serve: state file: %w", err)
	}
	owned := created
	if !created && existing != nil && !isProcessAlive(existing.PID) {
		early.StartedAt = existing.StartedAt
		if err := writeServeState(statePath, early); err != nil {
			return fmt.Errorf("serve: state file: %w", err)
		}
		owned = true
	}
	bindLost := false
	removeState := func() {
		if !owned {
			return
		}
		if err := removeServeStateIfOwner(statePath, os.Getpid(), bindLost); err != nil {
			log.Printf("serve: remove state file: %v", err)
		}
	}
	defer removeState()

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("serve: open store: %w", err)
	}
	defer st.Close()

	// br-GI-1-06's startup self-test: scan stored header JSON for a
	// reachable x-api-key value, belt-and-braces for the redaction the
	// proxy already applies.
	checkRedaction(context.Background(), st, log.Printf)

	// br-GI-17-06's retention purge: a startup run plus a 24-hour ticker
	// (below), so a tool that is opened and closed around work sessions
	// still sees the setting take effect. purgeOnStartup no-ops when
	// cfg.RetentionDays <= 0 (the default: keep forever).
	sk := sink.New(sink.DefaultCapacity)
	broker := api.NewBroker()
	// PublishingStore wraps st so the consumer's writes also publish SSE
	// events (br-GI-1-09's broker) — see internal/api/publishing_store.go's
	// doc comment for why this lives here as a decorator instead of a
	// consumer.go change: consumer.go is outside this bead's Files-to-Touch
	// list, and consumer.New's Store parameter is already a narrow
	// interface anything satisfies structurally.
	pubStore := api.NewPublishingStore(st, broker)
	// Session grouping (br-GI-1-12): one object is both halves of it — the
	// pre-insert resolver that names the session a call belongs to, and the
	// post-insert aggregator that folds the call into that session's totals.
	// It writes through the bare store, not pubStore: a session's aggregate
	// moving is not a new call arriving, and the dashboard's SSE feed is a
	// feed of calls.
	sess := session.New(st, cfg.SessionGapMinutes)
	//
	// The dropped-parameter rule engine (br-GI-1-10) is registered here, not
	// baked into consumer.New, for the same reason the publishing store is a
	// decorator: consumer.New already takes its analyzers as arguments, and
	// br-GI-1-07's tests construct Consumers that assert exact warning counts
	// on bodies those rules legitimately fire on. Defaulting them in would
	// have rewritten bead-07's tests; passing the config-resolved engine in
	// keeps the flags->rules->rows path explicit, and Analyze stays a pure
	// function of the config tables it is handed.
	//
	// cal is the third of the engine's config-resolved tables and rides in as
	// an argument rather than a setter, so no caller can forget it: a
	// forgotten calendar would price every 2026 holiday at 2x with every test
	// still green. That is also why wireCalendar exists for the other two
	// install points — see its doc.
	cons := consumer.New(sk, pubStore, sess, analyze.NewRules(cfg.ModelMap, cfg.ModelMaxTokens, cal))
	cons.SetSessionAggregator(sess)
	// The cost step (br-GI-1-11) is installed here rather than baked into
	// consumer.New for the same reason the rules engine above is: it is a
	// separate pre-insert step, not an analyzer, and br-GI-1-07's tests
	// construct Consumers without one. The Loader re-reads prices.toml when
	// it changes, so `lens prices --set` takes effect without a restart.
	cons.SetPriceTable(pricing.NewLoader(pricing.DefaultPath()))
	// Undoing transport Content-Encoding (internal/decode) is installed here for
	// the third time for the same reason: it is another separate pre-insert step,
	// and a Consumer built without it — every test that predates this — must keep
	// storing bodies exactly as captured. The limit is cfg.BodyCapBytes, the same
	// cap the proxy tees with: without a second cap on the decoded form, a small
	// compressed body would expand past the configured per-body cap.
	cons.SetBodyDecoding(cfg.BodyCapBytes)

	proxySrv, err := proxy.NewServer(cfg, sk)
	if err != nil {
		return fmt.Errorf("serve: build proxy: %w", err)
	}
	// The dashboard's read endpoints use the bare *store.Store (not
	// pubStore) so a read can never itself trigger a broker publish.
	//
	// proxySrv.Handler is handed to the API as well: POST
	// /api/requests/{id}/replay re-issues a captured request through the live
	// proxy Handler, which is what makes a replay use the same transport, the
	// same tee and the same consumer writer as every other call (br-GI-1-13).
	// cfg.ReplayEnabled is the endpoint's opt-in control — the dashboard route
	// exists but answers 403 until `lens serve --replay` is passed.
	dashAPI := api.New(st, sk, cons, broker, web.Files, proxySrv.Handler, cfg.ReplayEnabled)
	dashAPI.SetPricing(pricing.DefaultPath())
	dashAPI.SetRetention(cfg.RetentionDays, st)
	wireCalendar(cal, cons, dashAPI)
	dashSrv := &http.Server{
		Addr:    cfg.DashboardAddr,
		Handler: dashAPI,
	}

	printBanner(os.Stdout, cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	dashAPI.SetStop(stop)

	proxyLn, err := net.Listen("tcp", cfg.ProxyAddr)
	if err != nil {
		bindLost = true
		return fmt.Errorf("serve: listen proxy: %w", err)
	}
	dashLn, err := net.Listen("tcp", cfg.DashboardAddr)
	if err != nil {
		bindLost = true
		proxyLn.Close()
		return fmt.Errorf("serve: listen dashboard: %w", err)
	}
	if owned {
		early.ProxyAddr = proxyLn.Addr().String()
		early.DashboardAddr = dashLn.Addr().String()
		if err := writeServeState(statePath, early); err != nil {
			log.Printf("serve: rewrite state file: %v", err)
		}
	}

	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		_ = cons.Run(ctx) // Run never returns a non-nil error; see its doc.
	}()

	errCh := make(chan error, 2)
	go func() {
		if err := proxySrv.Serve(proxyLn); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("proxy server: %w", err)
		}
	}()
	go func() {
		if err := dashSrv.Serve(dashLn); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("dashboard server: %w", err)
		}
	}()

	// One 24h maintenance goroutine. A second reader of the same ticker
	// would steal alternate ticks. It starts only after both listeners are up.
	var maint sync.WaitGroup
	maint.Add(1)
	go func() {
		defer maint.Done()
		runMaintenance(ctx, st, cfg.RetentionDays)
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		log.Printf("serve: %v", err)
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := proxySrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("serve: proxy shutdown: %v", err)
	}
	if err := dashSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("serve: dashboard shutdown: %v", err)
	}

	<-consumerDone
	joined := make(chan struct{})
	go func() {
		maint.Wait()
		close(joined)
	}()
	select {
	case <-joined:
		if err := st.Close(); err != nil {
			log.Printf("serve: close store: %v", err)
		}
		removeState()
		owned = false // defer must not remove again after the explicit removal
	case <-time.After(shutdownGrace):
		log.Printf("serve: maintenance goroutine still running; leaving state file in place")
		owned = false
	}

	return nil
}

func newEarlyState() serveState {
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	return serveState{
		PID:       os.Getpid(),
		Exe:       exe,
		Args:      append([]string(nil), os.Args[1:]...),
		Cwd:       cwd,
		StartedAt: time.Now().UTC(),
	}
}

func runMaintenance(ctx context.Context, st *store.Store, days int) {
	purgeOnStartup(ctx, st, days, log.Printf)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			purgeOnStartup(ctx, st, days, log.Printf)
		}
	}
}

// wireCalendar installs cal on both cold-path seams that take it as an
// optional setter. Split out from Serve for the same reason checkRedaction
// is: Serve cannot be driven from a test (two real listeners, a blocking
// signal context), so the wiring has to be exercised on its own.
//
// dash is an interface rather than *api.API because api.New returns the
// unexported *api, which package cli cannot name — the parameter only has to
// accept the one method this function calls.
//
// The third install point, analyze.NewRules, is deliberately not here: it is
// a constructor argument, so it is compile-enforced and cannot be forgotten.
// These two are the silent ones — the zero calendar is exactly today's rule,
// so a missed setter costs nothing in any test that does not look for it.
func wireCalendar(cal pricing.Calendar, cons *consumer.Consumer, dash interface{ SetCalendar(pricing.Calendar) }) {
	cons.SetCalendar(cal)
	dash.SetCalendar(cal)
}

// checkRedaction runs br-GI-1-06's startup leak self-test and reports the
// result through logf. It logs and continues rather than refusing to start:
// a reachable credential is worth knowing about, but a coding session must
// not die over the observer's problem (CLAUDE.md's "fail open"). Split out
// from Serve so the wiring is testable — the self-test's whole value is
// that it actually runs at boot, which is not true of a function no one
// calls.
func checkRedaction(ctx context.Context, st *store.Store, logf func(string, ...any)) {
	if err := st.RedactCheck(ctx); err != nil {
		logf("serve: %v", err)
	}
}

// purgeOnStartup runs br-GI-17-06's retention purge against st's writer
// connection and logs the deleted count through logf. days <= 0 means "keep
// forever" (the default) and is a no-op — nothing is deleted on an
// unconfigured install. Split out from Serve for the same reason
// checkRedaction is: Serve cannot be driven from a test (two real listeners,
// a blocking signal context), so the run itself has to be testable on its
// own against a temp store.
func purgeOnStartup(ctx context.Context, st *store.Store, days int, logf func(string, ...any)) {
	if days <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	res, err := st.PurgeOlderThan(ctx, cutoff)
	if err != nil {
		logf("serve: retention purge: %v", err)
		return
	}
	logf("serve: retention purge: deleted %d row(s) older than %s", res.Deleted, cutoff.Format(time.RFC3339))
}

// printBanner prints the copy-pasteable ANTHROPIC_BASE_URL line, the
// dashboard URL, and (per --no-capture) the standing "nothing is being
// recorded" warning.
func printBanner(w io.Writer, cfg *config.Config) {
	fmt.Fprintln(w, "deepseek-lens is running.")
	fmt.Fprintf(w, "  export ANTHROPIC_BASE_URL=http://%s\n", cfg.ProxyAddr)
	fmt.Fprintf(w, "  dashboard:  http://%s\n", cfg.DashboardAddr)
	if !cfg.Capture {
		fmt.Fprintln(w, "  WARNING: capture is disabled — nothing is being recorded.")
	}
}

// translateNoCapture rewrites the bead prose's --no-capture into the flag
// config.go actually implements, --capture=false. config.go is out of
// scope for this bead (see CLAUDE.md / the bead's own constraints), so the
// bead's --no-capture semantics are handled here instead of by adding a
// second, duplicate flag to internal/config.
func translateNoCapture(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--no-capture" || a == "-no-capture" {
			out = append(out, "--capture=false")
			continue
		}
		out = append(out, a)
	}
	return out
}
