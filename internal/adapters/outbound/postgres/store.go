package postgres

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	Pool           *pgxpool.Pool
	InventoryCache crawljobs.InventoryCache
	RetryDelay     time.Duration
	Metrics        Metrics
}

func New(ctx context.Context, url string) (*Store, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	config.MaxConns = 10
	config.ConnConfig.Tracer = queryTracer{}
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	store := &Store{Pool: pool}
	if err = pool.Ping(ctx); err == nil {
		err = store.Migrate(ctx)
	}
	if err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}
func (s *Store) Close() { s.Pool.Close() }

// NewMonitoring opens a small dedicated read-only pool for the snapshot
// collector. It never runs migrations (the application pool owns the schema)
// and is capped at two connections so monitoring scans cannot exhaust the
// application pool. The pool's lifetime belongs to the collector: the caller
// must Close it when the collector stops.
func NewMonitoring(ctx context.Context, url string) (*Store, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	config.MaxConns = 2
	config.ConnConfig.Tracer = queryTracer{}
	config.MinConns = 0
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	// Every monitoring query is a read; enforce it at the session level so a
	// future monitoring query can never write application state.
	config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "true"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool}, nil
}
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(686837411); CREATE TABLE IF NOT EXISTS schema_migrations(version integer PRIMARY KEY);`); err != nil {
		return err
	}
	for version, name := range []string{"001_inventory_jobs.sql", "002_details_lifecycle.sql", "003_normalization.sql", "004_bama_city.sql", "005_coverage.sql", "006_delayed_retries.sql", "007_user_and_saved_listings.sql"} {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version+1).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			body, readErr := migrations.ReadFile("migrations/" + name)
			if readErr != nil {
				return readErr
			}
			if _, err = tx.Exec(ctx, string(body)); err != nil {
				return err
			}
			if version+1 == 3 {
				if err = reindexNormalization(ctx, tx); err != nil {
					return err
				}
			}
			if version+1 == 4 {
				if err = repairBamaCities(ctx, tx); err != nil {
					return err
				}
			}
			if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations VALUES($1)`, version+1); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
func id() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func createJob(ctx context.Context, tx pgx.Tx, searchID, mode string, criteria vehicle.SearchCriteria) (string, error) {
	jobID := id()
	if _, err := tx.Exec(ctx, `INSERT INTO crawl_jobs(id,search_id,mode) VALUES($1,$2,$3)`, jobID, searchID, mode); err != nil {
		return "", err
	}
	platforms := []vehicle.Platform{vehicle.PlatformDivar, vehicle.PlatformBama, vehicle.PlatformSheypoor}
	if criteria.Platform != nil {
		platforms = []vehicle.Platform{*criteria.Platform}
	}
	for _, platform := range platforms {
		if _, err := tx.Exec(ctx, `INSERT INTO crawl_tasks(job_id,platform) VALUES($1,$2)`, jobID, platform); err != nil {
			return "", err
		}
	}
	return jobID, nil
}

func (s *Store) Submit(ctx context.Context, criteria vehicle.SearchCriteria, force bool) (crawljobs.Job, error) {
	return s.submit(ctx, criteria, force, false)
}
func (s *Store) SubmitStatus(ctx context.Context, criteria vehicle.SearchCriteria, force bool) (crawljobs.Job, error) {
	return s.submit(ctx, criteria, force, true)
}
func (s *Store) submit(ctx context.Context, criteria vehicle.SearchCriteria, force, statusOnly bool) (crawljobs.Job, error) {
	searchID, criteria := crawljobs.SearchIdentity(criteria)
	if statusOnly {
		var jobID string
		err := s.Pool.QueryRow(ctx, `SELECT id FROM crawl_jobs WHERE search_id=$1 AND (status IN ('queued','running','paused') OR (status IN ('complete','partial','failed') AND updated_at>now()-interval '5 minutes')) ORDER BY created_at DESC LIMIT 1`, searchID).Scan(&jobID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return crawljobs.Job{}, crawljobs.ErrNotFound
			}
			return crawljobs.Job{}, err
		}
		return s.Status(ctx, jobID)
	}
	raw, _ := json.Marshal(criteria)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return crawljobs.Job{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO crawl_searches(id,criteria) VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET demand=crawl_searches.demand*exp(-extract(epoch FROM(now()-crawl_searches.demand_updated_at))/86400)+1,requested_at=now(),demand_updated_at=now()`, searchID, raw)
	if err != nil {
		return crawljobs.Job{}, err
	}
	var jobID string
	err = tx.QueryRow(ctx, `SELECT id FROM crawl_jobs WHERE search_id=$1 AND (status IN ('queued','running','paused') OR ($2=false AND status IN ('complete','partial','failed') AND updated_at>now()-interval '5 minutes')) ORDER BY created_at DESC LIMIT 1`, searchID, force).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		jobID, err = createJob(ctx, tx, searchID, "incremental", criteria)
	}
	if err != nil {
		return crawljobs.Job{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return crawljobs.Job{}, err
	}
	if statusOnly {
		return s.Status(ctx, jobID)
	}
	return s.Get(ctx, jobID)
}

func (s *Store) Claim(ctx context.Context) (crawljobs.Task, bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return crawljobs.Task{}, false, err
	}
	defer tx.Rollback(ctx)
	var task crawljobs.Task
	var raw []byte
	var jobID string
	err = tx.QueryRow(ctx, `SELECT j.id FROM crawl_jobs j WHERE j.status IN ('queued','running') AND EXISTS(SELECT 1 FROM crawl_tasks t WHERE t.job_id=j.id AND ((t.status IN ('queued','retry') AND t.available_at<=now()) OR(t.status='running' AND t.lease_until<now()))) ORDER BY (j.mode='incremental') DESC,j.updated_at,j.created_at FOR UPDATE OF j SKIP LOCKED LIMIT 1`).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return task, false, nil
	}
	if err != nil {
		return task, false, err
	}
	err = tx.QueryRow(ctx, `SELECT t.job_id,j.search_id,t.platform,t.cursor,t.page,t.attempts,j.mode,s.criteria FROM crawl_tasks t JOIN crawl_jobs j ON j.id=t.job_id JOIN crawl_searches s ON s.id=j.search_id WHERE t.job_id=$1 AND ((t.status IN ('queued','retry') AND t.available_at<=now()) OR(t.status='running' AND t.lease_until<now())) ORDER BY t.available_at,t.platform FOR UPDATE OF t SKIP LOCKED LIMIT 1`, jobID).Scan(&task.JobID, &task.SearchID, &task.Platform, &task.Cursor, &task.Page, &task.Attempts, &task.Mode, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return task, false, nil
	}
	if err != nil {
		return task, false, err
	}
	if err = json.Unmarshal(raw, &task.Criteria); err != nil {
		return task, false, err
	}
	task.Token = id()
	task.Attempts++
	_, err = tx.Exec(ctx, `UPDATE crawl_tasks SET status='running',attempts=$3,token=$4,lease_until=now()+interval '60 seconds' WHERE job_id=$1 AND platform=$2`, task.JobID, task.Platform, task.Attempts, task.Token)
	if err != nil {
		return task, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE crawl_jobs SET status='running',updated_at=now() WHERE id=$1`, task.JobID)
	if err != nil {
		return task, false, err
	}
	return task, true, tx.Commit(ctx)
}

func (s *Store) Prepare(ctx context.Context, rows []vehicle.NormalizedVehicleListing) ([]crawljobs.Observation, error) {
	result := make([]crawljobs.Observation, 0, len(rows))
	for _, row := range rows {
		hash := crawljobs.SummaryHash(row)
		observation := crawljobs.Observation{Listing: row, SummaryHash: hash, NeedsDetails: true}
		var previousHash string
		var raw []byte
		var fresh bool
		err := s.Pool.QueryRow(ctx, `SELECT summary_hash,data,coalesce(details_at>now()-interval '48 hours',false) FROM listings WHERE platform=$1 AND id=$2`, row.Platform, row.ID).Scan(&previousHash, &raw, &fresh)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil && hash == previousHash && fresh {
			var previous vehicle.NormalizedVehicleListing
			if err = json.Unmarshal(raw, &previous); err != nil {
				return nil, err
			}
			previous.ScrapedAt = row.ScrapedAt
			observation.Listing = previous
			observation.NeedsDetails = false
		}
		result = append(result, observation)
	}
	return result, nil
}

func lockTask(ctx context.Context, tx pgx.Tx, task crawljobs.Task) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM crawl_searches WHERE id=$1 FOR UPDATE`, task.SearchID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM crawl_jobs WHERE id=$1 FOR UPDATE`, task.JobID); err != nil {
		return err
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT token=$3 AND status='running' AND lease_until>now() FROM crawl_tasks WHERE job_id=$1 AND platform=$2 FOR UPDATE`, task.JobID, task.Platform, task.Token).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return crawljobs.ErrLeaseLost
	}
	return nil
}

func finishJob(ctx context.Context, tx pgx.Tx, jobID string) error {
	var pending, paused, failed int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status IN ('queued','running','retry','delayed')),count(*) FILTER(WHERE status='paused'),count(*) FILTER(WHERE status IN ('unavailable','limited')) FROM crawl_tasks WHERE job_id=$1`, jobID).Scan(&pending, &paused, &failed); err != nil {
		return err
	}
	status := "running"
	if pending > 0 {
		var current string
		if err := tx.QueryRow(ctx, `SELECT status FROM crawl_jobs WHERE id=$1`, jobID).Scan(&current); err != nil {
			return err
		}
		if current == "paused" {
			status = "paused"
		}
	}
	if pending == 0 {
		status = "complete"
		if paused > 0 {
			status = "paused"
		} else if failed > 0 {
			status = "partial"
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE crawl_jobs SET status=$2,updated_at=now() WHERE id=$1`, jobID, status); err != nil {
		return err
	}
	if status == "complete" || status == "partial" {
		_, err := tx.Exec(ctx, `UPDATE crawl_searches s SET next_refresh=CASE WHEN s.coverage AND $2='partial' THEN now()+interval '15 minutes' WHEN s.coverage THEN greatest(j.created_at+interval '48 hours',now()) ELSE now()+interval '48 hours' END,page_interval_seconds=CASE WHEN s.coverage AND $2='complete' THEN greatest(1,least(900,129600/greatest(1,(SELECT coalesce(sum(page),1)::int FROM crawl_tasks WHERE job_id=j.id)))) ELSE s.page_interval_seconds END,last_full=CASE WHEN j.mode='full' AND $2='complete' AND NOT EXISTS(SELECT 1 FROM crawl_tasks t WHERE t.job_id=j.id AND t.status='limited') THEN now() ELSE last_full END FROM crawl_jobs j WHERE j.id=$1 AND s.id=j.search_id`, jobID, status)
		return err
	}
	return nil
}

func (s *Store) Commit(ctx context.Context, task crawljobs.Task, observations []crawljobs.Observation, next string, page int) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Acquire the cache revision before any listing lock, matching detail completion.
	// Otherwise the statement trigger and a concurrent detail update could deadlock.
	if _, err = tx.Exec(ctx, `SELECT id FROM inventory_revision WHERE id=true FOR UPDATE`); err != nil {
		return err
	}

	if err = lockTask(ctx, tx, task); err != nil {
		return err
	}
	// Serialize page commits for one search before updating job totals/state.
	if _, err = tx.Exec(ctx, `SELECT id FROM crawl_jobs WHERE id=$1 FOR UPDATE`, task.JobID); err != nil {
		return err
	}
	newCount, changedCount, knownCount := 0, 0, 0
	var invalidatedTickets int64
	var overlapPages int
	if err = tx.QueryRow(ctx, `SELECT overlap_pages FROM crawl_tasks WHERE job_id=$1 AND platform=$2`, task.JobID, task.Platform).Scan(&overlapPages); err != nil {
		return err
	}
	if page <= task.Page {
		page = task.Page + 1
	}
	for _, observation := range observations {
		row := observation.Listing
		row.Lifecycle = nil
		row.Provenance = nil
		if observation.NeedsDetails {
			row.DetailsStatus = "partial"
		}
		raw, _ := json.Marshal(row)
		var oldHash string
		var knownBefore bool
		err = tx.QueryRow(ctx, `SELECT summary_hash,first_seen<(SELECT created_at FROM crawl_jobs WHERE id=$3) AND NOT EXISTS(SELECT 1 FROM listing_observations o WHERE o.job_id=$3 AND o.platform=$1 AND o.listing_id=$2) FROM listings WHERE platform=$1 AND id=$2`, row.Platform, row.ID, task.JobID).Scan(&oldHash, &knownBefore)
		if knownBefore {
			knownCount++
		}
		if errors.Is(err, pgx.ErrNoRows) {
			newCount++
		} else if err != nil {
			return err
		} else if oldHash != observation.SummaryHash {
			changedCount++
		}
		_, err = tx.Exec(ctx, `INSERT INTO listings(platform,id,data,normalized_text,vehicle_type,city,price,mileage,year,body,summary_hash,details_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,CASE WHEN $12 THEN now() ELSE null END) ON CONFLICT(platform,id) DO UPDATE SET data=EXCLUDED.data,normalized_text=EXCLUDED.normalized_text,vehicle_type=EXCLUDED.vehicle_type,city=EXCLUDED.city,price=EXCLUDED.price,mileage=EXCLUDED.mileage,year=EXCLUDED.year,body=EXCLUDED.body,summary_hash=EXCLUDED.summary_hash,last_seen=now(),details_at=CASE WHEN $12 AND $13 THEN now() ELSE listings.details_at END`, row.Platform, row.ID, raw, vehicle.NormalizeModelText(row.Title+" "+row.BrandModel), row.VehicleType, row.CityID, row.PriceToman, row.MileageKm, vehicle.ScoringYear(row.Year), row.BodyStatus, observation.SummaryHash, row.DetailsStatus == "complete", observation.NeedsDetails)
		if err != nil {
			return err
		}
		if invalidated, err := recordSummary(ctx, tx, task.JobID, observation); err != nil {
			return err
		} else {
			invalidatedTickets += invalidated
		}
		if _, err = tx.Exec(ctx, `INSERT INTO search_listings(search_id,platform,listing_id) VALUES($1,$2,$3) ON CONFLICT(search_id,platform,listing_id) DO UPDATE SET last_seen=now()`, task.SearchID, row.Platform, row.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO listing_observations(job_id,platform,page,listing_id,data) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, task.JobID, row.Platform, page, row.ID, raw); err != nil {
			return err
		}
	}
	state := "queued"
	if len(observations) >= 5 && knownCount*10 >= len(observations)*9 {
		overlapPages++
	} else {
		overlapPages = 0
	}
	if next == "" || (task.Mode == "incremental" && overlapPages >= 2) {
		state = "complete"
	} else if (task.Mode == "incremental" && page >= 20) || page >= 10000 {
		state = "limited"
	}
	// A pause requested while this page was fetching takes effect after commit.
	var parent string
	if err = tx.QueryRow(ctx, `SELECT status FROM crawl_jobs WHERE id=$1`, task.JobID).Scan(&parent); err != nil {
		return err
	}
	if parent == "paused" && state == "queued" {
		state = "paused"
	}
	if _, err = tx.Exec(ctx, `UPDATE crawl_tasks SET status=$3,cursor=$4,next_cursor=$4,page=$5,attempts=0,token=null,lease_until=null,error_kind='',overlap_pages=$6,available_at=now()+make_interval(secs=>CASE WHEN (SELECT coverage FROM crawl_searches WHERE id=$7) THEN (SELECT page_interval_seconds FROM crawl_searches WHERE id=$7) ELSE 1 END) WHERE job_id=$1 AND platform=$2`, task.JobID, task.Platform, state, next, page, overlapPages, task.SearchID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE crawl_jobs SET new_count=new_count+$2,changed_count=changed_count+$3 WHERE id=$1`, task.JobID, newCount, changedCount); err != nil {
		return err
	}
	if err = finishJob(ctx, tx, task.JobID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if invalidatedTickets > 0 {
		s.metrics().RetryInvalidated("detail", invalidatedTickets)
	}
	return nil
}

func (s *Store) Fail(ctx context.Context, task crawljobs.Task, kind string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockTask(ctx, tx, task); err != nil {
		return err
	}
	state := "retry"
	var parent string
	if err = tx.QueryRow(ctx, `SELECT status FROM crawl_jobs WHERE id=$1`, task.JobID).Scan(&parent); err != nil {
		return err
	}
	deferred := kind == "source_wait" || kind == "budget_unavailable" || kind == "egress_unavailable" || kind == "rate_limited"
	if deferred {
		task.Attempts = max(0, task.Attempts-1)
	}
	if (!deferred && task.Attempts >= 3) || kind == "parser" || kind == "validation" || kind == "http_4xx" {
		state = "unavailable"
	}
	ticketScheduled := false
	if state == "unavailable" && transientRetry(kind, 0) {
		state = "delayed"
		ticketScheduled = true
		if err = s.enqueueRetry(ctx, tx, "page", string(task.Platform), task.JobID, "", kind); err != nil {
			return err
		}
	}
	if parent == "paused" && state != "delayed" {
		state = "paused"
	}
	if kind == "cursor" {
		task.Cursor = ""
		task.Page = 0
	}
	if _, err = tx.Exec(ctx, `UPDATE crawl_tasks SET status=$3,error_kind=$4,token=null,lease_until=null,cursor=$5,page=$6,attempts=$8,available_at=now()+make_interval(secs=>$7) WHERE job_id=$1 AND platform=$2`, task.JobID, task.Platform, state, kind, task.Cursor, task.Page, max(10, task.Attempts*10), task.Attempts); err != nil {
		return err
	}
	if err = finishJob(ctx, tx, task.JobID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if ticketScheduled {
		s.metrics().RetryScheduled("page")
	}
	return nil
}

func (s *Store) Get(ctx context.Context, jobID string) (crawljobs.Job, error) {
	return s.get(ctx, jobID, true)
}
func (s *Store) Status(ctx context.Context, jobID string) (crawljobs.Job, error) {
	return s.get(ctx, jobID, false)
}
func (s *Store) get(ctx context.Context, jobID string, includeData bool) (crawljobs.Job, error) {
	job := crawljobs.Job{Data: []vehicle.NormalizedVehicleListing{}, Sources: []vehicle.SourceStatus{}}
	var raw []byte
	var searchID string
	err := s.Pool.QueryRow(ctx, `SELECT j.id,j.status,j.mode,j.created_at,j.updated_at,j.new_count,j.changed_count,j.search_id,s.criteria FROM crawl_jobs j JOIN crawl_searches s ON s.id=j.search_id WHERE j.id=$1`, jobID).Scan(&job.ID, &job.Status, &job.Mode, &job.CreatedAt, &job.UpdatedAt, &job.New, &job.Changed, &searchID, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return job, crawljobs.ErrNotFound
	}
	if err != nil {
		return job, err
	}
	if err = json.Unmarshal(raw, &job.Criteria); err != nil {
		return job, err
	}
	job.Done = job.Status == "complete" || job.Status == "partial" || job.Status == "paused" || job.Status == "failed"
	tasks, err := s.Pool.Query(ctx, `SELECT t.platform,t.status,t.page,t.next_cursor,t.error_kind,(SELECT count(*) FROM search_listings m JOIN listings l ON l.platform=m.platform AND l.id=m.listing_id WHERE m.search_id=$2 AND m.platform=t.platform AND m.last_seen>now()-interval '7 days' AND ($3='' OR $3='iran' OR l.platform<>'bama' OR l.city=$3)) FROM crawl_tasks t WHERE t.job_id=$1 ORDER BY t.platform`, jobID, searchID, job.Criteria.CitySlug)
	if err != nil {
		return job, err
	}
	for tasks.Next() {
		var source vehicle.SourceStatus
		var status string
		if err = tasks.Scan(&source.Platform, &status, &source.Page, &source.NextCursor, &source.Error, &source.Count); err != nil {
			tasks.Close()
			return job, err
		}
		source.Status = "ok"
		if status == "running" {
			job.Done = false
		}
		if source.NextCursor == "" && (status == "queued" || status == "running" || status == "paused" || status == "retry") {
			source.NextCursor = "job:pending"
		}
		if status == "unavailable" || status == "retry" || status == "delayed" {
			source.Status = "unavailable"
		}
		job.Sources = append(job.Sources, source)
	}
	tasks.Close()
	if err = tasks.Err(); err != nil {
		return job, err
	}
	if includeData {
		rows, err := s.Pool.Query(ctx, `SELECT listing_payload(l) FROM search_listings m JOIN listings l ON l.platform=m.platform AND l.id=m.listing_id WHERE m.search_id=$1 AND m.last_seen>now()-interval '7 days' AND ($2='' OR $2='iran' OR l.platform<>'bama' OR l.city=$2) ORDER BY m.last_seen DESC,l.platform,l.id LIMIT 5001`, searchID, job.Criteria.CitySlug)
		if err != nil {
			return job, err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var row vehicle.NormalizedVehicleListing
			if err = rows.Scan(&raw); err != nil {
				return job, err
			}
			if err = json.Unmarshal(raw, &row); err != nil {
				return job, err
			}
			job.Data = append(job.Data, row)
		}
		if len(job.Data) > 5000 {
			job.Data = job.Data[:5000]
			job.Truncated = true
		}
		if err = rows.Err(); err != nil {
			return job, err
		}
		rows.Close()

	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE d.status IN ('queued','running','retry')),count(*) FILTER(WHERE d.status='failed') FROM job_details j JOIN detail_tasks d USING(platform,listing_id) WHERE j.job_id=$1`, jobID).Scan(&job.DetailsPending, &job.DetailsFailed); err != nil {
		return job, err
	}
	job.CrawlDone = job.Done
	var waitingPages, activePages int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='delayed'),count(*) FILTER(WHERE status IN ('queued','running','retry')) FROM crawl_tasks WHERE job_id=$1`, jobID).Scan(&waitingPages, &activePages); err != nil {
		return job, err
	}
	if waitingPages > 0 && activePages == 0 {
		job.Done = true
	}
	if job.Status != "paused" && job.DetailsPending > 0 {
		job.Done = false
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*),min(available_at) FROM (SELECT available_at FROM crawler_retries WHERE kind='page' AND job_id=$1 UNION ALL SELECT r.available_at FROM job_details d JOIN crawler_retries r ON r.kind='detail' AND r.platform=d.platform AND r.listing_id=d.listing_id WHERE d.job_id=$1) waiting`, jobID).Scan(&job.RetriesPending, &job.RetryAt); err != nil {
		return job, err
	}

	return job, nil
}

func (s *Store) Action(ctx context.Context, jobID, action string, platform vehicle.Platform) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var searchID string
	if err = tx.QueryRow(ctx, `SELECT search_id FROM crawl_jobs WHERE id=$1`, jobID).Scan(&searchID); errors.Is(err, pgx.ErrNoRows) {
		return crawljobs.ErrNotFound
	} else if err != nil {
		return err
	}
	// Serialize with submission/scheduling so resuming an old job cannot race a newer one.
	if _, err = tx.Exec(ctx, `SELECT id FROM crawl_searches WHERE id=$1 FOR UPDATE`, searchID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM crawl_jobs WHERE id=$1 FOR UPDATE`, jobID); err != nil {
		return err
	}
	if action == "pause" {
		_, err = tx.Exec(ctx, `UPDATE crawl_jobs SET status='paused',updated_at=now() WHERE id=$1 AND status IN ('queued','running')`, jobID)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE crawl_tasks SET status='paused' WHERE job_id=$1 AND status IN ('queued','retry')`, jobID)
		}
	} else if action == "resume" || action == "retry" {
		var active bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM crawl_jobs WHERE search_id=$1 AND id<>$2 AND status IN ('queued','running','paused'))`, searchID, jobID).Scan(&active); err != nil {
			return err
		}
		if active {
			return errors.New("a newer crawl is active")
		}
		changed, updateErr := tx.Exec(ctx, `UPDATE crawl_tasks SET status='queued',attempts=0,available_at=now() WHERE job_id=$1 AND status IN ('paused','unavailable','retry','limited') AND ($2='' OR platform=$2)`, jobID, platform)
		err = updateErr
		var delayed bool
		if err == nil {
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM crawl_tasks WHERE job_id=$1 AND status='delayed')`, jobID).Scan(&delayed)
		}
		if err == nil && (changed.RowsAffected() > 0 || delayed) {
			_, err = tx.Exec(ctx, `UPDATE crawl_jobs SET status='queued',updated_at=now() WHERE id=$1`, jobID)
		}
	} else {
		return errors.New("invalid job action")
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Schedule(ctx context.Context) error {
	if err := s.releaseRetries(ctx); err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT s.id,s.criteria,s.coverage FROM crawl_searches s WHERE s.next_refresh<=now() AND (s.coverage OR s.requested_at>now()-interval '7 days') AND NOT EXISTS(SELECT 1 FROM crawl_jobs j WHERE j.search_id=s.id AND j.status IN ('queued','running','paused')) ORDER BY s.next_refresh FOR UPDATE OF s SKIP LOCKED LIMIT 20`)
	if err != nil {
		return err
	}
	type due struct {
		id       string
		criteria vehicle.SearchCriteria
		full     bool
	}
	var searches []due
	for rows.Next() {
		var item due
		var raw []byte
		if err = rows.Scan(&item.id, &raw, &item.full); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &item.criteria); err != nil {
			rows.Close()
			return err
		}
		searches = append(searches, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, item := range searches {
		mode := "incremental"
		if item.full {
			mode = "full"
		}
		if _, err = createJob(ctx, tx, item.id, mode, item.criteria); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) Search(ctx context.Context, q crawljobs.InventoryQuery) (crawljobs.InventoryResult, error) {
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 50
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	var cacheKey string
	if s.InventoryCache != nil {
		var revision int64
		if err := s.Pool.QueryRow(ctx, `SELECT revision FROM inventory_revision WHERE id=true`).Scan(&revision); err != nil {
			return crawljobs.InventoryResult{}, err
		}
		raw, _ := json.Marshal(q)
		cacheKey = fmt.Sprintf("%d:%s", revision, raw)
		result, outcome := s.InventoryCache.GetInventory(ctx, cacheKey)
		s.metrics().InventoryCacheOutcome(outcome)
		if outcome == "hit" {
			return result, nil
		}
	} else {
		s.metrics().InventoryCacheOutcome("bypass")
	}
	s.metrics().InventoryQuery()
	result := crawljobs.InventoryResult{Data: []vehicle.NormalizedVehicleListing{}, Limit: q.Limit, Offset: q.Offset}
	var clauses []string
	var args []any
	add := func(format string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(format, len(args)))
	}
	text := vehicle.NormalizeModelText(q.Model + " " + q.Text)
	if text != "" {
		add(`(l.search_vector @@ plainto_tsquery('simple',$%[1]d) OR l.normalized_text %% $%[1]d OR $%[1]d <%% l.normalized_text)`, text)
	}
	for _, token := range strings.Fields(text) {
		if token != "" && strings.Trim(token, "0123456789") == "" {
			add(`l.search_vector @@ plainto_tsquery('simple',$%d)`, token)
		}
	}
	if q.Type != "" {
		add(`l.vehicle_type=$%d`, q.Type)
	}
	if q.Platform != "" {
		add(`l.platform=$%d`, q.Platform)
	}
	if q.Body != "" {
		add(`l.body=$%d`, q.Body)
	}
	if q.City != "" && q.City != "iran" {
		add(`l.city=$%d`, q.City)
	}

	// Legacy category-only clients retain their exact search-membership filter.
	// Inventory-first clients provide model text to match facts from every crawl partition.
	if q.Category != "" && q.Model == "" {
		add(`EXISTS(SELECT 1 FROM search_listings m JOIN crawl_searches s ON s.id=m.search_id WHERE m.platform=l.platform AND m.listing_id=l.id AND s.criteria->>'bamaCategory'=$%d)`, q.Category)
	}
	for _, filter := range []struct {
		format string
		value  *float64
	}{{`l.price>0 AND l.price >= $%d`, q.MinPrice}, {`l.price>0 AND l.price <= $%d`, q.MaxPrice}, {`l.mileage>=0 AND l.mileage >= $%d`, q.MinMileage}, {`l.mileage>=0 AND l.mileage <= $%d`, q.MaxMileage}} {
		if filter.value != nil {
			if math.IsNaN(*filter.value) || math.IsInf(*filter.value, 0) {
				return result, errors.New("invalid numeric filter")
			}
			add(filter.format, *filter.value)
		}
	}
	if q.MinYear != nil {
		add(`l.year>0 AND l.year >= $%d`, *q.MinYear)
	}
	if q.MaxYear != nil {
		add(`l.year>0 AND l.year <= $%d`, *q.MaxYear)
	}
	clauses = append(clauses, `l.availability<>'unavailable' AND l.last_seen>now()-interval '7 days'`)
	where := strings.Join(clauses, " AND ")
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM listings l WHERE `+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	order := `l.last_seen DESC,l.platform,l.id`
	switch q.Sort {
	case "price-asc":
		order = `nullif(l.price,0) ASC NULLS LAST,l.platform,l.id`
	case "price-desc":
		order = `nullif(l.price,0) DESC NULLS LAST,l.platform,l.id`
	case "mileage-asc":
		order = `CASE WHEN l.mileage<0 THEN null ELSE l.mileage END ASC NULLS LAST,l.platform,l.id`
	case "year-desc":
		order = `nullif(l.year,0) DESC NULLS LAST,l.platform,l.id`
	case "relevance":
		if text != "" {
			order = `greatest(similarity(l.normalized_text,$1),word_similarity($1,l.normalized_text)) DESC,l.last_seen DESC,l.platform,l.id`
		}
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := s.Pool.Query(ctx, `SELECT listing_payload(l) FROM listings l WHERE `+where+` ORDER BY `+order+fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var row vehicle.NormalizedVehicleListing
		if err = rows.Scan(&raw); err != nil {
			return result, err
		}
		if err = json.Unmarshal(raw, &row); err != nil {
			return result, err
		}
		result.Data = append(result.Data, row)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if s.InventoryCache != nil {
		s.metrics().InventoryCacheWrite(s.InventoryCache.SetInventory(ctx, cacheKey, result))
	}
	return result, nil
}
