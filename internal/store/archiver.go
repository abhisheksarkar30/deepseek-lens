package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const archiveBatch = 32

type archiveRow struct {
	id   int64
	at   int64
	req  []byte
	resp []byte
}

// ArchiveOlderThan moves bodies started before the boundary into per-UTC-day
// zstd files. The day-file insert commits before the hot marker and NULL.
// stopAfter "day" returns after the day-file commit; "hot" returns after the
// hot commit. Both leave the body recoverable.
func (s *Store) ArchiveOlderThan(ctx context.Context, before time.Time, stopAfter string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := s.writer.QueryContext(ctx, `
			SELECT id, started_at, req_body, resp_body FROM requests
			WHERE started_at < ?
			  AND (req_body IS NOT NULL OR resp_body IS NOT NULL)
			  AND id NOT IN (SELECT request_id FROM body_archive)
			ORDER BY started_at LIMIT ?`, before.UnixNano(), archiveBatch)
		if err != nil {
			return fmt.Errorf("store: archive select: %w", err)
		}
		var batch []archiveRow
		for rows.Next() {
			var r archiveRow
			if err := rows.Scan(&r.id, &r.at, &r.req, &r.resp); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		byDay := map[string][]archiveRow{}
		for _, r := range batch {
			day := time.Unix(0, r.at).UTC().Format("2006-01-02")
			byDay[day] = append(byDay[day], r)
		}
		for day, rs := range byDay {
			if err := s.writeDay(day, rs); err != nil {
				return err
			}
		}
		if stopAfter == "day" {
			return nil
		}
		tx, err := s.writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		now := time.Now().UnixNano()
		for _, r := range batch {
			day := time.Unix(0, r.at).UTC().Format("2006-01-02")
			mask := 0
			if len(r.req) > 0 {
				mask |= 1
			}
			if len(r.resp) > 0 {
				mask |= 2
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO body_archive(request_id, day, archived_at, body_mask) VALUES (?,?,?,?)`,
				r.id, day, now, mask); err != nil {
				tx.Rollback()
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE requests SET req_body = NULL, resp_body = NULL WHERE id = ?`, r.id); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if stopAfter == "hot" {
			return nil
		}
	}
}

func (s *Store) writeDay(day string, rows []archiveRow) error {
	dir := filepath.Join(filepath.Dir(s.dbPath), "archive")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "bodies-"+day+".db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	s.dayOpens++
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS bodies (
		request_id INTEGER PRIMARY KEY,
		req_body BLOB,
		resp_body BLOB)`); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer enc.Close()
	for _, r := range rows {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO bodies(request_id, req_body, resp_body) VALUES (?,?,?)`,
			r.id, enc.EncodeAll(r.req, nil), enc.EncodeAll(r.resp, nil)); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func archiveTargets(ctx context.Context, q queryer, where string, args ...any) (map[string][]int64, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT request_id, day FROM body_archive
		WHERE request_id IN (SELECT id FROM requests WHERE `+where+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]int64{}
	for rows.Next() {
		var id int64
		var day string
		if err := rows.Scan(&id, &day); err != nil {
			return nil, err
		}
		out[day] = append(out[day], id)
	}
	return out, rows.Err()
}

func (s *Store) deleteDayRows(ctx context.Context, byDay map[string][]int64) error {
	for day, ids := range byDay {
		if err := s.execDay(ctx, day, `DELETE FROM bodies WHERE request_id IN (`, ids); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) archivedBytes(ctx context.Context, where string, args ...any) (int64, error) {
	byDay, err := archiveTargets(ctx, s.reader, where, args...)
	if err != nil {
		return 0, fmt.Errorf("store: archived bytes: %w", err)
	}
	var total int64
	for day, ids := range byDay {
		n, err := s.sumDayBytes(ctx, day, ids)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

func (s *Store) sumDayBytes(ctx context.Context, day string, ids []int64) (int64, error) {
	path := filepath.Join(filepath.Dir(s.dbPath), "archive", "bodies-"+day+".db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	s.dayOpens++
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `SELECT COALESCE(SUM(LENGTH(req_body)+LENGTH(resp_body)), 0) FROM bodies WHERE request_id IN (` + strings.Join(placeholders, ",") + `)`
	var n int64
	if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) execDay(ctx context.Context, day, prefix string, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	path := filepath.Join(filepath.Dir(s.dbPath), "archive", "bodies-"+day+".db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	s.dayOpens++
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	_, err = db.ExecContext(ctx, prefix+strings.Join(placeholders, ",")+`)`, args...)
	return err
}

// GCArchive deletes day-file rows that no marker still references.
func (s *Store) GCArchive(ctx context.Context) error {
	rows, err := s.reader.QueryContext(ctx, `SELECT DISTINCT day FROM body_archive`)
	if err != nil {
		return err
	}
	var days []string
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			rows.Close()
			return err
		}
		days = append(days, day)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	dir := filepath.Join(filepath.Dir(s.dbPath), "archive")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	known := map[string]bool{}
	for _, day := range days {
		known[day] = true
	}
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasPrefix(name, "bodies-") || !strings.HasSuffix(name, ".db") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, "bodies-"), ".db")
		path := filepath.Join(dir, name)
		db, err := sql.Open("sqlite", path)
		if err != nil {
			return err
		}
		s.dayOpens++
		ids, err := s.markerIDs(ctx, day)
		if err != nil {
			db.Close()
			return err
		}
		if len(ids) == 0 {
			db.Close()
			if !known[day] {
				os.Remove(path)
			}
			continue
		}
		placeholders := make([]string, len(ids))
		args := make([]any, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		q := `DELETE FROM bodies WHERE request_id NOT IN (` + strings.Join(placeholders, ",") + `)`
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			db.Close()
			return err
		}
		db.Close()
	}
	return nil
}

func (s *Store) markerIDs(ctx context.Context, day string) ([]int64, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT request_id FROM body_archive WHERE day = ?`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) hydrateBodies(ctx context.Context, reqs []*Request) error {
	if len(reqs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(reqs))
	byID := map[int64]*Request{}
	for _, r := range reqs {
		if r == nil {
			continue
		}
		ids = append(ids, strconv.FormatInt(r.ID, 10))
		byID[r.ID] = r
	}
	if len(ids) == 0 {
		return nil
	}
	q := `SELECT request_id, day FROM body_archive WHERE request_id IN (` + strings.Join(ids, ",") + `)`
	rows, err := s.reader.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("store: hydrate markers: %w", err)
	}
	byDay := map[string][]int64{}
	for rows.Next() {
		var id int64
		var day string
		if err := rows.Scan(&id, &day); err != nil {
			rows.Close()
			return err
		}
		byDay[day] = append(byDay[day], id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return err
	}
	defer dec.Close()
	for day, idList := range byDay {
		path := filepath.Join(filepath.Dir(s.dbPath), "archive", "bodies-"+day+".db")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			return err
		}
		s.dayOpens++
		placeholders := make([]string, len(idList))
		args := make([]any, len(idList))
		for i, id := range idList {
			placeholders[i] = "?"
			args[i] = id
		}
		q := `SELECT request_id, req_body, resp_body FROM bodies WHERE request_id IN (` + strings.Join(placeholders, ",") + `)`
		brows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			db.Close()
			return err
		}
		for brows.Next() {
			var id int64
			var req, resp []byte
			if err := brows.Scan(&id, &req, &resp); err != nil {
				brows.Close()
				db.Close()
				return err
			}
			r := byID[id]
			if r == nil {
				continue
			}
			if len(req) > 0 {
				plain, err := dec.DecodeAll(req, nil)
				if err != nil {
					brows.Close()
					db.Close()
					return err
				}
				r.ReqBody = plain
			}
			if len(resp) > 0 {
				plain, err := dec.DecodeAll(resp, nil)
				if err != nil {
					brows.Close()
					db.Close()
					return err
				}
				r.RespBody = plain
			}
		}
		err = brows.Err()
		brows.Close()
		db.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
