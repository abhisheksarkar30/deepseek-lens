package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// Doctor is `lens doctor`'s os.Stdout-writing entrypoint. args are config
// flags (--proxy-addr, --db-path, --replay, ...) — like `lens serve`,
// doctor forwards them straight to config.Load, since its whole job is
// reporting the resolved configuration and the store's health under it.
func Doctor(args []string) error {
	return runDoctor(args, os.Stdout)
}

type checkStatus string

const (
	statusPass checkStatus = "PASS"
	statusWarn checkStatus = "WARN"
	statusFail checkStatus = "FAIL"
)

type doctorCheck struct {
	Name   string
	Status checkStatus
	Detail string
}

// runDoctor prints the effective configuration and a PASS/WARN/FAIL check
// list to w, and returns a non-nil error (for a non-zero process exit) if
// any check FAILs — mitigation for plan risk 4: when the user's coding
// session is broken because the proxy is wedged, this is the one command
// that says what is wrong.
func runDoctor(args []string, w io.Writer) error {
	cfg, err := config.Load(args)
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "configuration:")
	cfgRows := [][]string{
		{"proxy_addr", cfg.ProxyAddr},
		{"dashboard_addr", cfg.DashboardAddr},
		{"upstream_url", cfg.UpstreamURL},
		{"db_path", cfg.DBPath},
		{"body_policy", cfg.BodyPolicy},
		{"body_cap_bytes", fmt.Sprintf("%d", cfg.BodyCapBytes)},
		{"allow_remote", fmt.Sprintf("%t", cfg.AllowRemote)},
		{"capture", fmt.Sprintf("%t", cfg.Capture)},
		{"session_gap_minutes", fmt.Sprintf("%d", cfg.SessionGapMinutes)},
		{"replay_enabled", fmt.Sprintf("%t", cfg.ReplayEnabled)},
	}
	fmt.Fprint(w, table([]string{"FIELD", "VALUE"}, cfgRows, 0))

	checks := runChecks(cfg)

	fmt.Fprintln(w, "\nchecks:")
	checkRows := make([][]string, 0, len(checks))
	failed := false
	for _, c := range checks {
		checkRows = append(checkRows, []string{c.Name, string(c.Status), c.Detail})
		if c.Status == statusFail {
			failed = true
		}
	}
	fmt.Fprint(w, table([]string{"CHECK", "STATUS", "DETAIL"}, checkRows, 0))

	if failed {
		return fmt.Errorf("doctor: one or more checks failed")
	}
	return nil
}

// runChecks runs every doctor check against cfg. Checks that need the
// store open it themselves and stop early if that fails — nothing past
// that point can run.
func runChecks(cfg *config.Config) []doctorCheck {
	var checks []doctorCheck

	if verr := cfg.Validate(); verr != nil {
		checks = append(checks, doctorCheck{"config_valid", statusFail, verr.Error()})
	} else {
		checks = append(checks, doctorCheck{"config_valid", statusPass, "ok"})
	}

	if dirWritable(filepath.Dir(cfg.DBPath)) {
		checks = append(checks, doctorCheck{"db_dir_writable", statusPass, cfg.DBPath})
	} else {
		checks = append(checks, doctorCheck{"db_dir_writable", statusFail, filepath.Dir(cfg.DBPath) + " is not writable"})
	}

	if hostResolves(cfg.UpstreamURL) {
		checks = append(checks, doctorCheck{"upstream_resolves", statusPass, cfg.UpstreamURL})
	} else {
		checks = append(checks, doctorCheck{"upstream_resolves", statusWarn, "DNS lookup failed for " + cfg.UpstreamURL})
	}

	if cfg.AllowRemote {
		checks = append(checks, doctorCheck{"allow_remote", statusWarn, "proxy/dashboard may bind to non-loopback addresses"})
	} else {
		checks = append(checks, doctorCheck{"allow_remote", statusPass, "loopback-only"})
	}

	if cfg.BodyPolicy == "full" {
		checks = append(checks, doctorCheck{"body_policy", statusWarn, "full request/response bodies are captured and stored"})
	} else {
		checks = append(checks, doctorCheck{"body_policy", statusPass, cfg.BodyPolicy})
	}

	replayDetail := "off (default; enable with --replay, LENS_REPLAY_ENABLED, or replay_enabled in the config file)"
	if cfg.ReplayEnabled {
		replayDetail = "on — replay endpoint guarded by an Origin/Host allowlist, no shared secret"
	}
	checks = append(checks, doctorCheck{"replay_posture", statusPass, replayDetail})

	checks = append(checks, doctorCheck{"schema_version", statusPass, "n/a — no migrations table in v1"})

	// Sink accepted/dropped and consumer last-write age live only in a
	// running `lens serve` process's memory. br-GI-1-09 (not yet landed)
	// is what will give doctor an HTTP endpoint to read them from; a
	// standalone doctor run genuinely cannot see them yet.
	checks = append(checks, doctorCheck{"live_stats", statusWarn,
		"sink accepted/dropped and consumer last-write age require a running `lens serve` and the dashboard API (br-GI-1-09); unavailable from a standalone doctor run"})

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		checks = append(checks, doctorCheck{"store_open", statusFail, err.Error()})
		return checks
	}
	defer st.Close()
	checks = append(checks, doctorCheck{"store_open", statusPass, "ok"})

	// store.Open forces WAL mode unconditionally (see internal/store's
	// pragmaDSN) — this is a static fact about the code, not a live query:
	// Store exposes no method to run an arbitrary PRAGMA from outside the
	// package.
	checks = append(checks, doctorCheck{"wal_mode", statusPass, "enabled (forced by store.Open)"})

	sum, err := st.StatsSummary(context.Background(), time.Time{})
	if err != nil {
		checks = append(checks, doctorCheck{"row_count", statusFail, err.Error()})
	} else {
		checks = append(checks, doctorCheck{"row_count", statusPass, fmt.Sprintf("%d requests", sum.RequestCount)})
	}

	return checks
}

// dirWritable reports whether dir exists (creating it if needed) and a
// file can be created in it.
func dirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".lens-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// hostResolves reports whether rawURL's host resolves via DNS. It never
// contacts the upstream itself — doctor must work without it being up.
func hostResolves(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	_, err = net.LookupHost(u.Hostname())
	return err == nil
}
