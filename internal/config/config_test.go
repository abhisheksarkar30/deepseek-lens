package config

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// clearLensEnv unsets (via empty value, which Load treats as unset) every
// LENS_* var so tests don't inherit the outer environment.
func clearLensEnv(t *testing.T) {
	t.Helper()
	for env := range fieldsByEnv {
		t.Setenv(env, "")
	}
}

func writeConfigFile(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".deepseek-lens")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func freshHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearLensEnv(t)
	return home
}

func TestLoadDefaults(t *testing.T) {
	freshHome(t)
	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Default()
	if *cfg != *want {
		t.Errorf("Load() = %+v, want defaults %+v", *cfg, *want)
	}
}

func TestFileOverridesDefault(t *testing.T) {
	home := freshHome(t)
	writeConfigFile(t, home, `ProxyAddr = "127.0.0.1:9999"`+"\n")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProxyAddr != "127.0.0.1:9999" {
		t.Errorf("ProxyAddr = %q, want file value", cfg.ProxyAddr)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	home := freshHome(t)
	writeConfigFile(t, home, `ProxyAddr = "127.0.0.1:9999"`+"\n")
	t.Setenv("LENS_PROXY_ADDR", "127.0.0.1:7777")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProxyAddr != "127.0.0.1:7777" {
		t.Errorf("ProxyAddr = %q, want env value", cfg.ProxyAddr)
	}
}

// TestApplyKVRejectsBadValues exercises applyKV's own conversion error
// branches (int, bool, float) and its unknown-key branch, through Load's
// env path — applyKV has no test of its own since it is unexported.
func TestApplyKVRejectsBadValues(t *testing.T) {
	for _, tc := range []struct {
		name, env, val string
	}{
		{"BodyCapBytes not an int", "LENS_BODY_CAP_BYTES", "not-a-number"},
		{"AllowRemote not a bool", "LENS_ALLOW_REMOTE", "not-a-bool"},
		{"Capture not a bool", "LENS_CAPTURE", "not-a-bool"},
		{"SessionGapMinutes not an int", "LENS_SESSION_GAP_MINUTES", "not-a-number"},
		{"ReplayEnabled not a bool", "LENS_REPLAY_ENABLED", "not-a-bool"},
		{"ReplayCostThresholdUSD not a float", "LENS_REPLAY_COST_THRESHOLD_USD", "not-a-number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			freshHome(t)
			t.Setenv(tc.env, tc.val)
			if _, err := Load(nil); err == nil {
				t.Errorf("Load: expected error for %s=%q, got nil", tc.env, tc.val)
			}
		})
	}
}

// TestApplyKVRejectsUnknownKey covers applyKV's default branch through the
// config-file path, since env vars are only ever the fixed fieldsByEnv set.
func TestApplyKVRejectsUnknownKey(t *testing.T) {
	home := freshHome(t)
	writeConfigFile(t, home, "NotARealField = value\n")
	if _, err := Load(nil); err == nil {
		t.Fatal("Load: expected error for an unknown config key, got nil")
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	freshHome(t)
	t.Setenv("LENS_PROXY_ADDR", "127.0.0.1:7777")

	cfg, err := Load([]string{"-proxy-addr", "127.0.0.1:5555"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProxyAddr != "127.0.0.1:5555" {
		t.Errorf("ProxyAddr = %q, want flag value", cfg.ProxyAddr)
	}
}

func TestLensProxyAddrEnvVar(t *testing.T) {
	freshHome(t)
	t.Setenv("LENS_PROXY_ADDR", "10.0.0.1:1234")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProxyAddr != "10.0.0.1:1234" {
		t.Errorf("ProxyAddr = %q, want env value", cfg.ProxyAddr)
	}
}

func TestFileParsesFlatFields(t *testing.T) {
	home := freshHome(t)
	writeConfigFile(t, home, `
# a comment, and a blank line above
ProxyAddr = "127.0.0.1:1111"
DashboardAddr = "127.0.0.1:2222"
UpstreamURL = "https://example.com/api"
DBPath = "/tmp/lens.db"
BodyPolicy = "truncated"
BodyCapBytes = 1024
AllowRemote = true
Capture = false
SessionGapMinutes = 15
ReplayEnabled = true
ReplayCostThresholdUSD = 1.5
ModelMap = "opus:deepseek-v4-pro,*:deepseek-flash"
ModelMaxTokens = "deepseek-v4-pro:1"
`)

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := &Config{
		ProxyAddr:         "127.0.0.1:1111",
		DashboardAddr:     "127.0.0.1:2222",
		UpstreamURL:       "https://example.com/api",
		DBPath:            "/tmp/lens.db",
		BodyPolicy:        "truncated",
		BodyCapBytes:      1024,
		AllowRemote:       true,
		Capture:           false,
		SessionGapMinutes: 15,
		ReplayEnabled:     true,
		// Parsed by strconv.ParseFloat — the one float key in the file.
		ReplayCostThresholdUSD: 1.5,
		ModelMap:               "opus:deepseek-v4-pro,*:deepseek-flash",
		ModelMaxTokens:         "deepseek-v4-pro:1",
	}
	if *cfg != *want {
		t.Errorf("Load() = %+v, want %+v", *cfg, *want)
	}
}

func TestMalformedFileMissingEquals(t *testing.T) {
	home := freshHome(t)
	writeConfigFile(t, home, "this line has no equals sign\n")

	if _, err := Load(nil); err == nil {
		t.Fatal("Load: expected error for malformed config file, got nil")
	}
}

func TestMalformedFileUnterminatedQuote(t *testing.T) {
	home := freshHome(t)
	writeConfigFile(t, home, `ProxyAddr = "127.0.0.1:9999`+"\n")

	if _, err := Load(nil); err == nil {
		t.Fatal("Load: expected error for unterminated quote, got nil")
	}
}

func TestValidateRejectsNonLoopbackWithoutAllowRemote(t *testing.T) {
	cfg := Default()
	cfg.ProxyAddr = "0.0.0.0:8787"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate: expected error for non-loopback address, got nil")
	}
}

func TestValidateAcceptsNonLoopbackWithAllowRemote(t *testing.T) {
	cfg := Default()
	cfg.ProxyAddr = "0.0.0.0:8787"
	cfg.AllowRemote = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
}

func TestValidateRejectsBadBodyPolicy(t *testing.T) {
	cfg := Default()
	cfg.BodyPolicy = "sometimes"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate: expected error for invalid BodyPolicy, got nil")
	}
}

func TestValidateRejectsBodyCapBytes(t *testing.T) {
	for _, v := range []int{0, -1} {
		cfg := Default()
		cfg.BodyCapBytes = v
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate: expected error for BodyCapBytes=%d, got nil", v)
		}
	}
}

func TestValidateRejectsBadUpstreamURL(t *testing.T) {
	for _, u := range []string{"ftp://x", "https://"} {
		cfg := Default()
		cfg.UpstreamURL = u
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate: expected error for UpstreamURL=%q, got nil", u)
		}
	}
}

func TestValidateRejectsSessionGapMinutes(t *testing.T) {
	cfg := Default()
	cfg.SessionGapMinutes = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate: expected error for SessionGapMinutes=0, got nil")
	}
}

// TestValidateReplayCostThresholdUSD covers the NaN-rejection comment on
// Validate: `!(x >= 0)` is the one predicate meant to reject both a negative
// threshold and a NaN one (every comparison against NaN is false, so a naive
// `x < 0` check would let NaN slip through as "not negative").
func TestValidateReplayCostThresholdUSD(t *testing.T) {
	for _, v := range []float64{-0.01, -1, math.NaN()} {
		cfg := Default()
		cfg.ReplayCostThresholdUSD = v
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate: expected error for ReplayCostThresholdUSD=%v, got nil", v)
		}
	}
	for _, v := range []float64{0, 0.01, 100} {
		cfg := Default()
		cfg.ReplayCostThresholdUSD = v
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate: expected no error for ReplayCostThresholdUSD=%v, got %v", v, err)
		}
	}
}

func TestRetentionDaysPrecedence(t *testing.T) {
	t.Run("unset defaults to zero", func(t *testing.T) {
		freshHome(t)
		cfg, err := Load(nil)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.RetentionDays != 0 {
			t.Errorf("RetentionDays = %d, want 0", cfg.RetentionDays)
		}
	})

	t.Run("file only", func(t *testing.T) {
		home := freshHome(t)
		writeConfigFile(t, home, "RetentionDays = 30\n")
		cfg, err := Load(nil)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.RetentionDays != 30 {
			t.Errorf("RetentionDays = %d, want file value 30", cfg.RetentionDays)
		}
	})

	t.Run("env overrides file", func(t *testing.T) {
		home := freshHome(t)
		writeConfigFile(t, home, "RetentionDays = 30\n")
		t.Setenv("LENS_RETENTION_DAYS", "14")
		cfg, err := Load(nil)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.RetentionDays != 14 {
			t.Errorf("RetentionDays = %d, want env value 14", cfg.RetentionDays)
		}
	})

	t.Run("flag overrides env", func(t *testing.T) {
		freshHome(t)
		t.Setenv("LENS_RETENTION_DAYS", "14")
		cfg, err := Load([]string{"-retention-days", "7"})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.RetentionDays != 7 {
			t.Errorf("RetentionDays = %d, want flag value 7", cfg.RetentionDays)
		}
	})
}

func TestValidateRejectsNegativeRetentionDays(t *testing.T) {
	t.Run("direct", func(t *testing.T) {
		cfg := Default()
		cfg.RetentionDays = -1
		if err := cfg.Validate(); err == nil {
			t.Fatal("Validate: expected error for RetentionDays=-1, got nil")
		}
	})

	t.Run("via file", func(t *testing.T) {
		home := freshHome(t)
		writeConfigFile(t, home, "RetentionDays = -1\n")
		cfg, err := Load(nil)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("Validate: expected error for RetentionDays=-1 from file, got nil")
		}
	})

	t.Run("via env", func(t *testing.T) {
		freshHome(t)
		t.Setenv("LENS_RETENTION_DAYS", "-1")
		cfg, err := Load(nil)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("Validate: expected error for RetentionDays=-1 from env, got nil")
		}
	})

	t.Run("via flag", func(t *testing.T) {
		freshHome(t)
		cfg, err := Load([]string{"-retention-days", "-1"})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("Validate: expected error for RetentionDays=-1 from flag, got nil")
		}
	})

	for _, v := range []int{0, 1, 365} {
		cfg := Default()
		cfg.RetentionDays = v
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate: expected no error for RetentionDays=%d, got %v", v, err)
		}
	}
}
