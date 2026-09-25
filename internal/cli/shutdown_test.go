package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestShutdownStripsTimeoutAndRemovesStaleFile(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "lens.db")
	cmd := exec.Command("cmd", "/c", "exit", "0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	st := serveState{
		PID:           cmd.Process.Pid,
		StartedAt:     time.Now().Add(-time.Hour),
		ProxyAddr:     "127.0.0.1:1",
		DashboardAddr: "127.0.0.1:1",
	}
	path := serveStatePath(db)
	if err := writeServeState(path, st); err != nil {
		t.Fatal(err)
	}
	err := Shutdown([]string{"--timeout", "1s", "--db-path", db, "--proxy-addr", "127.0.0.1:1", "--dashboard-addr", "127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("stale state file still present: %v", statErr)
	}
}

func TestShutdownNoFileExitsZeroWhenHealthRefuses(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "lens.db")
	err := Shutdown([]string{"--db-path", db, "--proxy-addr", "127.0.0.1:1", "--dashboard-addr", "127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
}
