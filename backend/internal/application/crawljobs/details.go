package crawljobs

import (
	"context"
	"errors"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"time"
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
	Store DetailRepository
	Fetch func(context.Context, vehicle.Platform, string) (vehicle.NormalizedVehicleListing, error)
	Merge func(*vehicle.NormalizedVehicleListing, vehicle.NormalizedVehicleListing)
}

func (s *DetailService) WorkOne(ctx context.Context) (bool, error) {
	task, ok, err := s.Store.ClaimDetail(ctx)
	if err != nil || !ok {
		return ok, err
	}
	work, cancel := context.WithTimeout(ports.WithCrawlPolicy(ctx, ports.CrawlPolicy{RefreshDetails: true}), 15*time.Second)
	defer cancel()
	facts, fetchErr := s.Fetch(work, task.Listing.Platform, task.Listing.URL)
	if ctx.Err() != nil {
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
	} else {
		task.Facts = &facts
		s.Merge(&row, facts)
	}
	return true, s.Store.CompleteDetail(ctx, task, row, code, kind)
}
func (s *DetailService) Run(ctx context.Context, report func(error)) {
	for ctx.Err() == nil {
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
