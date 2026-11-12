package sheypoor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestScraper_PublicSearch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v10.0.0/search") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[{"id":"sheypoor-real-token","attributes":{"title":"پژو 206","url":"https://sheypoor.com/v/1.html","price":[{"amount":350000000,"currency":"تومان"}]}}]}`))
			return
		}
		if r.Method != "GET" || r.URL.Query().Get("q") != "پژو 206" {
			t.Errorf("wrong search request: %s", r.URL)
		}
		w.Write([]byte(`<a data-test-id="ad-item-real-token" href="/v/car-real-token.html"><img alt="پژو 206" src="https://cdn.sheypoor.com/1.webp"><div><span>۳۵۰,۰۰۰,۰۰۰</span><span data-test-id="icon-toman"></span></div></a>`))
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
	if rows[0].ID != "sheypoor-real-token" && rows[0].ID != "sheypoor-car-real-token.html" {
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
