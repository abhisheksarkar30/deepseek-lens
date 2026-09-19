package pricing

import (
	"fmt"
	"strings"
	"time"
)

// dateLayout is both the wire format of the configured date sets and the key
// format of the parsed maps: a UTC calendar date.
const dateLayout = "2006-01-02"

// Calendar decides peak vs off-peak for an instant: DeepSeek's peak window
// and weekend rule, plus two configured date sets that answer the rest-day
// question — an off-peak (rest-day) set and a work-day set.
//
// The zero value is meaningful and is exactly the pre-holiday behaviour: no
// dates configured, so IsPeak is the window-and-weekend rule alone. That is
// what keeps every existing test and every un-wired caller honest without a
// change, and it is why the calendar is installed optionally.
type Calendar struct {
	offPeak map[string]bool // "2006-01-02" UTC → a rest day: off-peak for the whole day
	work    map[string]bool // "2006-01-02" UTC → a work day: peak inside the window, like any weekday (调休)

	// the two strings NewCalendar was given, kept so DateSets can echo them
	// back verbatim — the read-only Settings display and doctor's print of
	// the effective calendar both read this
	offPeakSrc string
	workSrc    string
}

// NewCalendar parses the two date sets. Both may be empty.
//
// The grammar is deliberately the smallest thing that mirrors the source
// document: comma-separated items, each a single YYYY-MM-DD or an inclusive
// YYYY-MM-DD..YYYY-MM-DD range, whitespace trimmed around the whole string
// and around each item. The State Council notice is seven ranges, so the
// shipped default reads like the notice it cites.
//
// A malformed item is an error naming it, never a skipped entry. That is a
// deliberate departure from ModelMap/ModelMaxTokens, which skip bad entries
// leniently: a skipped model mapping degrades to a fallback that is visible
// in the dashboard's model column, whereas a silently skipped holiday date is
// a silent 2x overcharge on exactly the days the calendar exists to fix.
//
// A date present in both sets is also an error, naming the date — the one
// case where "last rule wins" would produce an arbitrary answer.
func NewCalendar(offPeak, work string) (Calendar, error) {
	off, err := parseDateSet(offPeak, "off-peak")
	if err != nil {
		return Calendar{}, err
	}
	wk, err := parseDateSet(work, "work")
	if err != nil {
		return Calendar{}, err
	}
	for d := range wk {
		if off[d] {
			return Calendar{}, fmt.Errorf("pricing: date %s is in both the off-peak and the work date sets; a date is one or the other", d)
		}
	}
	return Calendar{offPeak: off, work: wk, offPeakSrc: offPeak, workSrc: work}, nil
}

// DateSets returns the two configured date strings exactly as supplied to
// NewCalendar — the verbatim echo, not a re-render of the parsed maps. A
// range comes back as its range ("2026-02-15..2026-02-23"), not expanded into
// nine dates, and the string matches config.toml / env / flag byte for byte
// so the user can compare it. The zero calendar returns ("", "").
func (c Calendar) DateSets() (offPeak, work string) {
	return c.offPeakSrc, c.workSrc
}

// IsPeak reports whether t is billed at DeepSeek's peak rate.
//
// One question — is this a rest day? — followed by one window rule, resolved
// in this order:
//
//  1. the date is in the off-peak set  → off-peak for the whole day;
//  2. else the date is in the work set → the window rule, skipping the
//     weekend check (a 调休 make-up day is a working day, and must beat rule 3
//     or it is inexpressible);
//  3. else Saturday or Sunday          → off-peak;
//  4. else inside 01:00-04:00 or 06:00-10:00 UTC → peak;
//  5. else                             → off-peak.
//
// Rules 2 and 3 are what make the two configured sets symmetric, and the
// symmetry is the whole reason the model is correct: a work day — reached by
// default (a weekday) or by the work set (a 调休 weekend) — peaks only inside
// the window, and a rest day — reached by default (a weekend) or by the
// off-peak set (a weekday holiday) — never peaks. Classifying a 调休 date as
// peak for the whole day instead would overcharge every off-window hour of
// it, which is the silent 2x overstatement this calendar exists to remove.
//
// The key is the UTC date, and that is correct rather than merely convenient.
// Beijing date D spans [D-1 16:00 UTC, D 16:00 UTC). Both peak windows sit
// inside the 00:00-16:00 UTC half of that span, so every peak window on
// Beijing date D carries UTC date D, and the two keyings agree. They agree
// *because* of rules 1-3: outside the window a work day and an off-peak
// holiday both give the answer the weekday/weekend rule already gives, so the
// only instants either set can change are peak-window instants — and a peak
// window never straddles the 16:00 UTC date boundary.
//
// ponytail: the argument above rests on both windows lying in the UTC
// 00:00-16:00 half of the day. If DeepSeek ever moves a window across
// 16:00 UTC, UTC keying and Beijing keying part ways and this needs a UTC+8
// constant instead.
//
// This window is duplicated from two implementations lens cannot import —
// agentic-ai-artifacts's hooks/deepseek-peak-guard.sh and
// hooks/deepseek-auto-toggle.js — so it is now the third copy; keep it in
// sync with both by hand.
func (c Calendar) IsPeak(t time.Time) bool {
	u := t.UTC()
	if c.offPeak[u.Format(dateLayout)] {
		return false // rule 1: a rest day, all day
	}
	if !c.work[u.Format(dateLayout)] { // rule 2 skips this check for a make-up work day
		switch u.Weekday() {
		case time.Saturday, time.Sunday:
			return false // rule 3
		}
	}
	hour := u.Hour()
	return (hour >= 1 && hour < 4) || (hour >= 6 && hour < 10) // rules 4 and 5
}

// Covers reports whether year is named by either configured date set. It
// exists for doctor's coverage check, not for pricing — IsPeak never consults
// it.
func (c Calendar) Covers(year int) bool {
	prefix := fmt.Sprintf("%04d-", year)
	for d := range c.offPeak {
		if strings.HasPrefix(d, prefix) {
			return true
		}
	}
	for d := range c.work {
		if strings.HasPrefix(d, prefix) {
			return true
		}
	}
	return false
}

// PeakPriced is the one predicate behind both the peak_pricing warning and
// the per-session rollup, so the two cannot restate it and drift. A nil cost
// is an unpriced call and a configured model with zero tokens prices to a
// real, non-nil 0 — neither was billed at peak.
func (c Calendar) PeakPriced(at time.Time, cost *float64) bool {
	return cost != nil && *cost > 0 && c.IsPeak(at)
}

// parseDateSet parses one comma-separated date list. label names the set in
// the error, so a typo says which of the two keys it came from.
func parseDateSet(s, label string) (map[string]bool, error) {
	out := map[string]bool{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("pricing: %s dates: empty item (a trailing or doubled comma)", label)
		}
		lo, hi, isRange := strings.Cut(item, "..")
		start, err := time.Parse(dateLayout, strings.TrimSpace(lo))
		if err != nil {
			return nil, fmt.Errorf("pricing: %s dates: item %q is not a zero-padded YYYY-MM-DD date", label, item)
		}
		end := start
		if isRange {
			if end, err = time.Parse(dateLayout, strings.TrimSpace(hi)); err != nil {
				return nil, fmt.Errorf("pricing: %s dates: item %q is not a zero-padded YYYY-MM-DD date", label, item)
			}
			if end.Before(start) {
				return nil, fmt.Errorf("pricing: %s dates: item %q is a reversed range (starts after it ends)", label, item)
			}
		}
		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			out[d.Format(dateLayout)] = true
		}
	}
	return out, nil
}
