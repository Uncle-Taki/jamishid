package crawljobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

// recordingMetrics captures worker instrumentation events for assertions.
type recordingMetrics struct {
	mu           sync.Mutex
	active       map[string]int
	ticks        map[string]int
	claims       map[string]int
	attempts     []attempt
	schedules    map[string]int
	scheduleErrs map[string]int
	capacity     map[string]int
}

type attempt struct {
	pool, source, outcome, failure string
}

func newRecording() *recordingMetrics {
	return &recordingMetrics{
		active: map[string]int{}, ticks: map[string]int{}, claims: map[string]int{},
		schedules: map[string]int{}, scheduleErrs: map[string]int{}, capacity: map[string]int{},
	}
}

func (r *recordingMetrics) TaskStart(pool, source string) func(outcome, failure string) {
	r.mu.Lock()
	r.active[pool]++
	r.mu.Unlock()
	return func(outcome, failure string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.active[pool]--
		r.attempts = append(r.attempts, attempt{pool, source, outcome, failure})
	}
}
func (r *recordingMetrics) LoopTick(pool, loop string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ticks[pool]++
}
func (r *recordingMetrics) ClaimError(pool string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claims[pool]++
}
func (r *recordingMetrics) ScheduleAttempt(scheduler string) func(error) {
	r.mu.Lock()
	r.schedules[scheduler]++
	r.mu.Unlock()
	return func(err error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if err != nil {
			r.scheduleErrs[scheduler]++
		}
	}
}
func (r *recordingMetrics) SetWorkerCapacity(pool string, size int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.capacity[pool] = size
}

func (r *recordingMetrics) snapshot() (map[string]int, []attempt) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active, r.attempts
}

type fakeClaimRepo struct {
	task     Task
	claimOK  bool
	claimErr error
	failErr  error
}

func newClaimRepo() fakeClaimRepo {
	return fakeClaimRepo{claimOK: true, task: Task{JobID: "j1", SearchID: "s1", Platform: vehicle.PlatformDivar, Mode: "incremental"}}
}

func (f *fakeClaimRepo) Submit(context.Context, vehicle.SearchCriteria, bool) (Job, error) {
	return Job{}, nil
}
func (f *fakeClaimRepo) Get(context.Context, string) (Job, error)                       { return Job{}, nil }
func (f *fakeClaimRepo) Action(context.Context, string, string, vehicle.Platform) error { return nil }
func (f *fakeClaimRepo) Claim(context.Context) (Task, bool, error) {
	return f.task, f.claimOK, f.claimErr
}
func (f *fakeClaimRepo) Prepare(_ context.Context, rows []vehicle.NormalizedVehicleListing) ([]Observation, error) {
	result := make([]Observation, len(rows))
	for i, row := range rows {
		result[i] = Observation{Listing: row, SummaryHash: "h", NeedsDetails: true}
	}
	return result, nil
}
func (f *fakeClaimRepo) Commit(context.Context, Task, []Observation, string, int) error { return nil }
func (f *fakeClaimRepo) Fail(context.Context, Task, string) error                       { return f.failErr }
func (f *fakeClaimRepo) Schedule(context.Context) error                                 { return nil }
func (f *fakeClaimRepo) Search(context.Context, InventoryQuery) (InventoryResult, error) {
	return InventoryResult{}, nil
}

type fakeCrawler struct {
	result vehicle.CrawlResult
	err    error
}

func (f *fakeCrawler) AggregateWithSources(context.Context, vehicle.SearchCriteria) (vehicle.CrawlResult, error) {
	return f.result, f.err
}

func TestWorkOneOutcomeClassification(t *testing.T) {
	cases := []struct {
		name    string
		repo    fakeClaimRepo
		crawler fakeCrawler
		outcome string
		failure string
	}{
		{
			name:    "committed success",
			repo:    newClaimRepo(),
			crawler: fakeCrawler{result: vehicle.CrawlResult{Sources: []vehicle.SourceStatus{{Status: "ok"}}}},
			outcome: OutcomeSuccess, failure: "none",
		},
		{
			name:    "source failure",
			repo:    newClaimRepo(),
			crawler: fakeCrawler{err: errors.New("boom")},
			outcome: OutcomeSourceFailure, failure: "other",
		},
		{
			name:    "deferral by failure kind",
			repo:    newClaimRepo(),
			crawler: fakeCrawler{err: &ports.SourceError{Kind: "source_wait"}},
			outcome: OutcomeDeferral, failure: "source_wait",
		},
		{
			name:    "deferral by source status kind",
			repo:    newClaimRepo(),
			crawler: fakeCrawler{err: errors.New("source unavailable"), result: vehicle.CrawlResult{Sources: []vehicle.SourceStatus{{Status: "error", FailureKind: "budget_unavailable"}}}},
			outcome: OutcomeDeferral, failure: "budget_unavailable",
		},
		{
			name: "claim error reported without task",
			repo: fakeClaimRepo{claimErr: errors.New("connection refused")},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			metrics := newRecording()
			service := &Service{Store: &testCase.repo, Crawler: &testCase.crawler, Metrics: metrics}
			_, _ = service.WorkOne(context.Background())
			active, attempts := metrics.snapshot()
			if active["page"] != 0 {
				t.Fatalf("active gauge not drained: %v", active)
			}
			if testCase.repo.claimErr != nil {
				if metrics.claims["page"] == 0 {
					t.Fatal("claim error not reported")
				}
				if len(attempts) != 0 {
					t.Fatalf("claim error must not count as attempt: %v", attempts)
				}
				return
			}
			if len(attempts) != 1 {
				t.Fatalf("expected one attempt, got %v", attempts)
			}
			if attempts[0].outcome != testCase.outcome {
				t.Fatalf("outcome %q, want %q", attempts[0].outcome, testCase.outcome)
			}
			if attempts[0].failure != testCase.failure {
				t.Fatalf("failure %q, want %q", attempts[0].failure, testCase.failure)
			}
			if attempts[0].source != string(vehicle.PlatformDivar) {
				t.Fatalf("source %q", attempts[0].source)
			}
		})
	}
}

func TestRunLoopTicksIncludingIdlePolls(t *testing.T) {
	metrics := newRecording()
	repo := &fakeClaimRepo{} // no work available
	service := &Service{Store: repo, Crawler: &fakeCrawler{}, Metrics: metrics, Loop: "2"}
	ctx, cancel := context.WithCancel(context.Background())
	go service.Run(ctx, func(error) {})
	time.Sleep(1500 * time.Millisecond)
	cancel()
	metrics.mu.Lock()
	ticks := metrics.ticks["page"]
	metrics.mu.Unlock()
	if ticks < 2 {
		t.Fatalf("idle loop must still tick: %d", ticks)
	}
	_, attempts := metrics.snapshot()
	if len(attempts) != 0 {
		t.Fatalf("idle loop must not invent attempts: %v", attempts)
	}
}

func TestNilMetricsIsSafe(t *testing.T) {
	service := &Service{Store: &fakeClaimRepo{}, Crawler: &fakeCrawler{}}
	if _, err := service.WorkOne(context.Background()); err != nil {
		t.Fatal(err)
	}
}
