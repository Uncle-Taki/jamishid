package scrapers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestBamaCityFiltersApplyToInitialAndContinuationRequests(t *testing.T) {
	for _, tc := range []struct{ city, route, api, location string }{
		{"tehran", "tehran-tehran", "tehran,tehran", "تهران / آزادی"},
		{"shiraz", "fars-shiraz", "fars,shiraz", "شیراز"},
		{"mashhad", "razavi_khorasan-mashhad", "razavi_khorasan,mashhad", "مشهد، سجاد"},
		{"iran", "", "", "شیراز"},
	} {
		t.Run(tc.city, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					if r.URL.Path != "/car/peugeot-206ir" || r.URL.Query().Get("region") != tc.route {
						t.Errorf("wrong page region: %s", r.URL)
					}
					w.Write([]byte(`<script id="__NUXT_DATA__">[{"ads":1,"filters":2},[],{"vehicle":3},[4],{"value":5},"peugeot,206ir"]</script>`))
					return
				}
				if r.URL.Path != "/cad/api/search" || r.URL.Query().Get("region") != tc.api {
					t.Errorf("wrong API region: %s", r.URL)
				}
				if tc.city == "iran" && r.URL.Query().Has("region") {
					t.Error("national search has region")
				}
				ads := []any{}
				for _, location := range []string{tc.location, "پاکدشت", "تهرانسر", ""} {
					ads = append(ads, map[string]any{"detail": map[string]any{"code": "id-" + location, "title": "پژو 206", "url": "/car/detail-" + location + "-peugeot-206ir-1402", "location": location}})
				}
				json.NewEncoder(w).Encode(map[string]any{"status": true, "data": map[string]any{"ads": ads}, "metadata": map[string]any{"has_next": calls == 2}})
			}))
			defer srv.Close()
			c := vehicle.SearchCriteria{Query: "پژو 206", Type: vehicle.VehicleTypeCar, CitySlug: tc.city, BamaCategory: "peugeot-206ir"}
			page, err := PublicSearchPage(context.Background(), NewSharedClient(), srv.URL, vehicle.PlatformBama, c, "")
			if err != nil {
				t.Fatal(err)
			}
			expected := 1
			if tc.city == "iran" {
				expected = 4
			}
			if len(page.Listings) != expected || page.NextCursor == "" {
				t.Fatalf("wrong city results: %+v", page)
			}
			if tc.city == "iran" && page.Listings[0].CityID != "shiraz" {
				t.Fatal("national result lost actual city")
			}
			next, err := PublicSearchPage(context.Background(), NewSharedClient(), srv.URL, vehicle.PlatformBama, c, page.NextCursor)
			if err != nil || len(next.Listings) != expected || next.NextCursor != "" {
				t.Fatalf("continuation: %+v %v", next, err)
			}
		})
	}
}

func TestBamaCityRejectionKeepsNextCursor(t *testing.T) {
	c := vehicle.SearchCriteria{Query: "پژو 206", CitySlug: "tehran", Type: vehicle.VehicleTypeCar}
	token := encodeCursor(vehicle.PlatformBama, c, 1, bamaContinuation{Vehicle: "peugeot,206ir"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":true,"data":{"ads":[{"detail":{"code":"wrong","title":"پژو 206","url":"/car/detail-wrong","location":"شیراز"}}]},"metadata":{"has_next":true}}`))
	}))
	defer srv.Close()
	page, err := PublicSearchPage(context.Background(), NewSharedClient(), srv.URL, vehicle.PlatformBama, c, token)
	if err != nil || len(page.Listings) != 0 || page.NextCursor == "" {
		t.Fatalf("filtered page lost continuation: %+v %v", page, err)
	}
}
