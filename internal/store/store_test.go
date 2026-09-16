package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fullRequest returns a fully-populated Request fixture, every nullable
// field set to a non-nil value except ReplayOf, so round-trip tests exercise
// every column. ReplayOf is left nil here because it is a real foreign key
// (schema.sql) into requests.id: a fixture value has nothing to reference,
// so tests that care about ReplayOf's round trip (TestRoundTrip) set it to
// an actually-inserted row's id instead.
func fullRequest() *Request {
	stopReason := "end_turn"
	errorText := "upstream 502"
	sessionHeader := "sess-abc"
	sessionID := "sess-abc"
	costUSD := 0.1234
	costSource := "pricing-table-v1"
	replayEdits := `{"max_tokens":512}`
	return &Request{
		StartedAt:           time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC),
		TTFB:                123 * time.Millisecond,
		Duration:            456 * time.Millisecond,
		Method:              "POST",
		Path:                "/v1/messages",
		RemoteAddr:          "127.0.0.1:54321",
		Status:              200,
		ReqHeaders:          `{"X-Api-Key":["[redacted]"],"Content-Type":["application/json"]}`,
		RespHeaders:         `{"Content-Type":["application/json"]}`,
		ReqBody:             []byte(`{"model":"claude-sonnet-5"}`),
		RespBody:            []byte(`{"id":"msg_1"}`),
		InputTokens:         100,
		OutputTokens:        200,
		CacheCreationTokens: 10,
		CacheReadTokens:     20,
		StopReason:          &stopReason,
		ModelRequested:      "claude-sonnet-5",
		ModelResolved:       "claude-sonnet-5-20260101",
		ErrorText:           &errorText,
		SessionHeader:       &sessionHeader,
		SessionID:           &sessionID,
		CostUSD:             &costUSD,
		CostSource:          &costSource,
		ReplayEdits:         &replayEdits,
		PrefixHash:          "abcdef0123456789",
	}
}

func ptrStrEqual(a, b *string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

func ptrFloatEqual(a, b *float64) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

func ptrInt64Equal(a, b *int64) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

func assertRequestEqual(t *testing.T, want, got *Request) {
	t.Helper()
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("StartedAt: got %v want %v", got.StartedAt, want.StartedAt)
	}
	if got.TTFB != want.TTFB {
		t.Errorf("TTFB: got %v want %v", got.TTFB, want.TTFB)
	}
	if got.Duration != want.Duration {
		t.Errorf("Duration: got %v want %v", got.Duration, want.Duration)
	}
	if got.Method != want.Method {
		t.Errorf("Method: got %q want %q", got.Method, want.Method)
	}
	if got.Path != want.Path {
		t.Errorf("Path: got %q want %q", got.Path, want.Path)
	}
	if got.RemoteAddr != want.RemoteAddr {
		t.Errorf("RemoteAddr: got %q want %q", got.RemoteAddr, want.RemoteAddr)
	}
	if got.Status != want.Status {
		t.Errorf("Status: got %d want %d", got.Status, want.Status)
	}
	if got.ReqHeaders != want.ReqHeaders {
		t.Errorf("ReqHeaders: got %q want %q", got.ReqHeaders, want.ReqHeaders)
	}
	if got.RespHeaders != want.RespHeaders {
		t.Errorf("RespHeaders: got %q want %q", got.RespHeaders, want.RespHeaders)
	}
	if !bytes.Equal(got.ReqBody, want.ReqBody) {
		t.Errorf("ReqBody mismatch")
	}
	if !bytes.Equal(got.RespBody, want.RespBody) {
		t.Errorf("RespBody mismatch")
	}
	if got.InputTokens != want.InputTokens {
		t.Errorf("InputTokens: got %d want %d", got.InputTokens, want.InputTokens)
	}
	if got.OutputTokens != want.OutputTokens {
		t.Errorf("OutputTokens: got %d want %d", got.OutputTokens, want.OutputTokens)
	}
	if got.CacheCreationTokens != want.CacheCreationTokens {
		t.Errorf("CacheCreationTokens: got %d want %d", got.CacheCreationTokens, want.CacheCreationTokens)
	}
	if got.CacheReadTokens != want.CacheReadTokens {
		t.Errorf("CacheReadTokens: got %d want %d", got.CacheReadTokens, want.CacheReadTokens)
	}
	if !ptrStrEqual(got.StopReason, want.StopReason) {
		t.Errorf("StopReason mismatch")
	}
	if got.ModelRequested != want.ModelRequested {
		t.Errorf("ModelRequested: got %q want %q", got.ModelRequested, want.ModelRequested)
	}
	if got.ModelResolved != want.ModelResolved {
		t.Errorf("ModelResolved: got %q want %q", got.ModelResolved, want.ModelResolved)
	}
	if !ptrStrEqual(got.ErrorText, want.ErrorText) {
		t.Errorf("ErrorText mismatch")
	}
	if !ptrStrEqual(got.SessionHeader, want.SessionHeader) {
		t.Errorf("SessionHeader mismatch")
	}
	if !ptrStrEqual(got.SessionID, want.SessionID) {
		t.Errorf("SessionID mismatch")
	}
	if !ptrFloatEqual(got.CostUSD, want.CostUSD) {
		t.Errorf("CostUSD mismatch")
	}
	if !ptrStrEqual(got.CostSource, want.CostSource) {
		t.Errorf("CostSource mismatch")
	}
	if !ptrInt64Equal(got.ReplayOf, want.ReplayOf) {
		t.Errorf("ReplayOf mismatch")
	}
	if !ptrStrEqual(got.ReplayEdits, want.ReplayEdits) {
		t.Errorf("ReplayEdits mismatch")
	}
	if got.PrefixHash != want.PrefixHash {
		t.Errorf("PrefixHash: got %q want %q", got.PrefixHash, want.PrefixHash)
	}
}

func TestRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	base, err := s.InsertRequest(ctx, fullRequest())
	if err != nil {
		t.Fatalf("InsertRequest(base): %v", err)
	}

	want := fullRequest()
	want.ReplayOf = &base
	id, err := s.InsertRequest(ctx, want)
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	if id == 0 {
		t.Fatalf("expected a nonzero id")
	}
	if want.ID != id {
		t.Errorf("InsertRequest did not set want.ID: got %d want %d", want.ID, id)
	}

	got, err := s.GetRequest(ctx, id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if got.ID != id {
		t.Errorf("ID: got %d want %d", got.ID, id)
	}
	assertRequestEqual(t, want, got)
}

func TestBodiesRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r := fullRequest()
	nonUTF8 := []byte{0xff, 0xfe, 0x00, 0x80, 0x81, 0xc0, 0xc1}
	big := make([]byte, 256*1024)
	for i := range big {
		big[i] = byte(i % 256)
	}
	r.ReqBody = append(append([]byte{}, nonUTF8...), big...)
	r.RespBody = big

	id, err := s.InsertRequest(ctx, r)
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	got, err := s.GetRequest(ctx, id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if !bytes.Equal(got.ReqBody, r.ReqBody) {
		t.Errorf("ReqBody: got %d bytes want %d bytes (mismatch)", len(got.ReqBody), len(r.ReqBody))
	}
	if !bytes.Equal(got.RespBody, r.RespBody) {
		t.Errorf("RespBody: got %d bytes want %d bytes (mismatch)", len(got.RespBody), len(r.RespBody))
	}
}

func TestNullHandling(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r := fullRequest()
	r.StopReason = nil
	r.SessionHeader = nil
	r.SessionID = nil
	r.CostUSD = nil
	r.CostSource = nil
	r.ErrorText = nil
	r.ReplayEdits = nil

	id, err := s.InsertRequest(ctx, r)
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	got, err := s.GetRequest(ctx, id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}

	if got.StopReason != nil {
		t.Errorf("StopReason: got %q want nil", *got.StopReason)
	}
	if got.SessionHeader != nil {
		t.Errorf("SessionHeader: got %q want nil", *got.SessionHeader)
	}
	if got.SessionID != nil {
		t.Errorf("SessionID: got %q want nil", *got.SessionID)
	}
	if got.CostUSD != nil {
		t.Errorf("CostUSD: got %v want nil", *got.CostUSD)
	}
	if got.CostSource != nil {
		t.Errorf("CostSource: got %q want nil", *got.CostSource)
	}
	if got.ErrorText != nil {
		t.Errorf("ErrorText: got %q want nil", *got.ErrorText)
	}
	if got.ReplayOf != nil {
		t.Errorf("ReplayOf: got %v want nil", *got.ReplayOf)
	}
	if got.ReplayEdits != nil {
		t.Errorf("ReplayEdits: got %q want nil", *got.ReplayEdits)
	}
}

func TestWALMode(t *testing.T) {
	s := newTestStore(t)
	var mode string
	if err := s.writer.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if strings.ToLower(mode) != "wal" {
		t.Errorf("journal_mode: got %q want %q", mode, "wal")
	}
}

func TestWarningsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, err := s.InsertRequest(ctx, fullRequest())
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}

	warnings := []Warning{
		{Kind: "dropped_param", Severity: "warn", Detail: "top_k dropped", CreatedAt: time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)},
		{Kind: "dropped_param", Severity: "warn", Detail: "top_p dropped", CreatedAt: time.Date(2026, 1, 2, 3, 0, 1, 0, time.UTC)},
		{Kind: "unsupported_block", Severity: "error", Detail: "document block", CreatedAt: time.Date(2026, 1, 2, 3, 0, 2, 0, time.UTC)},
	}
	if err := s.InsertWarnings(ctx, id, warnings); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}

	all, err := s.ListWarnings(ctx, Filter{})
	if err != nil {
		t.Fatalf("ListWarnings: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListWarnings: got %d want 3", len(all))
	}
	for _, w := range all {
		if w.RequestID != id {
			t.Errorf("warning %d: RequestID got %d want %d", w.ID, w.RequestID, id)
		}
	}

	filtered, err := s.ListWarnings(ctx, Filter{Kind: "unsupported_block"})
	if err != nil {
		t.Fatalf("ListWarnings filtered: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("ListWarnings filtered by kind: got %d want 1", len(filtered))
	}
	if filtered[0].Detail != "document block" {
		t.Errorf("filtered warning: got %q want %q", filtered[0].Detail, "document block")
	}
}

func TestFilterSince(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)

	old := fullRequest()
	old.StartedAt = now.Add(-2 * time.Hour)
	if _, err := s.InsertRequest(ctx, old); err != nil {
		t.Fatalf("InsertRequest(old): %v", err)
	}

	recent := fullRequest()
	recent.StartedAt = now.Add(-1 * time.Hour)
	if _, err := s.InsertRequest(ctx, recent); err != nil {
		t.Fatalf("InsertRequest(recent): %v", err)
	}

	got, err := s.ListRequests(ctx, Filter{Since: now.Add(-90 * time.Minute)})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d requests want 1", len(got))
	}
	if !got[0].StartedAt.Equal(recent.StartedAt) {
		t.Errorf("wrong request returned: StartedAt %v", got[0].StartedAt)
	}
}

func TestFilterSession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sessA, sessB := "session-a", "session-b"

	rA := fullRequest()
	rA.SessionID = &sessA
	if _, err := s.InsertRequest(ctx, rA); err != nil {
		t.Fatalf("InsertRequest(A): %v", err)
	}

	rB := fullRequest()
	rB.SessionID = &sessB
	if _, err := s.InsertRequest(ctx, rB); err != nil {
		t.Fatalf("InsertRequest(B): %v", err)
	}

	got, err := s.ListRequests(ctx, Filter{SessionID: sessA})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d requests want 1", len(got))
	}
	if got[0].SessionID == nil || *got[0].SessionID != sessA {
		t.Errorf("wrong session returned: %v", got[0].SessionID)
	}
}

func TestFilterOnlyWarned(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	warnedID, err := s.InsertRequest(ctx, fullRequest())
	if err != nil {
		t.Fatalf("InsertRequest(warned): %v", err)
	}
	if err := s.InsertWarnings(ctx, warnedID, []Warning{
		{Kind: "k", Severity: "warn", Detail: "m", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}

	if _, err := s.InsertRequest(ctx, fullRequest()); err != nil {
		t.Fatalf("InsertRequest(unwarned): %v", err)
	}

	got, err := s.ListRequests(ctx, Filter{OnlyWarned: true})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d requests want 1", len(got))
	}
	if got[0].ID != warnedID {
		t.Errorf("got id %d want %d", got[0].ID, warnedID)
	}
}

func TestStatsSummary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	r1 := fullRequest()
	r1.StartedAt = base
	r1.InputTokens, r1.OutputTokens = 100, 50
	r1.CacheCreationTokens, r1.CacheReadTokens = 5, 10
	c1 := 0.01
	r1.CostUSD = &c1
	r1.ErrorText = nil
	if _, err := s.InsertRequest(ctx, r1); err != nil {
		t.Fatalf("InsertRequest(r1): %v", err)
	}

	r2 := fullRequest()
	r2.StartedAt = base.Add(time.Minute)
	r2.InputTokens, r2.OutputTokens = 200, 75
	r2.CacheCreationTokens, r2.CacheReadTokens = 0, 0
	c2 := 0.02
	r2.CostUSD = &c2
	errText := "boom"
	r2.ErrorText = &errText
	if _, err := s.InsertRequest(ctx, r2); err != nil {
		t.Fatalf("InsertRequest(r2): %v", err)
	}

	sum, err := s.StatsSummary(ctx, base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("StatsSummary: %v", err)
	}
	if sum.RequestCount != 2 {
		t.Errorf("RequestCount: got %d want 2", sum.RequestCount)
	}
	if sum.ErrorCount != 1 {
		t.Errorf("ErrorCount: got %d want 1", sum.ErrorCount)
	}
	if sum.InputTokens != 300 {
		t.Errorf("InputTokens: got %d want 300", sum.InputTokens)
	}
	if sum.OutputTokens != 125 {
		t.Errorf("OutputTokens: got %d want 125", sum.OutputTokens)
	}
	if sum.CacheCreationTokens != 5 {
		t.Errorf("CacheCreationTokens: got %d want 5", sum.CacheCreationTokens)
	}
	if sum.CacheReadTokens != 10 {
		t.Errorf("CacheReadTokens: got %d want 10", sum.CacheReadTokens)
	}
	const wantCost = 0.03
	if diff := sum.CostUSDTotal - wantCost; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CostUSDTotal: got %v want %v", sum.CostUSDTotal, wantCost)
	}
}

func TestStatsByDay(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	day1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC)

	for _, d := range []time.Time{day1, day1, day2, day3, day3, day3} {
		r := fullRequest()
		r.StartedAt = d
		if _, err := s.InsertRequest(ctx, r); err != nil {
			t.Fatalf("InsertRequest: %v", err)
		}
	}

	stats, err := s.StatsByDay(ctx, day1.Add(-time.Hour))
	if err != nil {
		t.Fatalf("StatsByDay: %v", err)
	}
	if len(stats) != 3 {
		t.Fatalf("got %d buckets want 3", len(stats))
	}
	counts := map[string]int{}
	for _, d := range stats {
		counts[d.Day] = d.RequestCount
	}
	if counts["2026-01-01"] != 2 {
		t.Errorf("2026-01-01: got %d want 2", counts["2026-01-01"])
	}
	if counts["2026-01-02"] != 1 {
		t.Errorf("2026-01-02: got %d want 1", counts["2026-01-02"])
	}
	if counts["2026-01-03"] != 3 {
		t.Errorf("2026-01-03: got %d want 3", counts["2026-01-03"])
	}
}

// TestReaderDoesNotBlock is latency invariant 5: a reader query must
// complete while a writer transaction is open (uncommitted), because they
// use separate connections and WAL lets readers see a consistent snapshot
// without waiting on the writer lock.
func TestReaderDoesNotBlock(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.InsertRequest(ctx, fullRequest()); err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}

	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE requests SET status = status"); err != nil {
		t.Fatalf("hold write transaction: %v", err)
	}

	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.ListRequests(readCtx, Filter{})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListRequests while writer transaction open: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader query blocked on an open writer transaction")
	}
}

func TestPurgeOlderThan(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	old := fullRequest()
	old.StartedAt = cutoff.Add(-time.Hour)
	oldID, err := s.InsertRequest(ctx, old)
	if err != nil {
		t.Fatalf("InsertRequest(old): %v", err)
	}
	if err := s.InsertWarnings(ctx, oldID, []Warning{
		{Kind: "k", Severity: "warn", Detail: "m", CreatedAt: old.StartedAt},
	}); err != nil {
		t.Fatalf("InsertWarnings: %v", err)
	}

	newer := fullRequest()
	newer.StartedAt = cutoff.Add(time.Hour)
	newID, err := s.InsertRequest(ctx, newer)
	if err != nil {
		t.Fatalf("InsertRequest(newer): %v", err)
	}

	res, err := s.PurgeOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("PurgeOlderThan: %v", err)
	}
	if res.Deleted != 1 {
		t.Fatalf("PurgeOlderThan: removed %d want 1", res.Deleted)
	}

	if _, err := s.GetRequest(ctx, oldID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("old request should be gone: err=%v", err)
	}
	if _, err := s.GetRequest(ctx, newID); err != nil {
		t.Errorf("new request should remain: %v", err)
	}

	remaining, err := s.ListWarnings(ctx, Filter{})
	if err != nil {
		t.Fatalf("ListWarnings: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("warnings for the purged request should be gone, got %d", len(remaining))
	}
}

// TestPurgeOlderThanReconcilesSessionAggregates covers the gap left by
// UpsertSession's additive-only design: a purge that removes some of a
// session's requests must shrink that session's counters to match what
// remains, and a purge that removes all of them must remove the session row
// too — otherwise a session keeps reporting calls that no longer exist.
func TestPurgeOlderThanReconcilesSessionAggregates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	mkReq := func(sessionID string, startedAt time.Time, tokens int, cost *float64) int64 {
		r := fullRequest()
		r.SessionID = &sessionID
		r.StartedAt = startedAt
		r.InputTokens = tokens
		r.OutputTokens = tokens
		r.CostUSD = cost
		r.ModelResolved = "deepseek-flash"
		id, err := s.InsertRequest(ctx, r)
		if err != nil {
			t.Fatalf("InsertRequest: %v", err)
		}
		return id
	}
	upsertFor := func(sessionID string, id int64, startedAt time.Time, tokens int, cost *float64) {
		t.Helper()
		sess := &Session{
			ID: sessionID, FirstSeen: startedAt, LastSeen: startedAt,
			RequestCount: 1, TotalInputTokens: int64(tokens), TotalOutputTokens: int64(tokens),
			ModelSet: "deepseek-flash",
		}
		if cost != nil {
			sess.TotalCostUSD = *cost
			sess.PricedCount = 1
		} else {
			sess.UnpricedCount = 1
		}
		if err := s.UpsertSession(ctx, sess); err != nil {
			t.Fatalf("UpsertSession: %v", err)
		}
	}

	price := 0.5
	oldID := mkReq("partial", cutoff.Add(-time.Hour), 10, &price)
	upsertFor("partial", oldID, cutoff.Add(-time.Hour), 10, &price)
	newID := mkReq("partial", cutoff.Add(time.Hour), 20, nil)
	upsertFor("partial", newID, cutoff.Add(time.Hour), 20, nil)

	goneID := mkReq("emptied", cutoff.Add(-2*time.Hour), 5, nil)
	upsertFor("emptied", goneID, cutoff.Add(-2*time.Hour), 5, nil)

	if _, err := s.PurgeOlderThan(ctx, cutoff); err != nil {
		t.Fatalf("PurgeOlderThan: %v", err)
	}

	partial, err := s.GetSession(ctx, "partial")
	if err != nil {
		t.Fatalf("GetSession(partial): %v", err)
	}
	if partial.RequestCount != 1 {
		t.Errorf("partial.RequestCount = %d, want 1 (the purged request must not still be counted)", partial.RequestCount)
	}
	if partial.TotalInputTokens != 20 {
		t.Errorf("partial.TotalInputTokens = %d, want 20 (only the surviving request's)", partial.TotalInputTokens)
	}
	if partial.TotalCostUSD != 0 || partial.PricedCount != 0 || partial.UnpricedCount != 1 {
		t.Errorf("partial cost/priced/unpriced = %v/%d/%d, want 0/0/1 (the priced request was purged)",
			partial.TotalCostUSD, partial.PricedCount, partial.UnpricedCount)
	}

	if _, err := s.GetSession(ctx, "emptied"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("emptied session should be deleted once its only request is purged, got err=%v", err)
	}
}

// mkCostRow inserts a request with no session and the given cost_source,
// cost_usd and input_tokens — the fields purgeUnpricedWhere's predicate
// reads. Used by the PurgeUnpriced tests below, which don't care about
// session reconciliation (that is covered separately).
func mkCostRow(t *testing.T, s *Store, costSource *string, costUSD *float64, tokens int) int64 {
	t.Helper()
	r := fullRequest()
	r.SessionID = nil
	r.SessionHeader = nil
	r.CostSource = costSource
	r.CostUSD = costUSD
	r.InputTokens = tokens
	r.OutputTokens = 0
	r.CacheCreationTokens = 0
	r.CacheReadTokens = 0
	id, err := s.InsertRequest(context.Background(), r)
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	return id
}

// TestPurgeUnpricedDeletesOnlyTheD5Predicate is the discriminating test for
// the whole predicate: it seeds every class the COALESCE and the
// positive-token conjunct are meant to tell apart, and only one of them
// must be deleted. It fails if the token conjunct is dropped (the
// zero-token unpriced row would also be deleted) and it fails if the
// COALESCE is dropped for a bare NULL test (the unknown-model row would be
// swept in).
func TestPurgeUnpricedDeletesOnlyTheD5Predicate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	configured, unknown, unpriced := "configured", "unknown-model", "unpriced"
	cost := 0.5

	pricedID := mkCostRow(t, s, &configured, &cost, 100)
	unknownID := mkCostRow(t, s, &unknown, nil, 100)
	unpricedZeroTokensID := mkCostRow(t, s, &unpriced, nil, 0)
	unpricedWithTokensID := mkCostRow(t, s, &unpriced, nil, 100)
	nullSourceWithTokensID := mkCostRow(t, s, nil, nil, 50) // COALESCE folds this into "unpriced"

	res, err := s.PurgeUnpriced(ctx)
	if err != nil {
		t.Fatalf("PurgeUnpriced: %v", err)
	}
	if res.Deleted != 2 {
		t.Errorf("Deleted = %d, want 2 (only the two unpriced-with-tokens rows)", res.Deleted)
	}

	for _, id := range []int64{pricedID, unknownID, unpricedZeroTokensID} {
		if _, err := s.GetRequest(ctx, id); err != nil {
			t.Errorf("request %d should survive PurgeUnpriced: %v", id, err)
		}
	}
	for _, id := range []int64{unpricedWithTokensID, nullSourceWithTokensID} {
		if _, err := s.GetRequest(ctx, id); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("request %d should have been purged: err=%v", id, err)
		}
	}
}

// TestPurgeUnpricedEmptyMatchTouchesNothing pins that a purge over an empty
// match returns a zero PurgeResult and reconciles no session row — the
// baseline every other assertion in this file assumes.
func TestPurgeUnpricedEmptyMatchTouchesNothing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	configured := "configured"
	cost := 0.5
	mkCostRow(t, s, &configured, &cost, 100)

	res, err := s.PurgeUnpriced(ctx)
	if err != nil {
		t.Fatalf("PurgeUnpriced: %v", err)
	}
	if res != (PurgeResult{}) {
		t.Errorf("PurgeUnpriced over an empty match = %+v, want the zero value", res)
	}
}

// TestPreviewAgreesWithPurgeByConstruction asserts, for both predicate
// pairs, that the preview's count and bytes equal what the corresponding
// purge then actually deletes — the "agree by construction" design claim
// (D5/D6), tested rather than merely commented.
func TestPreviewAgreesWithPurgeByConstruction(t *testing.T) {
	ctx := context.Background()
	body := bytes.Repeat([]byte("x"), 100)

	t.Run("older-than", func(t *testing.T) {
		s := newTestStore(t)
		cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		configured := "configured"
		cost := 0.5

		mk := func(startedAt time.Time) {
			r := fullRequest()
			r.SessionID, r.SessionHeader = nil, nil
			r.StartedAt = startedAt
			r.CostSource, r.CostUSD = &configured, &cost
			r.ReqBody, r.RespBody = body, body
			if _, err := s.InsertRequest(ctx, r); err != nil {
				t.Fatalf("InsertRequest: %v", err)
			}
		}
		mk(cutoff.Add(-2 * time.Hour))
		mk(cutoff.Add(-time.Hour))
		mk(cutoff.Add(time.Hour)) // survives: after cutoff

		count, _, _, err := s.CountPurgeable(ctx, cutoff)
		if err != nil {
			t.Fatalf("CountPurgeable: %v", err)
		}
		previewBytes, err := s.PurgeableBytes(ctx, cutoff)
		if err != nil {
			t.Fatalf("PurgeableBytes: %v", err)
		}

		res, err := s.PurgeOlderThan(ctx, cutoff)
		if err != nil {
			t.Fatalf("PurgeOlderThan: %v", err)
		}
		if int64(count) != res.Deleted {
			t.Errorf("preview count = %d, delete count = %d, want equal", count, res.Deleted)
		}
		wantBytes := int64(count) * int64(len(body)*2)
		if previewBytes != wantBytes {
			t.Errorf("preview bytes = %d, want %d (count * body size)", previewBytes, wantBytes)
		}
	})

	t.Run("unpriced", func(t *testing.T) {
		s := newTestStore(t)
		unpriced, configured := "unpriced", "configured"
		cost := 0.5

		mk := func(costSource *string, costUSD *float64, tokens int) {
			r := fullRequest()
			r.SessionID, r.SessionHeader = nil, nil
			r.CostSource, r.CostUSD = costSource, costUSD
			r.InputTokens, r.OutputTokens, r.CacheCreationTokens, r.CacheReadTokens = tokens, 0, 0, 0
			r.ReqBody, r.RespBody = body, body
			if _, err := s.InsertRequest(ctx, r); err != nil {
				t.Fatalf("InsertRequest: %v", err)
			}
		}
		mk(&unpriced, nil, 100)
		mk(&unpriced, nil, 50)
		mk(&configured, &cost, 100) // survives: priced

		count, err := s.CountUnpriced(ctx)
		if err != nil {
			t.Fatalf("CountUnpriced: %v", err)
		}
		previewBytes, err := s.UnpricedBytes(ctx)
		if err != nil {
			t.Fatalf("UnpricedBytes: %v", err)
		}

		res, err := s.PurgeUnpriced(ctx)
		if err != nil {
			t.Fatalf("PurgeUnpriced: %v", err)
		}
		if int64(count) != res.Deleted {
			t.Errorf("preview count = %d, delete count = %d, want equal", count, res.Deleted)
		}
		wantBytes := int64(count) * int64(len(body)*2)
		if previewBytes != wantBytes {
			t.Errorf("preview bytes = %d, want %d (count * body size)", previewBytes, wantBytes)
		}
	})
}

// TestPreviewsOverEmptyEligibleSetsAreZeroNotError covers the state a
// days-larger-than-the-data's-age or a freshly-installed database is in:
// zero eligible rows, reported as a clean zero rather than an error, and
// oldest/newest nil (not a zero time.Time).
func TestPreviewsOverEmptyEligibleSetsAreZeroNotError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	configured := "configured"
	cost := 0.5
	mkCostRow(t, s, &configured, &cost, 100)

	// A cutoff before every row: nothing is eligible for older-than.
	cutoff := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	count, oldest, newest, err := s.CountPurgeable(ctx, cutoff)
	if err != nil {
		t.Fatalf("CountPurgeable: %v", err)
	}
	if count != 0 || oldest != nil || newest != nil {
		t.Errorf("CountPurgeable = (%d, %v, %v), want (0, nil, nil)", count, oldest, newest)
	}
	purgeableBytes, err := s.PurgeableBytes(ctx, cutoff)
	if err != nil {
		t.Fatalf("PurgeableBytes: %v", err)
	}
	if purgeableBytes != 0 {
		t.Errorf("PurgeableBytes = %d, want 0", purgeableBytes)
	}

	// No unpriced rows at all (the seeded row is priced).
	unprCount, err := s.CountUnpriced(ctx)
	if err != nil {
		t.Fatalf("CountUnpriced: %v", err)
	}
	if unprCount != 0 {
		t.Errorf("CountUnpriced = %d, want 0", unprCount)
	}
	unprBytes, err := s.UnpricedBytes(ctx)
	if err != nil {
		t.Fatalf("UnpricedBytes: %v", err)
	}
	if unprBytes != 0 {
		t.Errorf("UnpricedBytes = %d, want 0", unprBytes)
	}
}

// TestPurgePreviewPairsAreNotInterchangeable proves the older-than and D5
// preview pairs are wired to different predicates: with both an aged set
// and an unpriced set present (and disjoint from each other), the two
// counts must differ.
func TestPurgePreviewPairsAreNotInterchangeable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	configured, unpriced := "configured", "unpriced"
	cost := 0.5

	// Two aged, priced rows (eligible for older-than, not for unpriced).
	for i := 0; i < 2; i++ {
		r := fullRequest()
		r.SessionID, r.SessionHeader = nil, nil
		r.StartedAt = cutoff.Add(-time.Hour)
		r.CostSource, r.CostUSD = &configured, &cost
		if _, err := s.InsertRequest(ctx, r); err != nil {
			t.Fatalf("InsertRequest: %v", err)
		}
	}
	// One recent, unpriced-with-tokens row (eligible for unpriced, not for
	// older-than).
	r := fullRequest()
	r.SessionID, r.SessionHeader = nil, nil
	r.StartedAt = cutoff.Add(time.Hour)
	r.CostSource, r.CostUSD = &unpriced, nil
	r.InputTokens, r.OutputTokens, r.CacheCreationTokens, r.CacheReadTokens = 10, 0, 0, 0
	if _, err := s.InsertRequest(ctx, r); err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}

	agedCount, _, _, err := s.CountPurgeable(ctx, cutoff)
	if err != nil {
		t.Fatalf("CountPurgeable: %v", err)
	}
	unprCount, err := s.CountUnpriced(ctx)
	if err != nil {
		t.Fatalf("CountUnpriced: %v", err)
	}
	if agedCount != 2 {
		t.Errorf("CountPurgeable = %d, want 2", agedCount)
	}
	if unprCount != 1 {
		t.Errorf("CountUnpriced = %d, want 1", unprCount)
	}
	if agedCount == unprCount {
		t.Fatalf("CountPurgeable and CountUnpriced both = %d — the two pairs must not be interchangeable", agedCount)
	}
}

// TestConcurrentPurgeAndInsertDoNotRace runs a purge and an InsertRequest
// concurrently under -race and asserts every row is accounted for — present
// or deleted, none lost or duplicated.
//
// This is NOT a race detector for the two calls themselves: s.writer is
// capped at one open connection (SetMaxOpenConns(1)), so database/sql
// serializes them before any Go-memory race can exist, and *sql.DB is
// concurrency-safe by contract. The serialization guarantee is
// SetMaxOpenConns(1)'s, not this test's; this test pins the no-lost-row
// behaviour that guarantee is meant to produce.
func TestConcurrentPurgeAndInsertDoNotRace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	configured := "configured"
	cost := 0.5

	const seeded = 20
	for i := 0; i < seeded; i++ {
		r := fullRequest()
		r.SessionID, r.SessionHeader = nil, nil
		r.StartedAt = cutoff.Add(-time.Hour)
		r.CostSource, r.CostUSD = &configured, &cost
		if _, err := s.InsertRequest(ctx, r); err != nil {
			t.Fatalf("seed InsertRequest: %v", err)
		}
	}

	var wg sync.WaitGroup
	var purgeErr, insertErr error
	var insertedID int64

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, purgeErr = s.PurgeOlderThan(ctx, cutoff)
	}()
	go func() {
		defer wg.Done()
		r := fullRequest()
		r.SessionID, r.SessionHeader = nil, nil
		r.StartedAt = cutoff.Add(time.Hour) // survives any purge over this cutoff
		insertedID, insertErr = s.InsertRequest(ctx, r)
	}()
	wg.Wait()

	if purgeErr != nil {
		t.Errorf("PurgeOlderThan: %v", purgeErr)
	}
	if insertErr != nil {
		t.Errorf("InsertRequest: %v", insertErr)
	}
	if _, err := s.GetRequest(ctx, insertedID); err != nil {
		t.Errorf("concurrently inserted row is missing: %v", err)
	}

	remaining, err := s.ListRequests(ctx, Filter{Limit: 1000})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	// Every seeded row was eligible for the purge, so only the concurrently
	// inserted survivor should remain — never more (a duplicate) and never
	// less (a second row lost).
	if len(remaining) != 1 {
		t.Errorf("remaining rows = %d, want exactly 1 (the concurrently inserted survivor)", len(remaining))
	}
}

// TestPurgeUnpricedReconciliationIsScopedToTouchedSessions is the test that
// catches a partial purgeWhere extraction: if only the requests DELETE were
// generalized to the D5 predicate while the affected-session query kept
// asking its own started_at question, Deleted would still be correct but
// SessionsReconciled would count every session in the table, and an
// untouched session's aggregate columns could be silently rewritten.
//
// Two disjoint session sets are seeded — one unpriced-with-tokens, one
// priced — so PurgeUnpriced must reconcile only the first, leaving the
// second's aggregate row byte-for-byte as UpsertSession left it.
func TestPurgeUnpricedReconciliationIsScopedToTouchedSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	unpriced, configured := "unpriced", "configured"
	cost := 0.5

	mkReq := func(sessionID string, costSource *string, costUSD *float64, tokens int) int64 {
		r := fullRequest()
		sid := sessionID
		r.SessionID = &sid
		r.SessionHeader = &sid
		r.CostSource, r.CostUSD = costSource, costUSD
		r.InputTokens, r.OutputTokens, r.CacheCreationTokens, r.CacheReadTokens = tokens, 0, 0, 0
		id, err := s.InsertRequest(ctx, r)
		if err != nil {
			t.Fatalf("InsertRequest: %v", err)
		}
		return id
	}
	upsertFor := func(sessionID string, startedAt time.Time, tokens int, cost *float64) {
		t.Helper()
		sess := &Session{
			ID: sessionID, FirstSeen: startedAt, LastSeen: startedAt,
			RequestCount: 1, TotalInputTokens: int64(tokens), TotalOutputTokens: int64(tokens),
			ModelSet: "deepseek-flash",
		}
		if cost != nil {
			sess.TotalCostUSD = *cost
			sess.PricedCount = 1
		} else {
			sess.UnpricedCount = 1
		}
		if err := s.UpsertSession(ctx, sess); err != nil {
			t.Fatalf("UpsertSession: %v", err)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	mkReq("unpriced-session", &unpriced, nil, 100)
	upsertFor("unpriced-session", now, 100, nil)

	mkReq("priced-session", &configured, &cost, 100)
	upsertFor("priced-session", now, 100, &cost)

	before, err := s.GetSession(ctx, "priced-session")
	if err != nil {
		t.Fatalf("GetSession(priced-session) before: %v", err)
	}

	res, err := s.PurgeUnpriced(ctx)
	if err != nil {
		t.Fatalf("PurgeUnpriced: %v", err)
	}
	if res.SessionsReconciled != 1 {
		t.Errorf("SessionsReconciled = %d, want 1 (only unpriced-session)", res.SessionsReconciled)
	}

	if _, err := s.GetSession(ctx, "unpriced-session"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unpriced-session should be gone (its only request was purged): err=%v", err)
	}

	after, err := s.GetSession(ctx, "priced-session")
	if err != nil {
		t.Fatalf("GetSession(priced-session) after: %v", err)
	}
	if *before != *after {
		t.Errorf("priced-session was touched by an unpriced-only purge:\n before %+v\n after  %+v", *before, *after)
	}
}

// TestRedactCheckDetectsLeak proves RedactCheck actually scans: given a
// header blob that (unlike anything internal/proxy/redact.go would ever
// produce) carries a live x-api-key value, RedactCheck must catch it and
// name the value.
func TestRedactCheckDetectsLeak(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sentinel := "TESTSENTINEL-do-not-leak-1234567890"
	leaked := fullRequest()
	leaked.ReqHeaders = fmt.Sprintf(`{"X-Api-Key":["%s"],"Content-Type":["application/json"]}`, sentinel)
	if _, err := s.InsertRequest(ctx, leaked); err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}

	err := s.RedactCheck(ctx)
	if err == nil {
		t.Fatal("RedactCheck did not catch a reachable x-api-key value")
	}
	if !strings.Contains(err.Error(), sentinel) {
		t.Errorf("RedactCheck error does not name the leaked value: %v", err)
	}
}

// TestRedactCheckDetectsLeakInOtherSensitiveHeaders proves RedactCheck's
// scan is not x-api-key-only: Authorization and Cookie are equally
// sensitive and must be caught too.
func TestRedactCheckDetectsLeakInOtherSensitiveHeaders(t *testing.T) {
	for _, name := range []string{"Authorization", "Cookie"} {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()

			sentinel := "TESTSENTINEL-do-not-leak-" + name
			leaked := fullRequest()
			leaked.RespHeaders = fmt.Sprintf(`{%q:["%s"],"Content-Type":["application/json"]}`, name, sentinel)
			if _, err := s.InsertRequest(ctx, leaked); err != nil {
				t.Fatalf("InsertRequest: %v", err)
			}

			err := s.RedactCheck(ctx)
			if err == nil {
				t.Fatalf("RedactCheck did not catch a reachable %s value", name)
			}
			if !strings.Contains(err.Error(), sentinel) {
				t.Errorf("RedactCheck error does not name the leaked value: %v", err)
			}
		})
	}
}

// TestRedactionSweepRawBytes exercises the normal flow: requests are
// inserted with headers already redacted (as internal/proxy/redact.go does
// before anything reaches the sink), and asserts the sentinel — standing in
// for a real secret — never appears anywhere in the raw database file
// bytes, and that RedactCheck passes clean.
func TestRedactionSweepRawBytes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	ctx := context.Background()
	sentinel := "TESTSENTINEL-do-not-leak-1234567890"
	for i := 0; i < 3; i++ {
		r := fullRequest()
		// The real value never reaches store — only the redaction
		// marker does, exactly as the proxy's redactHeaders produces.
		r.ReqHeaders = `{"X-Api-Key":["[redacted]"],"Content-Type":["application/json"]}`
		if _, err := s.InsertRequest(ctx, r); err != nil {
			t.Fatalf("InsertRequest: %v", err)
		}
	}

	if err := s.RedactCheck(ctx); err != nil {
		t.Errorf("RedactCheck on properly-redacted headers: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db file: %v", err)
	}
	if bytes.Contains(raw, []byte(sentinel)) {
		t.Errorf("sentinel value found in raw db file bytes")
	}
}

func TestIdempotentSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lens.db")
	ctx := context.Background()

	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s1.InsertRequest(ctx, fullRequest()); err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close first Store: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	got, err := s2.ListRequests(ctx, Filter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d requests want 1 (reapplying schema must not duplicate data)", len(got))
	}
}

// TestConcurrentInsertsSerialize is the bead's "concurrent writers guarded"
// case: InsertRequest from many goroutines at once must serialize (via the
// writer's SetMaxOpenConns(1)) rather than drop or corrupt rows.
func TestConcurrentInsertsSerialize(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.InsertRequest(ctx, fullRequest()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent InsertRequest failed: %v", err)
	}

	got, err := s.ListRequests(ctx, Filter{Limit: n + 10})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(got) != n {
		t.Fatalf("got %d requests want %d (concurrent writers must serialize, not drop rows)", len(got), n)
	}
	seen := make(map[int64]bool, n)
	for _, r := range got {
		if seen[r.ID] {
			t.Fatalf("duplicate id %d — corruption under concurrent writers", r.ID)
		}
		seen[r.ID] = true
	}
}

// TestInsertRequestsRollsBackOnFailure exercises the "all-or-nothing" batch
// guarantee documented on InsertRequests: a batch that fails partway through
// must leave no row behind, not just the failing one.
func TestInsertRequestsRollsBackOnFailure(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.InsertRequest(ctx, fullRequest()); err != nil {
		t.Fatalf("baseline InsertRequest: %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	batch := []*Request{fullRequest(), fullRequest(), fullRequest()}
	if err := s.InsertRequests(canceled, batch); err == nil {
		t.Fatal("InsertRequests with a canceled context: got nil error, want one")
	}

	got, err := s.ListRequests(ctx, Filter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d requests after failed batch, want 1 (only the baseline row — batch must roll back atomically)", len(got))
	}
}

// TestListRequestsPerformanceAndLimits covers the bead's 10,000-row
// performance requirement and the Filter{Limit: 0} cap in one pass, since
// both need the same fixture.
func TestListRequestsPerformanceAndLimits(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	const total = 10000
	for i := 0; i < total; i++ {
		r := fullRequest()
		r.StartedAt = base.Add(time.Duration(i) * time.Second)
		if _, err := s.InsertRequest(ctx, r); err != nil {
			t.Fatalf("InsertRequest[%d]: %v", i, err)
		}
	}

	got, err := s.ListRequests(ctx, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRequests{Limit:10}: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d requests want 10", len(got))
	}

	unlimited, err := s.ListRequests(ctx, Filter{Limit: 0})
	if err != nil {
		t.Fatalf("ListRequests{Limit:0}: %v", err)
	}
	if len(unlimited) != DefaultLimit {
		t.Fatalf("Filter{Limit:0}: got %d rows want capped at %d, not unbounded", len(unlimited), DefaultLimit)
	}

	budget := 100 * time.Millisecond
	if raceEnabled {
		// The race detector instruments every memory access; see
		// race_on_test.go for why the bound is relaxed here.
		budget = 5 * time.Second
	}
	start := time.Now()
	if _, err := s.StatsSummary(ctx, base.Add(-time.Hour)); err != nil {
		t.Fatalf("StatsSummary: %v", err)
	}
	if elapsed := time.Since(start); elapsed > budget {
		t.Errorf("StatsSummary took %v over 10,000 rows, want under %v", elapsed, budget)
	}
}
