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
	"golang.org/x/sync/singleflight"
)

type AggregationService struct {
	scrapers []ports.MarketplaceScraper
	cache    ports.ListingCacheRepository
	weights  vehicle.ScoreWeights
	pages    singleflight.Group
	observer ports.Observer
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
		observer: ports.NoopObserver{},
	}
}

func (s *AggregationService) SetObserver(observer ports.Observer) { s.observer = observer }

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
	var selected []ports.MarketplaceScraper
	for _, sc := range s.scrapers {
		if (criteria.Platform == nil || *criteria.Platform == sc.Platform()) && (len(criteria.Cursors) == 0 || criteria.Cursors[sc.Platform()] != "") {
			selected = append(selected, sc)
		}
	}
	if len(selected) == 0 {
		return result, fmt.Errorf("no matching marketplaces")
	}
	type sourceResult struct {
		nextCursor string
		page       int
		listings   []vehicle.NormalizedVehicleListing
		err        error
		cached     bool
	}
	batches := make([]sourceResult, len(selected))
	var wg sync.WaitGroup
	for i, sc := range selected {
		wg.Add(1)
		go func(i int, sc ports.MarketplaceScraper) {
			defer wg.Done()
			scrapeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			sourceCtx, finish := s.observer.Start(scrapeCtx, "source", sc.Platform())
			page, cached, err := s.sourcePage(sourceCtx, sc, criteria)
			cacheState := "miss"
			if cached {
				cacheState = "hit"
			}
			finish(ports.Observation{Cache: cacheState, Error: err, Listings: len(page.Listings)})
			batches[i] = sourceResult{listings: page.Listings, err: err, nextCursor: page.NextCursor, page: page.Page, cached: cached}
		}(i, sc)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	var filtered []vehicle.NormalizedVehicleListing
	seen := map[string]bool{}
	successes := 0
	result.Cached = true
	for i, batch := range batches {
		result.Cached = result.Cached && batch.cached
		status := vehicle.SourceStatus{Platform: selected[i].Platform(), Status: "ok", NextCursor: batch.nextCursor, Page: batch.page}
		if batch.err != nil {
			status.Status = "unavailable"
			status.Error = batch.err.Error()
			status.FailureKind = ports.FailureKind(batch.err)
			var sourceErr *ports.SourceError
			if errors.As(batch.err, &sourceErr) && sourceErr.StatusCode >= 400 && sourceErr.StatusCode < 500 && sourceErr.StatusCode != 429 {
				status.FailureKind = "http_4xx"
			}
			status.NextCursor = criteria.Cursors[selected[i].Platform()]
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
	_, finishScoring := s.observer.Start(ctx, "scoring", "")
	weights := s.weights
	if criteria.Weights != nil {
		weights = *criteria.Weights
	}
	for _, l := range filtered {
		result.Listings = append(result.Listings, vehicle.ScoredListing{NormalizedVehicleListing: l, Score: vehicle.CalculateCohortScores(l, filtered, weights)})
	}
	sort.SliceStable(result.Listings, func(i, j int) bool { return result.Listings[i].Score.TotalScore > result.Listings[j].Score.TotalScore })
	finishScoring(ports.Observation{Listings: len(result.Listings)})
	return result, nil
}

// Cache raw source pages, including continuation metadata. Filtering and scoring
// run on every response, so a cache hit never freezes a relative time filter.
func (s *AggregationService) sourcePage(ctx context.Context, sc ports.MarketplaceScraper, criteria vehicle.SearchCriteria) (vehicle.ListingPage, bool, error) {
	if err := ctx.Err(); err != nil {
		return vehicle.ListingPage{}, false, err
	}
	platform := sc.Platform()
	sourceCriteria := vehicle.SearchCriteria{Query: criteria.Query, Keywords: criteria.Keywords,
		BamaCategory: criteria.BamaCategory, CitySlug: criteria.CitySlug, Type: criteria.Type, Platform: &platform,
		Cursors: map[vehicle.Platform]string{platform: criteria.Cursors[platform]}}
	key := generateCacheKey(sourceCriteria)
	policy := ports.CrawlPolicyFrom(ctx)
	if policy.SkipDetails {
		key += ":summary"
	}
	if page, ok := s.cache.Get(ctx, key); ok && !policy.Refresh {
		return page, true, nil
	}
	type fetchedPage struct {
		page   vehicle.ListingPage
		cached bool
	}
	ready := s.pages.DoChan(key, func() (any, error) {
		// One caller leaving must not cancel a fetch shared by other callers.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if page, ok := s.cache.Get(fetchCtx, key); ok && !policy.Refresh {
			return fetchedPage{page: page, cached: true}, nil
		}
		fetch := func(ctx context.Context) (vehicle.ListingPage, error) {
			if pager, ok := sc.(interface {
				ScrapePage(context.Context, vehicle.SearchCriteria, string) (vehicle.ListingPage, error)
			}); ok {
				return pager.ScrapePage(ctx, sourceCriteria, sourceCriteria.Cursors[platform])
			}
			rows, err := sc.Scrape(ctx, sourceCriteria)
			return vehicle.ListingPage{Listings: rows}, err
		}
		if shared, ok := s.cache.(ports.ListingPageRefresher); ok && policy.Refresh {
			page, cached, err := shared.RefreshPage(fetchCtx, key, fetch)
			return fetchedPage{page: page, cached: cached}, err
		}
		if shared, ok := s.cache.(ports.ListingPageLoader); ok {
			page, cached, err := shared.LoadPage(fetchCtx, key, fetch)
			return fetchedPage{page: page, cached: cached}, err
		}
		page, err := fetch(fetchCtx)
		if err != nil {
			return nil, err
		}
		if err := fetchCtx.Err(); err != nil {
			return nil, err
		}
		_ = s.cache.Set(fetchCtx, key, page)
		return fetchedPage{page: page}, nil
	})
	select {
	case <-ctx.Done():
		return vehicle.ListingPage{}, false, ctx.Err()
	case fetched := <-ready:
		if fetched.Err != nil {
			return vehicle.ListingPage{}, false, fetched.Err
		}
		page := fetched.Val.(fetchedPage)
		return page.page, page.cached, nil
	}
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
