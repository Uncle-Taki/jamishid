package divar

import (
	"context"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScraper_PublicSearch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Query().Get("q") != "پژو 206" {
			t.Errorf("wrong search request: %s", r.URL)
		}
		w.Write([]byte(`<script type="application/ld+json">[{"@type":"Car","name":"پژو 206","url":"https://divar.ir/v/car/real-token","productionDate":"1402","mileageFromOdometer":{"value":"15000"},"knownVehicleDamages":"intact","offers":{"price":"3500000000","priceCurrency":"IRR"}}]</script>`))
	}))
	defer ts.Close()
	sc := NewScraper(scrapers.NewSharedClient())
	sc.BaseURL = ts.URL
	rows, err := sc.Scrape(context.Background(), vehicle.SearchCriteria{Query: "پژو 206", CitySlug: "tehran"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v error=%v", rows, err)
	}
	if rows[0].PriceToman != 350000000 {
		t.Errorf("wrong Toman price: %v", rows[0].PriceToman)
	}
	if rows[0].ID != "divar-real-token" && rows[0].ID != "divar-car-real-token.html" {
		t.Errorf("wrong ID: %s", rows[0].ID)
	}

	page, err := sc.ScrapePage(context.Background(), vehicle.SearchCriteria{Query: "پژو 206", CitySlug: "tehran"}, "")
	if err != nil || len(page.Listings) != 1 {
		t.Fatalf("page=%v error=%v", page, err)
	}
}

func TestScraper_FailuresNeverGenerateListings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"blocked", 403, "blocked"}, {"malformed", 200, "<html>unexpected schema</html>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer ts.Close()
			sc := NewScraper(scrapers.NewSharedClient())
			sc.BaseURL = ts.URL
			rows, err := sc.Scrape(context.Background(), vehicle.SearchCriteria{Query: "206"})
			if err == nil || len(rows) != 0 {
				t.Fatalf("upstream failure produced rows=%v error=%v", rows, err)
			}
		})
	}
}
