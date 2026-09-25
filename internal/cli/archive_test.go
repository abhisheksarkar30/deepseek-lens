package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
	"github.com/abhisheksarkar30/deepseek-lens/internal/store"

	_ "modernc.org/sqlite"
)

// TestExportHydratesArchivedBodies is bead 11's hydration AC through the real
// reader (`lens export`'s runExport), not only the ListRequests proxy: a body
// moved into a day file must come back out of the export.
func TestExportHydratesArchivedBodies(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedRequest(t, st, func(r *store.Request) {
		r.StartedAt = time.Now().Add(-48 * time.Hour)
		r.ReqBody = []byte(`{"archived":"req"}`)
		r.RespBody = []byte(`{"archived":"resp"}`)
	})
	if err := st.ArchiveOlderThan(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := runExport(nil, &buf, st); err != nil {
		t.Fatalf("runExport: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("export output %q: %v", buf.String(), err)
	}
	if got["ReqBody"] == nil || got["RespBody"] == nil {
		t.Fatalf("export did not hydrate archived bodies: %v", got)
	}
}

func archiveCfg(dbPath string) *config.Config {
	cfg := config.Default()
	cfg.DBPath = dbPath
	cfg.HotDays = 7
	return cfg
}

func TestArchiveStatusCounts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seedRequest(t, st, func(r *store.Request) {
		r.StartedAt = time.Now().Add(-time.Hour)
		r.ReqBody = []byte("hot")
	})
	seedRequest(t, st, func(r *store.Request) {
		r.StartedAt = time.Now().Add(-10 * 24 * time.Hour)
		r.ReqBody = []byte("cold")
	})
	if err := st.ArchiveOlderThan(context.Background(), time.Now().Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	st.Close()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO body_archive(request_id, day, archived_at, body_mask) VALUES (999, '1999-01-01', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RestoreBetween(context.Background(), time.Now().Add(-11*24*time.Hour), time.Now().Add(-9*24*time.Hour), "hot"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	var buf bytes.Buffer
	if err := runArchive([]string{"status"}, &buf, archiveCfg(dbPath)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"archived: 2", "unarchived: 1", "missing_markers: 1", "restore_duplicates: 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
}

func TestArchiveRunDryRunIsIdentical(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seedRequest(t, st, func(r *store.Request) {
		r.StartedAt = time.Now().Add(-10 * 24 * time.Hour)
		r.ReqBody = []byte("move-me")
	})
	st.Close()
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := runArchive([]string{"run", "--dry-run"}, &buf, archiveCfg(dbPath)); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("dry-run changed the database")
	}
	if err := runArchive([]string{"run"}, &buf, archiveCfg(dbPath)); err == nil {
		t.Fatal("run without --yes should refuse")
	}
	if err := runArchive([]string{"run", "--yes"}, &buf, archiveCfg(dbPath)); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n, err := st.CountArchivable(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("archivable after run = %d", n)
	}
}

func TestArchiveRestoreRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-10 * 24 * time.Hour).UTC().Truncate(time.Second)
	seedRequest(t, st, func(r *store.Request) {
		r.StartedAt = started
		r.ReqBody = []byte("round")
		r.RespBody = []byte("trip")
	})
	if err := st.ArchiveOlderThan(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	since := started.Add(-time.Minute).Format(time.RFC3339)
	until := started.Add(time.Minute).Format(time.RFC3339)
	st.Close()

	var buf bytes.Buffer
	cfg := archiveCfg(dbPath)
	if err := runArchive([]string{"restore", "--since", since, "--until", until, "--dry-run"}, &buf, cfg); err != nil {
		t.Fatal(err)
	}
	if err := runArchive([]string{"restore", "--since", since, "--until", until}, &buf, cfg); err == nil {
		t.Fatal("restore without --yes should refuse")
	}
	if err := runArchive([]string{"restore", "--since", since, "--until", until, "--yes"}, &buf, cfg); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.ListRequests(context.Background(), store.Filter{Limit: 10, WithBodies: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || string(rows[0].ReqBody) != "round" || rows[0].ArchiveDay != nil {
		t.Fatalf("restored row = %+v", rows[0])
	}
	rep, err := st.ArchiveStatus(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Archived != 0 {
		t.Fatalf("markers left = %d", rep.Archived)
	}
}

func TestRestoreFaultKeepsMarker(t *testing.T) {
	st := newServeStore(t)
	started := time.Now().Add(-48 * time.Hour)
	seedRequest(t, st, func(r *store.Request) {
		r.StartedAt = started
		r.ReqBody = []byte("still-there")
	})
	ctx := context.Background()
	if err := st.ArchiveOlderThan(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RestoreBetween(ctx, started.Add(-time.Hour), started.Add(time.Hour), "hot"); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListRequests(ctx, store.Filter{Limit: 5, WithBodies: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || string(rows[0].ReqBody) != "still-there" {
		t.Fatal("hot body lost at the fault point")
	}
	rep, err := st.ArchiveStatus(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Archived != 1 || rep.RestoreDuplicates != 1 {
		t.Fatalf("archived=%d duplicates=%d", rep.Archived, rep.RestoreDuplicates)
	}
}

func newServeStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "lens.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
