package cli

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"
)

// serveState is <dir of DBPath>/serve.state.json. No secret is stored.
type serveState struct {
	PID           int       `json:"pid"`
	Exe           string    `json:"exe"`
	Args          []string  `json:"args"`
	Cwd           string    `json:"cwd"`
	StartedAt     time.Time `json:"started_at"`
	ProxyAddr     string    `json:"proxy_addr"`
	DashboardAddr string    `json:"dashboard_addr"`
	LogPath       string    `json:"log_path"`
}

func serveStatePath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "serve.state.json")
}

// writeServeStateExclusive creates the file with O_EXCL. On EEXIST it returns
// the existing state and created=false. A live owner's file is left untouched.
func writeServeStateExclusive(path string, st serveState) (existing *serveState, created bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, false, err
		}
		cur, rerr := readServeState(path)
		if rerr != nil {
			return nil, false, rerr
		}
		return cur, false, nil
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(st); err != nil {
		return nil, false, err
	}
	return nil, true, nil
}

func writeServeState(path string, st serveState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}

func readServeState(path string) (*serveState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st serveState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// removeServeStateIfOwner removes path only when its pid field equals pid.
// A missing file is success. bindLost vetoes removal: a process that lost
// Listen to a live winner must not delete the winner's file.
func removeServeStateIfOwner(path string, pid int, bindLost bool) error {
	if bindLost {
		return nil
	}
	cur, err := readServeState(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if cur.PID != pid {
		return nil
	}
	return os.Remove(path)
}

// serveStateFile owns the on-disk serve.state.json across one serve process's
// lifetime: the early O_EXCL create, the stale takeover, the post-bind
// rewrite, and the gated removal. Ownership of the file follows the
// **successful bind**, not the create (F10.1): whatever process wins both
// Listen calls rewrites the file with its own pid, whether or not it created
// it, and only that process's exit removes it. Kept a type rather than a
// handful of closures inside Serve so the create/takeover/bind/remove
// transitions can be driven from a test — Serve itself cannot (two real
// listeners, a blocking signal context).
type serveStateFile struct {
	path     string
	state    serveState
	bindLost bool
	suppress bool
}

// openServeStateFile creates the early state file with O_EXCL. When a previous
// file exists it is taken over only if its recorded pid is genuinely dead; a
// live owner's file is left untouched (F7.2). A process that took over a stale
// file is covered by the same gated removal bindWon/remove apply.
func openServeStateFile(path string, early serveState) (*serveStateFile, error) {
	existing, created, err := writeServeStateExclusive(path, early)
	if err != nil {
		return nil, err
	}
	if !created && existing != nil && !isProcessAlive(existing.PID) {
		early.StartedAt = existing.StartedAt
		if err := writeServeState(path, early); err != nil {
			return nil, err
		}
	}
	return &serveStateFile{path: path, state: early}, nil
}

// bindWon rewrites the file with this process's pid and the bound addresses.
// The create's ownership is irrelevant here: the bind winner is always the
// writer (F10.1), so a creator that lost the bind leaves the winner's file in
// place rather than stamping it with the creator's pid.
func (s *serveStateFile) bindWon(proxyAddr, dashboardAddr string) {
	s.state.ProxyAddr = proxyAddr
	s.state.DashboardAddr = dashboardAddr
	if err := writeServeState(s.path, s.state); err != nil {
		log.Printf("serve: rewrite state file: %v", err)
	}
}

// bindFailed records a failed Listen: this process must neither rewrite nor
// remove the file, since a live winner may own it (F10.1).
func (s *serveStateFile) bindFailed() { s.bindLost = true }

// remove runs the shared gated removal: the pid-guard plus the negative bind
// gate. Idempotent — a file already gone, or one another process owns, is a
// no-op. A process that never bound is gated by the pid-guard alone, so a
// pre-listener return still cleans up its own early-create file.
func (s *serveStateFile) remove() {
	if s.suppress {
		return
	}
	if err := removeServeStateIfOwner(s.path, os.Getpid(), s.bindLost); err != nil {
		log.Printf("serve: remove state file: %v", err)
	}
}

// leaveFile suppresses removal for the rest of this process's lifetime — the
// maintenance join expired, so the "file gone ⇒ store closed" proof must not
// be claimed while a writer may still hold the store (fail-closed).
func (s *serveStateFile) leaveFile() { s.suppress = true }

// dialAddr rewrites an unspecified bind host to loopback so a wildcard
// listener can be probed. Empty addr is returned unchanged.
func dialAddr(addr string) string {
	if addr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return net.JoinHostPort(host, port)
}

func portRefuses(addr string) bool {
	if addr == "" {
		return true
	}
	c, err := net.DialTimeout("tcp", dialAddr(addr), 200*time.Millisecond)
	if err != nil {
		return true
	}
	c.Close()
	return false
}
