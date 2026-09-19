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

// ErrInvalidName and ErrInvalidRate are the typed rejections CheckModel and
// CheckRate wrap with %w, so a caller (POST /api/prices) can map a bad model
// name or a bad rate to 400 with errors.Is, and anything else (a disk
// failure) to 500 — a full disk is not a bad request.
var (
	ErrInvalidName = errors.New("pricing: invalid model name")
	ErrInvalidRate = errors.New("pricing: invalid rate")
)

// CheckModel reports whether name is a valid model name, wrapping
// ErrInvalidName when it is not. It is the one guard every writer of the
// price file passes through: parseTable, Save, and cli.applySet.
func CheckModel(name string) error {
	if !validModel(name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return nil
}

// CheckRate reports whether v is a valid rate — finite and non-negative —
// wrapping ErrInvalidRate when it is not. 0 is valid: a genuinely free model
// is distinct from an unset (nil) one.
func CheckRate(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("%w: %v: want a number", ErrInvalidRate, v)
	}
	if v < 0 {
		return fmt.Errorf("%w: %v: must not be negative", ErrInvalidRate, v)
	}
	return nil
}

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
			if err := CheckModel(key); err != nil {
				return nil, fmt.Errorf("line %d: missing '=': %q: %w", i+1, line, err)
			}
			if _, ok := t[key]; !ok {
				t[key] = Rates{}
			}
			continue
		}
		model, field, ok := strings.Cut(key, ".")
		if !ok {
			return nil, fmt.Errorf("line %d: want model.<%s>, got %q", i+1, strings.Join(fieldOrder, "|"), key)
		}
		if err := CheckModel(model); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
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
	if err != nil {
		return 0, fmt.Errorf("invalid rate %q: want a number", s)
	}
	if err := CheckRate(v); err != nil {
		return 0, fmt.Errorf("invalid rate %q: %w", s, err)
	}
	return v, nil
}

// validModel keeps a bare model line from swallowing a typo: only model-name
// characters are accepted, so "input 0.28" (a missing '=') is reported as a
// malformed line instead of silently reading as a model named "input 0.28".
//
// '.' is deliberately not accepted, even though it once was: Save writes
// model+"."+field (below) and parseTable cuts at the first '.' (above), so a
// model named "a.b" would write "a.b.input = 0.28", which reads back as
// model "a" / field "b.input" — an unknown field, failing the whole Load.
// Known behaviour change: a previously-accepted bare line like "deepseek.v2"
// is now a parse error — no real DeepSeek model name contains '.', and such
// a line could never round-trip through Save anyway.
func validModel(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == ':':
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
//
// Every model name and rate in t is validated before anything is written —
// a rejection (ErrInvalidName / ErrInvalidRate, matched with errors.Is)
// leaves the target file untouched. The write itself is atomic: a uniquely
// named temp file in the target's directory, synced, then renamed over the
// target, so a Loader mid-read never observes a truncated or empty file —
// os.WriteFile(path, ..., O_TRUNC) could otherwise be caught between the
// truncate and the write and read back an empty table, which Load treats as
// a fresh install and merges with Default(), permanently storing the next
// captured call as cost_usd IS NULL.
func Save(path string, t Table) error {
	for _, model := range sortedModels(t) {
		if err := CheckModel(model); err != nil {
			return fmt.Errorf("pricing: save: %w", err)
		}
		r := t[model]
		for _, f := range fieldOrder {
			if v := r.rate(f); v != nil {
				if err := CheckRate(*v); err != nil {
					return fmt.Errorf("pricing: save: %w", err)
				}
			}
		}
	}

	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	if dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("pricing: save: creating price dir %s: %w", dir, err)
		}
	}

	var b strings.Builder
	b.WriteString("# deepseek-lens price table — US dollars per 1,000,000 tokens.\n")
	b.WriteString("# Written by `lens prices --set model.field=rate`; a model with no\n")
	b.WriteString("# rates listed below is known but unpriced. Re-read on change.\n")
	b.WriteString("#\n")
	b.WriteString("# These are OFF-PEAK rates. DeepSeek bills 2x during its peak-pricing\n")
	b.WriteString("# window (01:00-04:00 and 06:00-10:00 UTC, on a working day) — lens\n")
	b.WriteString("# detects and applies that multiplier itself; it is not configurable\n")
	b.WriteString("# here. Whether a given day is a working day is: weekends and China's\n")
	b.WriteString("# statutory holidays are off-peak all day, and a 调休 make-up day is\n")
	b.WriteString("# a working day. See off_peak_dates / work_dates in config.toml.\n")
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
	tmp, err := os.CreateTemp(dir, ".prices-*.tmp")
	if err != nil {
		return fmt.Errorf("pricing: save: creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return fmt.Errorf("pricing: save: writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("pricing: save: syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("pricing: save: closing temp file: %w", err)
	}
	// Windows opens files without FILE_SHARE_DELETE by default, so a rename
	// over a file a concurrent Loader has open for reading can transiently
	// fail with "access is denied" even though the read itself is done in
	// microseconds. Retry briefly rather than surface that race to the
	// caller — the alternative is the exact torn-read window this bead
	// exists to close.
	if err := renameWithRetry(tmpPath, path); err != nil {
		return fmt.Errorf("pricing: save: renaming temp file into place: %w", err)
	}
	renamed = true
	return nil
}

// renameWithRetry retries os.Rename briefly on failure. Needed on Windows,
// where a concurrent reader's open handle (no FILE_SHARE_DELETE by default)
// can make a rename-over-target transiently fail; harmless elsewhere, where
// a rename failure is not expected to be transient and the loop exits on
// its first (and only) attempt succeeding or its last attempt's error.
func renameWithRetry(oldpath, newpath string) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if err = os.Rename(oldpath, newpath); err == nil {
			return nil
		}
		time.Sleep(2 * time.Millisecond)
	}
	return err
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
