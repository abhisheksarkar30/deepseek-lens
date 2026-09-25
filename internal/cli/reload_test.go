package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestReloadStripsUnknownFlag(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "lens.db")
	err := Reload([]string{"--not-a-real-flag", "--db-path", db, "--proxy-addr", "127.0.0.1:1", "--dashboard-addr", "127.0.0.1:1"})
	if err == nil {
		t.Fatal("expected an error with no serve running")
	}
	if strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("unknown flag reached config.Load: %v", err)
	}
	if !strings.Contains(err.Error(), "no state file") {
		t.Fatalf("err = %v, want no state file", err)
	}
}

func TestStripKeepsHotDays(t *testing.T) {
	got := strings.Join(stripOwnFlags([]string{"--hot-days", "7", "--not-real"}), " ")
	if got != "--hot-days 7" {
		t.Fatalf("stripped = %q", got)
	}
}
