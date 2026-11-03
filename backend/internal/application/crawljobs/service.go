package crawljobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

var ErrNotFound = errors.New("crawl job not found")
var ErrLeaseLost = errors.New("crawl page lease lost")

type Task struct {
	JobID, SearchID, Token, Cursor, Mode string
	Platform                             vehicle.Platform
	Page, Attempts                       int
	Criteria                             vehicle.SearchCriteria
}
type Observation struct {
	Listing      vehicle.NormalizedVehicleListing
	SummaryHash  string
	NeedsDetails bool
}
type Job struct {
	RetryAt        *time.Time                         `json:"retryAt,omitempty"`
	RetriesPending int                                `json:"retriesPending,omitempty"`
	CrawlDone      bool                               `json:"crawlDone"`
	DetailsPending int                                `json:"detailsPending"`
	DetailsFailed  int                                `json:"detailsFailed"`
	ID             string                             `json:"id"`
	Status         string                             `json:"status"`
	Mode           string                             `json:"mode"`
	Criteria       vehicle.SearchCriteria             `json:"criteria"`
	CreatedAt      time.Time                          `json:"createdAt"`
	UpdatedAt      time.Time                          `json:"updatedAt"`
	Sources        []vehicle.SourceStatus             `json:"sources"`
	Data           []vehicle.NormalizedVehicleListing `json:"data"`
	Done           bool                               `json:"done"`
	Truncated      bool                               `json:"truncated"`
	New            int                                `json:"newCount"`
	Changed        int                                `json:"changedCount"`
}
type InventoryQuery struct {
	Text, Model, City, Category, Type, Platform, Body, Sort string
	MinPrice, MaxPrice, MinMileage, MaxMileage              *float64
	MinYear, MaxYear                                        *int
	Limit, Offset                                           int
}
type InventoryResult struct {
	Data   []vehicle.NormalizedVehicleListing `json:"data"`
	Total  int                                `json:"total"`
	Limit  int                                `json:"limit"`
	Offset int                                `json:"offset"`
}
type InventoryCache interface {
	GetInventory(context.Context, string) (InventoryResult, bool)
	SetInventory(context.Context, string, InventoryResult)
}
type Repository interface {
	Submit(context.Context, vehicle.SearchCriteria, bool) (Job, error)
	Get(context.Context, string) (Job, error)
	Action(context.Context, string, string, vehicle.Platform) error
	Claim(context.Context) (Task, bool, error)
	Prepare(context.Context, []vehicle.NormalizedVehicleListing) ([]Observation, error)
	Commit(context.Context, Task, []Observation, string, int) error
	Fail(context.Context, Task, string) error
	Schedule(context.Context) error
	Search(context.Context, InventoryQuery) (InventoryResult, error)
}
type Service struct {
	Store   Repository
	Crawler interface {
		AggregateWithSources(context.Context, vehicle.SearchCriteria) (vehicle.CrawlResult, error)
	}
	Enrich   func(context.Context, []vehicle.NormalizedVehicleListing)
	Observer ports.Observer
}

func SearchIdentity(criteria vehicle.SearchCriteria) (string, vehicle.SearchCriteria) {
	criteria.Query = vehicle.NormalizeSearchText(criteria.Query)
	criteria.Keywords = vehicle.NormalizeSearchText(criteria.Keywords)
	if criteria.CitySlug == "" {
		criteria.CitySlug = "tehran"
	}
	if criteria.Type == "" {
		criteria.Type = vehicle.VehicleTypeCar
	}
	criteria.Cursors = nil
	criteria.MinPrice = nil
	criteria.MaxPrice = nil
	criteria.MinMileage = nil
	criteria.MaxMileage = nil
	criteria.MinYear = nil
	criteria.MaxYear = nil
	criteria.DateCutoffHours = nil
	criteria.Weights = nil
	raw, _ := json.Marshal(criteria)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), criteria
}

func SummaryHash(row vehicle.NormalizedVehicleListing) string {
	row.ScrapedAt = ""
	row.Lifecycle = nil
	row.Provenance = nil
	// Relative publication estimates shift with every fetch; they are not listing edits.
	row.PublishedAt = ""
	row.DetailsStatus = ""
	row.Attributes = nil
	raw, _ := json.Marshal(row)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Service) WorkOne(ctx context.Context) (bool, error) {
	task, ok, err := s.Store.Claim(ctx)
	if err != nil || !ok {
		return ok, err
	}
	workCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	observer := s.Observer
	if observer == nil {
		observer = ports.NoopObserver{}
	}
	workCtx, finish := observer.Start(workCtx, "crawl_job_page", task.Platform)
	criteria := task.Criteria
	criteria.Platform = &task.Platform
	if task.Cursor != "" {
		criteria.Cursors = map[vehicle.Platform]string{task.Platform: task.Cursor}
	}
	fetchCtx := ports.WithCrawlPolicy(workCtx, ports.CrawlPolicy{Refresh: task.Attempts == 1, SkipDetails: true})
	result, fetchErr := s.Crawler.AggregateWithSources(fetchCtx, criteria)
	if fetchErr == nil && (len(result.Sources) != 1 || result.Sources[0].Status != "ok") {
		fetchErr = errors.New("source unavailable")
	}
	if fetchErr != nil {
		finish(ports.Observation{Error: fetchErr})
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		kind := ports.FailureKind(fetchErr)
		if len(result.Sources) == 1 && result.Sources[0].FailureKind != "" {
			kind = result.Sources[0].FailureKind
		}
		return true, s.Store.Fail(ctx, task, kind)
	}
	rows := make([]vehicle.NormalizedVehicleListing, len(result.Listings))
	for i, row := range result.Listings {
		rows[i] = row.NormalizedVehicleListing
	}
	observations, err := s.Store.Prepare(workCtx, rows)
	if err != nil {
		finish(ports.Observation{Error: err})
		return true, err
	}
	status := result.Sources[0]
	err = s.Store.Commit(workCtx, task, observations, status.NextCursor, status.Page)
	finish(ports.Observation{Error: err, Listings: len(observations)})
	return true, err
}

func (s *Service) Run(ctx context.Context, report func(error)) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	schedule := time.NewTicker(time.Minute)
	defer schedule.Stop()
	for {
		select {
		case <-schedule.C:
			if err := s.Store.Schedule(ctx); err != nil {
				report(err)
			}
		default:
		}
		worked, err := s.WorkOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			report(err)
		}
		if ctx.Err() != nil {
			return
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-schedule.C:
			if err := s.Store.Schedule(ctx); err != nil {
				report(err)
			}
		}
	}
}
