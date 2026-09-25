package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
)

// Restart implements `lens restart [--exe path] [--timeout 30s]`.
func Restart(args []string) error {
	timeout := 30 * time.Second
	var exeOverride string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--timeout":
			if i+1 >= len(args) {
				return fmt.Errorf("restart: --timeout needs a duration")
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				return fmt.Errorf("restart: --timeout: %w", err)
			}
			timeout = d
			i++
		case "--exe":
			if i+1 >= len(args) {
				return fmt.Errorf("restart: --exe needs a path")
			}
			exeOverride = args[i+1]
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	cfg, err := config.Load(rest)
	if err != nil {
		return err
	}
	path := serveStatePath(cfg.DBPath)
	st, err := readServeState(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("serve is not running (no state file at %s); start with lens serve", path)
		}
		return err
	}
	dash := st.DashboardAddr
	proxy := st.ProxyAddr
	if dash == "" {
		dash = cfg.DashboardAddr
	}
	if proxy == "" {
		proxy = cfg.ProxyAddr
	}
	dash, proxy = dialAddr(dash), dialAddr(proxy)
	if !healthUp(dash) && portRefuses(proxy) && portRefuses(dash) && !isProcessAlive(st.PID) {
		fmt.Fprintln(os.Stderr, "restart: stale state file, removing")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		if err := postShutdown(dash, timeout); err != nil {
			return err
		}
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			_, statErr := os.Stat(path)
			if os.IsNotExist(statErr) && portRefuses(proxy) && portRefuses(dash) {
				break
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			return fmt.Errorf("restart: timed out waiting for the state file to disappear")
		}
	}
	exe := st.Exe
	if exeOverride != "" {
		exe = exeOverride
	}
	cmd, err := detachCommand(exe, st.Args, st.Cwd, st.LogPath)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart: spawn: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if healthUp(dash) {
			fmt.Fprintf(os.Stderr, "restart: spawned pid %d exe %s log %s\n", cmd.Process.Pid, exe, st.LogPath)
			_ = cmd.Process.Release()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	if exeOverride == "" {
		fmt.Fprintln(os.Stderr, "timed out waiting for new process to bind; it may still be starting — check lens doctor")
		_ = cmd.Process.Release()
		return nil
	}
	_ = cmd.Process.Kill()
	prev, err := detachCommand(st.Exe, st.Args, st.Cwd, st.LogPath)
	if err != nil {
		return err
	}
	if err := prev.Start(); err != nil {
		return fmt.Errorf("restart: rollback spawn: %w", err)
	}
	_ = prev.Process.Release()
	return fmt.Errorf("restart: %s failed health; serving %s", exe, st.Exe)
}
