package pricing

import (
	"strings"
	"testing"
	"time"
)

func mustCal(t *testing.T, offPeak, work string) Calendar {
	t.Helper()
	c, err := NewCalendar(offPeak, work)
	if err != nil {
		t.Fatalf("NewCalendar(%q, %q): %v", offPeak, work, err)
	}
	return c
}

func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func TestNewCalendarGrammarAccepts(t *testing.T) {
	cases := []struct {
		name           string
		offPeak, work  string
		offPeakIn, wIn bool // whether 2026-02-17 / 2026-02-14 land in the set
	}{
		{"empty string is the empty set", "", "", false, false},
		{"all whitespace is the empty set", "   ", " \t ", false, false},
		{"single date", "2026-02-17", "", true, false},
		{"single range", "2026-02-15..2026-02-17", "", true, false},
		{"several items", "2026-01-01,2026-02-17,2026-04-04..2026-04-06", "", true, false},
		{"whitespace around the whole string and each item",
			"  2026-02-15 .. 2026-02-17 ,  2026-04-04  ", "", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := mustCal(t, tc.offPeak, tc.work)
			if got := c.offPeak["2026-02-17"]; got != tc.offPeakIn {
				t.Errorf("2026-02-17 in the off-peak set = %v, want %v", got, tc.offPeakIn)
			}
			if got := c.work["2026-02-14"]; got != tc.wIn {
				t.Errorf("2026-02-14 in the work set = %v, want %v", got, tc.wIn)
			}
		})
	}
}

func TestNewCalendarRejectsMalformedItems(t *testing.T) {
	cases := []struct {
		name, in, wantNamed string
	}{
		{"missing zero-pad", "2026-1-1", `"2026-1-1"`},
		{"non-date", "not-a-date", `"not-a-date"`},
		{"month out of range", "2026-13-01", `"2026-13-01"`},
		{"day out of range", "2026-02-30", `"2026-02-30"`},
		{"reversed range", "2026-03-05..2026-03-01", `"2026-03-05..2026-03-01"`},
		{"range end missing zero-pad", "2026-01-01..2026-1-05", `"2026-01-01..2026-1-05"`},
		{"trailing comma", "2026-01-01,", "empty item"},
		{"doubled comma", "2026-01-01,,2026-01-02", "empty item"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, set := range []string{"offPeak", "work"} {
				offPeak, work := tc.in, ""
				if set == "work" {
					offPeak, work = "", tc.in
				}
				_, err := NewCalendar(offPeak, work)
				if err == nil {
					t.Fatalf("%s: NewCalendar(%q, %q) = nil error, want a rejection", set, offPeak, work)
				}
				if !strings.Contains(err.Error(), tc.wantNamed) {
					t.Errorf("%s: error %q does not name the offending item %s", set, err, tc.wantNamed)
				}
				if !strings.Contains(err.Error(), set) && set == "work" {
					t.Errorf("error %q does not say which set the bad item came from", err)
				}
			}
		})
	}
}

func TestNewCalendarRejectsADateInBothSets(t *testing.T) {
	_, err := NewCalendar("2026-02-15..2026-02-17", "2026-02-16")
	if err == nil {
		t.Fatal("NewCalendar = nil error, want a rejection for a date in both sets")
	}
	if !strings.Contains(err.Error(), "2026-02-16") {
		t.Errorf("error %q does not name the contradictory date", err)
	}
}

func TestDateSetsEchoesVerbatim(t *testing.T) {
	const off, work = "2026-02-15..2026-02-23, 2026-04-04 ", "2026-02-14"
	c := mustCal(t, off, work)
	gotOff, gotWork := c.DateSets()
	if gotOff != off || gotWork != work {
		t.Errorf("DateSets() = (%q, %q), want (%q, %q)", gotOff, gotWork, off, work)
	}
	var zero Calendar
	if off, work := zero.DateSets(); off != "" || work != "" {
		t.Errorf("zero Calendar DateSets() = (%q, %q), want (\"\", \"\")", off, work)
	}
}

// TestCalendarPrecedence pins each of the five rules in IsPeak's order
// independently, so a regression names the rule it broke.
func TestCalendarPrecedence(t *testing.T) {
	// 2026-02-14 is a Saturday and a 调休 make-up work day; 2026-02-17 is a
	// Tuesday inside the Spring Festival range.
	c := mustCal(t, "2026-02-15..2026-02-23", "2026-02-14")

	cases := []struct {
		name string
		at   time.Time
		want bool
		rule string
	}{
		{"a work date is peak inside the window", at(2026, 2, 14, 2, 0), true, "2"},
		{"a work date is off-peak outside it", at(2026, 2, 14, 20, 0), false, "2"},
		{"a work date is peak in the second window too", at(2026, 2, 14, 9, 0), true, "2"},
		{"a weekday holiday is off-peak inside the window", at(2026, 2, 17, 2, 0), false, "1"},
		{"a weekday holiday is off-peak all day", at(2026, 2, 17, 9, 0), false, "1"},
		{"a plain Saturday is off-peak inside the window", at(2026, 9, 12, 2, 0), false, "3"},
		{"a plain Wednesday is peak inside the window", at(2026, 9, 9, 2, 0), true, "4"},
		{"a plain Wednesday is off-peak outside it", at(2026, 9, 9, 20, 0), false, "5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.IsPeak(tc.at); got != tc.want {
				t.Errorf("IsPeak(%s) = %v, want %v (rule %s)", tc.at, got, tc.want, tc.rule)
			}
		})
	}
}

// TestCalendarUTCKeying pins D4: a Beijing date D spans [D-1 16:00 UTC,
// D 16:00 UTC), and both peak windows sit in the 00:00-16:00 UTC half, so the
// two keyings agree. The positive control at the end is what stops this
// passing vacuously on an all-off-peak calendar.
func TestCalendarUTCKeying(t *testing.T) {
	c := mustCal(t, "2026-02-15..2026-02-23", "2026-02-14")

	for _, tc := range []struct {
		name string
		at   time.Time
	}{
		{"the last 8h of the previous UTC date", at(2026, 2, 16, 16, 30)},
		{"just after midnight on the Beijing date", at(2026, 2, 17, 0, 30)},
		{"inside the first window", at(2026, 2, 17, 2, 0)},
		{"inside the second window", at(2026, 2, 17, 9, 0)},
	} {
		if c.IsPeak(tc.at) {
			t.Errorf("%s: IsPeak(%s) = true, want false — a holiday reads the same either side of the 16:00 UTC boundary", tc.name, tc.at)
		}
	}

	// The counterexample the corrected model must answer off-peak: a 调休
	// Saturday at 16:30 UTC. UTC keying reads 2026-02-14 (a work date,
	// off-window); Beijing keying reads 2026-02-15 00:30, inside the Spring
	// Festival range. Both say off-peak. A whole-day-peak reading of the work
	// set would have said peak here and overcharged 15 of the day's 24 hours.
	if c.IsPeak(at(2026, 2, 14, 16, 30)) {
		t.Error("IsPeak(2026-02-14 16:30 UTC) = true, want false — off-window on a make-up work day")
	}

	// Positive control: the same weekday, one day past the range, at peak.
	if !c.IsPeak(at(2026, 2, 24, 2, 0)) {
		t.Error("positive control: IsPeak(2026-02-24 02:00 UTC) = false, want true — the search above proves nothing if nothing peaks")
	}
}

// TestZeroCalendarMatchesTheOldRule is the regression guard bead 02's
// mechanical edits rest on: the zero calendar must be the pre-GI-24 package
// function byte for byte. The oracle below is that function's body, frozen.
func TestZeroCalendarMatchesTheOldRule(t *testing.T) {
	oldRule := func(t time.Time) bool {
		u := t.UTC()
		dow := u.Weekday()
		if dow == time.Saturday || dow == time.Sunday {
			return false
		}
		hour := u.Hour()
		return (hour >= 1 && hour < 4) || (hour >= 6 && hour < 10)
	}

	var zero Calendar
	start := at(2026, 2, 9, 0, 0) // a Monday
	sawPeak, sawOff := false, false
	for i := 0; i < 7*24*4; i++ { // one week at quarter-hour resolution
		tt := start.Add(time.Duration(i) * 15 * time.Minute)
		got, want := zero.IsPeak(tt), oldRule(tt)
		if got != want {
			t.Fatalf("Calendar{}.IsPeak(%s) = %v, want %v", tt, got, want)
		}
		sawPeak = sawPeak || got
		sawOff = sawOff || !got
	}
	if !sawPeak || !sawOff {
		t.Fatalf("the sweep saw peak=%v offPeak=%v; it must exercise both or it proves nothing", sawPeak, sawOff)
	}
}

func TestZeroCalendarOtherOldShapes(t *testing.T) {
	var zero Calendar

	// UTC, not local: same instant, different location, same answer.
	utc := at(2026, 9, 11, 7, 0) // Friday, inside the window
	local := utc.In(time.FixedZone("UTC+5", 5*60*60))
	if !zero.IsPeak(utc) || !zero.IsPeak(local) {
		t.Errorf("IsPeak(utc)=%v IsPeak(local)=%v, want both true", zero.IsPeak(utc), zero.IsPeak(local))
	}

	// time.Time{} is Jan 1, year 1, 00:00 UTC — a Monday at hour 0. Off-peak.
	if zero.IsPeak(time.Time{}) {
		t.Error("IsPeak(time.Time{}) = true, want false")
	}
}

func TestCalendarCovers(t *testing.T) {
	c := mustCal(t, "2026-02-15..2026-02-23", "2027-01-04")
	if !c.Covers(2026) {
		t.Error("Covers(2026) = false, want true — the off-peak set names 2026")
	}
	if !c.Covers(2027) {
		t.Error("Covers(2027) = false, want true — the work set names 2027")
	}
	if c.Covers(2028) {
		t.Error("Covers(2028) = true, want false — neither set names 2028")
	}
	var zero Calendar
	if zero.Covers(2026) {
		t.Error("zero Calendar Covers(2026) = true, want false")
	}
}

func TestCalendarPeakPriced(t *testing.T) {
	var zero Calendar
	zeroCost := 0.0
	nonzero := 0.05
	peakAt := at(2026, 9, 9, 2, 0) // a plain Wednesday, inside the window
	offAt := at(2026, 9, 9, 20, 0) // the same day, outside it

	cases := []struct {
		name string
		at   time.Time
		cost *float64
		want bool
	}{
		{"a nil cost is not priced at peak", peakAt, nil, false},
		{"a zero cost is not priced at peak", peakAt, &zeroCost, false},
		{"a positive cost at peak is", peakAt, &nonzero, true},
		{"a positive cost off-peak is not", offAt, &nonzero, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := zero.PeakPriced(tc.at, tc.cost); got != tc.want {
				t.Errorf("PeakPriced(%s) = %v, want %v", tc.at, got, tc.want)
			}
		})
	}
}
