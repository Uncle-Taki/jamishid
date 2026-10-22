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
					"middle_description_text": "1400 | 20,000 km | بی‌رنگ"
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
}

func TestScraper_Fallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // Mock WAF block
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
