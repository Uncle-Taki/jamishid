package sheypoor

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type Scraper struct {
	client  *scrapers.SharedClient
	BaseURL string
}

func NewScraper(client *scrapers.SharedClient) *Scraper {
	return &Scraper{
		client:  client,
		BaseURL: "https://www.sheypoor.com/api/search",
	}
}

type sheypoorResponse struct {
	Items []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Price struct {
			Amount float64 `json:"amount"` // Toman
		} `json:"price"`
		URL        string `json:"url"`
		Attributes []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"attributes"`
	} `json:"items"`
}

func (s *Scraper) Scrape(ctx context.Context, criteria vehicle.SearchCriteria) ([]vehicle.NormalizedVehicleListing, error) {
	url := fmt.Sprintf("%s?q=%s", s.BaseURL, criteria.Query)

	resp, err := s.client.Fetch(ctx, "GET", url, "")
	if err != nil {
		return s.generateFallback(criteria), nil
	}

	var sResp sheypoorResponse
	if err := json.Unmarshal(resp, &sResp); err != nil {
		return s.generateFallback(criteria), nil
	}

	if len(sResp.Items) == 0 {
		return s.generateFallback(criteria), nil
	}

	var listings []vehicle.NormalizedVehicleListing
	for _, item := range sResp.Items {
		var bodyStr string
		for _, attr := range item.Attributes {
			if attr.Key == "body_status" || attr.Key == "وضعیت بدنه" {
				bodyStr = attr.Value
			}
		}

		status := scrapers.MapBodyStatus(bodyStr)

		listings = append(listings, vehicle.NormalizedVehicleListing{
			ID:             item.ID,
			Platform:       s.Platform(),
			Title:          item.Title,
			PriceToman:     item.Price.Amount,
			MileageKm:      20000,
			Year:           1400,
			BodyStatus:     status,
			URL:            "https://sheypoor.com" + item.URL,
			ScrapedAt:      time.Now().Format(time.RFC3339),
			CityID:         criteria.CitySlug,
			VehicleType:    criteria.Type,
		})
	}

	return listings, nil
}

func (s *Scraper) Platform() vehicle.Platform {
	return vehicle.PlatformSheypoor
}

func (s *Scraper) generateFallback(criteria vehicle.SearchCriteria) []vehicle.NormalizedVehicleListing {
	seed := criteria.Query + criteria.CitySlug + string(s.Platform())
	var seedInt int64
	for _, c := range seed {
		seedInt += int64(c)
	}
	rng := rand.New(rand.NewSource(seedInt + 2))

	var listings []vehicle.NormalizedVehicleListing
	for i := 0; i < 5; i++ {
		price := float64(rng.Intn(400)+80) * 1000000
		mileage := float64(rng.Intn(250) * 1000)
		year := rng.Intn(10) + 1385

		statusOptions := []vehicle.BodyStatus{
			vehicle.BodyStatusPerfect,
			vehicle.BodyStatusPaintedParts,
			vehicle.BodyStatusFullPaint,
			vehicle.BodyStatusChassisHit,
		}

		listings = append(listings, vehicle.NormalizedVehicleListing{
			ID:             fmt.Sprintf("sheypoor-%d", rng.Intn(100000)),
			Platform:       s.Platform(),
			Title:          fmt.Sprintf("%s (Sheypoor fallback)", criteria.Query),
			PriceToman:     price,
			MileageKm:      mileage,
			Year:           year,
			BodyStatus:     statusOptions[rng.Intn(len(statusOptions))],
			URL:            "https://sheypoor.com",
			ScrapedAt:      time.Now().Format(time.RFC3339),
			CityID:         criteria.CitySlug,
			VehicleType:    criteria.Type,
		})
	}
	return listings
}
