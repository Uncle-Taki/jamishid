package main

import (
	"context"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/postgres"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/rediscache"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/telemetry"
)

// probeDefaults bound each dependency probe independently. The poll's overall
// deadline is the sum of the two probe deadlines: every dependency gets its
// own bounded budget so a slow database can never starve the cache probe (and
// vice versa) into a false "down".
const (
	defaultPGProbeTimeout    = 3 * time.Second
	defaultRedisProbeTimeout = 3 * time.Second
)

// pgProbeStore is the read-only PostgreSQL slice the snapshot uses. It is an
// interface (not *postgres.Store) so the independent probe deadlines can be
// exercised against fakes that delay or fail.
type pgProbeStore interface {
	QueueCounts(ctx context.Context) ([]postgres.QueueCount, error)
	OldestReadyAges(ctx context.Context) ([]postgres.ReadyAge, error)
	RetryNextDue(ctx context.Context) (map[string]float64, error)
	InventoryCounts(ctx context.Context) ([]postgres.InventoryCount, error)
	Coverage(ctx context.Context) ([]postgres.CoverageStatus, error)
	ProxyCounts(ctx context.Context) ([]postgres.ProxyCount, error)
	ServiceStats(ctx context.Context) (postgres.ServiceStats, error)
	StorageSizes(ctx context.Context) (map[string]float64, error)
}

// redisProbe is the read-only Redis monitoring slice the snapshot uses.
type redisProbe interface {
	MonitoringStats(ctx context.Context) (rediscache.MonitoringStats, error)
}

// snapshotSource composes the read-only PostgreSQL and Redis snapshot queries
// into one telemetry snapshot.
//
// Each dependency is probed on its own bounded deadline derived from the poll
// context, so the two probes do not share a single deadline: a PostgreSQL query
// that exhausts its own budget cannot push an already-expired context at Redis
// and mark a healthy cache down (review finding C1). A dependency that was
// probed and failed reports down; a dependency that the poll's overall deadline
// prevented from probing stays unknown (last-good) rather than down.
//
// When Redis fails but PostgreSQL answers, the poll still succeeds: last-good
// Redis values are retained and redis_up=0 marks the outage. When PostgreSQL
// fails, the poll fails (queue/inventory state is only replaced by a fully
// successful poll) but Redis is still attempted and reported.
type snapshotSource struct {
	store pgProbeStore
	cache redisProbe
	// pgTimeout/redisTimeout bound each probe independently.
	pgTimeout, redisTimeout time.Duration
	// lastRedis retains the last-good Redis service values and budget cooldowns.
	lastRedis     telemetry.RedisService
	lastCooldowns []telemetry.AgeRow
	// redisEverObserved marks that Redis has been read at least once. A poll
	// that failed to read Redis still retains these last-good values, so the
	// measurements stay emit-able (not fabricated zeros) once observed, while a
	// never-observed cache emits nothing (review finding C3).
	redisEverObserved bool
}

func (s *snapshotSource) pgDeadline() time.Duration {
	if s.pgTimeout <= 0 {
		return defaultPGProbeTimeout
	}
	return s.pgTimeout
}

func (s *snapshotSource) redisDeadline() time.Duration {
	if s.redisTimeout <= 0 {
		return defaultRedisProbeTimeout
	}
	return s.redisTimeout
}

// probeContext derives an independently bounded probe context from the poll
// context. It returns the probe context, its cancel function, and whether the
// probe may still run. If the poll context's deadline has already passed when
// the probe starts (another dependency or the overall budget consumed it), the
// probe is skipped: ran is false and the caller must treat the dependency as
// unknown rather than down. Otherwise the probe runs for min(remaining,
// deadline). The returned cancel is always safe to call (it is a no-op when the
// probe was skipped).
func (s *snapshotSource) probeContext(ctx context.Context, deadline time.Duration) (context.Context, context.CancelFunc, bool) {
	if err := ctx.Err(); err != nil {
		return ctx, func() {}, false
	}
	if dl, ok := ctx.Deadline(); ok {
		remaining := time.Until(dl)
		if remaining <= 0 {
			return ctx, func() {}, false
		}
		if deadline > remaining {
			deadline = remaining
		}
	}
	probe, cancel := context.WithTimeout(ctx, deadline)
	return probe, cancel, true
}

// fillPostgresState runs every PostgreSQL-backed part of the snapshot. It stops
// at the first failure; the caller marks Postgres.Up accordingly.
func (s *snapshotSource) fillPostgresState(ctx context.Context, snap *telemetry.Snapshot) error {
	queues, err := s.store.QueueCounts(ctx)
	if err != nil {
		return err
	}
	for _, row := range queues {
		snap.QueueTasks = append(snap.QueueTasks, telemetry.QueueRow{Queue: row.Queue, Source: row.Source, State: row.State, Mode: row.Mode, Purpose: row.Purpose, Value: row.Count})
	}
	// A healthy empty queue must be distinguishable from a missing collector:
	// emit explicit zero rows for the backlog-facing states of every bounded
	// (queue, source) pair that has no real row. They sum to zero and never
	// change totals; mode/purpose use the "none" sentinel.
	zeroFill(snap)
	ages, err := s.store.OldestReadyAges(ctx)
	if err != nil {
		return err
	}
	// Empty queues use the documented zero-age convention.
	readySeen := map[string]bool{}
	for _, row := range ages {
		readySeen[row.Queue+":"+row.Source] = true
		snap.OldestReady = append(snap.OldestReady, telemetry.AgeRow(row))
	}
	for _, queue := range []string{"page", "detail"} {
		for _, source := range rediscache.Sources {
			if !readySeen[queue+":"+source] {
				snap.OldestReady = append(snap.OldestReady, telemetry.AgeRow{Queue: queue, Source: source, Seconds: 0})
			}
		}
	}
	due, err := s.store.RetryNextDue(ctx)
	if err != nil {
		return err
	}
	for _, queue := range []string{"retry_page", "retry_detail"} {
		snap.RetryNextDue = append(snap.RetryNextDue, telemetry.AgeRow{Queue: queue, Seconds: due[queue]})
	}
	inventory, err := s.store.InventoryCounts(ctx)
	if err != nil {
		return err
	}
	for _, row := range inventory {
		snap.Inventory = append(snap.Inventory, telemetry.InventoryRow{Source: row.Source, Type: row.Type, Availability: row.Availability, Freshness: row.Freshness, Value: row.Count})
	}
	coverage, err := s.store.Coverage(ctx)
	if err != nil {
		return err
	}
	for _, row := range coverage {
		entry := telemetry.CoverageRow{
			Source: row.Platform, Type: row.Type,
			Discovered: float64(row.Partitions), Complete: float64(row.Complete),
			Overdue: float64(row.Overdue), Active: float64(row.Active),
			CatalogError: row.CatalogError != "",
		}
		if row.LastDiscovery != nil {
			entry.LastSuccess = float64(row.LastDiscovery.Unix())
		}
		snap.Coverage = append(snap.Coverage, entry)
	}
	proxies, err := s.store.ProxyCounts(ctx)
	if err != nil {
		return err
	}
	for _, row := range proxies {
		snap.Proxies = append(snap.Proxies, telemetry.QueueRow{State: row.State, Value: row.Count})
	}
	stats, err := s.store.ServiceStats(ctx)
	if err != nil {
		return err
	}
	snap.Postgres = telemetry.PostgresService{
		Up: true, Connections: stats.Connections,
		XactCommit: stats.XactCommit, XactRollback: stats.XactRollback,
	}
	sizes, err := s.store.StorageSizes(ctx)
	if err != nil {
		return err
	}
	snap.Postgres.Storage = sizes
	return nil
}

// zeroFill emits explicit zero rows so a healthy, fully drained queue serves
// real zeros instead of absent series. Only the health-facing states are
// filled: page/detail "ready" and "running_expired" (backlog and stuck
// leases) and retry_page/retry_detail "due".
func zeroFill(snap *telemetry.Snapshot) {
	hasReady := map[string]bool{}
	hasDue := map[string]bool{}
	hasExpired := map[string]bool{}
	for _, row := range snap.QueueTasks {
		if row.State == "ready" {
			hasReady[row.Queue+":"+row.Source] = true
		}
		if row.State == "due" {
			hasDue[row.Queue+":"+row.Source] = true
		}
		if row.State == "running_expired" {
			hasExpired[row.Queue+":"+row.Source] = true
		}
	}
	for _, source := range rediscache.Sources {
		for _, queue := range []string{"page", "detail"} {
			if !hasReady[queue+":"+source] {
				snap.QueueTasks = append(snap.QueueTasks, telemetry.QueueRow{Queue: queue, Source: source, State: "ready", Mode: "none", Purpose: "none", Value: 0})
			}
			if !hasExpired[queue+":"+source] {
				snap.QueueTasks = append(snap.QueueTasks, telemetry.QueueRow{Queue: queue, Source: source, State: "running_expired", Mode: "none", Purpose: "none", Value: 0})
			}
		}
		if !hasDue["retry_page:"+source] {
			snap.QueueTasks = append(snap.QueueTasks, telemetry.QueueRow{Queue: "retry_page", Source: source, State: "due", Mode: "none", Purpose: "none", Value: 0})
		}
		if !hasDue["retry_detail:"+source] {
			snap.QueueTasks = append(snap.QueueTasks, telemetry.QueueRow{Queue: "retry_detail", Source: source, State: "due", Mode: "none", Purpose: "none", Value: 0})
		}
	}
}

func (s *snapshotSource) Snapshot(ctx context.Context) (telemetry.Snapshot, error) {
	snap := telemetry.Snapshot{}

	// PostgreSQL probe: its own bounded deadline, independent of Redis.
	pgCtx, pgCancel, pgRan := s.probeContext(ctx, s.pgDeadline())
	defer pgCancel()
	var pgErr error
	if pgRan {
		pgErr = s.fillPostgresState(pgCtx, &snap)
		snap.PostgresHealthKnown = true
		snap.Postgres.Up = pgErr == nil
	}

	// Redis probe: independently bounded so a PostgreSQL timeout cannot push an
	// already-expired context at a healthy cache. Redis is independent of
	// PostgreSQL and is attempted on every poll even when the PostgreSQL part
	// failed, so redis_up stays current as well.
	if s.cache != nil {
		redisCtx, redisCancel, redisRan := s.probeContext(ctx, s.redisDeadline())
		defer redisCancel()
		if redisRan {
			redisStats, redisErr := s.cache.MonitoringStats(redisCtx)
			snap.RedisHealthKnown = true
			if redisErr != nil {
				// Retain last-good values; availability marks the outage. The
				// measurements stay emit-able once Redis has been observed at
				// least once (last-good, not fabricated zeros); a never-observed
				// cache emits nothing.
				snap.Redis = s.lastRedis
				snap.Redis.Up = false
				// Write capability is NOT a last-good value: an unreachable Redis
				// cannot accept writes on this poll, so a prior accepted write must
				// not be re-emitted as current. The last-good memory and counters
				// above are retained; only the capability flag is cleared.
				snap.Redis.WriteOK = false
				snap.RedisObserved = s.redisEverObserved
				snap.BudgetCooldowns = s.lastCooldowns
			} else {
				snap.Redis = telemetry.RedisService{
					Up: true, UsedMemory: redisStats.UsedMemory, MaxMemory: redisStats.MaxMemory,
					Hits: redisStats.Hits, Misses: redisStats.Misses, Evicted: redisStats.Evicted, Clients: redisStats.Clients,
					WriteOK: redisStats.WriteProbeOK,
				}
				snap.RedisObserved = true
				s.redisEverObserved = true
				s.lastRedis = snap.Redis
				snap.BudgetCooldowns = nil
				for _, source := range rediscache.Sources {
					snap.BudgetCooldowns = append(snap.BudgetCooldowns, telemetry.AgeRow{Queue: "budget", Source: source, Seconds: redisStats.Cooldowns[source]})
				}
				s.lastCooldowns = snap.BudgetCooldowns
			}
		}
	}

	// The poll fails only when the PostgreSQL part was probed and failed. A
	// skipped probe (the overall deadline ran out) is reported as a poll error
	// so the collector marks staleness rather than serving partial data.
	if pgRan {
		if pgErr != nil {
			// The queue/inventory state is last-good only after a prior success;
			// the dependency measurements were not read this poll.
			snap.PostgresObserved = false
			// A first partial poll must not masquerade as a complete inventory
			// snapshot. The collector retains prior complete PostgreSQL data.
			snap.QueueTasks = nil
			snap.OldestReady = nil
			snap.RetryNextDue = nil
			snap.Inventory = nil
			snap.Coverage = nil
			snap.Proxies = nil
			snap.Postgres = telemetry.PostgresService{Up: false}
			return snap, pgErr
		}
		snap.PostgresObserved = true
		return snap, nil
	}
	return snap, ctx.Err()
}
