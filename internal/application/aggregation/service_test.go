package aggregation

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/memory"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type mockScraper struct {
	platform vehicle.Platform
	delay    time.Duration
	result   []vehicle.NormalizedVehicleListing
	err      error
	calls    atomic.Int32
}

func (m *mockScraper) Scrape(ctx context.Context, criteria vehicle.SearchCriteria) ([]vehicle.NormalizedVehicleListing, error) {
	m.calls.Add(1)
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return m.result, m.err
}

func (m *mockScraper) Platform() vehicle.Platform {
	return m.platform
}

func TestAggregationService_AggregateListings(t *testing.T) {
	cache := memory.NewCache(24 * time.Hour)
	weights := vehicle.ScoreWeights{
		PriceWeight:     40,
		ConditionWeight: 30,
		MileageWeight:   20,
		YearWeight:      10,
	}

	scrapers := []struct {
		platform vehicle.Platform
		result   []vehicle.NormalizedVehicleListing
		err      error
	}{
		{
			platform: vehicle.PlatformDivar,
			result: []vehicle.NormalizedVehicleListing{
				{ID: "1", Platform: vehicle.PlatformDivar, PriceToman: 100, Title: "Divar Car", Year: 1400},
			},
		},
		{
			platform: vehicle.PlatformBama,
			result: []vehicle.NormalizedVehicleListing{
				{ID: "2", Platform: vehicle.PlatformBama, PriceToman: 120, Title: "Bama Car", Year: 1395},
			},
		},
		{
			platform: vehicle.PlatformSheypoor,
			err:      errors.New("sheypoor failed"), // test partial failure
		},
	}

	import_ports_scrapers := make([]ports.MarketplaceScraper, 0)

	for _, sc := range scrapers {
		import_ports_scrapers = append(import_ports_scrapers, &mockScraper{platform: sc.platform, result: sc.result, err: sc.err})
	}

	svc := NewAggregationService(import_ports_scrapers, cache, weights)

	ctx := context.Background()
	criteria := vehicle.SearchCriteria{Query: "test"}

	results, err := svc.AggregateListings(ctx, criteria)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 results (1 failure ignored), got %d", len(results))
	}

	if _, err := svc.AggregateListings(ctx, criteria); err != nil {
		t.Fatal(err)
	}
	for _, scraper := range import_ports_scrapers {
		mock := scraper.(*mockScraper)
		want := int32(1)
		if mock.err != nil {
			want = 2
		}
		if mock.calls.Load() != want {
			t.Errorf("source %s fetched %d times, want %d; failures must retry, successes must be reused", mock.platform, mock.calls.Load(), want)
		}
	}

	if len(results) == 2 {
		if results[0].Score.TotalScore < results[1].Score.TotalScore {
			t.Errorf("expected descending sort by total score")
		}
	}
}

func TestAggregationService_DateCutoff(t *testing.T) {
	cache := memory.NewCache(24 * time.Hour)
	weights := vehicle.ScoreWeights{PriceWeight: 100}

	nowStr := time.Now().Format(time.RFC3339)
	oldStr := time.Now().Add(-10 * time.Hour).Format(time.RFC3339)

	scraper := &mockScraper{
		platform: vehicle.PlatformDivar,
		result: []vehicle.NormalizedVehicleListing{
			{ID: "1", PublishedAt: nowStr},
			{ID: "2", PublishedAt: oldStr},
		},
	}

	svc := NewAggregationService([]ports.MarketplaceScraper{scraper}, cache, weights)

	hours := 5
	criteria := vehicle.SearchCriteria{DateCutoffHours: &hours}

	results, _ := svc.AggregateListings(context.Background(), criteria)

	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "1" {
		t.Errorf("expected result ID 1")
	}
}

func TestAllSourcesFailedIsAnError(t *testing.T) {
	svc := NewAggregationService([]ports.MarketplaceScraper{&mockScraper{platform: vehicle.PlatformDivar, err: errors.New("blocked")}}, memory.NewCache(24*time.Hour), vehicle.ScoreWeights{})
	result, err := svc.AggregateWithSources(context.Background(), vehicle.SearchCriteria{Query: "206"})
	if !errors.Is(err, ErrSourcesUnavailable) || len(result.Listings) != 0 || len(result.Sources) != 1 || result.Sources[0].Status != "unavailable" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestPartialSourcesReportFailureAndRespectPlatform(t *testing.T) {
	divar := &mockScraper{platform: vehicle.PlatformDivar, result: []vehicle.NormalizedVehicleListing{{ID: "real", Platform: vehicle.PlatformDivar, PriceToman: 500}}}
	bama := &mockScraper{platform: vehicle.PlatformBama, err: errors.New("blocked")}
	svc := NewAggregationService([]ports.MarketplaceScraper{divar, bama}, memory.NewCache(24*time.Hour), vehicle.ScoreWeights{PriceWeight: 100})
	result, err := svc.AggregateWithSources(context.Background(), vehicle.SearchCriteria{Query: "206"})
	if err != nil || len(result.Listings) != 1 || result.Sources[1].Status != "unavailable" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	p := vehicle.PlatformDivar
	criteria := vehicle.SearchCriteria{Query: "206", Platform: &p}
	result, err = svc.AggregateWithSources(context.Background(), criteria)
	if err != nil || len(result.Sources) != 1 {
		t.Fatalf("platform filter ignored: %+v %v", result, err)
	}
	result, err = svc.AggregateWithSources(context.Background(), criteria)
	if err != nil || !result.Cached || len(result.Listings) != 1 {
		t.Fatalf("cache failed: %+v %v", result, err)
	}
}

func TestFiltersDoNotTreatUnknownFactsAsKnown(t *testing.T) {
	hours := 24
	maxMileage := 1000.0
	unknown := vehicle.NormalizedVehicleListing{MileageKm: -1, ScrapedAt: time.Now().Format(time.RFC3339)}
	if matches(unknown, vehicle.SearchCriteria{DateCutoffHours: &hours}) {
		t.Fatal("fetch time used as publication time")
	}
	if matches(unknown, vehicle.SearchCriteria{MaxMileage: &maxMileage}) {
		t.Fatal("unknown mileage treated as zero")
	}
}

type coverageScraper struct{ calls int }

func (s *coverageScraper) Platform() vehicle.Platform { return vehicle.PlatformBama }
func (s *coverageScraper) Scrape(_ context.Context, c vehicle.SearchCriteria) ([]vehicle.NormalizedVehicleListing, error) {
	s.calls++
	return []vehicle.NormalizedVehicleListing{{ID: c.NativeCategory, Platform: vehicle.PlatformBama, Title: c.NativeCategory, VehicleType: vehicle.VehicleTypeCar}}, nil
}
func TestNativeCoverageCategoriesReachAdaptersAndHaveSeparateCacheKeys(t *testing.T) {
	source := &coverageScraper{}
	service := NewAggregationService([]ports.MarketplaceScraper{source}, memory.NewCache(time.Hour), vehicle.ScoreWeights{PriceWeight: 100})
	for _, category := range []string{"first-brand", "second-brand", "first-brand"} {
		result, err := service.AggregateWithSources(context.Background(), vehicle.SearchCriteria{Type: vehicle.VehicleTypeCar, CitySlug: "iran", NativeCategory: category})
		if err != nil || len(result.Listings) != 1 || result.Listings[0].ID != category {
			t.Fatalf("lost native partition %s: %+v %v", category, result, err)
		}
	}
	if source.calls != 2 {
		t.Fatalf("partition cache calls: %d", source.calls)
	}
}

func TestAggregationService_AccessorAndObserverMethods(t *testing.T) {
	cache := memory.NewCache(time.Hour)
	scraper := &mockScraper{platform: vehicle.PlatformDivar}
	svc := NewAggregationService([]ports.MarketplaceScraper{scraper}, cache, vehicle.ScoreWeights{})

	svc.SetObserver(ports.NoopObserver{})
	if len(svc.GetScrapers()) != 1 {
		t.Errorf("expected 1 scraper, got %d", len(svc.GetScrapers()))
	}
	if svc.GetCache() != cache {
		t.Errorf("expected cache instance")
	}

	// Canceled context in sourcePage
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := svc.sourcePage(canceledCtx, scraper, vehicle.SearchCriteria{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestMatches_AllCriteriaBranches(t *testing.T) {
	minPrice := 100.0
	maxPrice := 500.0
	minYear := 1395
	maxYear := 1402
	minMileage := 5000.0
	maxMileage := 50000.0
	cutoff := 48

	now := time.Now()
	recent := now.Add(-10 * time.Hour).Format(time.RFC3339)
	old := now.Add(-100 * time.Hour).Format(time.RFC3339)

	validRow := vehicle.NormalizedVehicleListing{
		PriceToman:  300.0,
		Year:        1400,
		MileageKm:   20000.0,
		PublishedAt: recent,
	}

	criteriaAll := vehicle.SearchCriteria{
		MinPrice:        &minPrice,
		MaxPrice:        &maxPrice,
		MinYear:         &minYear,
		MaxYear:         &maxYear,
		MinMileage:      &minMileage,
		MaxMileage:      &maxMileage,
		DateCutoffHours: &cutoff,
	}

	if !matches(validRow, criteriaAll) {
		t.Errorf("expected valid row to match all criteria")
	}

	// MinPrice fail
	tooCheap := validRow
	tooCheap.PriceToman = 50.0
	if matches(tooCheap, criteriaAll) {
		t.Errorf("too cheap row should not match")
	}

	// MaxPrice fail
	tooExpensive := validRow
	tooExpensive.PriceToman = 600.0
	if matches(tooExpensive, criteriaAll) {
		t.Errorf("too expensive row should not match")
	}

	// MinYear fail
	tooOldYear := validRow
	tooOldYear.Year = 1390
	if matches(tooOldYear, criteriaAll) {
		t.Errorf("too old year should not match")
	}

	// MaxYear fail
	tooNewYear := validRow
	tooNewYear.Year = 1405
	if matches(tooNewYear, criteriaAll) {
		t.Errorf("too new year should not match")
	}

	// MinMileage fail
	tooLowMileage := validRow
	tooLowMileage.MileageKm = 1000.0
	if matches(tooLowMileage, criteriaAll) {
		t.Errorf("too low mileage should not match")
	}

	// MaxMileage fail
	tooHighMileage := validRow
	tooHighMileage.MileageKm = 90000.0
	if matches(tooHighMileage, criteriaAll) {
		t.Errorf("too high mileage should not match")
	}

	// Date cutoff fail
	tooOldDate := validRow
	tooOldDate.PublishedAt = old
	if matches(tooOldDate, criteriaAll) {
		t.Errorf("too old published date should not match")
	}
}

type refresherCache struct {
	ports.ListingCacheRepository
	refreshed bool
}

func (r *refresherCache) RefreshPage(ctx context.Context, key string, fetch func(context.Context) (vehicle.ListingPage, error)) (vehicle.ListingPage, bool, error) {
	r.refreshed = true
	p, err := fetch(ctx)
	return p, false, err
}

func TestAggregation_ErrorKindsAndRefresher(t *testing.T) {
	cache := &refresherCache{ListingCacheRepository: memory.NewCache(time.Hour)}
	mockSc := &mockScraper{
		platform: vehicle.PlatformDivar,
		err:      &ports.SourceError{StatusCode: 404, Kind: "http"},
	}
	mock500 := &mockScraper{
		platform: vehicle.PlatformBama,
		err:      &ports.SourceError{StatusCode: 503, Kind: "http"},
	}
	svc := NewAggregationService([]ports.MarketplaceScraper{mockSc, mock500}, cache, vehicle.ScoreWeights{})

	// 1. Error status codes mapped to http_4xx and http_5xx
	res, err := svc.AggregateWithSources(context.Background(), vehicle.SearchCriteria{Query: "test"})
	if err == nil {
		t.Fatalf("expected ErrSourcesUnavailable, got nil")
	}
	for _, s := range res.Sources {
		if s.Platform == vehicle.PlatformDivar && s.FailureKind != "http_4xx" {
			t.Errorf("expected http_4xx for divar, got %s", s.FailureKind)
		}
		if s.Platform == vehicle.PlatformBama && s.FailureKind != "http_5xx" {
			t.Errorf("expected http_5xx for bama, got %s", s.FailureKind)
		}
	}

	// 2. No matching marketplaces
	unknownPlat := vehicle.Platform("unknown")
	_, err = svc.AggregateWithSources(context.Background(), vehicle.SearchCriteria{Platform: &unknownPlat})
	if err == nil || err.Error() != "no matching marketplaces" {
		t.Fatalf("expected no matching marketplaces, got %v", err)
	}

	// 3. SkipDetails policy appends :summary to key, and Refresh triggers ListingPageRefresher
	validSc := &mockScraper{
		platform: vehicle.PlatformDivar,
		result:   []vehicle.NormalizedVehicleListing{{ID: "1", Platform: vehicle.PlatformDivar, Title: "Car"}},
	}
	validSvc := NewAggregationService([]ports.MarketplaceScraper{validSc}, cache, vehicle.ScoreWeights{})
	ctxPolicy := ports.WithCrawlPolicy(context.Background(), ports.CrawlPolicy{SkipDetails: true, Refresh: true})
	validRes, err := validSvc.AggregateWithSources(ctxPolicy, vehicle.SearchCriteria{Query: "car"})
	if err != nil || len(validRes.Listings) != 1 {
		t.Fatalf("expected success with policy, got len=%d err=%v", len(validRes.Listings), err)
	}
	if !cache.refreshed {
		t.Errorf("expected RefreshPage to be called on refresher cache")
	}
}
