package crawljobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type testStore struct {
	Repository
	task         Task
	committed    []Observation
	next, failed string
}

func (s *testStore) Claim(context.Context) (Task, bool, error) { return s.task, true, nil }
func (s *testStore) Prepare(_ context.Context, rows []vehicle.NormalizedVehicleListing) ([]Observation, error) {
	return []Observation{{Listing: rows[0], NeedsDetails: false}, {Listing: rows[1], NeedsDetails: true}}, nil
}
func (s *testStore) Commit(_ context.Context, _ Task, rows []Observation, next string, _ int) error {
	s.committed = rows
	s.next = next
	return nil
}
func (s *testStore) Fail(_ context.Context, _ Task, kind string) error { s.failed = kind; return nil }
func (s *testStore) Schedule(_ context.Context) error                 { return nil }

type crawlFunc func(context.Context, vehicle.SearchCriteria) (vehicle.CrawlResult, error)

func (f crawlFunc) AggregateWithSources(ctx context.Context, c vehicle.SearchCriteria) (vehicle.CrawlResult, error) {
	return f(ctx, c)
}
func TestWorkerCommitsSummariesWithoutWaitingForEnrichment(t *testing.T) {
	store := &testStore{task: Task{Platform: vehicle.PlatformBama, Cursor: "saved", Attempts: 1}}
	service := Service{Store: store, Crawler: crawlFunc(func(ctx context.Context, c vehicle.SearchCriteria) (vehicle.CrawlResult, error) {
		p := ports.CrawlPolicyFrom(ctx)
		if !p.Refresh || !p.SkipDetails || c.Cursors[vehicle.PlatformBama] != "saved" {
			t.Fatal("worker fetch policy/checkpoint lost")
		}
		return vehicle.CrawlResult{Listings: []vehicle.ScoredListing{{NormalizedVehicleListing: vehicle.NormalizedVehicleListing{ID: "old"}}, {NormalizedVehicleListing: vehicle.NormalizedVehicleListing{ID: "changed"}}}, Sources: []vehicle.SourceStatus{{Platform: vehicle.PlatformBama, Status: "ok", NextCursor: "next", Page: 2}}}, nil
	}), Enrich: func(ctx context.Context, rows []vehicle.NormalizedVehicleListing) {
		t.Fatal("page worker must not block on detail requests")
		if len(rows) != 1 || rows[0].ID != "changed" || !ports.CrawlPolicyFrom(ctx).RefreshDetails {
			t.Fatal("unchanged item enriched")
		}
		rows[0].DetailsStatus = "complete"
	}}
	if _, err := service.WorkOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.committed) != 2 || store.next != "next" || store.committed[1].Listing.DetailsStatus == "complete" {
		t.Fatal("page not persisted")
	}
}
func TestSearchIdentityExcludesDisplayFilters(t *testing.T) {
	a := vehicle.SearchCriteria{Query: "كيا ١٨٠"}
	b := vehicle.SearchCriteria{Query: "کیا 180"}
	price := 123.0
	b.MinPrice = &price
	first, _ := SearchIdentity(a)
	second, _ := SearchIdentity(b)
	if first != second {
		t.Fatal("normalization or display filters changed identity")
	}
	b.CitySlug = "shiraz"
	third, _ := SearchIdentity(b)
	if first == third {
		t.Fatal("city scope lost")
	}
}

func TestSummaryHashIgnoresObservationAndEstimatedPublicationTimes(t *testing.T) {
	a := vehicle.NormalizedVehicleListing{ID: "one", Title: "بنلی", PriceToman: 100, PublishedAt: "2026-11-01T12:00:00Z", ScrapedAt: "2026-11-02T12:00:00Z"}
	b := a
	b.PublishedAt = "2026-11-01T12:01:00Z"
	b.ScrapedAt = "2026-11-02T12:01:00Z"
	if SummaryHash(a) != SummaryHash(b) {
		t.Fatal("relative timestamps caused a false edit")
	}
	b.PriceToman = 200
	if SummaryHash(a) == SummaryHash(b) {
		t.Fatal("price edit was ignored")
	}
}

type errStore struct {
	testStore
	claimErr   error
	hasTask    bool
	prepareErr error
	commitErr  error
}

func (s *errStore) Claim(context.Context) (Task, bool, error) {
	return s.task, s.hasTask, s.claimErr
}
func (s *errStore) Prepare(_ context.Context, _ []vehicle.NormalizedVehicleListing) ([]Observation, error) {
	if s.prepareErr != nil {
		return nil, s.prepareErr
	}
	return []Observation{{Listing: vehicle.NormalizedVehicleListing{ID: "obs1"}}}, nil
}
func (s *errStore) Commit(_ context.Context, _ Task, _ []Observation, _ string, _ int) error {
	return s.commitErr
}

type failErrStore struct {
	errStore
	failErr error
}

func (s *failErrStore) Fail(_ context.Context, _ Task, _ string) error {
	return s.failErr
}

func TestWorkerErrorBranchesAndRun(t *testing.T) {
	ctx := context.Background()

	// 1. Claim returns false (no task)
	noTaskStore := &errStore{hasTask: false}
	service := Service{Store: noTaskStore}
	worked, err := service.WorkOne(ctx)
	if worked || err != nil {
		t.Fatalf("expected false, nil; got %v, %v", worked, err)
	}

	// 2. Claim returns error
	claimErrStore := &errStore{claimErr: errors.New("db down")}
	service = Service{Store: claimErrStore}
	worked, err = service.WorkOne(ctx)
	if worked || err == nil {
		t.Fatalf("expected false, err; got %v, %v", worked, err)
	}

	// 3. Crawler returns error and Fail succeeds
	crawlerErrStore := &errStore{hasTask: true}
	service = Service{
		Store: crawlerErrStore,
		Crawler: crawlFunc(func(ctx context.Context, c vehicle.SearchCriteria) (vehicle.CrawlResult, error) {
			return vehicle.CrawlResult{}, errors.New("upstream failed")
		}),
	}
	worked, err = service.WorkOne(ctx)
	if !worked || err != nil {
		t.Fatalf("expected true, nil when Fail succeeds; got %v, %v", worked, err)
	}

	// 3b. Crawler returns error and Fail returns ErrLeaseLost
	failErrStore := &failErrStore{errStore: errStore{hasTask: true}, failErr: ErrLeaseLost}
	service = Service{
		Store: failErrStore,
		Crawler: crawlFunc(func(ctx context.Context, c vehicle.SearchCriteria) (vehicle.CrawlResult, error) {
			return vehicle.CrawlResult{}, errors.New("upstream failed")
		}),
	}
	worked, err = service.WorkOne(ctx)
	if !worked || !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected true, ErrLeaseLost; got %v, %v", worked, err)
	}

	// 4. Prepare returns error
	prepErrStore := &errStore{hasTask: true, prepareErr: errors.New("prepare failed")}
	service = Service{
		Store: prepErrStore,
		Crawler: crawlFunc(func(ctx context.Context, c vehicle.SearchCriteria) (vehicle.CrawlResult, error) {
			return vehicle.CrawlResult{
				Listings: []vehicle.ScoredListing{{NormalizedVehicleListing: vehicle.NormalizedVehicleListing{ID: "x"}}},
				Sources:  []vehicle.SourceStatus{{Platform: vehicle.PlatformBama, Status: "ok"}},
			}, nil
		}),
	}
	worked, err = service.WorkOne(ctx)
	if !worked || err == nil {
		t.Fatalf("expected true, err; got %v, %v", worked, err)
	}

	// 5. Commit returns error
	commitErrStore := &errStore{hasTask: true, commitErr: errors.New("commit failed")}
	service = Service{
		Store: commitErrStore,
		Crawler: crawlFunc(func(ctx context.Context, c vehicle.SearchCriteria) (vehicle.CrawlResult, error) {
			return vehicle.CrawlResult{
				Listings: []vehicle.ScoredListing{{NormalizedVehicleListing: vehicle.NormalizedVehicleListing{ID: "x"}}},
				Sources:  []vehicle.SourceStatus{{Platform: vehicle.PlatformBama, Status: "ok"}},
			}, nil
		}),
	}
	worked, err = service.WorkOne(ctx)
	if !worked || err == nil {
		t.Fatalf("expected true, err; got %v, %v", worked, err)
	}

	// 6. Test Service.Run cancellation
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	var reported []error
	service.Run(cancelCtx, func(e error) {
		reported = append(reported, e)
	})
}

type togglePageStore struct {
	errStore
	calls int
}

func (s *togglePageStore) Claim(context.Context) (Task, bool, error) {
	s.calls++
	if s.calls == 1 {
		return Task{JobID: "task-1", Platform: vehicle.PlatformBama}, true, nil
	}
	if s.calls == 2 {
		return Task{}, false, errors.New("page loop err")
	}
	return Task{}, false, nil
}

func TestService_RunActive(t *testing.T) {
	store := &togglePageStore{}
	service := Service{
		Store: store,
		Crawler: crawlFunc(func(ctx context.Context, c vehicle.SearchCriteria) (vehicle.CrawlResult, error) {
			return vehicle.CrawlResult{
				Listings: []vehicle.ScoredListing{},
				Sources:  []vehicle.SourceStatus{{Platform: vehicle.PlatformBama, Status: "ok"}},
			}, nil
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	var reportedErr error
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	service.Run(ctx, func(err error) {
		reportedErr = err
	})

	if reportedErr == nil || reportedErr.Error() != "page loop err" {
		t.Fatalf("expected page loop err, got %v", reportedErr)
	}
}
