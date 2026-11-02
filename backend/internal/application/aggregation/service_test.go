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
