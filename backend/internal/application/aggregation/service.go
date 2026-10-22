package aggregation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type AggregationService struct {
	scrapers []ports.MarketplaceScraper
	cache    ports.ListingCacheRepository
	weights  vehicle.ScoreWeights
}

func NewAggregationService(
	scrapers []ports.MarketplaceScraper,
	cache ports.ListingCacheRepository,
	weights vehicle.ScoreWeights,
) *AggregationService {
	return &AggregationService{
		scrapers: scrapers,
		cache:    cache,
		weights:  weights,
	}
}

func (s *AggregationService) GetScrapers() []ports.MarketplaceScraper {
	return s.scrapers
}

func (s *AggregationService) GetCache() ports.ListingCacheRepository {
	return s.cache
}

// AggregateListings coordinates multiple scrapers, gathers results, deduplicates, and scores them.
func (s *AggregationService) AggregateListings(ctx context.Context, criteria vehicle.SearchCriteria) ([]vehicle.ScoredListing, error) {
	cacheKey := generateCacheKey(criteria)

	if cachedResult, found := s.cache.Get(ctx, cacheKey); found {
		return cachedResult, nil
	}

	var allListings []vehicle.NormalizedVehicleListing
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, scraper := range s.scrapers {
		wg.Add(1)
		go func(sc ports.MarketplaceScraper) {
			defer wg.Done()

			scrapeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()

			listings, err := sc.Scrape(scrapeCtx, criteria)
			if err != nil {
				log.Printf("Error scraping %s: %v", sc.Platform(), err)
				return
			}

			mu.Lock()
			allListings = append(allListings, listings...)
			mu.Unlock()
		}(scraper)
	}

	wg.Wait()

	// Apply date cutoff
	var filteredListings []vehicle.NormalizedVehicleListing
	if criteria.DateCutoffHours != nil {
		cutoffTime := time.Now().Add(-time.Duration(*criteria.DateCutoffHours) * time.Hour)
		for _, l := range allListings {
			t, err := time.Parse(time.RFC3339, l.ScrapedAt)
			if err == nil && t.After(cutoffTime) {
				filteredListings = append(filteredListings, l)
			}
		}
	} else {
		filteredListings = allListings
	}

	if len(filteredListings) == 0 {
		return []vehicle.ScoredListing{}, nil
	}

	marketMedian := vehicle.CalculateBatchMedianPrice(filteredListings)

	minYear := 9999
	maxYear := 0
	for _, l := range filteredListings {
		if l.Year < minYear {
			minYear = l.Year
		}
		if l.Year > maxYear {
			maxYear = l.Year
		}
	}

	// Use dynamic weights if provided, else fallback to service default
	activeWeights := s.weights
	if criteria.Weights != nil {
		activeWeights = *criteria.Weights
	}

	var scoredListings []vehicle.ScoredListing
	for _, listing := range filteredListings {
		scores := vehicle.CalculateUtilityScores(listing, activeWeights, marketMedian, minYear, maxYear)

		scoredListings = append(scoredListings, vehicle.ScoredListing{
			NormalizedVehicleListing: listing,
			Score:                    scores,
		})
	}

	sort.SliceStable(scoredListings, func(i, j int) bool {
		return scoredListings[i].Score.TotalScore > scoredListings[j].Score.TotalScore
	})

	_ = s.cache.Set(ctx, cacheKey, scoredListings)

	return scoredListings, nil
}

func generateCacheKey(criteria vehicle.SearchCriteria) string {
	b, _ := json.Marshal(criteria)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}
