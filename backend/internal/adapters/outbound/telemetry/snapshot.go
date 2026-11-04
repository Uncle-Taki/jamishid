package telemetry

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// QueueRow is one bounded-label queue gauge sample.
type QueueRow struct {
	Queue, Source, State, Mode, Purpose string
	Value                               float64
}

// AgeRow is the overdue-eligibility age of the oldest ready task, or a
// cooldown remaining, keyed by queue/source.
type AgeRow struct {
	Queue, Source string
	Seconds       float64
}

// InventoryRow counts stored listings by bounded categories.
type InventoryRow struct {
	Source, Type, Availability, Freshness string
	Value                                 float64
}

// CoverageRow holds per source/type coverage partition counts.
type CoverageRow struct {
	Source, Type                          string
	Discovered, Complete, Overdue, Active float64
	LastSuccess                           float64 // unix seconds; 0 = never
	CatalogError                          bool
}

// PostgresService holds PostgreSQL availability, activity and throughput.
type PostgresService struct {
	Up           bool
	Connections  map[string]float64
	XactCommit   float64
	XactRollback float64
	Storage      map[string]float64
}

// RedisService holds Redis availability and memory/cache counters from INFO.
type RedisService struct {
	Up                             bool
	UsedMemory, MaxMemory          float64
	Hits, Misses, Evicted, Clients float64
}

// Snapshot is the complete shared state collected by the designated collector.
type Snapshot struct {
	TakenAt         time.Time
	QueueTasks      []QueueRow
	OldestReady     []AgeRow
	RetryNextDue    []AgeRow // Seconds is a unix timestamp; 0 = none scheduled
	Inventory       []InventoryRow
	Coverage        []CoverageRow
	Proxies         []QueueRow // State carries the proxy pool state
	Postgres        PostgresService
	Redis           RedisService
	BudgetCooldowns []AgeRow
}

// SnapshotSource produces snapshots; it must be safe for concurrent use and
// must respect context timeouts.
type SnapshotSource interface {
	Snapshot(context.Context) (Snapshot, error)
}

type snapshotCollector struct {
	current atomic.Pointer[Snapshot]

	// Poll health, exported with the collector label only after the first
	// poll attempt so non-collector processes never serve false zeros.
	started     atomic.Bool
	succeeded   atomic.Bool
	lastGood    atomic.Int64 // unix seconds
	durationNs  atomic.Int64 // last poll duration
	errorsCount atomic.Uint64
}

func newSnapshotCollector() *snapshotCollector { return &snapshotCollector{} }

// collectorLabel marks shared snapshot metrics so exactly one designated
// collector process must enable JAMSHID_MONITORING_COLLECTOR.
const collectorLabel = "primary"

func snapshotDesc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(name, help, labels, prometheus.Labels{"collector": collectorLabel})
}

var (
	queueDesc          = snapshotDesc("jamshid_queue_tasks", "Durable queue tasks by derived state; a delayed task and its retry ticket are the same pending work.", "queue", "source", "state", "mode", "purpose")
	oldestReadyDesc    = snapshotDesc("jamshid_queue_oldest_ready_age_seconds", "Overdue eligibility age of the oldest ready task; 0 when the queue has no ready work.", "queue", "source")
	retryNextDueDesc   = snapshotDesc("jamshid_queue_retry_next_due_timestamp_seconds", "Unix timestamp of the next due delayed retry ticket; 0 when none is scheduled.", "queue")
	inventoryDesc      = snapshotDesc("jamshid_inventory_listings", "Stored listings by source, type, availability and freshness.", "source", "type", "availability", "freshness")
	coverageDesc       = snapshotDesc("jamshid_coverage_partitions", "Coverage partitions by source, type and state.", "source", "type", "state")
	coverageErrorDesc  = snapshotDesc("jamshid_coverage_catalog_failures", "1 when the coverage catalog for a source and type recorded an error.", "source", "type")
	coverageLastDesc   = snapshotDesc("jamshid_coverage_last_success_timestamp_seconds", "Unix timestamp of the last successful coverage catalog refresh; 0 when never.", "source", "type")
	proxyDesc          = snapshotDesc("jamshid_proxy_pool", "Egress proxy pool size by observed state.", "state")
	postgresUpDesc     = snapshotDesc("jamshid_postgres_up", "1 when the last PostgreSQL snapshot poll succeeded.")
	pgConnectionsDesc  = snapshotDesc("jamshid_postgres_connections", "PostgreSQL connections by activity state.", "state")
	pgXactDesc         = snapshotDesc("jamshid_postgres_xact_total", "PostgreSQL transactions by outcome since server start.", "outcome")
	pgStorageDesc      = snapshotDesc("jamshid_storage_bytes", "Storage used by scope.", "scope")
	redisUpDesc        = snapshotDesc("jamshid_redis_up", "1 when the last Redis snapshot poll succeeded.")
	redisMemoryDesc    = snapshotDesc("jamshid_redis_memory_used_bytes", "Redis used memory.")
	redisMaxMemoryDesc = snapshotDesc("jamshid_redis_memory_max_bytes", "Redis max memory limit (0 when unlimited).")
	redisHitsDesc      = snapshotDesc("jamshid_redis_keyspace_hits_total", "Redis keyspace hits since server start.")
	redisMissesDesc    = snapshotDesc("jamshid_redis_keyspace_misses_total", "Redis keyspace misses since server start.")
	redisEvictedDesc   = snapshotDesc("jamshid_redis_evicted_keys_total", "Redis evicted keys since server start.")
	redisClientsDesc   = snapshotDesc("jamshid_redis_connected_clients", "Redis connected clients.")
	budgetCooldownDesc = snapshotDesc("jamshid_source_budget_cooldown_seconds", "Observed shared source budget cooldown remaining; never provider quota remaining.", "source")

	snapshotSuccessDesc  = snapshotDesc("jamshid_monitoring_snapshot_success", "1 when the last monitoring snapshot poll succeeded.")
	snapshotLastGoodDesc = snapshotDesc("jamshid_monitoring_last_success_timestamp_seconds", "Unix timestamp of the last successful monitoring snapshot poll.")
	snapshotDurationDesc = snapshotDesc("jamshid_monitoring_snapshot_duration_seconds", "Duration of the last monitoring snapshot poll.")
	snapshotErrorsDesc   = snapshotDesc("jamshid_monitoring_snapshot_errors_total", "Monitoring snapshot poll failures.")
)

func (c *snapshotCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{queueDesc, oldestReadyDesc, retryNextDueDesc, inventoryDesc, coverageDesc, coverageErrorDesc, coverageLastDesc, proxyDesc, postgresUpDesc, pgConnectionsDesc, pgXactDesc, pgStorageDesc, redisUpDesc, redisMemoryDesc, redisMaxMemoryDesc, redisHitsDesc, redisMissesDesc, redisEvictedDesc, redisClientsDesc, budgetCooldownDesc, snapshotSuccessDesc, snapshotLastGoodDesc, snapshotDurationDesc, snapshotErrorsDesc} {
		ch <- desc
	}
}

func (c *snapshotCollector) Collect(ch chan<- prometheus.Metric) {
	// Poll health is exported even when no snapshot has ever been stored (for
	// example when every poll times out): a failing collector must be visible,
	// never silently empty. Series appear only after the first poll attempt
	// so processes that never run a collector serve nothing.
	if c.started.Load() {
		success := 0.0
		if c.succeeded.Load() {
			success = 1
		}
		ch <- prometheus.MustNewConstMetric(snapshotSuccessDesc, prometheus.GaugeValue, success)
		ch <- prometheus.MustNewConstMetric(snapshotLastGoodDesc, prometheus.GaugeValue, float64(c.lastGood.Load()))
		ch <- prometheus.MustNewConstMetric(snapshotDurationDesc, prometheus.GaugeValue, float64(c.durationNs.Load())/1e9)
		ch <- prometheus.MustNewConstMetric(snapshotErrorsDesc, prometheus.CounterValue, float64(c.errorsCount.Load()))
	}
	snap := c.current.Load()
	if snap == nil {
		return
	}
	emit := func(desc *prometheus.Desc, t prometheus.ValueType, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, t, v, labels...)
	}
	for _, row := range snap.QueueTasks {
		emit(queueDesc, prometheus.GaugeValue, row.Value, row.Queue, row.Source, row.State, row.Mode, row.Purpose)
	}
	for _, row := range snap.OldestReady {
		emit(oldestReadyDesc, prometheus.GaugeValue, row.Seconds, row.Queue, row.Source)
	}
	for _, row := range snap.RetryNextDue {
		emit(retryNextDueDesc, prometheus.GaugeValue, row.Seconds, row.Queue)
	}
	for _, row := range snap.Inventory {
		emit(inventoryDesc, prometheus.GaugeValue, row.Value, row.Source, row.Type, row.Availability, row.Freshness)
	}
	for _, row := range snap.Coverage {
		emit(coverageDesc, prometheus.GaugeValue, row.Discovered, row.Source, row.Type, "discovered")
		emit(coverageDesc, prometheus.GaugeValue, row.Complete, row.Source, row.Type, "complete")
		emit(coverageDesc, prometheus.GaugeValue, row.Overdue, row.Source, row.Type, "overdue")
		emit(coverageDesc, prometheus.GaugeValue, row.Active, row.Source, row.Type, "active")
		emit(coverageLastDesc, prometheus.GaugeValue, row.LastSuccess, row.Source, row.Type)
		if row.CatalogError {
			emit(coverageErrorDesc, prometheus.GaugeValue, 1, row.Source, row.Type)
		} else {
			emit(coverageErrorDesc, prometheus.GaugeValue, 0, row.Source, row.Type)
		}
	}
	for _, row := range snap.Proxies {
		emit(proxyDesc, prometheus.GaugeValue, row.Value, row.State)
	}
	up := 0.0
	if snap.Postgres.Up {
		up = 1
	}
	emit(postgresUpDesc, prometheus.GaugeValue, up)
	for state, v := range snap.Postgres.Connections {
		emit(pgConnectionsDesc, prometheus.GaugeValue, v, state)
	}
	emit(pgXactDesc, prometheus.CounterValue, snap.Postgres.XactCommit, "commit")
	emit(pgXactDesc, prometheus.CounterValue, snap.Postgres.XactRollback, "rollback")
	for scope, v := range snap.Postgres.Storage {
		emit(pgStorageDesc, prometheus.GaugeValue, v, scope)
	}
	up = 0.0
	if snap.Redis.Up {
		up = 1
	}
	emit(redisUpDesc, prometheus.GaugeValue, up)
	emit(redisMemoryDesc, prometheus.GaugeValue, snap.Redis.UsedMemory)
	emit(redisMaxMemoryDesc, prometheus.GaugeValue, snap.Redis.MaxMemory)
	emit(redisHitsDesc, prometheus.CounterValue, snap.Redis.Hits)
	emit(redisMissesDesc, prometheus.CounterValue, snap.Redis.Misses)
	emit(redisEvictedDesc, prometheus.CounterValue, snap.Redis.Evicted)
	emit(redisClientsDesc, prometheus.GaugeValue, snap.Redis.Clients)
	for _, row := range snap.BudgetCooldowns {
		emit(budgetCooldownDesc, prometheus.GaugeValue, row.Seconds, row.Source)
	}
}

// store replaces the served snapshot atomically; obsolete label series disappear.
func (c *snapshotCollector) store(snap *Snapshot) { c.current.Store(snap) }

// RunSnapshotCollector polls the source on a bounded interval until ctx is
// canceled. Polls never overlap; each poll has its own timeout. Failed polls
// retain the last-good snapshot and mark staleness through
// jamshid_monitoring_snapshot_success=0; values are never replaced with zeros.
// Poll-health metrics are exported with the collector label only by the
// process running this collector.
func (o *Observer) RunSnapshotCollector(ctx context.Context, source SnapshotSource, interval, timeout time.Duration) {
	if source == nil {
		return
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			pollCtx, cancel := context.WithTimeout(ctx, timeout)
			start := time.Now()
			snap, err := source.Snapshot(pollCtx)
			cancel()
			collector := o.snapshotCollector
			collector.started.Store(true)
			collector.durationNs.Store(int64(time.Since(start)))
			if err != nil {
				if ctx.Err() == nil {
					collector.errorsCount.Add(1)
					collector.succeeded.Store(false)
					o.logger.Error("monitoring snapshot failed", "error", err.Error())
				}
			} else {
				snap.TakenAt = time.Now()
				collector.current.Store(&snap)
				collector.succeeded.Store(true)
				collector.lastGood.Store(snap.TakenAt.Unix())
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// CurrentSnapshot exposes the last-good snapshot for tests.
func (o *Observer) CurrentSnapshot() *Snapshot { return o.snapshotCollector.current.Load() }
