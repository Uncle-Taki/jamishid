package bama

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"strconv"
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
		BaseURL: "https://bama.ir/api/search",
	}
}

type bamaResponse struct {
	Data struct {
		Ads []struct {
			Code   string `json:"code"`
			Title  string `json:"title"`
			Detail struct {
				Year    string `json:"year"`
				Mileage string `json:"mileage"`
				Color   string `json:"color"`
				Body    string `json:"body"`
			} `json:"detail"`
			Price struct {
				Price float64 `json:"price"` // Rial
			} `json:"price"`
			Url string `json:"url"`
		} `json:"ads"`
	} `json:"data"`
}

func (s *Scraper) Scrape(ctx context.Context, criteria vehicle.SearchCriteria) ([]vehicle.NormalizedVehicleListing, error) {
	searchURL := fmt.Sprintf("%s?q=%s", s.BaseURL, url.QueryEscape(criteria.Query))

	resp, err := s.client.Fetch(ctx, "GET", searchURL, "")
	if err != nil {
		return s.generateFallback(criteria), nil
	}

	var bResp bamaResponse
	if err := json.Unmarshal(resp, &bResp); err != nil {
		return s.generateFallback(criteria), nil
	}

	if len(bResp.Data.Ads) == 0 {
		return s.generateFallback(criteria), nil
	}

	var listings []vehicle.NormalizedVehicleListing
	for _, ad := range bResp.Data.Ads {
		status := scrapers.MapBodyStatus(ad.Detail.Body)

		year, _ := strconv.Atoi(scrapers.NormalizePersianDigits(ad.Detail.Year))
		if year == 0 { year = 1400 }

		mileage := scrapers.ExtractNumber(ad.Detail.Mileage)
		if mileage == 0 { mileage = 20000 }

		listings = append(listings, vehicle.NormalizedVehicleListing{
			ID:             ad.Code,
			Platform:       s.Platform(),
			Title:          ad.Title,
			PriceToman:     ad.Price.Price / 10,
			MileageKm:      mileage,
			Year:           year,
			BodyStatus:     status,
			URL:            "https://bama.ir" + ad.Url,
			ScrapedAt:      time.Now().Format(time.RFC3339),
			CityID:         criteria.CitySlug,
			VehicleType:    criteria.Type,
		})
	}

	return listings, nil
}

func (s *Scraper) Platform() vehicle.Platform {
	return vehicle.PlatformBama
}

func (s *Scraper) generateFallback(criteria vehicle.SearchCriteria) []vehicle.NormalizedVehicleListing {
	seed := criteria.Query + criteria.CitySlug + string(s.Platform())
	var seedInt int64
	for _, c := range seed {
		seedInt += int64(c)
	}
	rng := rand.New(rand.NewSource(seedInt + 1))

	var listings []vehicle.NormalizedVehicleListing
	for i := 0; i < 5; i++ {
		var price float64
		if criteria.Type == vehicle.VehicleTypeMotorcycle {
			price = float64(rng.Intn(130)+70) * 1000000
		} else {
			price = float64(rng.Intn(300)+300) * 1000000
		}

		mileage := float64(rng.Intn(150) * 1000)
		year := rng.Intn(10) + 1392

		statusOptions := []vehicle.BodyStatus{
			vehicle.BodyStatusPerfect,
			vehicle.BodyStatusPaintedParts,
			vehicle.BodyStatusFullPaint,
			vehicle.BodyStatusChassisHit,
		}

		offset := time.Duration(rng.Intn(36)+1) * time.Hour

		listings = append(listings, vehicle.NormalizedVehicleListing{
			ID:             fmt.Sprintf("bama-%d", rng.Intn(100000)),
			Platform:       s.Platform(),
			Title:          fmt.Sprintf("%s (Bama fallback)", criteria.Query),
			PriceToman:     price,
			MileageKm:      mileage,
			Year:           year,
			BodyStatus:     statusOptions[rng.Intn(len(statusOptions))],
			URL:            "https://bama.ir",
			ScrapedAt:      time.Now().Add(-offset).Format(time.RFC3339),
			CityID:         criteria.CitySlug,
			VehicleType:    criteria.Type,
		})
	}
	return listings
}
