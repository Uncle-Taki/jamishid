package telemetry

import (
	"context"
	"errors"
	"net/http/httptest"
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

func TestTelemetry_ExtendedMetricsAndObserver(t *testing.T) {
	var logs, traces strings.Builder
	observer, err := New(&logs, &traces)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Shutdown(context.Background())

	// 1. ScheduleAttempt (success and error)
	finishSuccess := observer.ScheduleAttempt("bama")
	finishSuccess(nil)
	finishErr := observer.ScheduleAttempt("bama")
	finishErr(errors.New("scheduler failed"))

	// 2. Retry lifecycle metrics
	observer.RetryScheduled("page")
	observer.RetryReleased("page")
	observer.RetryInvalidated("page", 5)
	observer.RetryInvalidated("page", 0) // zero check branch
	observer.RetryDiscarded("page")

	// 3. Inventory cache & query
	observer.InventoryCacheOutcome("hit")
	observer.InventoryCacheOutcome("miss")
	observer.InventoryQuery()

	// 4. Observe Postgres Pool
	observer.ObservePostgresPool(2, 8, 10)

	// 5. Registry
	if observer.Registry() == nil {
		t.Fatal("expected non-nil registry")
	}

	// 6. LoggingHeartbeat
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	observer.LoggingHeartbeat(ctx)

	// 7. StatusClass testing
	for _, tc := range []struct {
		code int
		want string
	}{
		{200, "2xx"},
		{301, "3xx"},
		{404, "4xx"},
		{500, "5xx"},
		{101, "other"},
		{600, "other"},
	} {
		if got := statusClass(tc.code); got != tc.want {
			t.Errorf("statusClass(%d) = %s, want %s", tc.code, got, tc.want)
		}
	}

	// 8. normalizeMethod other
	if got := normalizeMethod("CUSTOM"); got != "other" {
		t.Errorf("expected other, got %s", got)
	}

	// 9. statusWriter Unwrap
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec}
	if sw.Unwrap() != rec {
		t.Fatal("Unwrap failed")
	}

	// Scrape and verify metrics are recorded
	body := scrape(t, observer)
	for _, expected := range []string{
		`jamshid_scheduler_errors_total{scheduler="bama"} 1`,
		`jamshid_retry_tickets_total{action="scheduled",kind="page"} 1`,
		`jamshid_retry_tickets_total{action="released",kind="page"} 1`,
		`jamshid_retry_tickets_total{action="invalidated",kind="page"} 5`,
		`jamshid_retry_tickets_total{action="discarded",kind="page"} 1`,
		`jamshid_inventory_cache_requests_total{outcome="hit"} 1`,
		`jamshid_inventory_queries_total 1`,
		`jamshid_postgres_pool_connections{state="acquired"} 2`,
		`jamshid_postgres_pool_connections{state="idle"} 8`,
		`jamshid_postgres_pool_max_connections 10`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("expected %q in scraped body:\n%s", expected, body)
		}
	}
}
