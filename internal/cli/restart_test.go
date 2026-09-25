package cli

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The restart tests that need a real detached child reuse this test binary as
// the child: it is re-invoked with -test.run=^TestRestartChildHelper$ and the
// env below selects the role. No new stub binary has to be built.
const (
	restartHelperMode = "LENS_RESTART_HELPER"      // "health" | "sleep" | "detach"
	restartHelperAddr = "LENS_RESTART_HELPER_ADDR" // addr the health helper binds
	restartHelperPID  = "LENS_RESTART_HELPER_PID"  // file the child writes its pid to
	restartHelperRun  = "^TestRestartChildHelper$"
)

// TestRestartChildHelper is the spawned child, not a real test: without
// LENS_RESTART_HELPER set it skips. "health" serves 200 on /api/health,
// "sleep" blocks until killed, and "detach" spawns a detached health
// grandchild then exits — the last is what hosts the survives-parent proof.
func TestRestartChildHelper(t *testing.T) {
	mode := os.Getenv(restartHelperMode)
	if mode == "" {
		t.Skip("spawned-child helper; not a real test")
	}
	if mode != "detach" {
		if f := os.Getenv(restartHelperPID); f != "" {
			_ = os.WriteFile(f, []byte(strconv.Itoa(os.Getpid())), 0o600)
		}
	}
	switch mode {
	case "sleep":
		// Hold the process until the parent test kills it. time.Sleep keeps a
		// timer pending, so the runtime's deadlock detector does not fire the
		// way it would on a bare select{}.
		for {
			time.Sleep(time.Hour)
		}
	case "detach":
		// Spawn a detached health grandchild, then exit. The grandchild
		// inherits the mutated env, so it comes up in "health" mode.
		_ = os.Setenv(restartHelperMode, "health")
		child, err := detachCommand(os.Args[0], []string{"-test.run=" + restartHelperRun}, "", "")
		if err != nil {
			os.Exit(4)
		}
		if err := child.Start(); err != nil {
			os.Exit(5)
		}
		_ = child.Process.Release()
		os.Exit(0)
	default: // "health"
		ln, err := net.Listen("tcp", os.Getenv(restartHelperAddr))
		if err != nil {
			os.Exit(6)
		}
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/health" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
	}
}

// freeAddr returns a loopback address a test can hand to a child to bind: it
// listens on :0, reads the port, then closes so the address is free again.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// waitHealth polls /api/health through the loopback-normalizing dial until it
// answers 200 or d elapses.
func waitHealth(addr string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if healthUp(dialAddr(addr)) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// readPID waits briefly for a spawned child to write its pid, then parses it.
func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child pid file %s never appeared", path)
	return 0
}

// killChildren kills the pids recorded in the given files and waits for them
// to die, so a detached child's cwd handle cannot block t.TempDir cleanup on
// Windows. Registered as a test cleanup.
func killChildren(paths ...string) {
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			continue
		}
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
		for i := 0; i < 100 && isProcessAlive(pid); i++ {
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestRestartNoStateFileRefuses(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "lens.db")
	err := Restart([]string{"--timeout", "1s", "--db-path", db, "--proxy-addr", "127.0.0.1:1", "--dashboard-addr", "127.0.0.1:1"})
	if err == nil || !strings.Contains(err.Error(), "no state file") {
		t.Fatalf("err = %v, want no state file", err)
	}
}

// TestRestartStaleFileRemovesAndSpawns covers step 3(b)'s stale branch: a
// pre-seeded file whose pid is dead, with nothing listening, is removed and
// restart proceeds to spawn (step 5) without burning --timeout. A second run
// then hits step 2's "no state file" refusal.
func TestRestartStaleFileRemovesAndSpawns(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "lens.db")
	if err := writeServeState(serveStatePath(db), serveState{
		PID:           deadPID(t),
		Exe:           "cmd",
		Args:          []string{"/c", "exit", "0"},
		Cwd:           dir,
		StartedAt:     time.Now().Add(-time.Hour),
		ProxyAddr:     "127.0.0.1:1",
		DashboardAddr: "127.0.0.1:1",
		// LogPath is left empty: a non-empty path makes detachCommand hold
		// the file open, which blocks t.TempDir's cleanup once the helper
		// child has been spawned.
	}); err != nil {
		t.Fatal(err)
	}
	err := Restart([]string{"--timeout", "200ms", "--db-path", db, "--proxy-addr", "127.0.0.1:1", "--dashboard-addr", "127.0.0.1:1"})
	if err != nil {
		t.Fatalf("restart on a stale file: %v", err)
	}
	if _, statErr := os.Stat(serveStatePath(db)); !os.IsNotExist(statErr) {
		t.Fatalf("stale state file still present after restart: %v", statErr)
	}

	err = Restart([]string{"--timeout", "200ms", "--db-path", db, "--proxy-addr", "127.0.0.1:1", "--dashboard-addr", "127.0.0.1:1"})
	if err == nil || !strings.Contains(err.Error(), "no state file") {
		t.Fatalf("second restart err = %v, want no state file refusal", err)
	}
}

// TestRestartLivePidOldStartTreatsAsStarting covers step 3(b)'s starting
// branch: a file whose pid is alive (this process) but whose started_at is far
// in the past, with both ports refusing, is treated as *starting* — not
// removed — and restart falls through to the shutdown POST and bounded wait,
// which fails closed on --timeout rather than spawning a second instance.
func TestRestartLivePidOldStartTreatsAsStarting(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "lens.db")
	if err := writeServeState(serveStatePath(db), serveState{
		PID:           os.Getpid(), // alive
		StartedAt:     time.Now().Add(-48 * time.Hour),
		Exe:           "cmd",
		Args:          []string{"/c", "exit", "0"},
		Cwd:           dir,
		ProxyAddr:     "127.0.0.1:1",
		DashboardAddr: "127.0.0.1:1",
	}); err != nil {
		t.Fatal(err)
	}
	err := Restart([]string{"--timeout", "150ms", "--db-path", db, "--proxy-addr", "127.0.0.1:1", "--dashboard-addr", "127.0.0.1:1"})
	if err == nil {
		t.Fatal("restart on a live-pid file with refusing ports should not report success")
	}
	if _, statErr := os.Stat(serveStatePath(db)); statErr != nil {
		t.Fatalf("a live-pid file was removed (should be treated as starting): %v", statErr)
	}
}

// TestRestartDetachedChildSurvivesParent pins bead 09:46 / plan §5 B.5's risk
// row: the detached spawn outlives the process that launched it. An
// intermediate helper plays the parent — it spawns the detached health child
// and exits; the test then finds the child still answering /api/health.
func TestRestartDetachedChildSurvivesParent(t *testing.T) {
	addr := freeAddr(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv(restartHelperMode, "detach")
	t.Setenv(restartHelperAddr, addr)
	t.Setenv(restartHelperPID, pidFile)
	t.Cleanup(func() { killChildren(pidFile) })

	// The intermediate is the "parent": wait for it to exit, then prove the
	// detached child it launched is still up.
	parent := exec.Command(os.Args[0], "-test.run="+restartHelperRun)
	if err := parent.Run(); err != nil {
		t.Fatalf("intermediate parent helper: %v", err)
	}
	if !waitHealth(addr, 5*time.Second) {
		t.Fatal("detached child did not survive its parent")
	}
}

// TestRestartRollbackRespawnsPrevious pins bead 09:47: with a deliberately
// unhealthy --exe (cmd, which cannot run the helper args), health never comes
// up, so the command kills the bad child, respawns the retained previous
// binary, exits non-zero, and names which binary is serving.
func TestRestartRollbackRespawnsPrevious(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "lens.db")
	addr := freeAddr(t)
	pidFile := filepath.Join(dir, "pid")
	t.Setenv(restartHelperMode, "health")
	t.Setenv(restartHelperAddr, addr)
	t.Setenv(restartHelperPID, pidFile)
	t.Cleanup(func() { killChildren(pidFile) })

	// A stale file (dead pid, refusing ports) whose retained exe/args are the
	// health helper. Cwd is a non-temp dir so the detached child never locks
	// t.TempDir on Windows.
	if err := writeServeState(serveStatePath(db), serveState{
		PID:           deadPID(t),
		Exe:           os.Args[0],
		Args:          []string{"-test.run=" + restartHelperRun},
		Cwd:           os.TempDir(),
		StartedAt:     time.Now().Add(-time.Hour),
		ProxyAddr:     addr,
		DashboardAddr: addr,
	}); err != nil {
		t.Fatal(err)
	}

	err := Restart([]string{"--exe", "cmd", "--timeout", "300ms", "--db-path", db, "--proxy-addr", addr, "--dashboard-addr", addr})
	if err == nil {
		t.Fatal("restart with an unhealthy --exe should exit non-zero")
	}
	if !strings.Contains(err.Error(), "failed health") || !strings.Contains(err.Error(), os.Args[0]) {
		t.Fatalf("rollback error = %q, want it to name the respawned binary %s", err, os.Args[0])
	}
	if !waitHealth(addr, 5*time.Second) {
		t.Fatal("rollback did not respawn the previous binary into health")
	}
}

// TestRestartTimeoutWithoutExeKeepsChild pins bead 09:48: a health timeout
// with no --exe leaves the child alone (it may be mid-CREATE INDEX), rather
// than killing it.
func TestRestartTimeoutWithoutExeKeepsChild(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "lens.db")
	addr := freeAddr(t)
	pidFile := filepath.Join(dir, "pid")
	t.Setenv(restartHelperMode, "sleep")
	t.Setenv(restartHelperPID, pidFile)
	t.Cleanup(func() { killChildren(pidFile) })

	if err := writeServeState(serveStatePath(db), serveState{
		PID:           deadPID(t),
		Exe:           os.Args[0],
		Args:          []string{"-test.run=" + restartHelperRun},
		Cwd:           os.TempDir(),
		StartedAt:     time.Now().Add(-time.Hour),
		ProxyAddr:     addr,
		DashboardAddr: addr,
	}); err != nil {
		t.Fatal(err)
	}

	err := Restart([]string{"--timeout", "300ms", "--db-path", db, "--proxy-addr", addr, "--dashboard-addr", addr})
	if err != nil {
		t.Fatalf("timeout without --exe should not error: %v", err)
	}
	pid := readPID(t, pidFile)
	if !isProcessAlive(pid) {
		t.Fatalf("child pid %d was killed on timeout; it must survive", pid)
	}
}

// TestRestartWaitsForStateFileNotJustPorts pins bead 09:52: after the shutdown
// POST the ports may close before the store has drained, so restart must key
// on the state file disappearing — not on port refusal. A stub dashboard
// answers health and shutdown then stops listening while the file lingers; a
// wait gated on the file burns the timeout and reports it, whereas a wait that
// broke on ports alone would fall through, spawn, and return nil.
func TestRestartWaitsForStateFileNotJustPorts(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "lens.db")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/shutdown" {
			w.WriteHeader(http.StatusOK)
			go srv.Close() // ports refuse from here on; the file stays put
			return
		}
		w.WriteHeader(http.StatusOK) // /api/health
	}))
	addr := srv.Listener.Addr().String()

	if err := writeServeState(serveStatePath(db), serveState{
		PID:           os.Getpid(), // alive, so restart takes the shutdown path
		Exe:           "cmd",
		Args:          []string{"/c", "exit", "0"},
		Cwd:           os.TempDir(),
		StartedAt:     time.Now().Add(-time.Hour),
		ProxyAddr:     addr,
		DashboardAddr: addr,
	}); err != nil {
		t.Fatal(err)
	}

	err := Restart([]string{"--timeout", "300ms", "--db-path", db, "--proxy-addr", addr, "--dashboard-addr", addr})
	if err == nil || !strings.Contains(err.Error(), "state file to disappear") {
		t.Fatalf("restart = %v, want the file-gone timeout (a ports-only wait would spawn and return nil)", err)
	}
}
