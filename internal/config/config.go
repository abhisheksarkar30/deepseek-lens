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
)

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
	"LENS_PROXY_ADDR":          "ProxyAddr",
	"LENS_DASHBOARD_ADDR":      "DashboardAddr",
	"LENS_UPSTREAM_URL":        "UpstreamURL",
	"LENS_DB_PATH":             "DBPath",
	"LENS_BODY_POLICY":         "BodyPolicy",
	"LENS_BODY_CAP_BYTES":      "BodyCapBytes",
	"LENS_ALLOW_REMOTE":        "AllowRemote",
	"LENS_CAPTURE":             "Capture",
	"LENS_SESSION_GAP_MINUTES": "SessionGapMinutes",
	"LENS_REPLAY_ENABLED":      "ReplayEnabled",
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
			return nil, fmt.Errorf("line %d: missing '=': %q", i+1, line)
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if strings.HasPrefix(val, `"`) {
			if len(val) < 2 || !strings.HasSuffix(val, `"`) {
				return nil, fmt.Errorf("line %d: unterminated quote: %q", i+1, line)
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
		default:
			return fmt.Errorf("unknown key %q", key)
		}
		if err != nil {
			return fmt.Errorf("key %s: invalid value %q: %w", key, val, err)
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
			return nil, fmt.Errorf("config file %s: %w", path, perr)
		}
		if aerr := applyKV(cfg, kv); aerr != nil {
			return nil, fmt.Errorf("config file %s: %w", path, aerr)
		}
	case os.IsNotExist(err):
		// no config file — fine, defaults stand.
	default:
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	if err := applyKV(cfg, envKV()); err != nil {
		return nil, fmt.Errorf("environment: %w", err)
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
		return fmt.Errorf("BodyPolicy: invalid value %q (want full, truncated, or off)", c.BodyPolicy)
	}
	if c.BodyCapBytes <= 0 {
		return fmt.Errorf("BodyCapBytes: must be positive, got %d", c.BodyCapBytes)
	}
	u, err := url.Parse(c.UpstreamURL)
	if err != nil {
		return fmt.Errorf("UpstreamURL: invalid value %q: %v", c.UpstreamURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("UpstreamURL: invalid value %q (scheme must be http or https)", c.UpstreamURL)
	}
	if u.Host == "" {
		return fmt.Errorf("UpstreamURL: invalid value %q (missing host)", c.UpstreamURL)
	}
	if c.SessionGapMinutes <= 0 {
		return fmt.Errorf("SessionGapMinutes: must be positive, got %d", c.SessionGapMinutes)
	}
	return nil
}

func validateLoopback(field, addr string, allowRemote bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s: invalid address %q: %v", field, addr, err)
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
	return fmt.Errorf("%s: non-loopback address %q requires AllowRemote", field, addr)
}
