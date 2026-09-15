package analyze

import (
	"os"
	"strings"
	"testing"
)

// The README's warning table is the reference a user comes back to, so it is
// checked here rather than trusted: it must list exactly the kinds this tool
// can raise — every one AllKinds knows, and no kind it does not. Without this
// the table is a hand-copy that drifts the first time a rule is added.
//
// Both markers are load-bearing. The table sits between them so the check
// reads one region instead of every backticked word in the file, and a
// missing marker fails the test rather than quietly passing it.
const (
	readmePath = "../../README.md"
	tableBegin = "<!-- BEGIN warning kinds"
	tableEnd   = "<!-- END warning kinds"
)

// consumerSource is read (not imported) to check consumerKinds below: those
// kinds are string literals in internal/consumer, and importing the package
// from here would invert the dependency the pipeline is built on.
const consumerSource = "../consumer/consumer.go"

// consumerKinds are raised by internal/consumer rather than by this package,
// so they are deliberately absent from AllKinds — analyze cannot emit them.
// Upstream_error is not here: ruleUpstreamError raises that same kind for an
// error object inside a 200 body, so it is already in AllKinds, and the two
// are two triggers for one user-facing meaning rather than a duplicate.
var consumerKinds = []string{"analyzer_panic"}

func TestReadmeWarningTableMatchesKinds(t *testing.T) {
	readme := mustRead(t, readmePath)

	table, ok := between(readme, tableBegin, tableEnd)
	if !ok {
		t.Fatalf("%s: warning table markers %q / %q not found or out of order", readmePath, tableBegin, tableEnd)
	}

	inReadme := documentedKinds(table)

	// Every kind the code can emit is documented, and every documented kind
	// is one the code can emit. Equality, not containment: a row for a kind
	// nothing raises is as wrong as a missing row, and both are silent
	// otherwise.
	want := map[string]bool{}
	for _, info := range AllKinds() {
		want[string(info.Kind)] = true
		if !inReadme[string(info.Kind)] {
			t.Errorf("kind %q is emitted by analyze but has no row in %s's warning table", info.Kind, readmePath)
		}
		// The description is the product: a kind name alone teaches nothing,
		// and a table row that has drifted from the sentence that explains it
		// is the same failure as a missing row.
		if !strings.Contains(table, info.Description) {
			t.Errorf("kind %q's description from kinds.go does not appear in %s's warning table:\n%s", info.Kind, readmePath, info.Description)
		}
	}
	for _, k := range consumerKinds {
		want[k] = true
		if !inReadme[k] {
			t.Errorf("kind %q is raised by internal/consumer but has no row in %s's warning table", k, readmePath)
		}
	}
	for k := range inReadme {
		if !want[k] {
			t.Errorf("%s's warning table lists kind %q, which no code can emit", readmePath, k)
		}
	}

	// The consumer's kinds are literals in a package this test may not
	// import, so check they still exist where they are claimed to be: a row
	// that outlives the code raising it is exactly the drift this guards.
	src := mustRead(t, consumerSource)
	for _, k := range consumerKinds {
		if !strings.Contains(src, `"`+k+`"`) {
			t.Errorf("consumer kind %q is documented in %s but no longer appears as a literal in %s", k, readmePath, consumerSource)
		}
	}
}

// AllKinds is the source of truth, so a caller could mutate it and change
// what every other caller sees. Handing out a copy is only worth anything if
// it holds.
func TestAllKindsReturnsACopy(t *testing.T) {
	first := AllKinds()
	if len(first) == 0 {
		t.Fatal("AllKinds() is empty")
	}
	original := first[0]

	first[0].Kind = "mutated"
	first[0].Description = "mutated"
	if got := AllKinds()[0]; got != original {
		t.Errorf("AllKinds() shares state with its caller: got %+v, want %+v", got, original)
	}
}

// documentedKinds reads the kind names out of a markdown table: the first
// backticked token on each table row. Header and separator rows carry no
// backticks and are skipped; a description's own backticks come after the
// kind's, so the first pair is always the kind.
func documentedKinds(table string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(table, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		_, rest, ok := strings.Cut(line, "`")
		if !ok {
			continue
		}
		name, _, ok := strings.Cut(rest, "`")
		if !ok || name == "" {
			continue
		}
		out[name] = true
	}
	return out
}

// between returns the text between two markers, and whether both were found
// in that order.
func between(s, begin, end string) (string, bool) {
	_, rest, ok := strings.Cut(s, begin)
	if !ok {
		return "", false
	}
	inner, _, ok := strings.Cut(rest, end)
	if !ok {
		return "", false
	}
	return inner, true
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
