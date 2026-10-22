package aggregation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"sort"
	"strings"
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

	// Deduplicate by ID
	seen := make(map[string]bool)
	var dedupedListings []vehicle.NormalizedVehicleListing
	for _, l := range allListings {
		key := l.ID
		if l.URL != "" {
			key += "|" + l.URL
		}
		if !seen[key] {
			seen[key] = true
			dedupedListings = append(dedupedListings, l)
		}
	}

	// Apply filtering
	var filteredListings []vehicle.NormalizedVehicleListing
	var cutoffTime time.Time
	if criteria.DateCutoffHours != nil {
		cutoffTime = time.Now().Add(-time.Duration(*criteria.DateCutoffHours) * time.Hour)
	}

	for _, l := range dedupedListings {
		if criteria.DateCutoffHours != nil {
			t, err := time.Parse(time.RFC3339, l.ScrapedAt)
			if err == nil && t.Before(cutoffTime) {
				continue
			}
		}
		if criteria.MinPrice != nil && l.PriceToman < *criteria.MinPrice {
			continue
		}
		if criteria.MaxPrice != nil && l.PriceToman > *criteria.MaxPrice {
			continue
		}
		if criteria.MinYear != nil && l.Year < *criteria.MinYear {
			continue
		}
		if criteria.MaxYear != nil && l.Year > *criteria.MaxYear {
			continue
		}
		if criteria.MinMileage != nil && l.MileageKm < *criteria.MinMileage {
			continue
		}
		if criteria.MaxMileage != nil && l.MileageKm > *criteria.MaxMileage {
			continue
		}
		if criteria.Platform != nil && l.Platform != *criteria.Platform {
			continue
		}

		filteredListings = append(filteredListings, l)
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
	criteria.Query = strings.TrimSpace(criteria.Query)
	b, _ := json.Marshal(criteria)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}
