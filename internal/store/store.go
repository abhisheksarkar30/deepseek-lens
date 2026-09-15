// Package store is the SQLite-backed persistence layer: everything the
// consumer (br-GI-1-07) writes and everything the dashboard/API
// (br-GI-1-08 onward) reads. See CLAUDE.md's "Architecture essentials" for
// the single-writer discipline this package assumes: exactly one goroutine
// ever calls a writer method. Open's writer connection has
// SetMaxOpenConns(1), which is what turns a violation of that discipline
// into serialized queuing rather than silent corruption — belt and braces,
// not the primary guarantee.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// DefaultLimit is what Filter.Limit: 0 means for ListRequests and
// ListWarnings — a sane cap, never an unbounded scan of the table.
const DefaultLimit = 1000

// pragmaDSN is appended to the database file path for both the writer and
// reader connections: WAL so reader queries never block on an open writer
// transaction, a busy timeout so momentary contention retries instead of
// erroring, and NORMAL synchronous (safe under WAL — only a power loss, not
// a process crash, can lose the most recent commit).
const pragmaDSN = "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"

const redactedHeaderValue = "[redacted]"

// requestColumns is the fixed column order shared by every SELECT against
// requests; scanRequest's Scan call must list destinations in this same
// order.
const requestColumns = `id, started_at, ttfb_ns, duration_ns, method, path, remote_addr, status,
	req_headers, resp_headers, req_body, resp_body,
	input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
	stop_reason, model_requested, model_resolved, error_text,
	session_header, session_id, cost_usd, cost_source,
	replay_of, replay_edits, prefix_hash`

// insertColumns is requestColumns minus id (AUTOINCREMENT). insertRequestSQL
// is built from it so the column list and the "?" placeholder count can
// never drift apart.
var insertColumns = []string{
	"started_at", "ttfb_ns", "duration_ns", "method", "path", "remote_addr", "status",
	"req_headers", "resp_headers", "req_body", "resp_body",
	"input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens",
	"stop_reason", "model_requested", "model_resolved", "error_text",
	"session_header", "session_id", "cost_usd", "cost_source",
	"replay_of", "replay_edits", "prefix_hash",
}

var insertRequestSQL = fmt.Sprintf(
	"INSERT INTO requests (%s) VALUES (%s)",
	strings.Join(insertColumns, ", "),
	strings.TrimSuffix(strings.Repeat("?, ", len(insertColumns)), ", "),
)

// Store is the SQLite-backed persistence layer, opened with one writer
// connection (capped to a single open connection, see the package doc) and
// one reader connection pool used only for reads.
type Store struct {
	writer *sql.DB
	reader *sql.DB
}

// Open creates dbPath's parent directory (0700 if absent), opens the writer
// and reader connections, and applies schema.sql (idempotent —
// CREATE TABLE IF NOT EXISTS, safe to call on an existing database). The
// database file is best-effort chmod'd to 0600 after creation.
//
// Directory/file permission calls are made unconditionally but are
// meaningful only on POSIX systems — Windows' ACL model doesn't honor Unix
// mode bits, so this is a no-op there rather than a failure.
func Open(dbPath string) (*Store, error) {
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("store: create db dir %s: %w", dir, err)
		}
		_ = os.Chmod(dir, 0o700)
	}

	dsn := dbPath + pragmaDSN

	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	writer.SetMaxOpenConns(1)

	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		writer.Close()
		return nil, fmt.Errorf("store: open reader: %w", err)
	}
	reader.SetMaxOpenConns(4)

	if _, err := writer.ExecContext(context.Background(), schemaSQL); err != nil {
		writer.Close()
		reader.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}

	_ = os.Chmod(dbPath, 0o600)

	return &Store{writer: writer, reader: reader}, nil
}

// Close closes both connections.
func (s *Store) Close() error {
	err := s.writer.Close()
	if rerr := s.reader.Close(); err == nil {
		err = rerr
	}
	return err
}

// ptrOrNil converts a possibly-nil pointer into the interface{} form
// database/sql expects: nil itself for a NULL column, or the pointed-to
// value. This is the single place nil-vs-zero-value discipline is enforced
// on the write side.
func ptrOrNil[T any](p *T) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, letting
// scanRequest/scanSession/scanWarning serve GetX and ListX alike.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

// InsertRequest inserts r and sets r.ID to the assigned row id. Writer only
// — see the package doc.
func (s *Store) InsertRequest(ctx context.Context, r *Request) (int64, error) {
	res, err := s.writer.ExecContext(ctx, insertRequestSQL,
		r.StartedAt.UnixNano(), r.TTFB.Nanoseconds(), r.Duration.Nanoseconds(),
		r.Method, r.Path, r.RemoteAddr, r.Status,
		r.ReqHeaders, r.RespHeaders, r.ReqBody, r.RespBody,
		r.InputTokens, r.OutputTokens, r.CacheCreationTokens, r.CacheReadTokens,
		ptrOrNil(r.StopReason), r.ModelRequested, r.ModelResolved, ptrOrNil(r.ErrorText),
		ptrOrNil(r.SessionHeader), ptrOrNil(r.SessionID), ptrOrNil(r.CostUSD), ptrOrNil(r.CostSource),
		ptrOrNil(r.ReplayOf), ptrOrNil(r.ReplayEdits), r.PrefixHash,
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert request: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: insert request: last insert id: %w", err)
	}
	r.ID = id
	return id, nil
}

// InsertWarnings inserts warnings for reqID as one transaction. Writer only.
func (s *Store) InsertWarnings(ctx context.Context, reqID int64, warnings []Warning) error {
	if len(warnings) == 0 {
		return nil
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: insert warnings: begin: %w", err)
	}
	defer tx.Rollback()

	for _, w := range warnings {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO warnings (request_id, kind, severity, detail, path, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			reqID, w.Kind, w.Severity, w.Detail, w.Path, w.CreatedAt.UnixNano(),
		); err != nil {
			return fmt.Errorf("store: insert warnings: %w", err)
		}
	}
	return tx.Commit()
}

// scanRequest reads one requests row in requestColumns' order.
func scanRequest(sc rowScanner) (*Request, error) {
	var r Request
	var startedAt, ttfbNs, durationNs int64
	var stopReason, errorText, sessionHeader, sessionID, costSource, replayEdits sql.NullString
	var costUSD sql.NullFloat64
	var replayOf sql.NullInt64

	err := sc.Scan(
		&r.ID, &startedAt, &ttfbNs, &durationNs, &r.Method, &r.Path, &r.RemoteAddr, &r.Status,
		&r.ReqHeaders, &r.RespHeaders, &r.ReqBody, &r.RespBody,
		&r.InputTokens, &r.OutputTokens, &r.CacheCreationTokens, &r.CacheReadTokens,
		&stopReason, &r.ModelRequested, &r.ModelResolved, &errorText,
		&sessionHeader, &sessionID, &costUSD, &costSource,
		&replayOf, &replayEdits, &r.PrefixHash,
	)
	if err != nil {
		return nil, err
	}

	r.StartedAt = time.Unix(0, startedAt).UTC()
	r.TTFB = time.Duration(ttfbNs)
	r.Duration = time.Duration(durationNs)
	r.StopReason = nullStringPtr(stopReason)
	r.ErrorText = nullStringPtr(errorText)
	r.SessionHeader = nullStringPtr(sessionHeader)
	r.SessionID = nullStringPtr(sessionID)
	r.CostSource = nullStringPtr(costSource)
	r.ReplayEdits = nullStringPtr(replayEdits)
	if costUSD.Valid {
		v := costUSD.Float64
		r.CostUSD = &v
	}
	if replayOf.Valid {
		v := replayOf.Int64
		r.ReplayOf = &v
	}
	return &r, nil
}

func nullStringPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

// GetRequest returns the request with id, or sql.ErrNoRows if it does not
// exist.
func (s *Store) GetRequest(ctx context.Context, id int64) (*Request, error) {
	row := s.reader.QueryRowContext(ctx, "SELECT "+requestColumns+" FROM requests WHERE id = ?", id)
	r, err := scanRequest(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: get request: %w", err)
	}
	return r, nil
}

// ListRequests returns requests matching f, newest first. f.Limit <= 0 is
// capped at DefaultLimit — never unbounded.
func (s *Store) ListRequests(ctx context.Context, f Filter) ([]*Request, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	var where []string
	var args []interface{}
	if !f.Since.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, f.Since.UnixNano())
	}
	if f.SessionID != "" {
		where = append(where, "session_id = ?")
		args = append(args, f.SessionID)
	}
	if f.Model != "" {
		where = append(where, "(model_requested = ? OR model_resolved = ?)")
		args = append(args, f.Model, f.Model)
	}
	if f.OnlyErrors {
		where = append(where, "error_text IS NOT NULL")
	}
	if f.OnlyWarned {
		where = append(where, "id IN (SELECT DISTINCT request_id FROM warnings)")
	}

	query := "SELECT " + requestColumns + " FROM requests"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY started_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list requests: %w", err)
	}
	defer rows.Close()

	var out []*Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list requests: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StatsSummary aggregates counts, token totals, cost, and warning count
// over requests started at or after since, plus P50/P95 duration.
func (s *Store) StatsSummary(ctx context.Context, since time.Time) (*Summary, error) {
	sum := &Summary{}
	row := s.reader.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN error_text IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END), 0)
		FROM requests WHERE started_at >= ?`, since.UnixNano())
	if err := row.Scan(&sum.RequestCount, &sum.ErrorCount, &sum.InputTokens, &sum.OutputTokens,
		&sum.CacheCreationTokens, &sum.CacheReadTokens, &sum.CostUSDTotal, &sum.UnpricedCount); err != nil {
		return nil, fmt.Errorf("store: stats summary: %w", err)
	}

	wrow := s.reader.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM warnings w JOIN requests r ON r.id = w.request_id WHERE r.started_at >= ?`,
		since.UnixNano())
	if err := wrow.Scan(&sum.WarningCount); err != nil {
		return nil, fmt.Errorf("store: stats summary: warnings: %w", err)
	}

	p50, p95, err := s.durationPercentiles(ctx, since)
	if err != nil {
		return nil, err
	}
	sum.DurationP50Ms = p50
	sum.DurationP95Ms = p95

	return sum, nil
}

// durationPercentiles returns the P50/P95 request duration in milliseconds
// over requests started at or after since, using the nearest-rank method.
func (s *Store) durationPercentiles(ctx context.Context, since time.Time) (p50, p95 float64, err error) {
	rows, err := s.reader.QueryContext(ctx,
		"SELECT duration_ns FROM requests WHERE started_at >= ? ORDER BY duration_ns", since.UnixNano())
	if err != nil {
		return 0, 0, fmt.Errorf("store: duration percentiles: %w", err)
	}
	defer rows.Close()

	var durations []int64
	for rows.Next() {
		var d int64
		if err := rows.Scan(&d); err != nil {
			return 0, 0, fmt.Errorf("store: duration percentiles: %w", err)
		}
		durations = append(durations, d)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("store: duration percentiles: %w", err)
	}
	if len(durations) == 0 {
		return 0, 0, nil
	}
	return percentileMs(durations, 0.50), percentileMs(durations, 0.95), nil
}

// percentileMs returns the p-th percentile (nearest-rank, p in [0,1]) of
// ascending, already-sorted nanosecond durations, in milliseconds.
func percentileMs(sortedNs []int64, p float64) float64 {
	idx := int(math.Ceil(p*float64(len(sortedNs)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sortedNs) {
		idx = len(sortedNs) - 1
	}
	return float64(sortedNs[idx]) / float64(time.Millisecond)
}

// StatsByModel aggregates request count, token totals, and cost per model
// (model_resolved when known, else model_requested), for dashboard charts.
func (s *Store) StatsByModel(ctx context.Context, since time.Time) ([]ModelStat, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT CASE WHEN model_resolved <> '' THEN model_resolved ELSE model_requested END,
			COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END), 0)
		FROM requests
		WHERE started_at >= ?
		GROUP BY 1
		ORDER BY 2 DESC`, since.UnixNano())
	if err != nil {
		return nil, fmt.Errorf("store: stats by model: %w", err)
	}
	defer rows.Close()

	var out []ModelStat
	for rows.Next() {
		var m ModelStat
		if err := rows.Scan(&m.Model, &m.RequestCount, &m.InputTokens, &m.OutputTokens, &m.CostUSDTotal, &m.UnpricedCount); err != nil {
			return nil, fmt.Errorf("store: stats by model: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// StatsByDay aggregates request count, token totals, and cost per UTC
// calendar day, for dashboard charts.
func (s *Store) StatsByDay(ctx context.Context, since time.Time) ([]DayStat, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT strftime('%Y-%m-%d', started_at / 1000000000, 'unixepoch'),
			COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END), 0)
		FROM requests
		WHERE started_at >= ?
		GROUP BY 1
		ORDER BY 1`, since.UnixNano())
	if err != nil {
		return nil, fmt.Errorf("store: stats by day: %w", err)
	}
	defer rows.Close()

	var out []DayStat
	for rows.Next() {
		var d DayStat
		if err := rows.Scan(&d.Day, &d.RequestCount, &d.InputTokens, &d.OutputTokens, &d.CostUSDTotal, &d.UnpricedCount); err != nil {
			return nil, fmt.Errorf("store: stats by day: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// StatsByCostSource groups the window's requests by cost_source: the
// breakdown behind every surface that must not present a partial total as a
// whole one (br-GI-1-11). A NULL source — a row written before this bead, or
// by a writer with no price table — is reported as "unpriced", because those
// are exactly the rows with no cost_usd either; folding them in keeps the
// label set closed to the four real sources.
func (s *Store) StatsByCostSource(ctx context.Context, since time.Time) ([]CostSourceStat, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT COALESCE(cost_source, 'unpriced'),
			COUNT(*), COALESCE(SUM(cost_usd), 0)
		FROM requests
		WHERE started_at >= ?
		GROUP BY 1
		ORDER BY 2 DESC`, since.UnixNano())
	if err != nil {
		return nil, fmt.Errorf("store: stats by cost source: %w", err)
	}
	defer rows.Close()

	var out []CostSourceStat
	for rows.Next() {
		var c CostSourceStat
		if err := rows.Scan(&c.Source, &c.RequestCount, &c.CostUSDTotal); err != nil {
			return nil, fmt.Errorf("store: stats by cost source: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// sessionColumns is the fixed column order shared by every SELECT against
// sessions; scanSession's Scan call must list destinations in this order.
const sessionColumns = `id, prefix_hash, first_seen, last_seen, request_count,
	total_input_tokens, total_output_tokens, total_cost_usd,
	priced_count, unpriced_count, model_set, warning_count`

func scanSession(sc rowScanner) (*Session, error) {
	var sess Session
	var prefixHash sql.NullString
	var firstSeen, lastSeen int64
	if err := sc.Scan(&sess.ID, &prefixHash, &firstSeen, &lastSeen, &sess.RequestCount,
		&sess.TotalInputTokens, &sess.TotalOutputTokens, &sess.TotalCostUSD,
		&sess.PricedCount, &sess.UnpricedCount, &sess.ModelSet, &sess.WarningCount); err != nil {
		return nil, err
	}
	sess.PrefixHash = nullStringPtr(prefixHash)
	sess.FirstSeen = time.Unix(0, firstSeen).UTC()
	sess.LastSeen = time.Unix(0, lastSeen).UTC()
	return &sess, nil
}

// ListSessions returns every session, most recently active first.
func (s *Store) ListSessions(ctx context.Context) ([]*Session, error) {
	rows, err := s.reader.QueryContext(ctx,
		"SELECT "+sessionColumns+" FROM sessions ORDER BY last_seen DESC")
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	defer rows.Close()

	var out []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list sessions: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// GetSession returns the session with id, or sql.ErrNoRows if it does not
// exist.
func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	row := s.reader.QueryRowContext(ctx,
		"SELECT "+sessionColumns+" FROM sessions WHERE id = ?", id)
	sess, err := scanSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: get session: %w", err)
	}
	return sess, nil
}

// LatestSessionByPrefix returns the most recently active session whose
// prefix_hash is exactly prefixHash, or sql.ErrNoRows when there is none —
// br-GI-1-12's rule 2 lookup ("the most recent session with the same
// meta.PrefixHash"). Pass "" for the null-prefix bucket (rule 3). A
// header-keyed session stores NULL here, so it can never be found by this
// lookup whatever its body's prefix hash was.
func (s *Store) LatestSessionByPrefix(ctx context.Context, prefixHash string) (*Session, error) {
	row := s.reader.QueryRowContext(ctx,
		"SELECT "+sessionColumns+" FROM sessions WHERE prefix_hash = ? ORDER BY last_seen DESC LIMIT 1",
		prefixHash)
	sess, err := scanSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: latest session by prefix: %w", err)
	}
	return sess, nil
}

// StatsBySession returns sessions active at or after since, dearest first —
// `lens stats --by session`'s ranking. It reads the sessions table's own
// incrementally-maintained totals rather than re-aggregating requests, which
// is the whole point of maintaining them; the cost order is therefore over
// each session's priced subtotal, which is what UnpricedCount exists to
// qualify.
func (s *Store) StatsBySession(ctx context.Context, since time.Time) ([]*Session, error) {
	rows, err := s.reader.QueryContext(ctx,
		"SELECT "+sessionColumns+` FROM sessions WHERE last_seen >= ?
		 ORDER BY total_cost_usd DESC LIMIT ?`, since.UnixNano(), DefaultLimit)
	if err != nil {
		return nil, fmt.Errorf("store: stats by session: %w", err)
	}
	defer rows.Close()

	var out []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: stats by session: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// UpsertSession folds one call into its session row, inserting the row if
// sess.ID is new. sess carries that one call's contribution, not a running
// total: RequestCount is 1, FirstSeen/LastSeen are the call's own time, and
// the token/cost/pricing/model/warning fields are its deltas. The merge is a
// single SQL statement — every counter is `existing + excluded` — so
// concurrent readers never see a half-applied update, and first_seen/last_seen
// are MIN/MAX'd rather than assigned, which makes them monotonic: an
// out-of-order (older) call can neither move last_seen backwards nor claim to
// be the session's first call.
//
// model_set's distinctness is the one thing SQL does here rather than Go: an
// already-present model is left alone, an empty incoming model (an
// unresolved upstream) is ignored, and otherwise the two are appended. Doing
// it in Go would mean a read-modify-write, i.e. two statements and a window
// where the row is stale. Writer only.
func (s *Store) UpsertSession(ctx context.Context, sess *Session) error {
	_, err := s.writer.ExecContext(ctx, `
		INSERT INTO sessions (
			id, prefix_hash, first_seen, last_seen, request_count,
			total_input_tokens, total_output_tokens, total_cost_usd,
			priced_count, unpriced_count, model_set, warning_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			first_seen = MIN(sessions.first_seen, excluded.first_seen),
			last_seen = MAX(sessions.last_seen, excluded.last_seen),
			request_count = sessions.request_count + excluded.request_count,
			total_input_tokens = sessions.total_input_tokens + excluded.total_input_tokens,
			total_output_tokens = sessions.total_output_tokens + excluded.total_output_tokens,
			total_cost_usd = sessions.total_cost_usd + excluded.total_cost_usd,
			priced_count = sessions.priced_count + excluded.priced_count,
			unpriced_count = sessions.unpriced_count + excluded.unpriced_count,
			warning_count = sessions.warning_count + excluded.warning_count,
			model_set = CASE
				WHEN excluded.model_set = '' THEN sessions.model_set
				WHEN instr(',' || sessions.model_set || ',', ',' || excluded.model_set || ',') > 0
					THEN sessions.model_set
				WHEN sessions.model_set = '' THEN excluded.model_set
				ELSE sessions.model_set || ',' || excluded.model_set
			END`,
		sess.ID, ptrOrNil(sess.PrefixHash), sess.FirstSeen.UnixNano(), sess.LastSeen.UnixNano(),
		sess.RequestCount,
		sess.TotalInputTokens, sess.TotalOutputTokens, sess.TotalCostUSD,
		sess.PricedCount, sess.UnpricedCount, sess.ModelSet, sess.WarningCount)
	if err != nil {
		return fmt.Errorf("store: upsert session: %w", err)
	}
	return nil
}

func scanWarning(sc rowScanner) (*Warning, error) {
	var w Warning
	var createdAt int64
	if err := sc.Scan(&w.ID, &w.RequestID, &w.Kind, &w.Severity, &w.Detail, &w.Path, &createdAt); err != nil {
		return nil, err
	}
	w.CreatedAt = time.Unix(0, createdAt).UTC()
	return &w, nil
}

// ListWarnings returns warnings matching f (Kind/Severity/Since/Limit),
// newest first. f.Limit <= 0 is capped at DefaultLimit.
func (s *Store) ListWarnings(ctx context.Context, f Filter) ([]*Warning, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	var where []string
	var args []interface{}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if f.Severity != "" {
		where = append(where, "severity = ?")
		args = append(args, f.Severity)
	}
	if !f.Since.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, f.Since.UnixNano())
	}

	query := "SELECT id, request_id, kind, severity, detail, path, created_at FROM warnings"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list warnings: %w", err)
	}
	defer rows.Close()

	var out []*Warning
	for rows.Next() {
		w, err := scanWarning(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list warnings: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// PurgeOlderThan deletes requests started before cutoff and their warnings,
// as one transaction, and returns the number of requests deleted.
func (s *Store) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: purge: begin: %w", err)
	}
	defer tx.Rollback()

	before := cutoff.UnixNano()
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM warnings WHERE request_id IN (SELECT id FROM requests WHERE started_at < ?)", before,
	); err != nil {
		return 0, fmt.Errorf("store: purge: warnings: %w", err)
	}

	res, err := tx.ExecContext(ctx, "DELETE FROM requests WHERE started_at < ?", before)
	if err != nil {
		return 0, fmt.Errorf("store: purge: requests: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: purge: rows affected: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: purge: commit: %w", err)
	}
	return n, nil
}

// RedactCheck is a startup self-test: it scans every row's stored header
// JSON for a reachable x-api-key value, belt-and-braces for the redaction
// internal/proxy/redact.go already applies before a call ever reaches the
// sink. It returns an error naming the offending request id if one is
// found, nil otherwise. Header JSON that fails to parse is skipped rather
// than treated as a failure — this is a leak scanner, not a schema
// validator.
func (s *Store) RedactCheck(ctx context.Context) error {
	rows, err := s.reader.QueryContext(ctx, "SELECT id, req_headers, resp_headers FROM requests")
	if err != nil {
		return fmt.Errorf("store: redact check: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var reqHeaders, respHeaders string
		if err := rows.Scan(&id, &reqHeaders, &respHeaders); err != nil {
			return fmt.Errorf("store: redact check: %w", err)
		}
		if v, leaked := leakedAPIKey(reqHeaders); leaked {
			return fmt.Errorf("store: redact check: request %d: x-api-key value %q reachable in req_headers", id, v)
		}
		if v, leaked := leakedAPIKey(respHeaders); leaked {
			return fmt.Errorf("store: redact check: request %d: x-api-key value %q reachable in resp_headers", id, v)
		}
	}
	return rows.Err()
}

// leakedAPIKey reports whether headerJSON — a JSON object mapping header
// name to a string or array-of-string value, the shape json.Marshal
// produces for an http.Header — contains a non-empty, non-redacted
// x-api-key value.
func leakedAPIKey(headerJSON string) (string, bool) {
	if headerJSON == "" {
		return "", false
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(headerJSON), &obj); err != nil {
		return "", false
	}
	for k, v := range obj {
		if !strings.EqualFold(k, "x-api-key") {
			continue
		}
		for _, sv := range flattenStrings(v) {
			if sv != "" && sv != redactedHeaderValue {
				return sv, true
			}
		}
	}
	return "", false
}

// flattenStrings extracts every string leaf out of v, which is either a
// plain JSON string or a JSON array (of strings, typically — an
// http.Header value), recursively.
func flattenStrings(v interface{}) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []interface{}:
		var out []string
		for _, e := range t {
			out = append(out, flattenStrings(e)...)
		}
		return out
	default:
		return nil
	}
}
