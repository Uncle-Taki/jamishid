package crawljobs

// WorkerMetrics receives worker instrumentation events. Implementations must be
// safe for concurrent use; a nil Metrics is valid and disables instrumentation.
type WorkerMetrics interface {
	// TaskStart records a claimed task attempt for a pool and source and returns
	// a finish function that must be called exactly once with the final outcome
	// ("success", "source_failure", "deferral", "lease_lost",
	// "persistence_failure" or "canceled") and the controlled failure kind.
	TaskStart(pool, source string) func(outcome, failure string)
	// LoopTick records an actual loop iteration for a pool, including idle polls.
	LoopTick(pool, loop string)
	// ClaimError records a claim failure even when no task was acquired.
	ClaimError(pool string)
	// ScheduleAttempt records a scheduler cycle and returns a finish function
	// that must be called with the store operation's result.
	ScheduleAttempt(scheduler string) func(error)
	// SetWorkerCapacity records the configured loop count for a pool.
	SetWorkerCapacity(pool string, size int)
}

type noopWorkerMetrics struct{}

func (noopWorkerMetrics) TaskStart(_, _ string) func(string, string) {
	return func(string, string) {}
}
func (noopWorkerMetrics) LoopTick(_, _ string) {}
func (noopWorkerMetrics) ClaimError(string)    {}
func (noopWorkerMetrics) ScheduleAttempt(string) func(error) {
	return func(error) {}
}
func (noopWorkerMetrics) SetWorkerCapacity(string, int) {}

// workerMetrics returns a non-nil implementation.
func workerMetrics(m WorkerMetrics) WorkerMetrics {
	if m == nil {
		return noopWorkerMetrics{}
	}
	return m
}

// Task outcome categories shared by page and detail loops. Counters represent
// attempts, not unique listings.
const (
	OutcomeSuccess            = "success"
	OutcomeSourceFailure      = "source_failure"
	OutcomeDeferral           = "deferral"
	OutcomeLeaseLost          = "lease_lost"
	OutcomePersistenceFailure = "persistence_failure"
	OutcomeCanceled           = "canceled"
)
