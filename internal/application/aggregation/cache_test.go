package aggregation

import (
	"context"
	"errors"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/rediscache"
	"github.com/alicebob/miniredis/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/memory"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestSeparateServicesShareRedisPageAndFetchLease(t *testing.T) {
	server := miniredis.RunT(t)
	a, err := rediscache.New("redis://"+server.Addr(), 24*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := rediscache.New("redis://"+server.Addr(), 24*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var calls atomic.Int32
	scraper := &pageScraper{platform: vehicle.PlatformBama, fetch: func(context.Context, vehicle.SearchCriteria, string) (vehicle.ListingPage, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return vehicle.ListingPage{Page: 1, NextCursor: "continuation", Listings: []vehicle.NormalizedVehicleListing{{ID: "shared", Platform: vehicle.PlatformBama, PriceToman: 500}}}, nil
	}}
	services := []*AggregationService{NewAggregationService([]ports.MarketplaceScraper{scraper}, a, vehicle.ScoreWeights{}), NewAggregationService([]ports.MarketplaceScraper{scraper}, b, vehicle.ScoreWeights{})}
	criteria := vehicle.SearchCriteria{Query: "بنلی", BamaCategory: "benelli", CitySlug: "iran"}
	var wg sync.WaitGroup
	for _, service := range services {
		wg.Add(1)
		go func(service *AggregationService) {
			defer wg.Done()
			result, err := service.AggregateWithSources(context.Background(), criteria)
			if err != nil || len(result.Listings) != 1 || result.Sources[0].NextCursor != "continuation" {
				t.Errorf("service result: %+v %v", result, err)
			}
		}(service)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("services fetched same source twice: %d", calls.Load())
	}
	for _, service := range services {
		result, err := service.AggregateWithSources(context.Background(), criteria)
		if err != nil || !result.Cached {
			t.Fatalf("shared hit missing: %+v %v", result, err)
		}
	}
}

type pageScraper struct {
	platform vehicle.Platform
	fetch    func(context.Context, vehicle.SearchCriteria, string) (vehicle.ListingPage, error)
}

func (s *pageScraper) Platform() vehicle.Platform { return s.platform }
func (s *pageScraper) Scrape(ctx context.Context, criteria vehicle.SearchCriteria) ([]vehicle.NormalizedVehicleListing, error) {
	page, err := s.fetch(ctx, criteria, "")
	return page.Listings, err
}
func (s *pageScraper) ScrapePage(ctx context.Context, criteria vehicle.SearchCriteria, cursor string) (vehicle.ListingPage, error) {
	return s.fetch(ctx, criteria, cursor)
}

func TestCategoryAndContinuationPagesReuseCacheWithMetadata(t *testing.T) {
	var calls atomic.Int32
	scraper := &pageScraper{platform: vehicle.PlatformBama, fetch: func(ctx context.Context, criteria vehicle.SearchCriteria, cursor string) (vehicle.ListingPage, error) {
		calls.Add(1)
		if cursor == "" {
			return vehicle.ListingPage{Page: 1, NextCursor: "page-two", Listings: []vehicle.NormalizedVehicleListing{{ID: "one", Platform: vehicle.PlatformBama}}}, nil
		}
		return vehicle.ListingPage{Page: 2, NextCursor: "page-three", Listings: []vehicle.NormalizedVehicleListing{{ID: "two", Platform: vehicle.PlatformBama}}}, nil
	}}
	svc := NewAggregationService([]ports.MarketplaceScraper{scraper}, memory.NewCache(24*time.Hour), vehicle.ScoreWeights{PriceWeight: 100})
	criteria := vehicle.SearchCriteria{Query: "بنلی 180", BamaCategory: "benelli-tnt180s", CitySlug: "iran", Type: vehicle.VehicleTypeMotorcycle}
	for _, cursor := range []string{"", "page-two"} {
		criteria.Cursors = nil
		if cursor != "" {
			criteria.Cursors = map[vehicle.Platform]string{vehicle.PlatformBama: cursor}
		}
		first, err := svc.AggregateWithSources(context.Background(), criteria)
		if err != nil || first.Cached {
			t.Fatalf("initial page: %+v %v", first, err)
		}
		again, err := svc.AggregateWithSources(context.Background(), criteria)
		if err != nil || !again.Cached || len(again.Listings) != 1 || again.Sources[0] != first.Sources[0] || again.Listings[0].ID != first.Listings[0].ID {
			t.Fatalf("cached continuation changed: first=%+v again=%+v err=%v", first, again, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("refetched cached category pages: %d", calls.Load())
	}
}

func TestCachedPagesReapplyFiltersAndWeights(t *testing.T) {
	old := time.Now().Add(-25 * time.Hour).Format(time.RFC3339)
	recent := time.Now().Format(time.RFC3339)
	scraper := &mockScraper{platform: vehicle.PlatformDivar, result: []vehicle.NormalizedVehicleListing{
		{ID: "old", Platform: vehicle.PlatformDivar, PriceToman: 100, PublishedAt: old, BodyStatus: vehicle.BodyStatusPerfect},
		{ID: "recent", Platform: vehicle.PlatformDivar, PriceToman: 200, PublishedAt: recent, BodyStatus: vehicle.BodyStatusChassisHit},
	}}
	svc := NewAggregationService([]ports.MarketplaceScraper{scraper}, memory.NewCache(24*time.Hour), vehicle.ScoreWeights{ConditionWeight: 100})
	criteria := vehicle.SearchCriteria{Query: "206"}
	if _, err := svc.AggregateWithSources(context.Background(), criteria); err != nil {
		t.Fatal(err)
	}
	hours := 24
	criteria.DateCutoffHours = &hours
	criteria.Weights = &vehicle.ScoreWeights{PriceWeight: 100}
	result, err := svc.AggregateWithSources(context.Background(), criteria)
	if err != nil || !result.Cached || len(result.Listings) != 1 || result.Listings[0].ID != "recent" || result.Sources[0].Count != 1 {
		t.Fatalf("relative filtering not reapplied: %+v %v", result, err)
	}
	if result.Listings[0].Score.TotalScore != result.Listings[0].Score.PriceScore {
		t.Fatal("cached score ignored changed weights")
	}
	criteria.DateCutoffHours = nil
	maximum := 150.0
	criteria.MaxPrice = &maximum
	result, err = svc.AggregateWithSources(context.Background(), criteria)
	if err != nil || !result.Cached || len(result.Listings) != 1 || result.Listings[0].ID != "old" || scraper.calls.Load() != 1 {
		t.Fatalf("price filtering fetched again or lost raw rows: %+v %v calls=%d", result, err, scraper.calls.Load())
	}
}

func TestPageCacheSeparatesUpstreamSearchScope(t *testing.T) {
	scraper := &mockScraper{platform: vehicle.PlatformDivar}
	svc := NewAggregationService([]ports.MarketplaceScraper{scraper}, memory.NewCache(24*time.Hour), vehicle.ScoreWeights{})
	base := vehicle.SearchCriteria{Query: "206", BamaCategory: "peugeot-206", CitySlug: "tehran", Type: vehicle.VehicleTypeCar}
	variants := []vehicle.SearchCriteria{base, base, base, base, base, base}
	variants[1].CitySlug = "mashhad"
	variants[2].Keywords = "سالم"
	variants[3].BamaCategory = "peugeot-207"
	variants[4].Type = vehicle.VehicleTypeMotorcycle
	variants[5].Query = "207"
	for _, criteria := range variants {
		first, err := svc.AggregateWithSources(context.Background(), criteria)
		if err != nil || first.Cached {
			t.Fatalf("scope collision: %+v %v", first, err)
		}
		second, err := svc.AggregateWithSources(context.Background(), criteria)
		if err != nil || !second.Cached {
			t.Fatalf("valid empty page not cached: %+v %v", second, err)
		}
	}
	if scraper.calls.Load() != int32(len(variants)) {
		t.Fatalf("calls=%d", scraper.calls.Load())
	}
}

func TestConcurrentIdenticalSearchesFetchOnceAndCanceledWaitersReturn(t *testing.T) {
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	scraper := &pageScraper{platform: vehicle.PlatformBama, fetch: func(ctx context.Context, _ vehicle.SearchCriteria, _ string) (vehicle.ListingPage, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return vehicle.ListingPage{Page: 1}, nil
		case <-ctx.Done():
			return vehicle.ListingPage{}, ctx.Err()
		}
	}}
	svc := NewAggregationService([]ports.MarketplaceScraper{scraper}, memory.NewCache(24*time.Hour), vehicle.ScoreWeights{})
	criteria := vehicle.SearchCriteria{Query: "بنلی", BamaCategory: "benelli"}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.AggregateWithSources(context.Background(), criteria); err != nil {
				t.Error(err)
			}
		}()
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.AggregateWithSources(ctx, criteria); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("duplicate upstream fetches: %d", calls.Load())
	}
}

func TestFirstCallerCancelDoesNotCancelSharedPageFetch(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	scraper := &pageScraper{platform: vehicle.PlatformDivar, fetch: func(ctx context.Context, _ vehicle.SearchCriteria, _ string) (vehicle.ListingPage, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return vehicle.ListingPage{Page: 1, Listings: []vehicle.NormalizedVehicleListing{{ID: "shared", Platform: vehicle.PlatformDivar}}}, nil
		case <-ctx.Done():
			return vehicle.ListingPage{}, ctx.Err()
		}
	}}
	svc := NewAggregationService([]ports.MarketplaceScraper{scraper}, memory.NewCache(24*time.Hour), vehicle.ScoreWeights{})
	criteria := vehicle.SearchCriteria{Query: "206"}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := svc.AggregateWithSources(ctx, criteria); first <- err }()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller did not cancel: %v", err)
	}
	close(release)
	result, err := svc.AggregateWithSources(context.Background(), criteria)
	if err != nil || len(result.Listings) != 1 || calls.Load() != 1 {
		t.Fatalf("shared work lost: %+v %v calls=%d", result, err, calls.Load())
	}
}
