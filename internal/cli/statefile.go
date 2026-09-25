package cli

import (
	"encoding/json"
	"errors"
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
