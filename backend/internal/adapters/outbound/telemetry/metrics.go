package telemetry

import (
	"time"
)

// TaskStart implements crawljobs.WorkerMetrics.
func (o *Observer) TaskStart(pool, source string) func(outcome, failure string) {
	o.workerActive.WithLabelValues(pool).Inc()
	start := time.Now()
	return func(outcome, failure string) {
		o.workerActive.WithLabelValues(pool).Dec()
		o.workerAttempts.WithLabelValues(pool, source, outcome, failure).Inc()
		o.workerDuration.WithLabelValues(pool).Observe(time.Since(start).Seconds())
		if outcome == "success" {
			o.workerCompleted.WithLabelValues(pool).Set(float64(time.Now().Unix()))
		}
	}
}

// LoopTick implements crawljobs.WorkerMetrics.
func (o *Observer) LoopTick(pool, loop string) {
	o.workerLoopTick.WithLabelValues(pool, loop).Set(float64(time.Now().Unix()))
}

// ClaimError implements crawljobs.WorkerMetrics.
func (o *Observer) ClaimError(pool string) { o.workerClaimErrors.WithLabelValues(pool).Inc() }

// ScheduleAttempt implements crawljobs.WorkerMetrics.
func (o *Observer) ScheduleAttempt(scheduler string) func(error) {
	o.schedulerAttempt.WithLabelValues(scheduler).Set(float64(time.Now().Unix()))
	return func(err error) {
		if err != nil {
			o.schedulerErrors.WithLabelValues(scheduler).Inc()
			return
		}
		o.schedulerSuccess.WithLabelValues(scheduler).Set(float64(time.Now().Unix()))
	}
}

// SetWorkerCapacity implements crawljobs.WorkerMetrics.
func (o *Observer) SetWorkerCapacity(pool string, size int) {
	o.workerCapacity.WithLabelValues(pool).Set(float64(size))
}

// RetryScheduled, RetryReleased, RetryInvalidated and RetryDiscarded implement
// the postgres store instrumentation interface.
func (o *Observer) RetryScheduled(kind string) {
	o.retryTickets.WithLabelValues(kind, "scheduled").Inc()
}
func (o *Observer) RetryReleased(kind string) { o.retryTickets.WithLabelValues(kind, "released").Inc() }
func (o *Observer) RetryInvalidated(kind string) {
	o.retryTickets.WithLabelValues(kind, "invalidated").Inc()
}
func (o *Observer) RetryDiscarded(kind string) {
	o.retryTickets.WithLabelValues(kind, "discarded").Inc()
}

// InventoryCacheOutcome and InventoryQuery implement the inventory cache
// instrumentation interface used by the PostgreSQL store.
func (o *Observer) InventoryCacheOutcome(outcome string) {
	o.inventoryCache.WithLabelValues(outcome).Inc()
}
func (o *Observer) InventoryQuery() { o.inventoryQueries.Inc() }
