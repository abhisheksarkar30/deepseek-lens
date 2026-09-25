package web

import (
	"strings"
	"testing"
)

// TestDashboardAssetGuards is beads 06/07's web-asset guard as a durable test
// rather than a one-off grep recorded in a completion note. The base tree
// carried utcDayBound (app.js:452) and a toISOString() (app.js:464); both were
// superseded by the single hourBound helper (bead 06), and the Feed gained its
// range banner (bead 07).
//
// Positive control: a token known to be present must be found first, so a
// failed/empty read cannot make a "symbol absent" assertion pass vacuously.
func TestDashboardAssetGuards(t *testing.T) {
	app := readAsset(t, "app.js")
	if !strings.Contains(app, "hourBound") {
		t.Fatal("positive control failed: hourBound missing from app.js (asset not read?)")
	}
	for _, gone := range []string{"utcDayBound", "toISOString"} {
		if strings.Contains(app, gone) {
			t.Errorf("app.js still contains the superseded %q", gone)
		}
	}

	html := readAsset(t, "index.html")
	if !strings.Contains(html, "hourBound") && !strings.Contains(html, "feed-range-banner") {
		t.Fatal("positive control failed: no range-control markup found in index.html")
	}
	if !strings.Contains(html, "range view") {
		t.Error("index.html is missing the Feed range's live-paused banner")
	}
}

func readAsset(t *testing.T, name string) string {
	t.Helper()
	b, err := Files.ReadFile(name)
	if err != nil {
		t.Fatalf("read embedded %s: %v", name, err)
	}
	return string(b)
}
