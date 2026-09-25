package store

import (
	"bytes"
	"context"
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
