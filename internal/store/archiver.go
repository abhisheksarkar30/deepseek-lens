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
