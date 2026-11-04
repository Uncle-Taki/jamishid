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
	observer.RunSnapshotCollector(ctx, source, 10*time.Millisecond, 50*time.Millisecond)
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

func (f *fakeSource) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
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
