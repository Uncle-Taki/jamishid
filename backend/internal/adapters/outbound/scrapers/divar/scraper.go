package divar

import (
	"context"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type Scraper struct {
	client  *scrapers.SharedClient
	BaseURL string
}

func NewScraper(client *scrapers.SharedClient) *Scraper {
	return &Scraper{client: client, BaseURL: "https://divar.ir"}
}

func (s *Scraper) Scrape(ctx context.Context, criteria vehicle.SearchCriteria) ([]vehicle.NormalizedVehicleListing, error) {
	return scrapers.PublicPage(ctx, s.client, s.BaseURL, s.Platform(), criteria)
}

func (s *Scraper) Platform() vehicle.Platform { return vehicle.PlatformDivar }

func (s *Scraper) ScrapePage(ctx context.Context, criteria vehicle.SearchCriteria, cursor string) (vehicle.ListingPage, error) {
	return scrapers.PublicSearchPage(ctx, s.client, s.BaseURL, s.Platform(), criteria, cursor)
}
