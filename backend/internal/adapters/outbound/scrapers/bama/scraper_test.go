package bama

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
		if r.Method != "GET" || r.URL.Query().Get("region") != "tehran" || r.URL.Path != "/car/peugeot" {
			t.Errorf("wrong search request: %s", r.URL)
		}
		w.Write([]byte(`<script id="__NUXT_DATA__" type="application/json">[{"code":1,"title":2,"trim":3,"url":4,"year":5,"mileage":6,"body_status":7,"price":8,"location":10},"real-token","پژو 206","تیپ 5","/car/detail-real-token","1402","کارکرد 15,000 کیلومتر","بدون رنگ",{"price":9},"350,000,000","تهران، مرکز"]</script>`))
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
	if rows[0].ID != "bama-real-token" && rows[0].ID != "bama-car-real-token.html" {
		t.Errorf("wrong ID: %s", rows[0].ID)
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
