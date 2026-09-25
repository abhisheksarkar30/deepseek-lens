package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRestartNoStateFileRefuses(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "lens.db")
	err := Restart([]string{"--timeout", "1s", "--db-path", db, "--proxy-addr", "127.0.0.1:1", "--dashboard-addr", "127.0.0.1:1"})
	if err == nil || !strings.Contains(err.Error(), "no state file") {
		t.Fatalf("err = %v, want no state file", err)
	}
}
