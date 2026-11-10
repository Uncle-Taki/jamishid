package postgres

import (
	"context"
)

// Metrics receives store persistence transitions counted only after a
// successful commit. A nil Metrics is valid and disables instrumentation.
type Metrics interface {
	RetryScheduled(kind string)
	RetryReleased(kind string)
	// RetryInvalidated counts the actual number of detail tickets removed by a
	// page commit, not one per commit (a commit can invalidate many tickets).
	RetryInvalidated(kind string, tickets int64)
	// RetryDiscarded counts only tickets whose delete really removed a row;
	// a repeated concurrent release attempt that deletes nothing is not a
	// discarded ticket.
	RetryDiscarded(kind string)
	InventoryCacheOutcome(outcome string)
	// InventoryCacheWrite records inventory cache SET outcomes (ok/error) so
	// noeviction/write failures are visible; fallback semantics are unchanged.
	InventoryCacheWrite(outcome string)
	InventoryQuery()
}

type noopMetrics struct{}

func (noopMetrics) RetryScheduled(string)          {}
func (noopMetrics) RetryReleased(string)           {}
func (noopMetrics) RetryInvalidated(string, int64) {}
func (noopMetrics) RetryDiscarded(string)          {}
func (noopMetrics) InventoryCacheOutcome(string)   {}
func (noopMetrics) InventoryCacheWrite(string)     {}
func (noopMetrics) InventoryQuery()                {}

func (s *Store) metrics() Metrics {
	if s.Metrics == nil {
		return noopMetrics{}
	}
	return s.Metrics
}

// QueueCount is one derived queue gauge sample. The derivation mirrors the
// claim/release predicates in store.go, details.go and retries.go:
//
//   - terminal task states are reported verbatim,
//   - tasks of paused parent jobs (and tasks paused themselves) are "paused"
//     and never counted as runnable,
//   - "delayed" tasks are the parent rows of crawler_retries tickets; the same
//     pending work is visible in the retry queue and must not be double counted.
//     Delayed tasks stay releasable under any non-paused parent (releaseRetry
//     only skips paused jobs), so parent eligibility is not applied here,
//   - "blocked" tasks are stored state a worker can never claim: a parent job
//     outside ('queued','running'), a leaseless "running" row, or a stored
//     status outside the known lifecycle. It is not runnable work,
//   - "ready" means claimable now (queued/retry with available_at<=now and an
//     active parent in ('queued','running')); future work is "scheduled",
//   - running tasks split into valid leases and expired (reclaimable) leases;
//     Claim treats a NULL lease_until as not reclaimable, so monitoring does
//     too.
//
// TestQueueSnapshotMatchesClaimAPI proves the ready/running_expired
// classification against the real Claim/ClaimDetail calls rather than a second
// copy of this expression.
type QueueCount struct {
	Queue, Source, State, Mode, Purpose string
	Count                               float64
}

// boundedSource/…/boundedPurpose normalize stored values to fixed label sets so
// a stray row can never create unbounded metric label cardinality.
func boundedSource(value string) string {
	switch value {
	case "divar", "bama", "sheypoor":
		return value
	default:
		return "other"
	}
}

func boundedMode(value string) string {
	switch value {
	case "incremental", "full", "none":
		return value
	default:
		return "other"
	}
}

func boundedPurpose(value string) string {
	switch value {
	case "enrich", "verify", "none":
		return value
	default:
		return "other"
	}
}

func (s *Store) QueueCounts(ctx context.Context) ([]QueueCount, error) {
	rows, err := s.Pool.Query(ctx, `
	SELECT 'page' AS queue, t.platform AS source,
	  CASE
	    WHEN t.status IN ('complete','limited','unavailable') THEN t.status
	    WHEN t.status = 'paused' OR j.status = 'paused' THEN 'paused'
	    WHEN t.status = 'delayed' THEN 'delayed'
	    WHEN j.status NOT IN ('queued','running') THEN 'blocked'
	    WHEN t.status IN ('queued','retry') AND t.available_at <= now() THEN 'ready'
	    WHEN t.status IN ('queued','retry') THEN 'scheduled'
	    WHEN t.status = 'running' AND t.lease_until IS NOT NULL AND t.lease_until > now() THEN 'running_valid'
	    WHEN t.status = 'running' AND t.lease_until IS NOT NULL THEN 'running_expired'
	    ELSE 'blocked'
	  END AS state,
	  j.mode AS mode, 'none' AS purpose, count(*)::float8
	FROM crawl_tasks t JOIN crawl_jobs j ON j.id = t.job_id
	GROUP BY 1, 2, 3, 4, 5
UNION ALL
	SELECT 'detail' AS queue, d.platform AS source,
	  CASE
	    WHEN d.status IN ('complete','failed') THEN d.status
	    WHEN d.status = 'delayed' THEN 'delayed'
	    WHEN d.status IN ('queued','retry') AND d.available_at <= now() THEN 'ready'
	    WHEN d.status IN ('queued','retry') THEN 'scheduled'
	    WHEN d.status = 'running' AND d.lease_until IS NOT NULL AND d.lease_until > now() THEN 'running_valid'
	    WHEN d.status = 'running' AND d.lease_until IS NOT NULL THEN 'running_expired'
	    ELSE 'blocked'
	  END AS state,
	  'none' AS mode, d.purpose, count(*)::float8
	FROM detail_tasks d
	GROUP BY 1, 2, 3, 4, 5
UNION ALL
SELECT 'retry_' || r.kind AS queue, r.platform AS source,
  CASE
    WHEN r.kind = 'detail' AND NOT EXISTS (
      SELECT 1 FROM detail_tasks d
      WHERE d.platform = r.platform AND d.listing_id = r.subject_id
        AND d.status = 'delayed' AND d.summary_hash = r.summary_hash
    ) THEN 'orphaned'
    WHEN r.kind = 'page' AND EXISTS (
      SELECT 1 FROM crawl_jobs j WHERE j.id = r.job_id AND j.status = 'paused'
    ) THEN 'paused'
    WHEN r.kind = 'page' AND NOT EXISTS (
      SELECT 1 FROM crawl_tasks t
      WHERE t.job_id = r.job_id AND t.platform = r.platform AND t.status = 'delayed'
    ) THEN 'orphaned'
    WHEN r.available_at <= now() THEN 'due'
    ELSE 'future'
  END AS state,
  'none' AS mode, 'none' AS purpose, count(*)::float8
FROM crawler_retries r
GROUP BY 1, 2, 3, 4, 5`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Merge after normalization: two distinct raw source/mode/purpose values
	// (for example "divar" and "divar-legacy") both normalize to the bounded
	// label set, and exporting them as separate rows would create duplicate
	// Prometheus label sets that fail the whole registry gather. Merging by the
	// normalized key keeps counts summing exactly once.
	byKey := map[string]int{}
	result := []QueueCount{}
	for rows.Next() {
		var row QueueCount
		if err := rows.Scan(&row.Queue, &row.Source, &row.State, &row.Mode, &row.Purpose, &row.Count); err != nil {
			return nil, err
		}
		row.Source = boundedSource(row.Source)
		row.Mode = boundedMode(row.Mode)
		row.Purpose = boundedPurpose(row.Purpose)
		key := row.Queue + "\x00" + row.Source + "\x00" + row.State + "\x00" + row.Mode + "\x00" + row.Purpose
		if idx, exists := byKey[key]; exists {
			result[idx].Count += row.Count
		} else {
			byKey[key] = len(result)
			result = append(result, row)
		}
	}
	return result, rows.Err()
}

// ReadyAge is the overdue eligibility age of the oldest ready task per queue
// and source. available_at is eligibility time; it is not enqueue age.
type ReadyAge struct {
	Queue, Source string
	Seconds       float64
}

func (s *Store) OldestReadyAges(ctx context.Context) ([]ReadyAge, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT 'page', t.platform, coalesce(extract(epoch FROM (now() - min(t.available_at))), 0)
FROM crawl_tasks t JOIN crawl_jobs j ON j.id = t.job_id
WHERE j.status IN ('queued','running') AND t.status IN ('queued','retry') AND t.available_at <= now()
GROUP BY 1, 2
UNION ALL
SELECT 'detail', d.platform, coalesce(extract(epoch FROM (now() - min(d.available_at))), 0)
FROM detail_tasks d
WHERE d.status IN ('queued','retry') AND d.available_at <= now()
GROUP BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Bound the source label and merge rows that collapse to the same bounded
	// key: the query groups by raw platform, so two stray platforms that both
	// normalize to "other" would otherwise emit a duplicate (queue, source)
	// label set. The age is the oldest ready task, so a merged bucket keeps the
	// maximum age, not a sum.
	byKey := map[string]int{}
	result := []ReadyAge{}
	for rows.Next() {
		var row ReadyAge
		if err := rows.Scan(&row.Queue, &row.Source, &row.Seconds); err != nil {
			return nil, err
		}
		row.Source = boundedSource(row.Source)
		key := row.Queue + "\x00" + row.Source
		if idx, exists := byKey[key]; exists {
			if row.Seconds > result[idx].Seconds {
				result[idx].Seconds = row.Seconds
			}
		} else {
			byKey[key] = len(result)
			result = append(result, row)
		}
	}
	return result, rows.Err()
}

// RetryNextDue returns the next future ticket due time per retry queue; 0 when
// no releasable ticket is scheduled. Only tickets releaseRetries would act on
// count: page tickets need a non-paused parent job and a matching delayed task
// (otherwise the release discards the ticket without producing work); detail
// tickets need a delayed task with the same summary hash.
func (s *Store) RetryNextDue(ctx context.Context) (map[string]float64, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT 'retry_page', coalesce(extract(epoch FROM min(r.available_at)), 0)
FROM crawler_retries r
WHERE r.kind='page' AND r.available_at > now()
  AND EXISTS(SELECT 1 FROM crawl_jobs j WHERE j.id=r.job_id AND j.status <> 'paused')
  AND EXISTS(SELECT 1 FROM crawl_tasks t WHERE t.job_id=r.job_id AND t.platform=r.platform AND t.status='delayed')
UNION ALL
SELECT 'retry_detail', coalesce(extract(epoch FROM min(r.available_at)), 0)
FROM crawler_retries r
WHERE r.kind='detail' AND r.available_at > now()
  AND EXISTS(
    SELECT 1 FROM detail_tasks d
    WHERE d.platform=r.platform AND d.listing_id=r.subject_id
      AND d.status='delayed' AND d.summary_hash=r.summary_hash
  )`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]float64{}
	for rows.Next() {
		var queue string
		var due float64
		if err := rows.Scan(&queue, &due); err != nil {
			return nil, err
		}
		result[queue] = due
	}
	return result, rows.Err()
}

// InventoryCount counts stored listings by bounded categories. Freshness
// follows the seven-day inventory visibility window used by Search.
type InventoryCount struct {
	Source, Type, Availability, Freshness string
	Count                                 float64
}

func (s *Store) InventoryCounts(ctx context.Context) ([]InventoryCount, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT platform, vehicle_type, availability,
  CASE WHEN last_seen > now() - interval '7 days' THEN 'fresh' ELSE 'stale' END,
  count(*)::float8
FROM listings GROUP BY 1, 2, 3, 4`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Merge after normalization for the same duplicate-label-set reason as
	// QueueCounts: distinct raw type/availability values that collapse to
	// "other" must sum into one exported series.
	byKey := map[string]int{}
	result := []InventoryCount{}
	for rows.Next() {
		var row InventoryCount
		if err := rows.Scan(&row.Source, &row.Type, &row.Availability, &row.Freshness, &row.Count); err != nil {
			return nil, err
		}
		row.Source = boundedSource(row.Source)
		if row.Type != "car" && row.Type != "motorcycle" {
			row.Type = "other"
		}
		if row.Availability != "active" && row.Availability != "unavailable" {
			row.Availability = "other"
		}
		key := row.Source + "\x00" + row.Type + "\x00" + row.Availability + "\x00" + row.Freshness
		if idx, exists := byKey[key]; exists {
			result[idx].Count += row.Count
		} else {
			byKey[key] = len(result)
			result = append(result, row)
		}
	}
	return result, rows.Err()
}

// StorageSizes reports database and table storage in bytes.
func (s *Store) StorageSizes(ctx context.Context) (map[string]float64, error) {
	var database, listings float64
	err := s.Pool.QueryRow(ctx, `SELECT pg_database_size(current_database()), pg_total_relation_size('listings')`).Scan(&database, &listings)
	if err != nil {
		return nil, err
	}
	return map[string]float64{"database": database, "listings_table": listings}, nil
}

// ProxyCount reports the egress proxy pool by observed state; no endpoints.
type ProxyCount struct {
	State string
	Count float64
}

func (s *Store) ProxyCounts(ctx context.Context) ([]ProxyCount, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT CASE WHEN NOT enabled THEN 'disabled' WHEN cooldown_until > now() THEN 'cooling' ELSE 'eligible' END, count(*)::float8
FROM egress_proxies GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProxyCount{}
	for rows.Next() {
		var row ProxyCount
		if err := rows.Scan(&row.State, &row.Count); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// ServiceStats reports PostgreSQL activity and throughput for the application
// database.
type ServiceStats struct {
	Connections  map[string]float64
	XactCommit   float64
	XactRollback float64
}

func (s *Store) ServiceStats(ctx context.Context) (ServiceStats, error) {
	var stats ServiceStats
	stats.Connections = map[string]float64{}
	rows, err := s.Pool.Query(ctx, `SELECT coalesce(state, 'none'), count(*)::float8 FROM pg_stat_activity WHERE datname = current_database() GROUP BY 1`)
	if err != nil {
		return stats, err
	}
	for rows.Next() {
		var state string
		var count float64
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			return stats, err
		}
		stats.Connections[state] = count
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return stats, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT xact_commit::float8, xact_rollback::float8 FROM pg_stat_database WHERE datname = current_database()`).Scan(&stats.XactCommit, &stats.XactRollback)
	return stats, err
}

// SnapshotTaken reports whether the monitoring queries are answering; used for
// availability probes.
func (s *Store) Ping(ctx context.Context) error {
	return s.Pool.Ping(ctx)
}
