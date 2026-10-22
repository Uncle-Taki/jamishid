package aggregation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type mockScraper struct {
	platform vehicle.Platform
	delay    time.Duration
	result   []vehicle.NormalizedVehicleListing
	err      error
}

func (m *mockScraper) Scrape(ctx context.Context, criteria vehicle.SearchCriteria) ([]vehicle.NormalizedVehicleListing, error) {
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

type mockCache struct {
	data map[string][]vehicle.ScoredListing
}

func (m *mockCache) Get(ctx context.Context, key string) ([]vehicle.ScoredListing, bool) {
	val, ok := m.data[key]
	return val, ok
}

func (m *mockCache) Set(ctx context.Context, key string, listings []vehicle.ScoredListing) error {
	if m.data == nil {
		m.data = make(map[string][]vehicle.ScoredListing)
	}
	m.data[key] = listings
	return nil
}

func TestAggregationService_AggregateListings(t *testing.T) {
	cache := &mockCache{}
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

	key := generateCacheKey(criteria)
	if cached, found := cache.Get(ctx, key); !found || len(cached) != 2 {
		t.Errorf("expected results to be cached")
	}

	if len(results) == 2 {
		if results[0].Score.TotalScore < results[1].Score.TotalScore {
			t.Errorf("expected descending sort by total score")
		}
	}
}

func TestAggregationService_FilteringAndDedup(t *testing.T) {
	cache := &mockCache{}
	weights := vehicle.ScoreWeights{PriceWeight: 100}

	nowStr := time.Now().Format(time.RFC3339)
	oldStr := time.Now().Add(-10 * time.Hour).Format(time.RFC3339)

	scraper1 := &mockScraper{
		platform: vehicle.PlatformDivar,
		result: []vehicle.NormalizedVehicleListing{
			{ID: "1", URL: "/a", ScrapedAt: nowStr, PriceToman: 100, Year: 1400, MileageKm: 10000, Platform: vehicle.PlatformDivar},
			{ID: "1", URL: "/a", ScrapedAt: nowStr, PriceToman: 100, Year: 1400, MileageKm: 10000, Platform: vehicle.PlatformDivar}, // Duplicate
			{ID: "2", URL: "/b", ScrapedAt: oldStr, PriceToman: 200, Year: 1390, MileageKm: 50000, Platform: vehicle.PlatformDivar}, // Excluded by date
			{ID: "3", URL: "/c", ScrapedAt: nowStr, PriceToman: 50, Year: 1395, MileageKm: 30000, Platform: vehicle.PlatformDivar},  // Excluded by min price
			{ID: "4", URL: "/d", ScrapedAt: nowStr, PriceToman: 500, Year: 1402, MileageKm: 5000, Platform: vehicle.PlatformDivar},  // Excluded by max price
			{ID: "5", URL: "/e", ScrapedAt: nowStr, PriceToman: 150, Year: 1380, MileageKm: 200000, Platform: vehicle.PlatformDivar}, // Excluded by min year
		},
	}

	scraper2 := &mockScraper{
		platform: vehicle.PlatformBama,
		result: []vehicle.NormalizedVehicleListing{
			{ID: "6", URL: "/f", ScrapedAt: nowStr, PriceToman: 120, Year: 1398, MileageKm: 40000, Platform: vehicle.PlatformBama}, // Excluded by platform filter
		},
	}

	svc := NewAggregationService([]ports.MarketplaceScraper{scraper1, scraper2}, cache, weights)

	hours := 5
	minPrice := 80.0
	maxPrice := 300.0
	minYear := 1385
	maxYear := 1401
	minMileage := 5000.0
	maxMileage := 80000.0
	platformFilter := vehicle.PlatformDivar

	criteria := vehicle.SearchCriteria{
		DateCutoffHours: &hours,
		MinPrice:        &minPrice,
		MaxPrice:        &maxPrice,
		MinYear:         &minYear,
		MaxYear:         &maxYear,
		MinMileage:      &minMileage,
		MaxMileage:      &maxMileage,
		Platform:        &platformFilter,
	}

	results, err := svc.AggregateListings(context.Background(), criteria)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "1" {
		t.Errorf("expected result ID 1 to pass all filters and dedup")
	}
}
