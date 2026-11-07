package telemetry

import (
	"context"
	"strings"
	"testing"
)

// TestWorkerPoolZeroStateAndCacheWrites covers the review's acceptance gaps:
// idle pools must expose healthy zero states (active=0, completion=never)
// instead of absent series, and inventory cache SET failures must be visible
// as bounded write outcomes.
func TestWorkerPoolZeroStateAndCacheWrites(t *testing.T) {
	var logs, traces strings.Builder
	observer, err := New(&logs, &traces)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	observer.SetWorkerCapacity("page", 3)
	observer.InventoryCacheWrite("ok")
	observer.InventoryCacheWrite("error")

	body := scrape(t, observer)
	for _, expected := range []string{
		`jamshid_worker_capacity{pool="page"} 3`,
		`jamshid_worker_active{pool="page"} 0`,
		`jamshid_worker_last_completed_timestamp_seconds{pool="page"} 0`,
		`jamshid_inventory_cache_writes_total{outcome="ok"} 1`,
		`jamshid_inventory_cache_writes_total{outcome="error"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected %q in:\n%s", expected, body)
		}
	}
}
