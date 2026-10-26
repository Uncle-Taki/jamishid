package aggregation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

var ErrSourcesUnavailable = errors.New("all selected marketplaces are unavailable")

// AggregateListings coordinates multiple scrapers, gathers results, deduplicates, and scores them.
func (s *AggregationService) AggregateListings(ctx context.Context, criteria vehicle.SearchCriteria) ([]vehicle.ScoredListing, error) {
	result, err := s.AggregateWithSources(ctx, criteria)
	return result.Listings, err
}

func (s *AggregationService) AggregateWithSources(ctx context.Context, criteria vehicle.SearchCriteria) (vehicle.CrawlResult, error) {
	result := vehicle.CrawlResult{Listings: []vehicle.ScoredListing{}, Sources: []vehicle.SourceStatus{}}
	cacheKey := generateCacheKey(criteria)
	var selected []ports.MarketplaceScraper
	for _, sc := range s.scrapers {
		if (criteria.Platform == nil || *criteria.Platform == sc.Platform()) && (len(criteria.Cursors) == 0 || criteria.Cursors[sc.Platform()] != "") {
			selected = append(selected, sc)
		}
	}
	if len(selected) == 0 {
		return result, fmt.Errorf("no matching marketplaces")
	}
	if cached, found := s.cache.Get(ctx, cacheKey); found && criteria.BamaCategory == "" && len(criteria.Cursors) == 0 {
		result.Listings = cached
		result.Cached = true
		for _, sc := range selected {
			count := 0
			for _, l := range cached {
				if l.Platform == sc.Platform() {
					count++
				}
			}
			result.Sources = append(result.Sources, vehicle.SourceStatus{Platform: sc.Platform(), Status: "ok", Count: count})
		}
		return result, nil
	}
	type sourceResult struct {
		nextCursor string
		page       int
		listings   []vehicle.NormalizedVehicleListing
		err        error
	}
	batches := make([]sourceResult, len(selected))
	var wg sync.WaitGroup
	for i, sc := range selected {
		wg.Add(1)
		go func(i int, sc ports.MarketplaceScraper) {
			defer wg.Done()
			scrapeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if pager, ok := sc.(interface {
				ScrapePage(context.Context, vehicle.SearchCriteria, string) (vehicle.ListingPage, error)
			}); ok {
				page, err := pager.ScrapePage(scrapeCtx, criteria, criteria.Cursors[sc.Platform()])
				batches[i].listings = page.Listings
				batches[i].err = err
				batches[i].nextCursor = page.NextCursor
				batches[i].page = page.Page
			} else {
				batches[i].listings, batches[i].err = sc.Scrape(scrapeCtx, criteria)
			}
		}(i, sc)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	var filtered []vehicle.NormalizedVehicleListing
	seen := map[string]bool{}
	successes := 0
	failed := false
	for i, batch := range batches {
		status := vehicle.SourceStatus{Platform: selected[i].Platform(), Status: "ok", NextCursor: batch.nextCursor, Page: batch.page}
		if batch.err != nil {
			status.Status = "unavailable"
			status.Error = batch.err.Error()
			status.NextCursor = criteria.Cursors[selected[i].Platform()]
			failed = true
		} else {
			successes++
		}
		for _, l := range batch.listings {
			if batch.err != nil {
				break
			}
			key := string(l.Platform) + ":" + l.ID
			if seen[key] || !matches(l, criteria) {
				continue
			}
			seen[key] = true
			filtered = append(filtered, l)
			status.Count++
		}
		result.Sources = append(result.Sources, status)
	}
	if successes == 0 {
		return result, ErrSourcesUnavailable
	}
	median := vehicle.CalculateBatchMedianPrice(filtered)
	minYear, maxYear := 9999, 0
	for _, l := range filtered {
		y := vehicle.ScoringYear(l.Year)
		if y > 0 && y < minYear {
			minYear = y
		}
		if y > maxYear {
			maxYear = y
		}
	}
	weights := s.weights
	if criteria.Weights != nil {
		weights = *criteria.Weights
	}
	for _, l := range filtered {
		result.Listings = append(result.Listings, vehicle.ScoredListing{NormalizedVehicleListing: l, Score: vehicle.CalculateUtilityScores(l, weights, median, minYear, maxYear)})
	}
	sort.SliceStable(result.Listings, func(i, j int) bool { return result.Listings[i].Score.TotalScore > result.Listings[j].Score.TotalScore })
	// Do not hide partial failures behind a later cache hit.
	if !failed && criteria.BamaCategory == "" && len(criteria.Cursors) == 0 {
		_ = s.cache.Set(ctx, cacheKey, result.Listings)
	}
	return result, nil
}

func matches(l vehicle.NormalizedVehicleListing, c vehicle.SearchCriteria) bool {
	if c.MinPrice != nil && (l.PriceToman <= 0 || l.PriceToman < *c.MinPrice) {
		return false
	}
	if c.MaxPrice != nil && (l.PriceToman <= 0 || l.PriceToman > *c.MaxPrice) {
		return false
	}
	if c.MinYear != nil && (l.Year <= 0 || vehicle.ScoringYear(l.Year) < *c.MinYear) {
		return false
	}
	if c.MaxYear != nil && (l.Year <= 0 || vehicle.ScoringYear(l.Year) > *c.MaxYear) {
		return false
	}
	if c.MinMileage != nil && (l.MileageKm < 0 || l.MileageKm < *c.MinMileage) {
		return false
	}
	if c.MaxMileage != nil && (l.MileageKm < 0 || l.MileageKm > *c.MaxMileage) {
		return false
	}
	if c.DateCutoffHours != nil {
		t, err := time.Parse(time.RFC3339, l.PublishedAt)
		if err != nil || t.Before(time.Now().Add(-time.Duration(*c.DateCutoffHours)*time.Hour)) {
			return false
		}
	}
	return true
}

func generateCacheKey(criteria vehicle.SearchCriteria) string {
	b, _ := json.Marshal(criteria)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}
