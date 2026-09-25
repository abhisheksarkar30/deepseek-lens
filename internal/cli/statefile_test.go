package cli

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// deadPID returns a pid that is definitely not alive: it spawns a short-lived
// helper and waits for it, so the process is reaped before its pid is read.
// Same idiom as shutdown_test.go's stale-file case.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("cmd", "/c", "exit", "0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn helper: %v", err)
	}
	return cmd.Process.Pid
}

// TestStateFileBindWinnerRewritesWithoutHavingCreated pins F10.1: the rewrite
// is done by the process that won both binds, whether or not it created the
// file. Before the fix the rewrite was gated on the create, so a process that
// found a live file and then won both binds left the file naming the previous
// pid with no bound addresses — and its clean exit then failed to remove it,
// so `lens shutdown` burned its whole --timeout against a healthy, already
// stopped server.
func TestStateFileBindWinnerRewritesWithoutHavingCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.state.json")

	// A live predecessor's file: its pid is this process (definitely alive),
	// so openServeStateFile must leave it untouched (created=false).
	if err := writeServeState(path, serveState{PID: os.Getpid(), StartedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	sf, err := openServeStateFile(path, newEarlyState())
	if err != nil {
		t.Fatal(err)
	}
	got, err := readServeState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProxyAddr != "" || got.DashboardAddr != "" {
		t.Fatalf("openServeStateFile rewrote a live file it did not create: %+v", got)
	}

	// This process wins both binds ⇒ it rewrites the file with the bound
	// addresses even though it did not create it.
	sf.bindWon("127.0.0.1:1111", "127.0.0.1:2222")
	got, err = readServeState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProxyAddr != "127.0.0.1:1111" || got.DashboardAddr != "127.0.0.1:2222" {
		t.Fatalf("bind winner did not rewrite the file: %+v", got)
	}

	// The bind winner is the remover: its clean exit leaves no file behind.
	sf.remove()
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("bind winner's clean exit left the state file behind: %v", statErr)
	}
}

// TestStateFileLostBindRemovesNothing pins the other half of F10.1: a process
// that attempted and lost a Listen must not remove the file, even while its
// pid still matches — the winner may not have rewritten yet.
func TestStateFileLostBindRemovesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.state.json")
	sf, err := openServeStateFile(path, newEarlyState())
	if err != nil {
		t.Fatal(err)
	}
	sf.bindFailed()
	sf.remove()
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("lost-bind process removed the state file: %v", statErr)
	}
}

// TestStateFilePreListenerReturnRemovesOwnFile pins the F7.4/F11.1 early-return
// case: a process that created the file but never reached a Listen (a
// store.Open failure, an injected proxy.NewServer failure) removes its own
// file through the shared gated removal, gated by the pid-guard alone.
func TestStateFilePreListenerReturnRemovesOwnFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.state.json")
	sf, err := openServeStateFile(path, newEarlyState())
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("early create did not write the file: %v", statErr)
	}
	sf.remove()
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("pre-listener return left the state file behind: %v", statErr)
	}
}

// TestStateFileLeaveFileSuppressesRemoval pins the fail-closed join-bound case:
// when the maintenance goroutine outran the drain bound, removal is suppressed
// so the "file gone ⇒ store closed" proof is never claimed falsely.
func TestStateFileLeaveFileSuppressesRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.state.json")
	sf, err := openServeStateFile(path, newEarlyState())
	if err != nil {
		t.Fatal(err)
	}
	sf.leaveFile()
	sf.remove()
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("leaveFile did not suppress removal: %v", statErr)
	}
}

// TestStateFileStaleTakeover rewrites a dead-pid file with this process's pid
// and removes it on a clean exit — the stale-takeover branch.
func TestStateFileStaleTakeover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.state.json")
	if err := writeServeState(path, serveState{PID: deadPID(t), StartedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	sf, err := openServeStateFile(path, newEarlyState())
	if err != nil {
		t.Fatal(err)
	}
	got, err := readServeState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != os.Getpid() {
		t.Fatalf("stale takeover pid = %d, want this process %d", got.PID, os.Getpid())
	}
	sf.remove()
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("stale takeover's clean exit left the file behind: %v", statErr)
	}
}

// TestWriteServeStateExclusiveNeverClobbers pins F7.2: the O_EXCL create does
// not overwrite an existing file and hands the existing state back.
func TestWriteServeStateExclusiveNeverClobbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.state.json")
	existing, created, err := writeServeStateExclusive(path, serveState{PID: 4242})
	if err != nil || !created || existing != nil {
		t.Fatalf("first create: created=%v existing=%v err=%v", created, existing, err)
	}
	existing, created, err = writeServeStateExclusive(path, serveState{PID: 9999})
	if err != nil || created {
		t.Fatalf("second create: created=%v err=%v, want created=false", created, err)
	}
	if existing == nil || existing.PID != 4242 {
		t.Fatalf("existing = %+v, want the first process's 4242", existing)
	}
	got, err := readServeState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != 4242 {
		t.Fatalf("file pid = %d, want 4242 (never clobbered)", got.PID)
	}
}

// TestIsProcessAliveBoundary pins the fail-closed contract: a live pid is
// alive; a reaped process is dead; a nonsensical pid is dead.
func TestIsProcessAliveBoundary(t *testing.T) {
	if !isProcessAlive(os.Getpid()) {
		t.Fatal("this process reported dead")
	}
	if isProcessAlive(deadPID(t)) {
		t.Fatal("a reaped process reported alive")
	}
	if isProcessAlive(0) {
		t.Fatal("pid 0 reported alive")
	}
}

// TestDialAddrNormalizesWildcard pins F5.3: an unspecified bind host is
// rewritten to loopback before a client dial.
func TestDialAddrNormalizesWildcard(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"0.0.0.0:8080", "127.0.0.1:8080"},
		{"[::]:8080", "[::1]:8080"},
		{"127.0.0.1:8080", "127.0.0.1:8080"},
	} {
		if got := dialAddr(tc.in); got != tc.want {
			t.Errorf("dialAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestWildcardListenerIsLiveNotStale pins F5.3 end-to-end: a live wildcard-
// bound listener must be judged live (portRefuses false) through the
// loopback-normalizing dial, not dead because 0.0.0.0 is not dialable on
// Windows while the server is alive.
func TestWildcardListenerIsLiveNotStale(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skipf("cannot bind wildcard: %v", err)
	}
	defer ln.Close()
	if portRefuses(ln.Addr().String()) {
		t.Fatalf("wildcard-bound live listener %s judged dead", ln.Addr())
	}
}
