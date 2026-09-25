package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
)

// Shutdown implements `lens shutdown [--timeout 30s]`.
func Shutdown(args []string) error {
	timeout := 30 * time.Second
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--timeout" && i+1 < len(args) {
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				return fmt.Errorf("shutdown: --timeout: %w", err)
			}
			timeout = d
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	cfg, err := config.Load(rest)
	if err != nil {
		return err
	}
	path := serveStatePath(cfg.DBPath)
	st, err := readServeState(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if healthUp(dialAddr(cfg.DashboardAddr)) {
			return postShutdown(dialAddr(cfg.DashboardAddr), timeout)
		}
		fmt.Fprintln(os.Stderr, "shutdown: nothing to shut down")
		return nil
	}
	dash := st.DashboardAddr
	proxy := st.ProxyAddr
	if dash == "" {
		dash = cfg.DashboardAddr
	}
	if proxy == "" {
		proxy = cfg.ProxyAddr
	}
	dash = dialAddr(dash)
	proxy = dialAddr(proxy)
	if !healthUp(dash) && portRefuses(proxy) && portRefuses(dash) && !isProcessAlive(st.PID) {
		fmt.Fprintln(os.Stderr, "shutdown: stale state file, removing")
		return os.Remove(path)
	}
	if err := postShutdown(dash, timeout); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, statErr := os.Stat(path)
		if os.IsNotExist(statErr) && portRefuses(proxy) && portRefuses(dash) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("shutdown: timed out after %s", timeout)
}

func healthUp(addr string) bool {
	if addr == "" {
		return false
	}
	c := &http.Client{Timeout: 200 * time.Millisecond}
	res, err := c.Get("http://" + addr + "/api/health")
	if err != nil {
		return false
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

func postShutdown(addr string, timeout time.Duration) error {
	c := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/api/shutdown", nil)
	if err != nil {
		return err
	}
	res, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("shutdown: post: %w", err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("shutdown: post: status %d", res.StatusCode)
	}
	return nil
}
