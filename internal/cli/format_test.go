package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestHumanTokens(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{999, "999"},
		{1500, "1.5k"},
		{1_500_000, "1.5M"},
		{0, "0"},
	}
	for _, c := range cases {
		if got := humanTokens(c.n); got != c.want {
			t.Errorf("humanTokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Microsecond, "500µs"},
		{12 * time.Millisecond, "12ms"},
		{1400 * time.Millisecond, "1.4s"},
	}
	for _, c := range cases {
		if got := humanDuration(c.d); got != c.want {
			t.Errorf("humanDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestHumanCost(t *testing.T) {
	if got := humanCost(nil); got != "—" {
		t.Errorf("humanCost(nil) = %q, want %q (not $0.00 — that would claim a real zero cost)", got, "—")
	}
	small := 0.00012345
	if got := humanCost(&small); got != "$0.0001" {
		t.Errorf("humanCost(%v) = %q, want %q", small, got, "$0.0001")
	}
}

func TestRelTime(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{4 * time.Minute, "4m ago"},
		{3 * time.Hour, "3h ago"},
		{2 * 24 * time.Hour, "2d ago"},
	}
	for _, c := range cases {
		got := relTime(now.Add(-c.ago), now)
		if got != c.want {
			t.Errorf("relTime(now-%v) = %q, want %q", c.ago, got, c.want)
		}
	}
}

func TestTableAlignsRaggedInput(t *testing.T) {
	out := table([]string{"NAME", "VAL"}, [][]string{{"a", "100"}, {"bbbbb", ""}}, 0)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), out)
	}
	valCol := strings.Index(lines[0], "VAL")
	col100 := strings.Index(lines[1], "100")
	if valCol < 0 || col100 < 0 || valCol != col100 {
		t.Errorf("columns misaligned: VAL at %d, 100 at %d\n%s", valCol, col100, out)
	}
}

func TestTableValueWiderThanHeader(t *testing.T) {
	out := table([]string{"ID"}, [][]string{{"1234567890"}}, 0)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), out)
	}
	if lines[1] != "1234567890" {
		t.Errorf("got %q, want %q", lines[1], "1234567890")
	}
}

func TestTableTruncatesAtWidthBudget(t *testing.T) {
	out := table([]string{"A", "B"}, [][]string{{"short", strings.Repeat("x", 50)}}, 20)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for _, l := range lines {
		if n := utf8.RuneCountInString(l); n > 20 {
			t.Errorf("line exceeds maxWidth 20: %q (%d runes)", l, n)
		}
	}
	if !strings.Contains(out, "…") {
		t.Errorf("expected a truncation ellipsis in output:\n%s", out)
	}
}

func TestANSISuppressedWhenNotTTY(t *testing.T) {
	var buf bytes.Buffer
	if isTTY(&buf) {
		t.Error("bytes.Buffer must never report as a TTY")
	}
	if got := ansiHome(&buf); got != "" {
		t.Errorf("ansiHome on a non-TTY writer = %q, want empty (ANSI suppressed)", got)
	}
}

func TestIsTTYFalseForRegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "notty")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()
	if isTTY(f) {
		t.Error("a regular file must not report as a TTY")
	}
}

func TestParseSinceDurationAndRFC3339(t *testing.T) {
	got, err := parseSince("1h")
	if err != nil {
		t.Fatalf("parseSince(1h): %v", err)
	}
	if since := time.Since(got); since < 55*time.Minute || since > 65*time.Minute {
		t.Errorf("parseSince(1h) = %v, not ~1h ago", got)
	}

	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err = parseSince(want.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("parseSince(RFC3339): %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("parseSince(RFC3339) = %v, want %v", got, want)
	}

	if _, err := parseSince("not a time"); err == nil {
		t.Error("parseSince(garbage) should error")
	}
}

func TestOpenStoreUsesConfigDefaultDBPath(t *testing.T) {
	// openStore calls config.Load(nil), which resolves LENS_DB_PATH from
	// the environment — redirect it into a temp dir so this test never
	// touches the real $HOME/.deepseek-lens.
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	t.Setenv("LENS_DB_PATH", dbPath)

	st, err := openStore()
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer st.Close()

	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("expected openStore to create %s: %v", dbPath, err)
	}
}
