package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/postgres"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/rediscache"
)

// fakePG implements pgProbeStore. Each method honors ctx cancellation and can
// be told to block (respecting the probe deadline) or fail. QueueCounts is the
// first query, so it is where a slow database would consume the budget.
type fakePG struct {
	mu      sync.Mutex
	delay   time.Duration
	err     error
	queries int
}

func (f *fakePG) beginQuery(ctx context.Context) error {
	f.mu.Lock()
	f.queries++
	delay, err := f.delay, f.err
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (f *fakePG) QueueCounts(ctx context.Context) ([]postgres.QueueCount, error) {
	if err := f.beginQuery(ctx); err != nil {
		return nil, err
	}
	return nil, nil
}
func (f *fakePG) OldestReadyAges(ctx context.Context) ([]postgres.ReadyAge, error) {
	return nil, f.beginQuery(ctx)
}
func (f *fakePG) RetryNextDue(ctx context.Context) (map[string]float64, error) {
	if err := f.beginQuery(ctx); err != nil {
		return nil, err
	}
	return map[string]float64{}, nil
}
func (f *fakePG) InventoryCounts(ctx context.Context) ([]postgres.InventoryCount, error) {
	return nil, f.beginQuery(ctx)
}
func (f *fakePG) Coverage(ctx context.Context) ([]postgres.CoverageStatus, error) {
	return nil, f.beginQuery(ctx)
}
func (f *fakePG) ProxyCounts(ctx context.Context) ([]postgres.ProxyCount, error) {
	return nil, f.beginQuery(ctx)
}
func (f *fakePG) ServiceStats(ctx context.Context) (postgres.ServiceStats, error) {
	if err := f.beginQuery(ctx); err != nil {
		return postgres.ServiceStats{}, err
	}
	return postgres.ServiceStats{Connections: map[string]float64{"active": 1}}, nil
}
func (f *fakePG) StorageSizes(ctx context.Context) (map[string]float64, error) {
	return nil, f.beginQuery(ctx)
}

// fakeRedis implements redisProbe; it can block (respecting the probe deadline)
// or fail independently of PostgreSQL.
type fakeRedis struct {
	delay time.Duration
	err   error
	stats rediscache.MonitoringStats
}

func (f *fakeRedis) MonitoringStats(ctx context.Context) (rediscache.MonitoringStats, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return rediscache.MonitoringStats{}, ctx.Err()
		}
	}
	if f.err != nil {
		return rediscache.MonitoringStats{}, f.err
	}
	return f.stats, nil
}

// TestSnapshotPGTimeoutDoesNotImplicateRedis proves a slow PostgreSQL that
// exhausts its own probe budget does NOT push an already-expired context at a
// healthy Redis: Redis is probed on its own deadline and reports UP, while the
// poll still fails on the PostgreSQL error (review finding C1).
func TestSnapshotPGTimeoutDoesNotImplicateRedis(t *testing.T) {
	pg := &fakePG{delay: 500 * time.Millisecond} // far beyond the 100ms probe budget
	redis := &fakeRedis{stats: rediscache.MonitoringStats{Up: true, UsedMemory: 1024, Cooldowns: map[string]float64{}}}
	source := &snapshotSource{store: pg, cache: redis, pgTimeout: 100 * time.Millisecond, redisTimeout: 100 * time.Millisecond}

	pollCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := source.Snapshot(pollCtx)
	if err == nil {
		t.Fatal("poll must fail when PostgreSQL times out")
	}
	if !source.lastRedis.Up {
		t.Fatalf("healthy Redis must be observed UP, got down (shared-deadline regression): %#v", source.lastRedis)
	}
	if source.lastRedis.UsedMemory != 1024 {
		t.Fatalf("healthy Redis measurement lost: %v", source.lastRedis.UsedMemory)
	}
}

// TestSnapshotRedisTimeoutWithHealthyPG proves an independent Redis timeout is
// reported as a Redis outage (redis_up=0) while PostgreSQL stays UP and the
// poll succeeds (Redis failure does not fail the poll).
func TestSnapshotRedisTimeoutWithHealthyPG(t *testing.T) {
	pg := &fakePG{}
	redis := &fakeRedis{delay: 500 * time.Millisecond} // beyond the 100ms probe budget
	source := &snapshotSource{store: pg, cache: redis, pgTimeout: 100 * time.Millisecond, redisTimeout: 100 * time.Millisecond}

	pollCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snap, err := source.Snapshot(pollCtx)
	if err != nil {
		t.Fatalf("poll must succeed when only Redis times out (last-good retained): %v", err)
	}
	if !snap.Postgres.Up {
		t.Fatalf("healthy PostgreSQL must stay UP")
	}
	if !snap.RedisHealthKnown {
		t.Fatalf("Redis availability must be current (known), not skipped")
	}
	if snap.Redis.Up {
		t.Fatalf("timed-out Redis must be reported down, not up")
	}
	if snap.RedisObserved {
		t.Fatalf("unread Redis must not be marked observed (would fabricate zeros)")
	}
}

// TestSnapshotBothUnavailable proves both dependencies down reports both as
// down and the poll fails on the PostgreSQL error.
func TestSnapshotBothUnavailable(t *testing.T) {
	pg := &fakePG{err: errors.New("pg down")}
	redis := &fakeRedis{err: errors.New("redis down")}
	source := &snapshotSource{store: pg, cache: redis, pgTimeout: 100 * time.Millisecond, redisTimeout: 100 * time.Millisecond}

	pollCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snap, err := source.Snapshot(pollCtx)
	if err == nil {
		t.Fatal("poll must fail when PostgreSQL is down")
	}
	if snap.Postgres.Up || !snap.PostgresHealthKnown {
		t.Fatalf("PostgreSQL must be known and down: %+v", snap.Postgres)
	}
	if snap.Redis.Up || !snap.RedisHealthKnown {
		t.Fatalf("Redis must be known and down: %+v", snap.Redis)
	}
}

// TestSnapshotShutdownCancellationSkipsProbes proves an already-canceled poll
// context does not run either probe (both dependencies stay unknown) and the
// poll reports the cancellation. A canceled probe must not be misreported as a
// healthy dependency down.
func TestSnapshotShutdownCancellationSkipsProbes(t *testing.T) {
	pg := &fakePG{}
	redis := &fakeRedis{stats: rediscache.MonitoringStats{Up: true}}
	source := &snapshotSource{store: pg, cache: redis, pgTimeout: 100 * time.Millisecond, redisTimeout: 100 * time.Millisecond}

	pollCtx, cancel := context.WithCancel(context.Background())
	cancel()
	snap, err := source.Snapshot(pollCtx)
	if err == nil {
		t.Fatal("poll must fail on cancellation")
	}
	if snap.PostgresHealthKnown {
		t.Fatalf("canceled PostgreSQL probe must stay unknown, not down: %+v", snap.Postgres)
	}
	if snap.RedisHealthKnown {
		t.Fatalf("canceled Redis probe must stay unknown, not down: %+v", snap.Redis)
	}
}

// TestSnapshotRedisOutageClearsWriteAccepts proves the write-capability flag is
// NOT treated as a last-good value (review R2): after a poll that accepted a
// probe write, a poll whose Redis probe fails must emit write_accepts=false even
// though the prior poll recorded write_accepts=true, while last-good memory
// counters are retained. The collector-level transition (including recovery and
// the partial-snapshot merge with a failing PostgreSQL poll) is covered in the
// telemetry package, where the collector and its test helpers live.
func TestSnapshotRedisOutageClearsWriteAccepts(t *testing.T) {
	redis := &fakeRedis{stats: rediscache.MonitoringStats{Up: true, UsedMemory: 1024, WriteProbeOK: true, Cooldowns: map[string]float64{}}}
	pg := &fakePG{}
	source := &snapshotSource{store: pg, cache: redis, pgTimeout: 100 * time.Millisecond, redisTimeout: 100 * time.Millisecond}

	// Poll 1: healthy Redis, probe write accepted.
	poll1, cancel1 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel1()
	snap1, err := source.Snapshot(poll1)
	if err != nil {
		t.Fatalf("poll 1 must succeed: %v", err)
	}
	if !snap1.Redis.Up || !snap1.Redis.WriteOK {
		t.Fatalf("poll 1 must observe healthy, write-accepting Redis: %+v", snap1.Redis)
	}

	// Poll 2: Redis probe now fails (unreachable). Write capability must be
	// cleared even though the last-good value was accepted; memory retained.
	redis.err = errors.New("connection refused")
	poll2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	snap2, err := source.Snapshot(poll2)
	if err != nil {
		t.Fatalf("poll 2 must still succeed when only Redis is down: %v", err)
	}
	if !snap2.RedisHealthKnown {
		t.Fatalf("Redis availability must be current (known) on the failed probe")
	}
	if snap2.Redis.Up {
		t.Fatalf("failed Redis probe must be reported down")
	}
	if snap2.Redis.WriteOK {
		t.Fatalf("write_accepts must be cleared on a failed Redis probe, not retained as last-good")
	}
	if snap2.Redis.UsedMemory != 1024 {
		t.Fatalf("last-good memory must be retained: got %v", snap2.Redis.UsedMemory)
	}

	// Poll 3: recovery restores write acceptance.
	redis.err = nil
	poll3, cancel3 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel3()
	snap3, err := source.Snapshot(poll3)
	if err != nil {
		t.Fatalf("poll 3 must succeed: %v", err)
	}
	if !snap3.Redis.Up || !snap3.Redis.WriteOK {
		t.Fatalf("recovered Redis must report up and write-accepting again: %+v", snap3.Redis)
	}
}

func TestSnapshotPGError(t *testing.T) {
	pg := &fakePG{err: errors.New("pg query failed")}
	redis := &fakeRedis{}
	source := &snapshotSource{store: pg, cache: redis, pgTimeout: 100 * time.Millisecond, redisTimeout: 100 * time.Millisecond}

	poll, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	snap, err := source.Snapshot(poll)
	if err == nil {
		t.Fatalf("expected error when PG fails, got nil")
	}
	if snap.Postgres.Up {
		t.Fatalf("expected Postgres.Up=false on error")
	}
}
