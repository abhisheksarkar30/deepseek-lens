package pricing

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultPath is ~/.deepseek-lens/prices.toml, derived the same way
// internal/config/config.go derives its own path — $HOME first, so tests (and
// users) can redirect it uniformly across platforms. The helper is duplicated
// rather than exported out of internal/config so this stays a leaf package.
func DefaultPath() string {
	home := os.Getenv("HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	if home == "" {
		return filepath.Join(".deepseek-lens", "prices.toml")
	}
	return filepath.Join(home, ".deepseek-lens", "prices.toml")
}

// Default is the shipped table: the two models DeepSeek's model map resolves
// to (br-GI-1-10), present with no rates. An entry exists so "unpriced" is
// reachable and distinct from "unknown-model" the moment the tool runs, and
// the rates are nil so no number is ever invented.
func Default() Table {
	return Table{"deepseek-v4-pro": {}, "deepseek-flash": {}}
}

// fieldOrder is the write order and the only set of field names accepted.
var fieldOrder = []string{"input", "output", "cache_read", "cache_write"}

// Load reads the price table at path. A missing file is not an error: it
// yields Default(), so a fresh install reports "unpriced" rather than
// "unknown-model" for the models DeepSeek actually serves. Every Default()
// model is present in the result whether or not the file mentions it, so
// configuring one model never demotes the other to unknown-model.
//
// A file that fails to parse returns an error naming the offending line and
// no table at all — callers keep their last good table rather than adopting
// a half-applied one.
func Load(path string) (Table, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("pricing: load: reading price table: %w", err)
	}
	t, err := parseTable(string(data))
	if err != nil {
		return nil, fmt.Errorf("pricing: load: %s: %w", path, err)
	}
	for model, r := range Default() {
		if _, ok := t[model]; !ok {
			t[model] = r
		}
	}
	return t, nil
}

// parseTable parses the flat `key = value` price file.
//
// Despite the .toml extension there is no TOML parser here and this repo
// takes no TOML dependency: the extension is a naming convention shared with
// ~/.deepseek-lens/config.toml, not a format promise. Same discipline as
// internal/config's parseFlatFile — blank lines and '#' comments ignored,
// everything else is either `model.field = rate` or a bare model name
// meaning "known, unpriced".
func parseTable(data string) (Table, error) {
	t := Table{}
	for i, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, hasEq := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !hasEq {
			if !validModel(key) {
				return nil, fmt.Errorf("line %d: missing '=': %q", i+1, line)
			}
			if _, ok := t[key]; !ok {
				t[key] = Rates{}
			}
			continue
		}
		model, field, ok := strings.Cut(key, ".")
		if !ok || !validModel(model) {
			return nil, fmt.Errorf("line %d: want model.<%s>, got %q", i+1, strings.Join(fieldOrder, "|"), key)
		}
		rate, err := parseRate(strings.TrimSpace(val))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		r := t[model]
		if err := r.Set(field, rate); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		t[model] = r
	}
	return t, nil
}

// parseRate accepts a non-negative, finite dollar rate. A negative rate is
// rejected rather than clamped: it would silently subtract from every total
// it touched.
func parseRate(s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("invalid rate %q: want a number", s)
	}
	if v < 0 {
		return 0, fmt.Errorf("invalid rate %q: must not be negative", s)
	}
	return v, nil
}

// validModel keeps a bare model line from swallowing a typo: only model-name
// characters are accepted, so "input 0.28" (a missing '=') is reported as a
// malformed line instead of silently reading as a model named "input 0.28".
func validModel(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}

// Save writes t to path (creating ~/.deepseek-lens if needed) in a fixed
// model-then-field order, so a write/read round trip is stable. A model with
// no rates is written as a bare model line: that is what keeps "known but
// unpriced" alive across a round trip, and what makes `lens prices --unset`
// revert a model to unpriced rather than deleting it into unknown-model.
func Save(path string, t Table) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("pricing: save: creating price dir %s: %w", dir, err)
		}
	}

	var b strings.Builder
	b.WriteString("# deepseek-lens price table — US dollars per 1,000,000 tokens.\n")
	b.WriteString("# Written by `lens prices --set model.field=rate`; a model with no\n")
	b.WriteString("# rates listed below is known but unpriced. Re-read on change.\n")
	for _, model := range sortedModels(t) {
		r := t[model]
		wrote := false
		for _, f := range fieldOrder {
			v := r.rate(f)
			if v == nil {
				continue
			}
			fmt.Fprintf(&b, "%s.%s = %s\n", model, f, strconv.FormatFloat(*v, 'g', -1, 64))
			wrote = true
		}
		if !wrote {
			b.WriteString(model + "\n")
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("pricing: save: writing price table: %w", err)
	}
	return nil
}

// rate returns the named field's value, nil when unset. Unexported: the
// fields are read directly everywhere else, and only the field-name-driven
// callers (Save, parseTable) need the indirection.
func (r Rates) rate(field string) *float64 {
	switch field {
	case "input":
		return r.Input
	case "output":
		return r.Output
	case "cache_read":
		return r.CacheRead
	case "cache_write":
		return r.CacheWrite
	}
	return nil
}

func sortedModels(t Table) []string {
	models := make([]string, 0, len(t))
	for m := range t {
		models = append(models, m)
	}
	sort.Strings(models)
	return models
}

// Loader serves the running consumer's price table, re-reading prices.toml
// when it changes so `lens prices --set` takes effect without a restart. The
// consumer is a long-lived goroutine, so it cannot read the table once at
// startup; Table() is safe for concurrent use.
//
// A parse failure keeps the last good table and is never surfaced to a
// request: CLAUDE.md's "fail open" covers the price file too, and a person
// mid-edit in $EDITOR must not cost them a row.
type Loader struct {
	path string

	mu   sync.Mutex
	mod  time.Time
	size int64
	tbl  Table // last good
}

// NewLoader returns a Loader over path. The first Table() call reads the file.
func NewLoader(path string) *Loader { return &Loader{path: path} }

// Table returns the current price table, re-reading the file when its mtime
// or size has changed since the last read.
//
// ponytail: change detection is a stat, not fsnotify — a dependency is not
// worth it for a file a human edits by hand, and the worst case (an edit
// that lands inside the filesystem's mtime granularity and preserves the
// size) is one stale table until the next call. Upgrade to a watcher if a
// price change ever needs to be observed with no call in between.
func (l *Loader) Table() Table {
	l.mu.Lock()
	defer l.mu.Unlock()

	fi, err := os.Stat(l.path)
	if err != nil {
		// Absent or unreadable: keep whatever we last had, defaulting to the
		// shipped table so a fresh install prices as "unpriced", not as
		// "unknown-model".
		if l.tbl == nil {
			l.tbl = Default()
		}
		return l.tbl
	}
	if l.tbl != nil && fi.ModTime().Equal(l.mod) && fi.Size() == l.size {
		return l.tbl
	}

	t, err := Load(l.path)
	if err != nil {
		if l.tbl == nil {
			l.tbl = Default()
		}
		return l.tbl
	}
	l.mod, l.size, l.tbl = fi.ModTime(), fi.Size(), t
	return t
}
