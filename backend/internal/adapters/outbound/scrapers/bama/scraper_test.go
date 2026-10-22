package bama

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
		"data": {
			"ads": [
				{
					"code": "bama-123",
					"title": "Samand",
					"detail": {
						"body": "چند لکه"
					},
					"price": {
						"price": 3000000000
					},
					"url": "/car/samand"
				}
			]
		}
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

	criteria := vehicle.SearchCriteria{Query: "samand"}
	listings, err := scraper.Scrape(context.Background(), criteria)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(listings) != 1 {
		t.Fatalf("expected 1 listing, got %d", len(listings))
	}

	if listings[0].ID != "bama-123" {
		t.Errorf("expected ID bama-123, got %s", listings[0].ID)
	}
	if listings[0].PriceToman != 300000000 {
		t.Errorf("expected Toman price 300,000,000, got %f", listings[0].PriceToman)
	}
	if listings[0].BodyStatus != vehicle.BodyStatusPaintedParts {
		t.Errorf("expected BodyStatusPaintedParts, got %s", listings[0].BodyStatus)
	}
}

func TestScraper_Fallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()

	client := scrapers.NewSharedClient()
	scraper := NewScraper(client)
	scraper.BaseURL = ts.URL

	criteria := vehicle.SearchCriteria{Query: "samand"}
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

	criteria := vehicle.SearchCriteria{Query: "samand"}
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
		w.Write([]byte(`{"data": {"ads": []}}`))
	}))
	defer ts.Close()

	client := scrapers.NewSharedClient()
	scraper := NewScraper(client)
	scraper.BaseURL = ts.URL

	criteria := vehicle.SearchCriteria{Query: "samand"}
	listings, err := scraper.Scrape(context.Background(), criteria)

	if err != nil {
		t.Fatalf("expected no error from fallback, got %v", err)
	}

	if len(listings) != 5 {
		t.Fatalf("expected 5 fallback listings due to empty ads list, got %d", len(listings))
	}
}
