package config

import (
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
