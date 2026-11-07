package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	mu    sync.Mutex
	snap  Snapshot
	err   error
	calls int
	delay time.Duration
	hang  bool
}

func (f *fakeSource) Snapshot(ctx context.Context) (Snapshot, error) {
	f.mu.Lock()
	f.calls++
	hang := f.hang
	delay := f.delay
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return Snapshot{}, ctx.Err()
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap, f.err
}

// TestSnapshotCollectorStalenessAndRecovery proves failed polls keep the
// last-good values, mark success=0, and that a successful refresh replaces
// obsolete series instead of accumulating them.
func TestSnapshotCollectorStalenessAndRecovery(t *testing.T) {
	observer := newTestObserver(t)
	source := &fakeSource{snap: Snapshot{QueueTasks: []QueueRow{
		{Queue: "page", Source: "divar", State: "ready", Mode: "incremental", Purpose: "none", Value: 4},
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer.RunSnapshotCollector(ctx, source, 20*time.Millisecond, 250*time.Millisecond)
	waitFor(t, func() bool { return observer.CurrentSnapshot() != nil })

	body := scrape(t, observer)
	if !strings.Contains(body, `jamshid_queue_tasks{collector="primary",mode="incremental",purpose="none",queue="page",source="divar",state="ready"} 4`) {
		t.Fatalf("queue gauge missing:\n%s", body)
	}
	if !strings.Contains(body, "jamshid_monitoring_snapshot_success{collector=\"primary\"} 1") {
		t.Fatalf("snapshot success missing:\n%s", body)
	}

	// Fail the source; values must be retained with an explicit failure mark.
	source.mu.Lock()
	source.err = errors.New("database unavailable")
	source.mu.Unlock()
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), "jamshid_monitoring_snapshot_success{collector=\"primary\"} 0")
	})
	body = scrape(t, observer)
	if !strings.Contains(body, `state="ready"} 4`) {
		t.Fatalf("last-good value replaced during outage:\n%s", body)
	}
	if !strings.Contains(body, "jamshid_monitoring_snapshot_errors_total{collector=\"primary\"} 1") {
		t.Fatalf("error counter missing:\n%s", body)
	}

	// Recover with different labels; obsolete series must disappear.
	source.mu.Lock()
	source.err = nil
	source.snap = Snapshot{QueueTasks: []QueueRow{
		{Queue: "page", Source: "bama", State: "ready", Mode: "incremental", Purpose: "none", Value: 1},
	}}
	source.mu.Unlock()
	waitFor(t, func() bool {
		return !strings.Contains(scrape(t, observer), `source="divar",state="ready"`)
	})
	body = scrape(t, observer)
	if !strings.Contains(body, `source="bama",state="ready"} 1`) {
		t.Fatalf("refreshed value missing:\n%s", body)
	}
	if !strings.Contains(body, "jamshid_monitoring_snapshot_success{collector=\"primary\"} 1") {
		t.Fatalf("success not restored:\n%s", body)
	}
}

// TestSnapshotCollectorCancellationAndTimeout proves shutdown cancels pollers
// and that a hanging poll does not block /metrics.
func TestSnapshotCollectorCancellationAndTimeout(t *testing.T) {
	observer := newTestObserver(t)
	source := &fakeSource{hang: true}
	ctx, cancel := context.WithCancel(context.Background())
	done := observer.RunSnapshotCollector(ctx, source, 10*time.Millisecond, 50*time.Millisecond)
	waitFor(t, func() bool { return source.Calls() >= 1 })

	start := time.Now()
	scrape(t, observer)
	if time.Since(start) > time.Second {
		t.Fatal("metrics endpoint blocked on hanging poll")
	}
	// The poll-health series appears only after the first poll completes (the
	// hanging poll times out), so wait for the failure marker.
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), "jamshid_monitoring_snapshot_success{collector=\"primary\"} 0")
	})

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("collector did not join after cancellation")
	}
	// The poller must stop issuing polls. A poll already in flight when
	// cancellation lands may still return (and count), so require the call
	// count to go stable rather than frozen at its cancellation-time value.
	time.Sleep(150 * time.Millisecond)
	first := source.Calls()
	time.Sleep(300 * time.Millisecond)
	if second := source.Calls(); second != first {
		t.Fatalf("poller continued after cancellation: %d -> %d calls", first, second)
	}
}

func TestRedisMeasurementsRefreshWhilePostgresFails(t *testing.T) {
	observer := newTestObserver(t)
	source := &fakeSource{snap: Snapshot{PostgresObserved: true, PostgresHealthKnown: true, Postgres: PostgresService{Up: true}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := observer.RunSnapshotCollector(ctx, source, 10*time.Millisecond, time.Second)
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool { return observer.CurrentSnapshot() != nil })
	source.mu.Lock()
	source.err = errors.New("postgres down")
	source.snap = Snapshot{PostgresHealthKnown: true, RedisHealthKnown: true, RedisObserved: true, Redis: RedisService{Up: true, UsedMemory: 1234}}
	source.mu.Unlock()
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), `jamshid_redis_memory_used_bytes{collector="primary"} 1234`)
	})
}

func (f *fakeSource) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestSnapshotDependencyHealthOnOutage: during a failed poll the collector must
// retain last-good measurements but let the current dependency availability
// through. A green database card during an outage is worse than a stale
// measurement (review finding R2).
func TestSnapshotDependencyHealthOnOutage(t *testing.T) {
	observer := newTestObserver(t)
	source := &fakeSource{snap: Snapshot{
		Postgres:         PostgresService{Up: true, XactCommit: 42},
		PostgresObserved: true,
		QueueTasks:       []QueueRow{{Queue: "page", Source: "divar", State: "ready", Mode: "incremental", Purpose: "none", Value: 4}},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer.RunSnapshotCollector(ctx, source, 20*time.Millisecond, 250*time.Millisecond)
	waitFor(t, func() bool { return observer.CurrentSnapshot() != nil })

	// The failed poll observed PostgreSQL down (health known) while its
	// measurements could not be read.
	source.mu.Lock()
	source.err = errors.New("database unavailable")
	source.snap = Snapshot{PostgresHealthKnown: true, Postgres: PostgresService{Up: false}}
	source.mu.Unlock()
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), "jamshid_monitoring_snapshot_success{collector=\"primary\"} 0")
	})
	body := scrape(t, observer)
	if !strings.Contains(body, "jamshid_postgres_up{collector=\"primary\"} 0") {
		t.Fatalf("postgres_up must reflect the failed poll attempt, not last-good:\n%s", body)
	}
	if !strings.Contains(body, `state="ready"} 4`) {
		t.Fatalf("last-good queue value replaced during outage:\n%s", body)
	}
	if !strings.Contains(body, `jamshid_postgres_xact_total{collector="primary",outcome="commit"} 42`) {
		t.Fatalf("last-good postgres measurement replaced during outage:\n%s", body)
	}

	// Recovery restores everything.
	source.mu.Lock()
	source.err = nil
	source.snap = Snapshot{Postgres: PostgresService{Up: true, XactCommit: 43}, PostgresHealthKnown: true, PostgresObserved: true}
	source.mu.Unlock()
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), "jamshid_monitoring_snapshot_success{collector=\"primary\"} 1")
	})
	body = scrape(t, observer)
	if !strings.Contains(body, "jamshid_postgres_up{collector=\"primary\"} 1") {
		t.Fatalf("postgres_up not restored:\n%s", body)
	}
	if !strings.Contains(body, `outcome="commit"} 43`) {
		t.Fatalf("postgres measurement not refreshed:\n%s", body)
	}
}

// TestSnapshotFirstEverFailureServesDependencyHealth: when no last-good
// snapshot exists, a failed poll that still observed a dependency down must
// export that observation as a real 0 instead of serving nothing (an absent
// series reads as "collector not running", not "database down").
func TestSnapshotFirstEverFailureServesDependencyHealth(t *testing.T) {
	observer := newTestObserver(t)
	source := &fakeSource{
		err:  errors.New("database unavailable"),
		snap: Snapshot{PostgresHealthKnown: true, Postgres: PostgresService{Up: false}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer.RunSnapshotCollector(ctx, source, 20*time.Millisecond, 250*time.Millisecond)
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), "jamshid_monitoring_snapshot_success{collector=\"primary\"} 0")
	})
	body := scrape(t, observer)
	if !strings.Contains(body, "jamshid_postgres_up{collector=\"primary\"} 0") {
		t.Fatalf("first-ever failure must export the observed dependency state:\n%s", body)
	}
	if strings.Contains(body, "jamshid_queue_tasks{") {
		t.Fatalf("no queue measurements exist to serve:\n%s", body)
	}
}

// TestCollectorScrapeSucceedsWithMergedLabels proves the real registry Gather
// (HTTP scrape) succeeds for a snapshot whose rows have been merged after
// normalization, and that each exported label set appears exactly once with the
// summed count. A duplicate label set would make promhttp fail the whole scrape
// (review finding C2). This exercises the actual /metrics export path, not just
// row counts.
func TestCollectorScrapeSucceedsWithMergedLabels(t *testing.T) {
	observer := newTestObserver(t)
	source := &fakeSource{snap: Snapshot{
		QueueTasks: []QueueRow{
			{Queue: "page", Source: "other", State: "ready", Mode: "incremental", Purpose: "none", Value: 2},
			{Queue: "detail", Source: "divar", State: "ready", Mode: "none", Purpose: "other", Value: 2},
		},
		Inventory: []InventoryRow{
			{Source: "divar", Type: "other", Availability: "other", Freshness: "fresh", Value: 2},
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer.RunSnapshotCollector(ctx, source, 20*time.Millisecond, 250*time.Millisecond)
	waitFor(t, func() bool { return observer.CurrentSnapshot() != nil })

	body := scrape(t, observer) // real promhttp gather; a duplicate would panic/500
	// Each merged label set must appear exactly once with the summed value.
	for _, series := range []struct {
		label, value string
	}{
		{`jamshid_queue_tasks{collector="primary",mode="incremental",purpose="none",queue="page",source="other",state="ready"}`, "2"},
		{`jamshid_queue_tasks{collector="primary",mode="none",purpose="other",queue="detail",source="divar",state="ready"}`, "2"},
		{`jamshid_inventory_listings{availability="other",collector="primary",freshness="fresh",source="divar",type="other"}`, "2"},
	} {
		count := 0
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, series.label+" ") {
				count++
				if !strings.HasSuffix(line, " "+series.value) {
					t.Fatalf("unexpected value for %s:\n%s", series.label, line)
				}
			}
		}
		if count != 1 {
			t.Fatalf("%s must appear exactly once, got %d:\n%s", series.label, count, body)
		}
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// TestFirstEverFailureDoesNotFabricateUsage proves a first-ever failed poll that
// observed PostgreSQL down (health known) but could not read any measurements
// exports the down availability as a real 0 while emitting NO transaction,
// connection or storage series. Previously the zero-valued PostgresService was
// exported unconditionally, turning an unobserved dependency into fabricated
// zero usage (review finding C3).
func TestFirstEverFailureDoesNotFabricateUsage(t *testing.T) {
	observer := newTestObserver(t)
	source := &fakeSource{
		err:  errors.New("database unavailable before any query"),
		snap: Snapshot{PostgresHealthKnown: true, Postgres: PostgresService{Up: false}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer.RunSnapshotCollector(ctx, source, 20*time.Millisecond, 250*time.Millisecond)
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), "jamshid_monitoring_snapshot_success{collector=\"primary\"} 0")
	})
	body := scrape(t, observer)
	if !strings.Contains(body, "jamshid_postgres_up{collector=\"primary\"} 0") {
		t.Fatalf("observed down database must be exported as a real 0:\n%s", body)
	}
	// The unobserved measurements must be ABSENT, not fabricated zeros.
	for _, absent := range []string{
		"jamshid_postgres_xact_total",
		"jamshid_postgres_connections",
		"jamshid_storage_bytes",
	} {
		if strings.Contains(body, absent) {
			t.Fatalf("%s must not be emitted for an unobserved database:\n%s", absent, body)
		}
	}
}

// TestPartialFailureAfterQueuesBeforeInfra proves a poll that read queue state
// but failed before the infrastructure stats serves the queue measurements and
// the current (down) database availability, while withholding the unobserved
// transaction/connection/storage series.
func TestPartialFailureAfterQueuesBeforeInfra(t *testing.T) {
	observer := newTestObserver(t)
	// A prior successful poll establishes last-good, including observed PG stats.
	source := &fakeSource{snap: Snapshot{
		Postgres:         PostgresService{Up: true, XactCommit: 7},
		PostgresObserved: true,
		QueueTasks:       []QueueRow{{Queue: "page", Source: "divar", State: "ready", Mode: "incremental", Purpose: "none", Value: 2}},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer.RunSnapshotCollector(ctx, source, 20*time.Millisecond, 250*time.Millisecond)
	waitFor(t, func() bool { return observer.CurrentSnapshot() != nil })

	// The next poll read queues but failed before infra stats: health known down,
	// measurements unobserved.
	source.mu.Lock()
	source.err = errors.New("infra stats timeout")
	source.snap = Snapshot{
		PostgresHealthKnown: true, Postgres: PostgresService{Up: false},
		QueueTasks: []QueueRow{{Queue: "page", Source: "divar", State: "ready", Mode: "incremental", Purpose: "none", Value: 2}},
	}
	source.mu.Unlock()
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), "jamshid_monitoring_snapshot_success{collector=\"primary\"} 0")
	})
	body := scrape(t, observer)
	if !strings.Contains(body, `state="ready"} 2`) {
		t.Fatalf("last-good queue value must be retained:\n%s", body)
	}
	if !strings.Contains(body, "jamshid_postgres_up{collector=\"primary\"} 0") {
		t.Fatalf("current down availability must be current, not last-good:\n%s", body)
	}
	// Last-good xact was retained from the previous success; it must NOT be
	// re-fabricated as a zero, and it must stay at the last-good value.
	if !strings.Contains(body, `outcome="commit"} 7`) {
		t.Fatalf("last-good transaction counter must be retained, not zeroed:\n%s", body)
	}
}

// TestAbsentRedisEmitsNoSeries proves a poll that never probed Redis (unconfigured
// or skipped because the overall deadline ran out) emits no Redis series at all,
// rather than advertising a down cache or fabricated zero memory.
func TestAbsentRedisEmitsNoSeries(t *testing.T) {
	observer := newTestObserver(t)
	source := &fakeSource{snap: Snapshot{Postgres: PostgresService{Up: true}, PostgresHealthKnown: true, PostgresObserved: true}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer.RunSnapshotCollector(ctx, source, 20*time.Millisecond, 250*time.Millisecond)
	waitFor(t, func() bool { return observer.CurrentSnapshot() != nil })
	body := scrape(t, observer)
	for _, absent := range []string{
		"jamshid_redis_up",
		"jamshid_redis_memory_used_bytes",
		"jamshid_redis_keyspace_hits_total",
		"jamshid_redis_connected_clients",
	} {
		if strings.Contains(body, absent) {
			t.Fatalf("%s must not be emitted for an unprobed Redis:\n%s", absent, body)
		}
	}
}

// TestRedisDownDoesNotFabricateMemory proves a poll that observed Redis down but
// could not read INFO exports redis_up=0 while withholding memory/cache series
// (last-good values are retained by the collector, not re-fabricated as zeros).
func TestRedisDownDoesNotFabricateMemory(t *testing.T) {
	observer := newTestObserver(t)
	source := &fakeSource{snap: Snapshot{
		Postgres: PostgresService{Up: true}, PostgresHealthKnown: true, PostgresObserved: true,
		Redis: RedisService{Up: true, UsedMemory: 1024}, RedisHealthKnown: true, RedisObserved: true,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer.RunSnapshotCollector(ctx, source, 20*time.Millisecond, 250*time.Millisecond)
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), `jamshid_redis_memory_used_bytes{collector="primary"} 1024`)
	})

	source.mu.Lock()
	source.err = nil
	// Mirrors the real source: a Redis-down poll retains last-good memory and,
	// having been observed before, keeps it emit-able (not a fabricated zero).
	source.snap = Snapshot{
		Postgres: PostgresService{Up: true}, PostgresHealthKnown: true, PostgresObserved: true,
		Redis: RedisService{Up: false, UsedMemory: 1024}, RedisHealthKnown: true, RedisObserved: true,
	}
	source.mu.Unlock()
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), `jamshid_redis_up{collector="primary"} 0`)
	})
	body := scrape(t, observer)
	if !strings.Contains(body, `jamshid_redis_up{collector="primary"} 0`) {
		t.Fatalf("observed down Redis must be exported as a real 0:\n%s", body)
	}
	// Last-good memory retained; it must not be zeroed into a fabricated 0.
	if !strings.Contains(body, `jamshid_redis_memory_used_bytes{collector="primary"} 1024`) {
		t.Fatalf("last-good Redis memory must be retained, not zeroed:\n%s", body)
	}
}

// TestRedisWriteProbeExposed proves the per-poll write probe is exported as a
// separate series from redis_up: a full instance under noeviction is reachable
// (up=1) but rejects writes (write_accepts=0), which is the memory-pressure
// signal that PING/INFO alone cannot show.
func TestRedisWriteProbeExposed(t *testing.T) {
	observer := newTestObserver(t)
	source := &fakeSource{snap: Snapshot{
		Postgres: PostgresService{Up: true}, PostgresHealthKnown: true, PostgresObserved: true,
		Redis: RedisService{Up: true, WriteOK: true, UsedMemory: 100}, RedisHealthKnown: true, RedisObserved: true,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer.RunSnapshotCollector(ctx, source, 20*time.Millisecond, 250*time.Millisecond)
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), `jamshid_redis_write_accepts{collector="primary"} 1`)
	})

	// Reachable but memory-pressed: up stays 1, the probe reports rejection.
	source.mu.Lock()
	source.snap = Snapshot{
		Postgres: PostgresService{Up: true}, PostgresHealthKnown: true, PostgresObserved: true,
		Redis: RedisService{Up: true, WriteOK: false, UsedMemory: 256000000}, RedisHealthKnown: true, RedisObserved: true,
	}
	source.mu.Unlock()
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), `jamshid_redis_write_accepts{collector="primary"} 0`)
	})
	body := scrape(t, observer)
	if !strings.Contains(body, `jamshid_redis_up{collector="primary"} 1`) {
		t.Fatalf("reachable instance must stay up while rejecting writes:\n%s", body)
	}

	// A full Redis outage keeps both series at 0.
	source.mu.Lock()
	source.snap = Snapshot{
		Postgres: PostgresService{Up: true}, PostgresHealthKnown: true, PostgresObserved: true,
		Redis: RedisService{Up: false}, RedisHealthKnown: true,
	}
	source.mu.Unlock()
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), `jamshid_redis_up{collector="primary"} 0`)
	})
	body = scrape(t, observer)
	if !strings.Contains(body, `jamshid_redis_up{collector="primary"} 0`) {
		t.Fatalf("outage must be down:\n%s", body)
	}
	if !strings.Contains(body, `jamshid_redis_write_accepts{collector="primary"} 0`) {
		t.Fatalf("full Redis outage must also clear write_accepts (it cannot accept writes):\n%s", body)
	}
}

// TestRedisWriteAcceptsClearsOnFailedPollTransition covers the full collector
// transition that review R2 targets: a healthy, write-accepting poll, then a
// FAILED poll (PostgreSQL down) whose Redis probe still ran and observed a
// write-rejection or outage, then recovery. The partial-snapshot merge must
// clear write_accepts to 0 during the outage (a stale success must not be
// re-emitted) while retaining last-good memory, and restore it on recovery.
func TestRedisWriteAcceptsClearsOnFailedPollTransition(t *testing.T) {
	observer := newTestObserver(t)
	source := &fakeSource{snap: Snapshot{
		Postgres: PostgresService{Up: true}, PostgresHealthKnown: true, PostgresObserved: true,
		Redis: RedisService{Up: true, WriteOK: true, UsedMemory: 5000}, RedisHealthKnown: true, RedisObserved: true,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer.RunSnapshotCollector(ctx, source, 10*time.Millisecond, 500*time.Millisecond)
	waitFor(t, func() bool {
		return strings.Contains(scrape(t, observer), `jamshid_redis_write_accepts{collector="primary"} 1`)
	})

	// Failed poll: PostgreSQL down (so the poll fails and the merge path runs),
	// but the Redis probe still ran and observed the instance rejecting writes
	// (reachable-but-OOM): up=1, write_accepts=0. Memory is not observed this
	// poll, so last-good memory must be retained.
	source.mu.Lock()
	source.err = errors.New("postgres down")
	source.snap = Snapshot{
		Postgres: PostgresService{Up: false}, PostgresHealthKnown: true, PostgresObserved: false,
		Redis: RedisService{Up: true, WriteOK: false}, RedisHealthKnown: true, RedisObserved: false,
	}
	source.mu.Unlock()
	waitFor(t, func() bool {
		body := scrape(t, observer)
		return strings.Contains(body, `jamshid_monitoring_snapshot_success{collector="primary"} 0`) &&
			strings.Contains(body, `jamshid_redis_up{collector="primary"} 1`) &&
			strings.Contains(body, `jamshid_redis_write_accepts{collector="primary"} 0`)
	})
	body := scrape(t, observer)
	if strings.Contains(body, `jamshid_redis_write_accepts{collector="primary"} 1`) {
		t.Fatalf("stale write_accepts=1 survived the failed poll:\n%s", body)
	}
	if !strings.Contains(body, `jamshid_redis_memory_used_bytes{collector="primary"} 5000`) {
		t.Fatalf("last-good Redis memory lost during the failed poll:\n%s", body)
	}
	// Full Redis outage while PostgreSQL is still down also uses the merge.
	source.mu.Lock()
	source.snap.Redis.Up = false
	source.mu.Unlock()
	waitFor(t, func() bool {
		body := scrape(t, observer)
		return strings.Contains(body, `jamshid_redis_up{collector="primary"} 0`) &&
			strings.Contains(body, `jamshid_redis_write_accepts{collector="primary"} 0`)
	})

	// Recovery: poll succeeds again and the probe write is accepted.
	source.mu.Lock()
	source.err = nil
	source.snap = Snapshot{
		Postgres: PostgresService{Up: true}, PostgresHealthKnown: true, PostgresObserved: true,
		Redis: RedisService{Up: true, WriteOK: true, UsedMemory: 5000}, RedisHealthKnown: true, RedisObserved: true,
	}
	source.mu.Unlock()
	waitFor(t, func() bool {
		body := scrape(t, observer)
		return strings.Contains(body, `jamshid_monitoring_snapshot_success{collector="primary"} 1`) &&
			strings.Contains(body, `jamshid_redis_write_accepts{collector="primary"} 1`)
	})
}
func TestAbsentRedisEmitsNoProbeSeries(t *testing.T) {
	observer := newTestObserver(t)
	source := &fakeSource{snap: Snapshot{Postgres: PostgresService{Up: true}, PostgresHealthKnown: true, PostgresObserved: true}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer.RunSnapshotCollector(ctx, source, 20*time.Millisecond, 250*time.Millisecond)
	waitFor(t, func() bool { return observer.CurrentSnapshot() != nil })
	if body := scrape(t, observer); strings.Contains(body, "jamshid_redis_write_accepts") {
		t.Fatalf("unprobed Redis must not emit the probe series:\n%s", body)
	}
}
