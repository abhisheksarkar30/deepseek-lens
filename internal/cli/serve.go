package cli

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/analyze"
	"github.com/abhisheksarkar30/deepseek-lens/internal/api"
	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/consumer"
	"github.com/abhisheksarkar30/deepseek-lens/internal/proxy"
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

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("serve: open store: %w", err)
	}
	defer st.Close()

	sk := sink.New(sink.DefaultCapacity)
	broker := api.NewBroker()
	// PublishingStore wraps st so the consumer's writes also publish SSE
	// events (br-GI-1-09's broker) — see internal/api/publishing_store.go's
	// doc comment for why this lives here as a decorator instead of a
	// consumer.go change: consumer.go is outside this bead's Files-to-Touch
	// list, and consumer.New's Store parameter is already a narrow
	// interface anything satisfies structurally.
	pubStore := api.NewPublishingStore(st, broker)
	// This bead injects a nil SessionResolver, matching br-GI-1-07's own
	// convention (session resolution lands in br-GI-1-12) — Request.SessionID
	// simply stays unset until then.
	//
	// The dropped-parameter rule engine (br-GI-1-10) is registered here, not
	// baked into consumer.New, for the same reason the publishing store is a
	// decorator: consumer.New already takes its analyzers as arguments, and
	// br-GI-1-07's tests construct Consumers that assert exact warning counts
	// on bodies those rules legitimately fire on. Defaulting them in would
	// have rewritten bead-07's tests; passing the config-resolved engine in
	// keeps the flags->rules->rows path explicit, and Analyze stays a pure
	// function of the config tables it is handed.
	cons := consumer.New(sk, pubStore, nil, analyze.NewRules(cfg.ModelMap, cfg.ModelMaxTokens))

	proxySrv, err := proxy.NewServer(cfg, sk)
	if err != nil {
		return fmt.Errorf("serve: build proxy: %w", err)
	}
	// The dashboard's read endpoints use the bare *store.Store (not
	// pubStore) so a read can never itself trigger a broker publish.
	dashSrv := &http.Server{Addr: cfg.DashboardAddr, Handler: api.New(st, sk, cons, broker, web.Files)}

	printBanner(os.Stdout, cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		_ = cons.Run(ctx) // Run never returns a non-nil error; see its doc.
	}()

	errCh := make(chan error, 2)
	go func() {
		if err := proxySrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("proxy server: %w", err)
		}
	}()
	go func() {
		if err := dashSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("dashboard server: %w", err)
		}
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

	// ctx is already cancelled by the signal (or by the error path above),
	// which is what makes cons.Run perform its own bounded drain-and-flush
	// and return — see internal/consumer's shutdownBound.
	<-consumerDone

	return nil
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
