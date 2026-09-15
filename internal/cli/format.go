// Package cli implements the `lens` command surface: one exported
// entrypoint per subcommand (Doctor, Serve, LS, Show, Tail, Stats,
// Warnings, Export, Prices, Replay), each writing to os.Stdout and each
// backed by an unexported, testable core function that takes an io.Writer
// and (where relevant) a *store.Store directly — so tests can capture
// output with a bytes.Buffer instead of spawning a subprocess.
//
// This file holds the shared output helpers the bead calls for: human
// unit formatting, the column-aligned table renderer, TTY detection, and
// the small store/config-loading glue every read-only subcommand repeats.
package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// repeatedFlag collects a repeatable string flag (e.g. `--set a --set b`) —
// stdlib flag has no built-in for that. Shared by prices.go's --set and
// replay.go's --set, which each used to hand-roll their own identical type.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ", ") }

func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// humanTokens formats a token count compactly: a plain integer under
// 1000, otherwise one decimal place with a k/M suffix (1500 -> "1.5k",
// 1_500_000 -> "1.5M").
func humanTokens(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// humanDuration formats a duration at whichever unit keeps it readable:
// microseconds below 1ms, milliseconds below 1s, seconds (1dp) at or
// above.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
}

// humanCost formats a per-request cost to 4 decimal places, or "—" when
// cost is unknown (nil) — never "$0.00", which would falsely claim a real
// zero cost.
func humanCost(c *float64) string {
	if c == nil {
		return "—"
	}
	return fmt.Sprintf("$%.4f", *c)
}

// relTime formats t relative to now: "just now" under a minute, then
// minutes/hours/days ago.
func relTime(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

// isTTY reports whether w is a character-device *os.File. Stdlib-only, per
// the bead's constraint against adding golang.org/x/term: any writer that
// isn't a real terminal — a bytes.Buffer, a regular file, a pipe — reports
// false, which is what suppresses ANSI escapes when output is piped.
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

// ansiHome returns the ANSI "clear screen, cursor home" escape sequence
// when w is a TTY, or "" otherwise — tail's redraw-in-place mechanism,
// and the thing that keeps piped tail output to plain lines.
func ansiHome(w io.Writer) string {
	if !isTTY(w) {
		return ""
	}
	return "\x1b[2J\x1b[H"
}

// tableGutter separates columns.
const tableGutter = "  "

// table renders headers and rows as an aligned, space-padded table.
// Columns are sized to their widest cell (ragged rows — fewer cells than
// headers — render the missing cells blank). If maxWidth > 0, columns are
// shrunk (widest first) until the rendered line fits within it, truncating
// clipped cells with a trailing "…".
func table(headers []string, rows [][]string, maxWidth int) string {
	n := len(headers)
	widths := make([]int, n)
	for i, h := range headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range rows {
		for i := 0; i < n; i++ {
			var cell string
			if i < len(row) {
				cell = row[i]
			}
			if w := utf8.RuneCountInString(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}
	if maxWidth > 0 {
		shrinkToFit(widths, maxWidth)
	}

	var b strings.Builder
	writeRow(&b, headers, widths)
	for _, row := range rows {
		writeRow(&b, row, widths)
	}
	return b.String()
}

func rowWidth(widths []int) int {
	total := 0
	for _, w := range widths {
		total += w
	}
	if len(widths) > 1 {
		total += (len(widths) - 1) * len(tableGutter)
	}
	return total
}

// shrinkToFit reduces the widest column(s) one rune at a time until the
// rendered row fits within maxWidth, never shrinking a column below 1 rune
// (leaving room for at least a single "…").
func shrinkToFit(widths []int, maxWidth int) {
	for rowWidth(widths) > maxWidth {
		widest := 0
		for i, w := range widths {
			if w > widths[widest] {
				widest = i
			}
		}
		if widths[widest] <= 1 {
			return // nothing left to shrink
		}
		widths[widest]--
	}
}

func writeRow(b *strings.Builder, cells []string, widths []int) {
	for i, w := range widths {
		var cell string
		if i < len(cells) {
			cell = cells[i]
		}
		cell = truncate(cell, w)
		if i > 0 {
			b.WriteString(tableGutter)
		}
		b.WriteString(cell)
		if pad := w - utf8.RuneCountInString(cell); pad > 0 && i < len(widths)-1 {
			b.WriteString(strings.Repeat(" ", pad))
		}
	}
	b.WriteByte('\n')
}

// truncate clips s to at most width runes, replacing the last rune with
// "…" when it had to cut anything.
func truncate(s string, width int) string {
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	return string(r[:width-1]) + "…"
}

// openStore resolves the effective DB path from env/config-file/defaults
// (config.Load(nil): a subcommand's own flags — --limit, --session, and so
// on — are never config flags, so they are never forwarded here) and opens
// the store. Every read-only subcommand entrypoint (ls, show, tail, stats,
// warnings, export) shares this.
func openStore() (*store.Store, error) {
	cfg, err := config.Load(nil)
	if err != nil {
		return nil, err
	}
	return store.Open(cfg.DBPath)
}

// parseSince accepts a Go duration ("24h", "30m", meaning "that long
// ago") or a full RFC3339 timestamp, for every subcommand's --since flag.
func parseSince(s string) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid --since value %q (want a duration like 24h or an RFC3339 timestamp)", s)
}
