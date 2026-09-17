package pricing

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/parse"
)

// f is the pointer-to-rate literal the fixtures below read in.
func f(v float64) *float64 { return &v }

func TestComputeUnpricedIsNeverZero(t *testing.T) {
	// The model is present, its rates are not: the whole point of the bead is
	// that this is nil, not 0 — 0 reads as "this call was free".
	tbl := Table{"deepseek-flash": {}}
	got := Compute("deepseek-flash", parse.Usage{InputTokens: 100, OutputTokens: 50}, tbl, time.Time{})
	if got.Amount != nil {
		t.Errorf("Amount = %d, want nil for an unpriced model", *got.Amount)
	}
	if got.Source != SourceUnpriced {
		t.Errorf("Source = %q, want %q", got.Source, SourceUnpriced)
	}
}

func TestComputeUnknownModel(t *testing.T) {
	tbl := Table{"deepseek-flash": {Input: f(0.28)}}
	got := Compute("gpt-9", parse.Usage{InputTokens: 100}, tbl, time.Time{})
	if got.Amount != nil {
		t.Errorf("Amount = %d, want nil for a model not in the table", *got.Amount)
	}
	if got.Source != SourceUnknownModel {
		t.Errorf("Source = %q, want %q", got.Source, SourceUnknownModel)
	}
}

func TestComputeConfiguredMillionTokens(t *testing.T) {
	tbl := Table{"deepseek-flash": {Input: f(0.28)}}
	got := Compute("deepseek-flash", parse.Usage{InputTokens: 1_000_000}, tbl, time.Time{})
	if got.Amount == nil {
		t.Fatal("Amount = nil, want 280000 micro-dollars")
	}
	if *got.Amount != 280000 {
		t.Errorf("Amount = %d µ$, want 280000", *got.Amount)
	}
	if got.Source != SourceConfigured {
		t.Errorf("Source = %q, want %q", got.Source, SourceConfigured)
	}
}

func TestComputeConfiguredMixed(t *testing.T) {
	// Hand-checked: 1000 in @ $0.28/M = 280 µ$, 500 out @ $0.42/M = 210 µ$,
	// 300 cache-read @ $0.07/M = 21 µ$ → 511 µ$ = $0.000511.
	tbl := Table{"deepseek-flash": {Input: f(0.28), Output: f(0.42), CacheRead: f(0.07)}}
	got := Compute("deepseek-flash", parse.Usage{
		InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 300,
	}, tbl, time.Time{})
	if got.Amount == nil {
		t.Fatal("Amount = nil, want 511 micro-dollars")
	}
	if *got.Amount != 511 {
		t.Errorf("Amount = %d µ$, want 511", *got.Amount)
	}
	if got.Source != SourceConfigured {
		t.Errorf("Source = %q, want %q", got.Source, SourceConfigured)
	}
}

func TestComputeZeroTokensIsARealZero(t *testing.T) {
	tbl := Table{"deepseek-flash": {Input: f(0.28), Output: f(0.42)}}
	got := Compute("deepseek-flash", parse.Usage{}, tbl, time.Time{})
	if got.Amount == nil {
		t.Fatal("Amount = nil, want a non-nil 0 for a configured model with no tokens")
	}
	if *got.Amount != 0 {
		t.Errorf("Amount = %d µ$, want 0", *got.Amount)
	}
	if got.Source != SourceConfigured {
		t.Errorf("Source = %q, want %q", got.Source, SourceConfigured)
	}
}

// TestComputeNoDriftOverManyCalls is the reason Amount is an integer number
// of micro-dollars rather than a float: 10,000 individually-rounded calls
// must sum exactly, with no accumulation.
//
// The fixture rate is $1.00/M on purpose. At $0.28/M one token is 0.28 µ$,
// which rounds to 0 — every row would store $0.00, the total would be
// trivially exact, and the test would pass without exercising anything.
func TestComputeNoDriftOverManyCalls(t *testing.T) {
	tbl := Table{"deepseek-flash": {Input: f(1.00)}}
	const calls = 10_000

	var totalMicros int64
	for i := 0; i < calls; i++ {
		got := Compute("deepseek-flash", parse.Usage{InputTokens: 1}, tbl, time.Time{})
		if got.Amount == nil {
			t.Fatalf("call %d: Amount = nil", i)
		}
		if *got.Amount != 1 {
			t.Fatalf("call %d: Amount = %d µ$, want exactly 1 (rate $1.00/M, 1 token)", i, *got.Amount)
		}
		totalMicros += *got.Amount
	}
	if totalMicros != 10_000 {
		t.Errorf("total = %d µ$, want exactly 10000 µ$ (= $0.01)", totalMicros)
	}
	if dollars := float64(totalMicros) / 1e6; dollars != 0.01 {
		t.Errorf("total = %v dollars, want exactly 0.01", dollars)
	}
}

// TestComputeRoundsHalfUp pins rounding *at the call boundary*: half a
// micro-dollar rounds up, and (the other half of half-up) below half rounds
// down — otherwise "always rounds up" would pass the first assertion.
func TestComputeRoundsHalfUp(t *testing.T) {
	half := Table{"deepseek-flash": {Input: f(0.50)}}
	got := Compute("deepseek-flash", parse.Usage{InputTokens: 1}, half, time.Time{})
	if got.Amount == nil || *got.Amount != 1 {
		t.Errorf("1 token at $0.50/M = 0.5 µ$, got %v, want 1 (half-up, not truncation)", got.Amount)
	}

	below := Table{"deepseek-flash": {Input: f(0.28)}}
	got = Compute("deepseek-flash", parse.Usage{InputTokens: 1}, below, time.Time{})
	if got.Amount == nil || *got.Amount != 0 {
		t.Errorf("1 token at $0.28/M = 0.28 µ$, got %v, want 0 (below half rounds down)", got.Amount)
	}
}

func TestComputeCacheReadAtCacheRate(t *testing.T) {
	tbl := Table{"deepseek-flash": {Input: f(1.00), CacheRead: f(0.25)}}
	got := Compute("deepseek-flash", parse.Usage{CacheReadTokens: 400}, tbl, time.Time{})
	if got.Amount == nil || *got.Amount != 100 {
		t.Errorf("Amount = %v, want 100 µ$ (400 tokens at the $0.25/M cache-read rate)", got.Amount)
	}
	if got.Source != SourceConfigured {
		t.Errorf("Source = %q, want %q", got.Source, SourceConfigured)
	}
}

func TestComputeCacheReadWithoutCacheRateIsApproximate(t *testing.T) {
	tbl := Table{"deepseek-flash": {Input: f(1.00)}}
	got := Compute("deepseek-flash", parse.Usage{CacheReadTokens: 400}, tbl, time.Time{})
	if got.Amount == nil || *got.Amount != 400 {
		t.Errorf("Amount = %v, want 400 µ$ (400 tokens at the input rate)", got.Amount)
	}
	if got.Source != SourceApproximate {
		t.Errorf("Source = %q, want %q", got.Source, SourceApproximate)
	}
}

// TestComputeUnpricedBeatsApproximate settles the precedence the taxonomy
// implies: with no rates at all there is nothing to apply to any category, so
// cache-read tokens cannot downgrade an unpriced model to "approximate" — the
// stronger, more actionable label wins.
func TestComputeUnpricedBeatsApproximate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model string
		tbl   Table
		want  string
	}{
		{"unpriced", "deepseek-flash", Table{"deepseek-flash": {}}, SourceUnpriced},
		{"unknown", "gpt-9", Table{"deepseek-flash": {Input: f(1)}}, SourceUnknownModel},
	} {
		got := Compute(tc.model, parse.Usage{CacheReadTokens: 400}, tc.tbl, time.Time{})
		if got.Source != tc.want || got.Amount != nil {
			t.Errorf("%s: got {%v, %q}, want {nil, %q}", tc.name, got.Amount, got.Source, tc.want)
		}
	}
}

// friPeak and friOff share the fixture date the agentic-ai-artifacts plugin
// hooks use (Friday 2026-09-11) — this window now exists in three
// implementations that cannot import from each other, and sharing the date
// keeps the suites reading analogously, even though it detects no drift
// on its own (each side's own boundary test is what does that).
var (
	friPeak = time.Date(2026, 9, 11, 7, 0, 0, 0, time.UTC)  // Friday, inside the window
	friOff  = time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC) // Friday, outside it
)

func TestIsPeakBoundariesWeekday(t *testing.T) {
	d := func(h, m int) time.Time { return time.Date(2026, 9, 11, h, m, 0, 0, time.UTC) } // Friday
	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"00:59", d(0, 59), false},
		{"01:00", d(1, 0), true},
		{"03:59", d(3, 59), true},
		{"04:00", d(4, 0), false},
		{"05:59", d(5, 59), false},
		{"06:00", d(6, 0), true},
		{"09:59", d(9, 59), true},
		{"10:00", d(10, 0), false},
	}
	for _, tc := range cases {
		if got := IsPeak(tc.t); got != tc.want {
			t.Errorf("%s: IsPeak = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsPeakWeekendNeverPeaks(t *testing.T) {
	for _, tt := range []time.Time{
		time.Date(2026, 9, 12, 2, 0, 0, 0, time.UTC), // Saturday, in the 01:00-04:00 window
		time.Date(2026, 9, 12, 7, 0, 0, 0, time.UTC), // Saturday, in the 06:00-10:00 window
		time.Date(2026, 9, 13, 2, 0, 0, 0, time.UTC), // Sunday
		time.Date(2026, 9, 13, 7, 0, 0, 0, time.UTC), // Sunday
	} {
		if IsPeak(tt) {
			t.Errorf("IsPeak(%s) = true, want false (weekend)", tt)
		}
	}
}

// TestIsPeakUsesUTCNotLocal is the trap this bead exists to close: writing
// t.Hour() instead of t.UTC().Hour() silently answers in the caller's zone.
func TestIsPeakUsesUTCNotLocal(t *testing.T) {
	utc := time.Date(2026, 9, 11, 7, 0, 0, 0, time.UTC) // Friday, 07:00 UTC: inside the window
	loc := time.FixedZone("UTC+5", 5*60*60)
	local := utc.In(loc) // same instant; wall-clock hour 12 in this zone (outside every window)
	if !IsPeak(utc) {
		t.Error("IsPeak(utc) = false, want true")
	}
	if !IsPeak(local) {
		t.Error("IsPeak(local) = false, want true — same instant as utc, must classify the same regardless of location")
	}
}

func TestIsPeakZeroTimeIsOffPeak(t *testing.T) {
	// time.Time{} is Jan 1, year 1, 00:00 UTC — a Monday, hour 0. Off-peak, so
	// br-GI-4-02's mechanical time.Time{} edits preserve every existing
	// assertion's meaning.
	if IsPeak(time.Time{}) {
		t.Error("IsPeak(time.Time{}) = true, want false")
	}
}

func TestComputePeakIsExactlyDoubleOffPeak(t *testing.T) {
	tbl := Table{"deepseek-flash": {Input: f(0.28)}}
	usage := parse.Usage{InputTokens: 1_000_000}
	off := Compute("deepseek-flash", usage, tbl, friOff)
	peak := Compute("deepseek-flash", usage, tbl, friPeak)
	if off.Amount == nil || peak.Amount == nil {
		t.Fatalf("off.Amount=%v peak.Amount=%v, want both priced", off.Amount, peak.Amount)
	}
	if *peak.Amount != 2**off.Amount {
		t.Errorf("peak = %d µ$, want exactly 2x off-peak (%d)", *peak.Amount, 2**off.Amount)
	}
}

// TestComputePeakRoundsSumNotTotal pins that the multiplier is applied to
// the rate before summation, not to the rounded total afterward:
// roundHalfUp(2 * sum) and 2 * roundHalfUp(sum) differ here on purpose, and
// this bead's Compute must produce the former.
func TestComputePeakRoundsSumNotTotal(t *testing.T) {
	tbl := Table{"deepseek-flash": {Input: f(0.25)}}
	usage := parse.Usage{InputTokens: 1}
	off := Compute("deepseek-flash", usage, tbl, friOff)
	if off.Amount == nil || *off.Amount != 0 {
		t.Fatalf("off-peak Amount = %v, want 0 (0.25 µ$ rounds down)", off.Amount)
	}
	peak := Compute("deepseek-flash", usage, tbl, friPeak)
	if peak.Amount == nil || *peak.Amount != 1 {
		t.Errorf("peak Amount = %v, want 1 (round(2*0.25)=round(0.5)=1, not 2*round(0.25)=0)", peak.Amount)
	}
}

func TestComputeUnpricedAtPeak(t *testing.T) {
	tbl := Table{"deepseek-flash": {}}
	got := Compute("deepseek-flash", parse.Usage{InputTokens: 100}, tbl, friPeak)
	if got.Amount != nil || got.Source != SourceUnpriced {
		t.Errorf("got {%v, %q}, want {nil, %q}", got.Amount, got.Source, SourceUnpriced)
	}
}

func TestComputeUnknownModelAtPeak(t *testing.T) {
	tbl := Table{"deepseek-flash": {Input: f(0.28)}}
	got := Compute("gpt-9", parse.Usage{InputTokens: 100}, tbl, friPeak)
	if got.Amount != nil || got.Source != SourceUnknownModel {
		t.Errorf("got {%v, %q}, want {nil, %q}", got.Amount, got.Source, SourceUnknownModel)
	}
}

func TestComputeApproximateAtPeakUsesPeakMultipliedInputRate(t *testing.T) {
	tbl := Table{"deepseek-flash": {Input: f(1.00)}}
	got := Compute("deepseek-flash", parse.Usage{CacheReadTokens: 400}, tbl, friPeak)
	if got.Amount == nil || *got.Amount != 800 {
		t.Errorf("Amount = %v, want 800 µ$ (400 tokens at the peak-multiplied $2.00/M input rate)", got.Amount)
	}
	if got.Source != SourceApproximate {
		t.Errorf("Source = %q, want %q", got.Source, SourceApproximate)
	}
}

func TestTableRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	want := Table{
		"deepseek-v4-pro": {}, // bare model line: known, unpriced
		"deepseek-flash":  {Input: f(0.28), Output: f(0.42)},
		"deepseek-lite":   {Input: f(1), CacheRead: f(0.1), CacheWrite: f(2)},
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip changed the table:\n got %+v\nwant %+v", got, want)
	}
}

func TestLoadMissingFileIsTheShippedTable(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("Load on a missing file: %v", err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Errorf("got %+v, want Default() (%+v)", got, Default())
	}
	if got["deepseek-flash"].Source() != SourceUnpriced {
		t.Errorf("fresh install should be unpriced, got %q", got["deepseek-flash"].Source())
	}
}

func TestLoadMalformedNamesTheLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	body := "# comment\n\ndeepseek-flash.input = 0.28\ndeepseek-flash.output = not-a-number\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load on a malformed file returned no error")
	}
	if !strings.Contains(err.Error(), "line 4") {
		t.Errorf("error = %q, want it to name line 4", err)
	}
}

func TestLoadRejectsNegativeRate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	if err := os.WriteFile(path, []byte("deepseek-flash.input = -0.28\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted a negative rate")
	}
	if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "negative") {
		t.Errorf("error = %q, want it to name line 1 and the negative rate", err)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	if err := os.WriteFile(path, []byte("deepseek-flash.thinking = 0.28\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted an unknown rate field")
	}
}

// TestLoaderTakesEffectWithoutRestart covers the bead's "writes prices.toml
// and takes effect without restart": one long-lived loader must see a later
// write, and must keep the last good table while the file is malformed.
func TestLoaderTakesEffectWithoutRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.toml")
	if err := Save(path, Table{"deepseek-flash": {Input: f(0.28)}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	l := NewLoader(path)

	if got := l.Table()["deepseek-flash"].Input; got == nil || *got != 0.28 {
		t.Fatalf("first read: input = %v, want 0.28", got)
	}

	// Same-length edit: only the mtime can signal it.
	time.Sleep(20 * time.Millisecond)
	if err := Save(path, Table{"deepseek-flash": {Input: f(0.31)}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := l.Table()["deepseek-flash"].Input; got == nil || *got != 0.31 {
		t.Errorf("after an mtime-only change: input = %v, want 0.31", got)
	}

	// A malformed edit must not cost the running process its table.
	if err := os.WriteFile(path, []byte("deepseek-flash.input = oops\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := l.Table()["deepseek-flash"].Input; got == nil || *got != 0.31 {
		t.Errorf("after a malformed edit: input = %v, want the last good 0.31", got)
	}

	// A deleted file likewise keeps the last good table.
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := l.Table()["deepseek-flash"].Input; got == nil || *got != 0.31 {
		t.Errorf("after deleting the file: input = %v, want the last good 0.31", got)
	}
}

func TestDefaultPathHonorsHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	want := filepath.Join(dir, ".deepseek-lens", "prices.toml")
	if got := DefaultPath(); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestLoaderWithNoFileIsUnpriced(t *testing.T) {
	l := NewLoader(filepath.Join(t.TempDir(), "absent.toml"))
	if got := l.Table()["deepseek-flash"].Source(); got != SourceUnpriced {
		t.Errorf("source = %q, want %q", got, SourceUnpriced)
	}
}

// TestSaveRejectsInvalidModelName covers br-GI-17-01: a model name
// containing a newline, '=', '.', or a space is rejected with ErrInvalidName
// and the target file is left byte-for-byte unchanged.
func TestSaveRejectsInvalidModelName(t *testing.T) {
	for _, bad := range []string{"x\nfoo", "x=foo", "a.b", "a b"} {
		t.Run(bad, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "prices.toml")
			seed := Table{"deepseek-flash": {Input: f(0.28)}}
			if err := Save(path, seed); err != nil {
				t.Fatalf("seeding Save: %v", err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading seeded file: %v", err)
			}

			err = Save(path, Table{bad: {Input: f(0.1)}})
			if !errors.Is(err, ErrInvalidName) {
				t.Fatalf("Save(%q) error = %v, want ErrInvalidName", bad, err)
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading file after rejected Save: %v", err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Errorf("rejected Save changed the file:\n before %q\n after  %q", before, after)
			}
		})
	}
}

// TestSaveRejectsInvalidRate covers the shared ErrInvalidRate check: NaN,
// +Inf, -Inf, and a negative rate are all rejected; 0 is accepted and reads
// back distinct from unset.
func TestSaveRejectsInvalidRate(t *testing.T) {
	for name, v := range map[string]float64{
		"NaN":  math.NaN(),
		"+Inf": math.Inf(1),
		"-Inf": math.Inf(-1),
		"-1":   -1,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "prices.toml")
			err := Save(path, Table{"deepseek-flash": {Input: f(v)}})
			if !errors.Is(err, ErrInvalidRate) {
				t.Fatalf("Save(rate=%v) error = %v, want ErrInvalidRate", v, err)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "prices.toml")
	if err := Save(path, Table{"deepseek-flash": {Input: f(0)}}); err != nil {
		t.Fatalf("Save(rate=0): %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if in := got["deepseek-flash"].Input; in == nil || *in != 0 {
		t.Errorf("input = %v, want a non-nil 0 (a free model, distinct from unset)", in)
	}
}

// TestParseTableRejectsDottedBareModelLine pins the D4 tightening: a bare
// model line containing '.' cannot round-trip through Save (Save writes
// model+"."+field, and parseTable cuts at the first '.'), so it is now a
// parse error rather than silently accepted.
func TestParseTableRejectsDottedBareModelLine(t *testing.T) {
	_, err := parseTable("deepseek.v2\n")
	if err == nil {
		t.Fatal("parseTable accepted a dotted bare model line")
	}
	if !errors.Is(err, ErrInvalidName) {
		t.Errorf("error = %v, want it to wrap ErrInvalidName", err)
	}
}

// TestSaveIsAtomicUnderConcurrentReaders is br-GI-17-01's actual P0 claim.
// The property that distinguishes temp-file+rename from truncate-in-place
// (os.WriteFile with O_TRUNC) is only observable from a reader racing a
// writer: a reader landing inside the old code's write window sees an
// *empty* file, and parseTable("") returns an empty table with *no error*
// (table.go's Load then merges Default() and the caller never learns
// anything was wrong — the exact silent-corruption defect this bead exists
// to close). So this drives a real writer against a real reader and asserts
// every successful read is one of the two tables actually written — never
// an empty one, and never a partially-written (parse-error) one.
//
// A raw os.ReadFile can still fail with a transient "access denied"/sharing
// violation while the rename is in flight — Windows opens files without
// FILE_SHARE_DELETE by default, so MoveFileEx briefly excludes a concurrent
// Open, unlike POSIX rename(2). That is not the defect: it is a visible,
// retryable I/O error, and the production reader (Loader.Table()) already
// treats any Load error as "keep the last good table"
// (TestLoaderTakesEffectWithoutRestart pins that for a malformed edit). So
// this test tolerates read errors but asserts none of them is a parse
// error — a parse error would mean the reader saw a truncated write, which
// only a non-atomic Save could produce.
func TestSaveIsAtomicUnderConcurrentReaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prices.toml")
	tblA := Table{"deepseek-flash": {Input: f(0.28)}}
	tblB := Table{"deepseek-flash": {Input: f(0.31)}}
	if err := Save(path, tblA); err != nil {
		t.Fatalf("seeding Save: %v", err)
	}

	const iterations = 300
	stop := make(chan struct{})
	var readErrs, parseErrs, emptyReads, wrongReads int
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			got, err := Load(path)
			mu.Lock()
			switch {
			case err != nil:
				readErrs++
				// A parse error (as opposed to a plain I/O open failure)
				// means Load got as far as reading bytes and rejecting
				// them — that would mean it saw a torn write.
				if strings.Contains(err.Error(), "line ") {
					parseErrs++
				}
			case got["deepseek-flash"].Input == nil:
				emptyReads++
			case *got["deepseek-flash"].Input != 0.28 && *got["deepseek-flash"].Input != 0.31:
				wrongReads++
			}
			mu.Unlock()
		}
	}()

	for i := 0; i < iterations; i++ {
		tbl := tblA
		if i%2 == 1 {
			tbl = tblB
		}
		if err := Save(path, tbl); err != nil {
			t.Fatalf("iteration %d: Save: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()

	t.Logf("%d concurrent reads raced the writer, %d of them errored (expected on Windows — see comment above)", iterations, readErrs)
	if parseErrs != 0 {
		t.Errorf("%d reads hit a parse error (want 0 — that means a torn/truncated write was observed)", parseErrs)
	}
	if wrongReads != 0 {
		t.Errorf("%d reads returned a rate that was neither table written (want 0 — that means corrupted content)", wrongReads)
	}
	if emptyReads != 0 {
		t.Errorf("%d concurrent reads saw an empty/unset table (want 0 — that is the truncate-in-place defect this bead fixes)", emptyReads)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, ".prices-*.tmp"))
	if len(matches) != 0 {
		t.Errorf("left %d .tmp file(s) behind: %v", len(matches), matches)
	}
}
