package store

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveRoundTripAndCrashPoints(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	day1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	r1 := fullRequest()
	r1.StartedAt = day1
	r1.ReqBody = []byte("req-one")
	r1.RespBody = []byte("resp-one")
	id1, err := s.InsertRequest(ctx, r1)
	if err != nil {
		t.Fatal(err)
	}
	r2 := fullRequest()
	r2.StartedAt = day2
	r2.ReqBody = []byte("req-two")
	r2.RespBody = []byte("resp-two")
	if _, err := s.InsertRequest(ctx, r2); err != nil {
		t.Fatal(err)
	}

	if err := s.ArchiveOlderThan(ctx, time.Now().Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	s.dayOpens = 0
	listed, err := s.ListRequests(ctx, Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range listed {
		if row.ReqBody != nil || row.RespBody != nil {
			t.Fatalf("list returned bodies: %+v", row.ReqBody)
		}
	}
	if s.dayOpens != 0 {
		t.Fatalf("list opened %d day files, want 0", s.dayOpens)
	}
	got, err := s.GetRequest(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.ReqBody, []byte("req-one")) || !bytes.Equal(got.RespBody, []byte("resp-one")) {
		t.Fatalf("hydrated = %q %q", got.ReqBody, got.RespBody)
	}
	s.dayOpens = 0
	hydrated, err := s.ListRequests(ctx, Filter{Limit: 10, WithBodies: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.dayOpens != 2 {
		t.Fatalf("export-style hydrate opened %d day files, want 2", s.dayOpens)
	}
	if len(hydrated) != 2 {
		t.Fatalf("len = %d", len(hydrated))
	}
}

func TestArchiveCrashPoints(t *testing.T) {
	ctx := context.Background()
	before := time.Now().Add(time.Hour)

	s := newTestStore(t)
	r := fullRequest()
	r.ReqBody = []byte("keep-hot")
	r.RespBody = []byte("keep-hot-resp")
	id, err := s.InsertRequest(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveOlderThan(ctx, before, "day"); err != nil {
		t.Fatal(err)
	}
	hot, err := s.GetRequest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hot.ReqBody, []byte("keep-hot")) {
		t.Fatal("day-file crash lost the hot body")
	}

	s2 := newTestStore(t)
	r = fullRequest()
	r.ReqBody = []byte("from-archive")
	r.RespBody = []byte("from-archive-resp")
	id, err = s2.InsertRequest(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.ArchiveOlderThan(ctx, before, "hot"); err != nil {
		t.Fatal(err)
	}
	got, err := s2.GetRequest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.ReqBody, []byte("from-archive")) || !bytes.Equal(got.RespBody, []byte("from-archive-resp")) {
		t.Fatalf("hot-commit crash body = %q %q", got.ReqBody, got.RespBody)
	}
}

func TestPurgeDeletesArchivedBodies(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := fullRequest()
	r.StartedAt = time.Now().Add(-48 * time.Hour)
	r.ReqBody = []byte("archived-req")
	r.RespBody = []byte("archived-resp")
	id, err := s.InsertRequest(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveOlderThan(ctx, time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	n, err := s.PurgeableBytes(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("PurgeableBytes ignored the archived body")
	}
	if _, err := s.PurgeOlderThan(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRequest(ctx, id); err == nil {
		t.Fatal("purged request still readable")
	}
	var left int
	err = s.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM body_archive WHERE request_id = ?`, id).Scan(&left)
	if err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("marker rows left = %d", left)
	}
}

func TestGCArchiveKeepsReferencedRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := fullRequest()
	r.ReqBody = []byte("keep")
	r.RespBody = []byte("keep-resp")
	id, err := s.InsertRequest(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveOlderThan(ctx, time.Now().Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	day := r.StartedAt.UTC().Format("2006-01-02")
	dbPath := dayFile(s, day)
	db, err := openDay(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bodies(request_id, req_body, resp_body) VALUES (?,?,?)`, int64(999001), []byte("orphan"), []byte("orphan")); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	if err := s.GCArchive(ctx); err != nil {
		t.Fatal(err)
	}
	db, err = openDay(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var orphan, kept int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bodies WHERE request_id = 999001`).Scan(&orphan); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM bodies WHERE request_id = ?`, id).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if orphan != 0 || kept != 1 {
		t.Fatalf("orphan=%d kept=%d", orphan, kept)
	}
}

func dayFile(s *Store, day string) string {
	return filepath.Join(filepath.Dir(s.dbPath), "archive", "bodies-"+day+".db")
}

func openDay(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path)
}
