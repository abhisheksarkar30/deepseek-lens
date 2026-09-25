// Command lens is the deepseek-lens CLI: a local observability proxy for
// DeepSeek API traffic.
package main

import (
	"fmt"
	"os"

	"github.com/abhisheksarkar30/deepseek-lens/internal/cli"
)

// commands dispatches subcommand names to their internal/cli implementation
// (br-GI-1-08). Every name here is implemented: br-GI-1-11 landed "prices",
// br-GI-1-13 landed "replay", and br-GI-17-08 landed "purge" — twelve names.
var commands = map[string]func([]string) error{
	"doctor":   cli.Doctor,
	"serve":    cli.Serve,
	"ls":       cli.LS,
	"show":     cli.Show,
	"tail":     cli.Tail,
	"stats":    cli.Stats,
	"sessions": cli.Sessions,
	"warnings": cli.Warnings,
	"export":   cli.Export,
	"prices":   cli.Prices,
	"replay":   cli.Replay,
	"purge":    cli.Purge,
	"shutdown": cli.Shutdown,
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
