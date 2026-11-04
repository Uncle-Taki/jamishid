package crawljobs

import (
	"context"
	"errors"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type DetailTask struct {
	Facts                *vehicle.NormalizedVehicleListing
	Listing              vehicle.NormalizedVehicleListing
	Token, Hash, Purpose string
	Attempts             int
}
type DetailRepository interface {
	ClaimDetail(context.Context) (DetailTask, bool, error)
	CompleteDetail(context.Context, DetailTask, vehicle.NormalizedVehicleListing, int, string) error
}
type DetailService struct {
	Store   DetailRepository
	Fetch   func(context.Context, vehicle.Platform, string) (vehicle.NormalizedVehicleListing, error)
	Merge   func(*vehicle.NormalizedVehicleListing, vehicle.NormalizedVehicleListing)
	Metrics WorkerMetrics
	// Loop identifies this loop instance within the detail pool (for heartbeats).
	Loop string
}

func (s *DetailService) WorkOne(ctx context.Context) (bool, error) {
	metrics := workerMetrics(s.Metrics)
	task, ok, err := s.Store.ClaimDetail(ctx)
	if err != nil {
		metrics.ClaimError("detail")
		return ok, err
	}
	if !ok {
		return false, nil
	}
	finish := metrics.TaskStart("detail", string(task.Listing.Platform))
	outcome, failure := OutcomeSuccess, "none"
	defer func() { finish(outcome, failure) }()
	work, cancel := context.WithTimeout(ports.WithCrawlPolicy(ctx, ports.CrawlPolicy{RefreshDetails: true}), 15*time.Second)
	defer cancel()
	facts, fetchErr := s.Fetch(work, task.Listing.Platform, task.Listing.URL)
	if ctx.Err() != nil {
		outcome, failure = OutcomeCanceled, "canceled"
		return true, ctx.Err()
	}
	code, kind := 200, ""
	row := task.Listing
	if fetchErr != nil {
		code = 0
		kind = ports.FailureKind(fetchErr)
		var source *ports.SourceError
		if errors.As(fetchErr, &source) {
			code = source.StatusCode
		}
		if kind == "source_wait" || kind == "budget_unavailable" || kind == "egress_unavailable" || code == 429 {
			outcome, failure = OutcomeDeferral, kind
		} else {
			outcome, failure = OutcomeSourceFailure, kind
		}
	} else {
		task.Facts = &facts
		s.Merge(&row, facts)
	}
	if err := s.Store.CompleteDetail(ctx, task, row, code, kind); err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			outcome, failure = OutcomeCanceled, "canceled"
		case errors.Is(err, ErrLeaseLost):
			outcome, failure = OutcomeLeaseLost, "lease_lost"
		default:
			outcome, failure = OutcomePersistenceFailure, ports.FailureKind(err)
		}
		return true, err
	}
	return true, nil
}
func (s *DetailService) Run(ctx context.Context, report func(error)) {
	metrics := workerMetrics(s.Metrics)
	loop := s.Loop
	if loop == "" {
		loop = "1"
	}
	for ctx.Err() == nil {
		metrics.LoopTick("detail", loop)
		worked, err := s.WorkOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			report(err)
		}
		if worked && err == nil {
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

type ListingHistory struct {
	Listing vehicle.NormalizedVehicleListing `json:"listing"`
	Events  []HistoryEvent                   `json:"events"`
	Offset  int                              `json:"offset"`
}
type HistoryEvent struct {
	ID         int64                            `json:"observationId"`
	At         time.Time                        `json:"observedAt"`
	Kind       string                           `json:"kind"`
	StatusCode int                              `json:"statusCode"`
	Error      string                           `json:"errorKind"`
	Data       vehicle.NormalizedVehicleListing `json:"data"`
}

type LifecycleRepository interface {
	History(context.Context, vehicle.Platform, string, int) (ListingHistory, error)
	Recheck(context.Context, vehicle.Platform, string) error
}
