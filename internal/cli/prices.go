package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/abhisheksarkar30/deepseek-lens/internal/pricing"
)

// Prices is `lens prices`'s os.Stdout-writing entrypoint: it prints the
// effective price table and, with flags, edits ~/.deepseek-lens/prices.toml.
func Prices(args []string) error {
	return runPrices(args, os.Stdout, pricing.DefaultPath())
}

// runPrices is the testable core; the path is a parameter so a test can point
// it at a temp file instead of the user's real table.
func runPrices(args []string, w io.Writer, path string) error {
	fs := flag.NewFlagSet("prices", flag.ContinueOnError)
	var sets multiFlag
	fs.Var(&sets, "set", "set a rate, model.field=rate (repeatable); fields: input, output, cache_read, cache_write")
	unset := fs.String("unset", "", "revert a model to unpriced")
	edit := fs.Bool("edit", false, "open the price file in $EDITOR")
	if err := fs.Parse(args); err != nil {
		return err
	}

	tbl, err := pricing.Load(path)
	if err != nil {
		return err
	}

	if len(sets) > 0 || *unset != "" {
		for _, kv := range sets {
			if err := applySet(tbl, kv); err != nil {
				return err
			}
		}
		if *unset != "" {
			// Reverting to unpriced is an empty entry, not a deletion: a
			// deleted model would come back as "unknown-model" instead.
			tbl[*unset] = pricing.Rates{}
		}
		if err := pricing.Save(path, tbl); err != nil {
			return err
		}
	}

	if *edit {
		if err := openEditor(path); err != nil {
			return err
		}
		// Re-read what the editor left behind, so a syntax error is reported
		// now rather than silently degrading later requests to the last good
		// table.
		if tbl, err = pricing.Load(path); err != nil {
			return err
		}
	}

	return printPrices(w, path, tbl)
}

// multiFlag collects a repeatable string flag (--set a --set b).
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ", ") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// applySet parses "model.field=rate" into tbl.
func applySet(tbl pricing.Table, kv string) error {
	key, val, ok := strings.Cut(kv, "=")
	if !ok {
		return fmt.Errorf("--set %q: want model.field=rate", kv)
	}
	model, field, ok := strings.Cut(strings.TrimSpace(key), ".")
	if !ok || model == "" {
		return fmt.Errorf("--set %q: want model.field=rate", kv)
	}
	rate, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
	if err != nil {
		return fmt.Errorf("--set %q: invalid rate %q: want a number", kv, val)
	}
	if rate < 0 {
		return fmt.Errorf("--set %q: rate must not be negative", kv)
	}
	r := tbl[model]
	if err := r.Set(field, rate); err != nil {
		return fmt.Errorf("--set %q: %w", kv, err)
	}
	tbl[model] = r
	return nil
}

// printPrices renders the effective table. The unit is in the header on
// purpose: a bare "0.28" next to a model name is ambiguous between per-token
// and per-million-token, and getting it wrong by 10^6 is not a mistake the
// reader can spot later.
func printPrices(w io.Writer, path string, tbl pricing.Table) error {
	fmt.Fprintf(w, "price table: %s\n", path)
	fmt.Fprintln(w, "rates are US dollars per 1,000,000 tokens; \"—\" means no rate is configured")

	models := make([]string, 0, len(tbl))
	for m := range tbl {
		models = append(models, m)
	}
	sort.Strings(models)

	rows := make([][]string, 0, len(models))
	for _, m := range models {
		r := tbl[m]
		rows = append(rows, []string{
			m, rateCell(r.Input), rateCell(r.Output), rateCell(r.CacheRead), rateCell(r.CacheWrite), r.Source(),
		})
	}
	fmt.Fprint(w, table([]string{"MODEL", "INPUT", "OUTPUT", "CACHE READ", "CACHE WRITE", "SOURCE"}, rows, 0))
	return nil
}

func rateCell(v *float64) string {
	if v == nil {
		return "—"
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}

// openEditor runs $EDITOR on the price file. There is no editor guessing: a
// wrong guess opens something unexpected, or hangs waiting on a terminal a
// piped invocation does not have. An unset $EDITOR is a clear error naming
// the file instead.
func openEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		return fmt.Errorf("$EDITOR is not set; set it, or edit %s directly", path)
	}
	// ponytail: split on whitespace only, no shell-quoting rules. That covers
	// the "code -w" case without a shell, and running the command through
	// `sh -c` to get quoting would be a command-injection surface for no gain.
	parts := strings.Fields(editor)
	cmd := exec.Command(parts[0], append(parts[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
