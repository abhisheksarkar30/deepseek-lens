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

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/consumer"
	"github.com/abhisheksarkar30/deepseek-lens/internal/proxy"
	"github.com/abhisheksarkar30/deepseek-lens/internal/sink"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// shutdownGrace bounds how long Serve waits for both http.Servers to
// finish in-flight requests during shutdown, on top of the consumer's own
// shutdownBound drain (internal/consumer).
const shutdownGrace = 5 * time.Second

// Serve runs `lens serve`: the proxy listener (br-GI-1-03), a placeholder
// dashboard listener, and the consumer (br-GI-1-07), in one process. The
// dashboard here is intentionally a stub — the real internal/api +
// internal/web land in br-GI-1-09, which depends on this bead and will
// replace this handler; this bead only proves the second listener binds
// and serves. It blocks until SIGINT, then shuts down both servers with a
// bounded drain.
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
	// This bead injects a nil SessionResolver, matching br-GI-1-07's own
	// convention (session resolution lands in br-GI-1-12) — Request.SessionID
	// simply stays unset until then.
	cons := consumer.New(sk, st, nil)

	proxySrv, err := proxy.NewServer(cfg, sk)
	if err != nil {
		return fmt.Errorf("serve: build proxy: %w", err)
	}
	dashSrv := &http.Server{Addr: cfg.DashboardAddr, Handler: placeholderDashboard(cfg)}

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

// placeholderDashboard is the stand-in dashboard handler for this bead.
// br-GI-1-09 replaces it with the real internal/api + internal/web; the
// `/api/requests/{id}/replay` endpoint (br-GI-1-13, guarded by an
// Origin/Host allowlist, gated on cfg.ReplayEnabled) is not implemented
// here either — both are explicitly out of this bead's scope.
func placeholderDashboard(cfg *config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "deepseek-lens dashboard: not yet implemented (br-GI-1-09)")
		if cfg.ReplayEnabled {
			fmt.Fprintln(w, "replay is enabled in config, but the endpoint is not implemented yet (br-GI-1-13)")
		}
	})
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
