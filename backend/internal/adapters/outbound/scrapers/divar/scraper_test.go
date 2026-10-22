package divar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestScraper_HappyPath(t *testing.T) {
	mockResponse := `
	{
		"widget_list": [
			{
				"data": {
					"token": "divar-123",
					"title": "Peugeot 206",
					"action": {
						"payload": {
							"url": "/v/peugeot-206"
						}
					},
					"top_description_text": "1402 | 15,000 km",
					"middle_description_text": "بی‌رنگ",
					"bottom_description_text": "350,000 تومان"
				}
			}
		]
	}
	`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(mockResponse))
	}))
	defer ts.Close()

	client := scrapers.NewSharedClient()
	scraper := NewScraper(client)
	scraper.BaseURL = ts.URL

	criteria := vehicle.SearchCriteria{Query: "206"}
	listings, err := scraper.Scrape(context.Background(), criteria)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(listings) != 1 {
		t.Fatalf("expected 1 listing, got %d", len(listings))
	}

	if listings[0].ID != "divar-123" {
		t.Errorf("expected ID divar-123, got %s", listings[0].ID)
	}
	if listings[0].BodyStatus != vehicle.BodyStatusPerfect {
		t.Errorf("expected BodyStatusPerfect, got %s", listings[0].BodyStatus)
	}
	if listings[0].PriceToman != 350000 {
		t.Errorf("expected PriceToman 350000, got %f", listings[0].PriceToman)
	}
	if listings[0].Year != 1402 {
		t.Errorf("expected Year 1402, got %d", listings[0].Year)
	}
	if listings[0].MileageKm != 15000 {
		t.Errorf("expected MileageKm 15000, got %f", listings[0].MileageKm)
	}
}

func TestScraper_Fallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // Mock WAF block
	}))
	defer ts.Close()

	client := scrapers.NewSharedClient()
	scraper := NewScraper(client)
	scraper.BaseURL = ts.URL

	criteria := vehicle.SearchCriteria{Query: "206", Type: vehicle.VehicleTypeMotorcycle}
	listings, err := scraper.Scrape(context.Background(), criteria)

	if err != nil {
		t.Fatalf("expected no error from fallback, got %v", err)
	}

	if len(listings) != 5 {
		t.Fatalf("expected 5 fallback listings, got %d", len(listings))
	}
}

func TestScraper_MalformedJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{malformed json`))
	}))
	defer ts.Close()

	client := scrapers.NewSharedClient()
	scraper := NewScraper(client)
	scraper.BaseURL = ts.URL

	criteria := vehicle.SearchCriteria{Query: "206"}
	listings, err := scraper.Scrape(context.Background(), criteria)

	if err != nil {
		t.Fatalf("expected no error from fallback, got %v", err)
	}

	if len(listings) != 5 {
		t.Fatalf("expected 5 fallback listings, got %d", len(listings))
	}
}

func TestScraper_EmptyDataFallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"widget_list": []}`))
	}))
	defer ts.Close()

	client := scrapers.NewSharedClient()
	scraper := NewScraper(client)
	scraper.BaseURL = ts.URL

	criteria := vehicle.SearchCriteria{Query: "206"}
	listings, err := scraper.Scrape(context.Background(), criteria)

	if err != nil {
		t.Fatalf("expected no error from fallback, got %v", err)
	}

	if len(listings) != 5 {
		t.Fatalf("expected 5 fallback listings due to empty widget list, got %d", len(listings))
	}
}

func TestScraper_FetchErrorFallback(t *testing.T) {
	client := scrapers.NewSharedClient()
	scraper := NewScraper(client)
	scraper.BaseURL = "http://invalid-url-that-will-fail.local"

	criteria := vehicle.SearchCriteria{Query: "206"}
	listings, err := scraper.Scrape(context.Background(), criteria)

	if err != nil {
		t.Fatalf("expected no error from fallback, got %v", err)
	}

	if len(listings) != 5 {
		t.Fatalf("expected 5 fallback listings, got %d", len(listings))
	}
}
