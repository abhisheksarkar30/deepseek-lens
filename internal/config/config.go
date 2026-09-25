// Package config loads deepseek-lens configuration from flags, environment
// variables, and a config file, in that order of precedence, falling back to
// built-in defaults.
package config

import (
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	// config -> pricing is the one import edge this package adds that is not
	// stdlib. It exists so Validate can reject a malformed calendar using the
	// same grammar NewCalendar parses, rather than a second date parser here
	// that could accept something pricing would then read differently. There
	// is no cycle: pricing imports only internal/parse (br-GI-24-03).
	"github.com/abhisheksarkar30/deepseek-lens/internal/pricing"
)

// DefaultModelMap is the built-in client-model → DeepSeek-model table
// (br-GI-1-10). It lives here, not in internal/analyze, because DeepSeek
// changing its mapping is a recoverable event only if the user can edit the
// table without a code change — see the plan's risk 6.
//
// Format: comma-separated "prefix:target" pairs, matched case-insensitively
// on prefix, first match wins. A "*" prefix is the catch-all that serves any
// client model the table does not otherwise recognize (its target is what
// the unrecognized-model warning names).
const DefaultModelMap = "opus:deepseek-v4-pro,sonnet:deepseek-flash,haiku:deepseek-flash,*:deepseek-flash"

// DefaultModelMaxTokens is the built-in per-DeepSeek-model max_tokens
// ceiling (br-GI-1-10's "max_tokens exceeds the model's ceiling, if known"
// rule), same "model:ceiling" comma-separated format. A model absent from
// this table simply skips the rule.
//
// ponytail: best-effort placeholder ceilings pending real DeepSeek
// documentation; the point of the rule is that a nonsensical max_tokens is
// visibly capped, so the exact figures matter less than their being editable.
const DefaultModelMaxTokens = "deepseek-v4-pro:64000,deepseek-flash:32000"

// DefaultOffPeakDates is the 2026 statutory-holiday set — DeepSeek bills these
// days off-peak for the whole day, where lens would otherwise charge peak for
// any of them that falls on a weekday (19 of the 33 in 2026).
//
// Source: 国办发明电〔2025〕7号 (2025-11-04), the State Council's 2026 notice,
// re-checked date by date rather than taken from its prose. The seven ranges
// are the notice's own (元旦, 春节, 清明, 劳动节, 端午, 中秋, 国庆) and are
// kept unexpanded so the default reads like the document it cites.
//
// The calendar cannot be computed — it is administratively declared each year
// — so it is data, and it goes stale on 2027-01-01. `lens doctor` warns when
// it no longer covers the current year.
const DefaultOffPeakDates = "2026-01-01..2026-01-03,2026-02-15..2026-02-23,2026-04-04..2026-04-06,2026-05-01..2026-05-05,2026-06-19..2026-06-21,2026-09-25..2026-09-27,2026-10-01..2026-10-07"

// DefaultWorkDates is the 2026 调休 make-up work-day set — the days the notice
// designates as working days to compensate for a holiday bridge.
//
// Every one of the six falls on a weekend, so this set is the only way to
// express them: without it each is priced off-peak by the weekend rule
// regardless of the holiday calendar. Under the same D3 rules as the off-peak
// set they are window-scoped rather than whole-day, so they change nothing
// outside 01:00-04:00 and 06:00-10:00 UTC.
//
// DeepSeek's own treatment of 调休 is unverified — see the README.
const DefaultWorkDates = "2026-01-04,2026-02-14,2026-02-28,2026-05-09,2026-09-20,2026-10-10"

// Config is the effective, fully-resolved configuration for lens.
type Config struct {
	ProxyAddr         string
	DashboardAddr     string
	UpstreamURL       string
	DBPath            string
	BodyPolicy        string // "full" | "truncated" | "off"
	BodyCapBytes      int
	AllowRemote       bool
	Capture           bool
	SessionGapMinutes int
	ReplayEnabled     bool
	// ReplayCostThresholdUSD is the spend `lens replay` will send without
	// confirmation: a replay whose original call cost more than this (or whose
	// original has no price at all, so the cost is unknown) needs --yes. Zero
	// is legitimate — it means every replay needs --yes.
	ReplayCostThresholdUSD float64
	ModelMap               string // client-model → DeepSeek-model table; see DefaultModelMap
	ModelMaxTokens         string // DeepSeek-model → max_tokens ceiling; see DefaultModelMaxTokens
	// OffPeakDates and WorkDates are the peak calendar's two date sets, in the
	// grammar pricing.NewCalendar parses: comma-separated YYYY-MM-DD or
	// inclusive YYYY-MM-DD..YYYY-MM-DD items. A malformed one is fatal rather
	// than skipped, unlike ModelMap — see DefaultOffPeakDates.
	OffPeakDates string
	WorkDates    string
	// RetentionDays is how long a request row is kept before serve purges
	// it; 0 (the default) means keep forever, matching this repo's fail-open
	// posture — nothing is deleted on an unconfigured install. A negative
	// value is rejected by Validate.
	RetentionDays int
	// HotDays is how many UTC days of bodies stay in the hot database.
	// 0 disables archival. A negative value is rejected by Validate.
	HotDays int
}

// Default returns the built-in defaults.
func Default() *Config {
	return &Config{
		ProxyAddr:         "127.0.0.1:8787",
		DashboardAddr:     "127.0.0.1:8788",
		UpstreamURL:       "https://api.deepseek.com/anthropic",
		DBPath:            defaultDBPath(),
		BodyPolicy:        "full",
		BodyCapBytes:      262144,
		AllowRemote:       false,
		Capture:           true,
		SessionGapMinutes: 30,
		ReplayEnabled:     false,
		// ponytail: 25 cents — a round number well above an ordinary single
		// call and well below a mistake. The point of the gate is that a replay
		// is never a surprise, not that the figure is right for every user.
		ReplayCostThresholdUSD: 0.25,
		ModelMap:               DefaultModelMap,
		ModelMaxTokens:         DefaultModelMaxTokens,
		OffPeakDates:           DefaultOffPeakDates,
		WorkDates:              DefaultWorkDates,
		RetentionDays:          0,
		HotDays:                0,
	}
}

// userHomeDir resolves the home directory, preferring $HOME so tests (and
// users) can override it uniformly across platforms.
func userHomeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func defaultDBPath() string {
	home := userHomeDir()
	if home == "" {
		return filepath.Join(".deepseek-lens", "lens.db")
	}
	return filepath.Join(home, ".deepseek-lens", "lens.db")
}

func configFilePath() string {
	home := userHomeDir()
	if home == "" {
		return filepath.Join(".deepseek-lens", "config.toml")
	}
	return filepath.Join(home, ".deepseek-lens", "config.toml")
}

// fieldsByEnv maps LENS_* environment variable names to Config field names.
var fieldsByEnv = map[string]string{
	"LENS_PROXY_ADDR":                "ProxyAddr",
	"LENS_DASHBOARD_ADDR":            "DashboardAddr",
	"LENS_UPSTREAM_URL":              "UpstreamURL",
	"LENS_DB_PATH":                   "DBPath",
	"LENS_BODY_POLICY":               "BodyPolicy",
	"LENS_BODY_CAP_BYTES":            "BodyCapBytes",
	"LENS_ALLOW_REMOTE":              "AllowRemote",
	"LENS_CAPTURE":                   "Capture",
	"LENS_SESSION_GAP_MINUTES":       "SessionGapMinutes",
	"LENS_REPLAY_ENABLED":            "ReplayEnabled",
	"LENS_REPLAY_COST_THRESHOLD_USD": "ReplayCostThresholdUSD",
	"LENS_MODEL_MAP":                 "ModelMap",
	"LENS_MODEL_MAX_TOKENS":          "ModelMaxTokens",
	"LENS_OFF_PEAK_DATES":            "OffPeakDates",
	"LENS_WORK_DATES":                "WorkDates",
	"LENS_RETENTION_DAYS":            "RetentionDays",
	"LENS_HOT_DAYS":                  "HotDays",
}

func envKV() map[string]string {
	kv := map[string]string{}
	for env, field := range fieldsByEnv {
		if v := os.Getenv(env); v != "" {
			kv[field] = v
		}
	}
	return kv
}

// parseFlatFile parses a minimal flat `key = value` file: blank lines and
// lines starting with '#' are ignored, every other line must contain '=',
// and a value may optionally be wrapped in double quotes.
//
// ponytail: hand-rolled flat reader; swap in BurntSushi/toml if nested
// config ever appears.
func parseFlatFile(data []byte) (map[string]string, error) {
	kv := map[string]string{}
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			return nil, fmt.Errorf("config: parse flat file: line %d: missing '=': %q", i+1, line)
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if strings.HasPrefix(val, `"`) {
			if len(val) < 2 || !strings.HasSuffix(val, `"`) {
				return nil, fmt.Errorf("config: parse flat file: line %d: unterminated quote: %q", i+1, line)
			}
			val = val[1 : len(val)-1]
		}
		kv[key] = val
	}
	return kv, nil
}

// applyKV sets cfg fields named by kv's keys (Config field names) to kv's
// values, converting to each field's type.
func applyKV(cfg *Config, kv map[string]string) error {
	for key, val := range kv {
		if val == "" {
			continue
		}
		var err error
		switch key {
		case "ProxyAddr":
			cfg.ProxyAddr = val
		case "DashboardAddr":
			cfg.DashboardAddr = val
		case "UpstreamURL":
			cfg.UpstreamURL = val
		case "DBPath":
			cfg.DBPath = val
		case "BodyPolicy":
			cfg.BodyPolicy = val
		case "BodyCapBytes":
			cfg.BodyCapBytes, err = strconv.Atoi(val)
		case "AllowRemote":
			cfg.AllowRemote, err = strconv.ParseBool(val)
		case "Capture":
			cfg.Capture, err = strconv.ParseBool(val)
		case "SessionGapMinutes":
			cfg.SessionGapMinutes, err = strconv.Atoi(val)
		case "ReplayEnabled":
			cfg.ReplayEnabled, err = strconv.ParseBool(val)
		case "ReplayCostThresholdUSD":
			cfg.ReplayCostThresholdUSD, err = strconv.ParseFloat(val, 64)
		case "ModelMap":
			cfg.ModelMap = val
		case "ModelMaxTokens":
			cfg.ModelMaxTokens = val
		case "OffPeakDates":
			cfg.OffPeakDates = val
		case "WorkDates":
			cfg.WorkDates = val
		case "RetentionDays":
			cfg.RetentionDays, err = strconv.Atoi(val)
		case "HotDays":
			cfg.HotDays, err = strconv.Atoi(val)
		default:
			return fmt.Errorf("config: apply: unknown key %q", key)
		}
		if err != nil {
			return fmt.Errorf("config: apply: key %s: invalid value %q: %w", key, val, err)
		}
	}
	return nil
}

// applyFlags overlays cfg with any flags explicitly passed in args. Each
// flag's default is cfg's current (file/env-resolved) value, so a flag the
// caller did not pass never overrides what came before it.
func applyFlags(cfg *Config, args []string) error {
	fs := flag.NewFlagSet("lens", flag.ContinueOnError)
	fs.StringVar(&cfg.ProxyAddr, "proxy-addr", cfg.ProxyAddr, "proxy listen address")
	fs.StringVar(&cfg.DashboardAddr, "dashboard-addr", cfg.DashboardAddr, "dashboard listen address")
	fs.StringVar(&cfg.UpstreamURL, "upstream-url", cfg.UpstreamURL, "upstream DeepSeek API URL")
	fs.StringVar(&cfg.DBPath, "db-path", cfg.DBPath, "SQLite database path")
	fs.StringVar(&cfg.BodyPolicy, "body-policy", cfg.BodyPolicy, "body capture policy: full|truncated|off")
	fs.IntVar(&cfg.BodyCapBytes, "body-cap-bytes", cfg.BodyCapBytes, "max bytes captured per body")
	fs.BoolVar(&cfg.AllowRemote, "allow-remote", cfg.AllowRemote, "allow non-loopback bind addresses")
	fs.BoolVar(&cfg.Capture, "capture", cfg.Capture, "enable capture")
	fs.IntVar(&cfg.SessionGapMinutes, "session-gap-minutes", cfg.SessionGapMinutes, "minutes of inactivity before a new session")
	fs.BoolVar(&cfg.ReplayEnabled, "replay", cfg.ReplayEnabled, "enable the replay endpoint")
	fs.Float64Var(&cfg.ReplayCostThresholdUSD, "replay-cost-threshold-usd", cfg.ReplayCostThresholdUSD,
		"replay needs --yes above this original-call cost in US dollars")
	fs.StringVar(&cfg.ModelMap, "model-map", cfg.ModelMap, "client-model to DeepSeek-model map, e.g. \"opus:deepseek-v4-pro,*:deepseek-flash\"")
	fs.StringVar(&cfg.ModelMaxTokens, "model-max-tokens", cfg.ModelMaxTokens, "per-DeepSeek-model max_tokens ceiling, e.g. \"deepseek-v4-pro:64000\"")
	fs.StringVar(&cfg.OffPeakDates, "off-peak-dates", cfg.OffPeakDates, "peak calendar: off-peak (holiday) dates, e.g. \"2026-10-01..2026-10-07\"")
	fs.StringVar(&cfg.WorkDates, "work-dates", cfg.WorkDates, "peak calendar: 调休 make-up work dates, e.g. \"2026-10-10\"")
	fs.IntVar(&cfg.RetentionDays, "retention-days", cfg.RetentionDays, "purge requests older than this many days; 0 means keep forever")
	fs.IntVar(&cfg.HotDays, "hot-days", cfg.HotDays, "keep this many days of bodies in the hot database; 0 disables archival")
	return fs.Parse(args)
}

// Load resolves configuration with precedence flags > env (LENS_*) > config
// file (~/.deepseek-lens/config.toml) > defaults.
func Load(args []string) (*Config, error) {
	cfg := Default()

	path := configFilePath()
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		kv, perr := parseFlatFile(data)
		if perr != nil {
			return nil, fmt.Errorf("config: load: config file %s: %w", path, perr)
		}
		if aerr := applyKV(cfg, kv); aerr != nil {
			return nil, fmt.Errorf("config: load: config file %s: %w", path, aerr)
		}
	case os.IsNotExist(err):
		// no config file — fine, defaults stand.
	default:
		return nil, fmt.Errorf("config: load: reading config file %s: %w", path, err)
	}

	if err := applyKV(cfg, envKV()); err != nil {
		return nil, fmt.Errorf("config: load: environment: %w", err)
	}

	if err := applyFlags(cfg, args); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate rejects configuration values that would be unsafe or nonsensical.
// Errors name the offending field and value.
func (c *Config) Validate() error {
	if err := validateLoopback("ProxyAddr", c.ProxyAddr, c.AllowRemote); err != nil {
		return err
	}
	if err := validateLoopback("DashboardAddr", c.DashboardAddr, c.AllowRemote); err != nil {
		return err
	}
	switch c.BodyPolicy {
	case "full", "truncated", "off":
	default:
		return fmt.Errorf("config: validate: BodyPolicy: invalid value %q (want full, truncated, or off)", c.BodyPolicy)
	}
	if c.BodyCapBytes <= 0 {
		return fmt.Errorf("config: validate: BodyCapBytes: must be positive, got %d", c.BodyCapBytes)
	}
	u, err := url.Parse(c.UpstreamURL)
	if err != nil {
		return fmt.Errorf("config: validate: UpstreamURL: invalid value %q: %w", c.UpstreamURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("config: validate: UpstreamURL: invalid value %q (scheme must be http or https)", c.UpstreamURL)
	}
	if u.Host == "" {
		return fmt.Errorf("config: validate: UpstreamURL: invalid value %q (missing host)", c.UpstreamURL)
	}
	if c.SessionGapMinutes <= 0 {
		return fmt.Errorf("config: validate: SessionGapMinutes: must be positive, got %d", c.SessionGapMinutes)
	}
	// A negative threshold is nonsense and a NaN one is worse: every
	// comparison against NaN is false, so the cost gate would silently never
	// fire rather than fire always. `!(x >= 0)` is the one predicate that
	// rejects both, since NaN fails it too. Zero is allowed — it means "every
	// replay needs --yes", a legitimate strict setting.
	if !(c.ReplayCostThresholdUSD >= 0) {
		return fmt.Errorf("config: validate: ReplayCostThresholdUSD: must be a non-negative number, got %v", c.ReplayCostThresholdUSD)
	}
	if c.RetentionDays < 0 {
		return fmt.Errorf("config: validate: RetentionDays: must not be negative, got %d", c.RetentionDays)
	}
	if c.HotDays < 0 {
		return fmt.Errorf("config: validate: HotDays: must not be negative, got %d", c.HotDays)
	}
	if c.HotDays > 0 && c.RetentionDays > 0 && c.HotDays > c.RetentionDays {
		return fmt.Errorf("config: validate: HotDays %d is greater than RetentionDays %d", c.HotDays, c.RetentionDays)
	}
	// Both sets in one call, which is what makes the "a date is in both" case
	// reachable — validating each string alone would parse both happily and
	// never see the contradiction. The error already names the offending item
	// or date; wrapping it with the field names adds which key it came from.
	if _, err := pricing.NewCalendar(c.OffPeakDates, c.WorkDates); err != nil {
		return fmt.Errorf("config: validate: OffPeakDates/WorkDates: %w", err)
	}
	return nil
}

func validateLoopback(field, addr string, allowRemote bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("config: validate: %s: invalid address %q: %w", field, addr, err)
	}
	if allowRemote {
		return nil
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("config: validate: %s: non-loopback address %q requires AllowRemote", field, addr)
}
