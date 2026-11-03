package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

func transientRetry(kind string, code int) bool {
	if code >= 500 && code <= 599 {
		return true
	}
	if code != 0 {
		return false
	}
	return kind == "timeout" || kind == "network" || kind == "http_5xx"
}
func (s *Store) enqueueRetry(ctx context.Context, tx pgx.Tx, kind, platform, subject, hash, failure string) error {
	delay := s.RetryDelay
	if delay <= 0 {
		delay = time.Hour
	}
	var jobID, listingID any
	if kind == "page" {
		jobID = subject
	} else {
		listingID = subject
	}
	_, err := tx.Exec(ctx, `INSERT INTO crawler_retries(kind,platform,subject_id,job_id,listing_id,summary_hash,error_kind,available_at) VALUES($1,$2,$3,$4,$5,$6,$7,now()+make_interval(secs=>$8)) ON CONFLICT(kind,platform,subject_id) DO UPDATE SET summary_hash=$6,error_kind=$7,available_at=EXCLUDED.available_at`, kind, platform, subject, jobID, listingID, hash, failure, delay.Seconds())
	return err
}

// Read candidates without holding retry locks; acquire parent/task locks first,
// matching completion lock order. Timestamp/hash fencing protects newer tickets.
func (s *Store) releaseRetries(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT kind,platform,subject_id,summary_hash,available_at FROM crawler_retries WHERE available_at<=now() AND (kind='detail' OR EXISTS(SELECT 1 FROM crawl_jobs j WHERE j.id=job_id AND j.status<>'paused')) ORDER BY available_at LIMIT 100`)
	if err != nil {
		return err
	}
	type ticket struct {
		kind, platform, subject, hash string
		due                           time.Time
	}
	var tickets []ticket
	for rows.Next() {
		var t ticket
		if err = rows.Scan(&t.kind, &t.platform, &t.subject, &t.hash, &t.due); err != nil {
			rows.Close()
			return err
		}
		tickets = append(tickets, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, t := range tickets {
		if err = s.releaseRetry(ctx, t.kind, t.platform, t.subject, t.hash, t.due); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) releaseRetry(ctx context.Context, kind, platform, subject, hash string, due time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if kind == "page" {
		// Paused jobs keep their tickets, so resuming does not erase the cooldown.
		var searchID, status string
		err = tx.QueryRow(ctx, `SELECT search_id FROM crawl_jobs WHERE id=$1`, subject).Scan(&searchID)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `SELECT id FROM crawl_searches WHERE id=$1 FOR UPDATE`, searchID); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `SELECT status FROM crawl_jobs WHERE id=$1 FOR UPDATE`, subject).Scan(&status); err != nil {
			return err
		}
		if status == "paused" {
			return nil
		}
		tag, e := tx.Exec(ctx, `UPDATE crawl_tasks SET status='queued',attempts=0,available_at=now(),token=NULL,lease_until=NULL WHERE job_id=$1 AND platform=$2 AND status='delayed' AND EXISTS(SELECT 1 FROM crawler_retries WHERE kind='page' AND subject_id=$1 AND platform=$2 AND available_at=$3)`, subject, platform, due)
		if e != nil {
			return e
		}
		if tag.RowsAffected() > 0 {
			if _, err = tx.Exec(ctx, `UPDATE crawl_jobs SET status='running',updated_at=now() WHERE id=$1`, subject); err != nil {
				return err
			}
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE detail_tasks SET status='queued',attempts=0,available_at=now(),token=NULL,lease_until=NULL WHERE listing_id=$1 AND platform=$2 AND summary_hash=$3 AND status='delayed' AND EXISTS(SELECT 1 FROM crawler_retries WHERE kind='detail' AND subject_id=$1 AND platform=$2 AND summary_hash=$3 AND available_at=$4)`, subject, platform, hash, due)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `DELETE FROM crawler_retries WHERE kind=$1 AND platform=$2 AND subject_id=$3 AND summary_hash=$4 AND available_at=$5`, kind, platform, subject, hash, due)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
