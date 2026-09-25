package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/pricing"
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

// archiveSuggestBytes is the store size past which doctor suggests archival
// when HotDays is still 0.
var archiveSuggestBytes int64 = 1 << 30

const (
	statusPass checkStatus = "PASS"
	statusInfo checkStatus = "INFO"
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
		{"replay_cost_threshold_usd", fmt.Sprintf("%.4f", cfg.ReplayCostThresholdUSD)},
		{"retention_days", fmt.Sprintf("%d", cfg.RetentionDays)},
		{"hot_days", fmt.Sprintf("%d", cfg.HotDays)},
		// The calendar changes how money is computed, so it belongs in the one
		// command whose job is "what is this install actually configured to
		// do" — the shipped default is a dated fact, and this is where a user
		// reads which dates it actually names.
		{"off_peak_dates", cfg.OffPeakDates},
		{"work_dates", cfg.WorkDates},
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

	archiveDir := filepath.Join(filepath.Dir(cfg.DBPath), "archive")
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		checks = append(checks, doctorCheck{"archive_writable", statusWarn, archiveDir + " is not writable"})
	} else {
		checks = append(checks, doctorCheck{"archive_writable", statusPass, archiveDir})
	}
	if cfg.BodyCapBytes > 262144 && cfg.HotDays == 0 {
		ceiling := int64(4096) * 2 * int64(cfg.BodyCapBytes)
		checks = append(checks, doctorCheck{"body_cap", statusWarn, fmt.Sprintf("body cap %d with HotDays 0; worst-case memory is 4096 × 2 × cap = %d bytes", cfg.BodyCapBytes, ceiling)})
	}

	if cfg.HotDays > 0 {
		checks = append(checks, doctorCheck{"hot_days_backup", statusWarn, "back up lens.db, lens.db-wal, and lens.db-shm before the first serve with HotDays > 0"})
	}
	if cfg.HotDays == 0 {
		if fi, err := os.Stat(cfg.DBPath); err == nil && fi.Size() > archiveSuggestBytes {
			checks = append(checks, doctorCheck{"archive_suggested", statusInfo, "store exceeds 1 GiB and HotDays is 0; enable archival"})
		}
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
		replayDetail = fmt.Sprintf(
			"on — POST /api/requests/{id}/replay guarded by an Origin/Host allowlist, no shared secret; `lens replay` needs --yes above %s",
			costCell(cfg.ReplayCostThresholdUSD, 1, 0))
	}
	checks = append(checks, doctorCheck{"replay_posture", statusPass, replayDetail})

	checks = append(checks, doctorCheck{"schema_version", statusPass, "n/a — no migrations table in v1"})

	checks = append(checks, providerHookCheck(cfg))

	// The shipped date set is a dated fact that cannot update itself, so the
	// mitigation is to make its expiry visible: the year it names is checked,
	// and a year it does not name WARNs rather than FAILs. This check must be
	// incapable of FAIL — runDoctor turns a FAIL into a non-zero exit, and a
	// coding session must not stop working because the user has not yet
	// updated a date list (CLAUDE.md's "fail open", applied to config).
	//
	// A malformed date does reach here: runChecks short-circuits on nothing,
	// so config_valid has already FAILed and this pass still runs. The error
	// is dropped rather than appended as a second FAIL — the malformed string
	// is reported once, by the check that owns it.
	if cal, err := pricing.NewCalendar(cfg.OffPeakDates, cfg.WorkDates); err == nil {
		year := time.Now().Year()
		if cal.Covers(year) {
			checks = append(checks, doctorCheck{"peak_calendar", statusPass,
				fmt.Sprintf("off-peak/work date sets cover %d", year)})
		} else {
			checks = append(checks, doctorCheck{"peak_calendar", statusWarn,
				fmt.Sprintf("no configured date set names %d — peak pricing may be overstating that year's holidays", year)})
		}
	}

	// Sink accepted/dropped and consumer last-write age live only in a
	// running `lens serve` process's memory, so doctor reads them from that
	// process's dashboard API (br-GI-1-09's GET /api/health). A standalone
	// doctor with no serve running must still succeed — reporting that the
	// observer is down is not a reason to fail the command — which is why
	// liveStatsCheck WARNs rather than FAILs on a connection failure.
	checks = append(checks, liveStatsCheck(cfg.DashboardAddr))

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

	sum, err := st.StatsSummary(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		checks = append(checks, doctorCheck{"row_count", statusFail, err.Error()})
	} else {
		checks = append(checks, doctorCheck{"row_count", statusPass, fmt.Sprintf("%d requests", sum.RequestCount)})
	}

	return checks
}

// liveStatsCheck reports the figures doctor cannot read from the database
// because they live in the running `lens serve` process's memory: the sink's
// accepted/dropped counts and the consumer's last-write age. It reads them
// from the dashboard API's /api/health (br-GI-1-09). When no server is
// listening — the common case for a standalone doctor run — it returns a
// WARN, never a FAIL: the command is there to diagnose a broken setup, not to
// require the observer to be up.
func liveStatsCheck(dashboardAddr string) doctorCheck {
	client := &http.Client{Timeout: 750 * time.Millisecond}
	resp, err := client.Get("http://" + dashboardAddr + "/api/health")
	if err != nil {
		return doctorCheck{"live_stats", statusWarn,
			"no running `lens serve` at " + dashboardAddr + " — sink accepted/dropped and consumer last-write age unavailable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return doctorCheck{"live_stats", statusWarn,
			fmt.Sprintf("%s answered %s, not the /api/health JSON", dashboardAddr, resp.Status)}
	}

	var h struct {
		SinkAccepted   uint64     `json:"sink_accepted"`
		SinkDropped    uint64     `json:"sink_dropped"`
		LastWriteAt    *time.Time `json:"last_write_at"`
		LastWriteAgeMs int64      `json:"last_write_age_ms"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return doctorCheck{"live_stats", statusWarn,
			"could not decode /api/health from " + dashboardAddr}
	}
	age := "no writes yet"
	if h.LastWriteAt != nil {
		age = humanDuration(time.Duration(h.LastWriteAgeMs)*time.Millisecond) + " ago"
	}
	return doctorCheck{"live_stats", statusPass,
		fmt.Sprintf("sink accepted=%d dropped=%d; consumer last write %s", h.SinkAccepted, h.SinkDropped, age)}
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

// claudeConfigDir resolves Claude Code's config directory by Claude Code's
// own precedence, not lens's — config.userHomeDir (internal/config/config.go)
// resolves $HOME first so lens's own files (config.toml, prices.toml, the
// database) redirect uniformly in tests, but that is the wrong answer here:
// Claude Code's documented Windows rule is that ~/.claude means
// %USERPROFILE%\.claude, not $HOME/.claude. Steps 2-4 are exactly what
// Node's os.homedir() resolves in the hooks, so this reads the same file
// they do.
func claudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	if up := os.Getenv("USERPROFILE"); up != "" {
		return filepath.Join(up, ".claude")
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".claude")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".claude")
	}
	return ""
}

// readSettingsBaseURL reads the effective ANTHROPIC_BASE_URL from Claude
// Code's settings.json. Any read or parse failure, or an absent value,
// reports not-found rather than propagating an error: settings.json is the
// plugin's file, and every way it can be missing or broken is a
// "nothing to check" state for provider_hooks, never a doctor failure.
func readSettingsBaseURL(path string) (url string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var s struct {
		Env struct {
			ANTHROPICBaseURL string `json:"ANTHROPIC_BASE_URL"`
		} `json:"env"`
	}
	if err := json.Unmarshal(data, &s); err != nil || s.Env.ANTHROPICBaseURL == "" {
		return "", false
	}
	return s.Env.ANTHROPICBaseURL, true
}

// readOverlayBaseURL reads the declared ANTHROPIC_BASE_URL from
// .deepseek-env.json, accepting both shapes br-GI-4-07's JS predicate
// handles: the sectioned one ({"env": {...}, "settings": {...}}) and the
// legacy flat one (a bare map of env keys). present is false for an absent,
// unreadable, or invalid overlay — the caller reads that as "plugin not
// managing this machine", not an error.
func readOverlayBaseURL(path string) (declared string, present bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", false
	}
	if envRaw, ok := raw["env"]; ok {
		var env struct {
			ANTHROPICBaseURL string `json:"ANTHROPIC_BASE_URL"`
		}
		_ = json.Unmarshal(envRaw, &env)
		return env.ANTHROPICBaseURL, true
	}
	if _, ok := raw["settings"]; ok {
		return "", true
	}
	var flat struct {
		ANTHROPICBaseURL string `json:"ANTHROPIC_BASE_URL"`
	}
	_ = json.Unmarshal(data, &flat)
	return flat.ANTHROPICBaseURL, true
}

// providerHookCheck reports whether the plugin hooks (br-GI-4-07) recognize
// this session's route — the third copy of their predicate, written once,
// in their clause order: substring on "deepseek" first, then equality with
// the overlay's declared URL. It can only PASS or WARN, never FAIL: a user
// with no plugin, no Claude Code, or a deliberately direct setup must still
// get a clean `doctor` (see liveStatsCheck for the same reasoning applied to
// a missing `serve`).
func providerHookCheck(cfg *config.Config) doctorCheck {
	dir := claudeConfigDir()
	if dir == "" {
		return doctorCheck{"provider_hooks", statusPass, "could not resolve a Claude Code config directory — nothing to check"}
	}

	settingsPath := filepath.Join(dir, "settings.json")
	effectiveURL, ok := readSettingsBaseURL(settingsPath)
	if !ok {
		return doctorCheck{"provider_hooks", statusPass, "no ANTHROPIC_BASE_URL in " + settingsPath + " — plugin not managing this session"}
	}

	if strings.Contains(effectiveURL, "deepseek") {
		return doctorCheck{"provider_hooks", statusPass, "peak guard recognizes this route (ANTHROPIC_BASE_URL contains \"deepseek\")"}
	}

	overlayPath := filepath.Join(dir, ".deepseek-env.json")
	declaredURL, overlayPresent := readOverlayBaseURL(overlayPath)
	if !overlayPresent {
		return doctorCheck{"provider_hooks", statusPass, "no " + overlayPath + " — plugin not managing this machine"}
	}

	if effectiveURL == declaredURL {
		return doctorCheck{"provider_hooks", statusPass, "peak guard recognizes this route (declared by " + overlayPath + ")"}
	}

	if lensURL := "http://" + cfg.ProxyAddr; effectiveURL == lensURL {
		return doctorCheck{"provider_hooks", statusWarn, fmt.Sprintf(
			"peak guard will NOT fire: %s points ANTHROPIC_BASE_URL at %s, but %s declares %s",
			settingsPath, effectiveURL, overlayPath, declaredURL)}
	}

	return doctorCheck{"provider_hooks", statusPass, "effective ANTHROPIC_BASE_URL does not match the lens proxy address — nothing to check"}
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
