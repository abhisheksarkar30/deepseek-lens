// Command lens is the deepseek-lens CLI: a local observability proxy for
// DeepSeek API traffic.
package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
)

// commands dispatches subcommand names to their implementation. Only
// "doctor" is implemented in this bead; later beads only add to this map.
var commands = map[string]func([]string) error{
	"doctor": runDoctor,
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lens <command> [flags]")
		os.Exit(1)
	}

	name := os.Args[1]
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "lens %s: not implemented yet\n", name)
		os.Exit(1)
	}

	if err := cmd(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "lens %s: %v\n", name, err)
		os.Exit(1)
	}
}

func runDoctor(args []string) error {
	cfg, err := config.Load(args)
	if err != nil {
		return err
	}

	lines := [][2]string{
		{"proxy_addr", cfg.ProxyAddr},
		{"dashboard_addr", cfg.DashboardAddr},
		{"upstream_url", cfg.UpstreamURL},
		{"db_path", cfg.DBPath},
		{"body_policy", cfg.BodyPolicy},
		{"body_cap_bytes", fmt.Sprintf("%d", cfg.BodyCapBytes)},
		{"allow_remote", fmt.Sprintf("%t", cfg.AllowRemote)},
		{"capture", fmt.Sprintf("%t", cfg.Capture)},
		{"session_gap_minutes", fmt.Sprintf("%d", cfg.SessionGapMinutes)},
		{"replay_enabled", fmt.Sprintf("%t", cfg.ReplayEnabled)},
		{"db_dir_writable", fmt.Sprintf("%t", dirWritable(filepath.Dir(cfg.DBPath)))},
		{"upstream_resolves", fmt.Sprintf("%t", hostResolves(cfg.UpstreamURL))},
	}

	width := 0
	for _, l := range lines {
		if len(l[0]) > width {
			width = len(l[0])
		}
	}
	for _, l := range lines {
		fmt.Printf("%-*s: %s\n", width, l[0], l[1])
	}

	if verr := cfg.Validate(); verr != nil {
		fmt.Printf("warning: config is invalid: %v\n", verr)
	}
	if cfg.AllowRemote {
		fmt.Println("warning: AllowRemote is set — proxy/dashboard may bind to non-loopback addresses")
	}
	if cfg.BodyPolicy == "full" {
		fmt.Println("warning: BodyPolicy=full — full request/response bodies are captured and stored")
	}

	return nil
}

// dirWritable reports whether dir exists (creating it if needed) and a file
// can be created in it.
func dirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".lens-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// hostResolves reports whether rawURL's host resolves via DNS. It never
// contacts the upstream itself — doctor must work without it being up.
func hostResolves(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	_, err = net.LookupHost(u.Hostname())
	return err == nil
}
