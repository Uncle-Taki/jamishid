package scrapers

import (
	"context"
	"encoding/json"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestContinuationBoundToSearch(t *testing.T) {
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeCar, CitySlug: "tehran", Query: "206", BamaCategory: "peugeot-206ir"}
	token := encodeCursor(vehicle.PlatformBama, c, 2, bamaContinuation{Vehicle: "peugeot,206ir", Index: 1})
	cursor, e := decodeCursor(token, vehicle.PlatformBama, c)
	if e != nil {
		t.Fatal(e)
	}
	if !time.Unix(cursor.Expires, 0).After(time.Now().Add(24 * time.Hour)) {
		t.Fatal("continuation expires before the cached source page")
	}
	changed := c
	changed.CitySlug = "karaj"
	if _, e := decodeCursor(token, vehicle.PlatformBama, changed); e == nil {
		t.Fatal("cursor accepted a different city")
	}
	if _, e := decodeCursor(token+"x", vehicle.PlatformBama, c); e == nil {
		t.Fatal("cursor accepted tampering")
	}
}

func TestDivarContinuationKeepsCityCategoryAndQuery(t *testing.T) {
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeCar, CitySlug: "karaj", Query: "206"}
	state := divarContinuation{Cities: []any{"2"}, Location: map[string]any{"places": []any{map[string]any{"place_id": "2"}}}, Search: map[string]any{"query": "206", "form_data": map[string]any{"data": map[string]any{"category": map[string]any{"str": map[string]any{"value": "light"}}}}}, Pagination: map[string]any{"page": 1, "viewed_tokens": "provider-token"}}
	token := encodeCursor(vehicle.PlatformDivar, c, 2, state)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request divarContinuation
		if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
			t.Fatal(e)
		}
		if len(request.Cities) != 1 || request.Cities[0] != "2" || request.Search["query"] != "206" || request.Pagination["viewed_tokens"] != "provider-token" {
			t.Errorf("lost source search: %+v", request)
		}
		w.Write([]byte(`{"list_widgets":[{"widget_type":"POST_ROW","data":{"token":"new","title":"206","middle_description_text":"۹۰۰,۰۰۰,۰۰۰ تومان","top_description_text":"۵۰,۰۰۰ کیلومتر"}}],"pagination":{"has_next_page":false}}`))
	}))
	defer ts.Close()
	page, e := PublicSearchPage(context.Background(), NewSharedClient(), ts.URL, vehicle.PlatformDivar, c, token)
	if e != nil || len(page.Listings) != 1 || page.Listings[0].PriceToman != 900000000 || page.Listings[0].MileageKm != 50000 || page.NextCursor != "" {
		t.Fatalf("%+v %v", page, e)
	}
}

func TestDivarTerminalPageMayOmitListingCollection(t *testing.T) {
	for _, tt := range []struct {
		body      string
		wantError bool
	}{
		{`{"pagination":{"has_next_page":false}}`, false},
		{`{"pagination":{"data":{"@type":"type.googleapis.com/post_list.PaginationData","page":11}},"action_log":{"server_side_info":{"item_type":{"type":"POST_LIST_LOAD_PAGE"}}}}`, false},
		{`{"pagination":{"has_next_page":true}}`, true},
		{`{"error":"unavailable"}`, true},
	} {
		t.Run(tt.body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tt.body)) }))
			defer srv.Close()
			c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeMotorcycle, CitySlug: "tehran", Query: "بنلی"}
			token := encodeCursor(vehicle.PlatformDivar, c, 3, divarContinuation{Search: map[string]any{"query": c.Query}})
			page, err := PublicSearchPage(context.Background(), NewSharedClient(), srv.URL, vehicle.PlatformDivar, c, token)
			if (err != nil) != tt.wantError || page.NextCursor != "" || len(page.Listings) != 0 {
				t.Fatalf("unexpected terminal page: %+v %v", page, err)
			}
		})
	}
}

func TestDivarKeepsNativeMatchesWithoutModelInTitleAndStopsSimilarAds(t *testing.T) {
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeMotorcycle, CitySlug: "tehran", BamaCategory: "benelli-tnt180s", Query: "بنلی TNT 180S"}
	token := encodeCursor(vehicle.PlatformDivar, c, 2, divarContinuation{NativeExact: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"action_log":{"server_side_info":{"info":{"search_layer":"fulltext_t1"}}},"list_widgets":[{"widget_type":"POST_ROW","data":{"token":"valid","title":"موتور سالم مدل ۱۴۰۲"}},{"widget_type":"SELECTOR_ROW","data":{"title":"آگهی‌های مشابه"}},{"widget_type":"POST_ROW","data":{"token":"recommendation","title":"بنلی ۱۸۰"}}],"pagination":{"has_next_page":true,"data":{"page":2}}}`))
	}))
	defer srv.Close()
	page, err := PublicSearchPage(context.Background(), NewSharedClient(), srv.URL, vehicle.PlatformDivar, c, token)
	if err != nil || len(page.Listings) != 1 || page.Listings[0].ID != "divar-valid" || page.NextCursor != "" {
		t.Fatalf("lost native matches or admitted similar ads: %+v %v", page, err)
	}
}

func TestDivarDoesNotIncludeFuzzyCategoryResults(t *testing.T) {
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeMotorcycle, CitySlug: "tehran", BamaCategory: "benelli-tnt180s", Query: "بنلی TNT 180S"}
	token := encodeCursor(vehicle.PlatformDivar, c, 9, divarContinuation{NativeExact: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"action_log":{"server_side_info":{"info":{"search_layer":"fuzzy_filter"}}},"list_widgets":[{"widget_type":"POST_ROW","data":{"token":"similar","title":"بنلی ۱۸۰"}}],"pagination":{"has_next_page":true,"data":{"page":9}}}`))
	}))
	defer srv.Close()
	page, err := PublicSearchPage(context.Background(), NewSharedClient(), srv.URL, vehicle.PlatformDivar, c, token)
	if err != nil || len(page.Listings) != 0 || page.NextCursor != "" {
		t.Fatalf("fuzzy results should end native search: %+v %v", page, err)
	}
}

func TestBamaAllIranOmitsRegionFilter(t *testing.T) {
	for _, city := range []string{"iran", "tehran"} {
		t.Run(city, func(t *testing.T) {
			c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeMotorcycle, CitySlug: city, BamaCategory: "benelli-tnt180s", Query: "بنلی TNT 180S"}
			token := encodeCursor(vehicle.PlatformBama, c, 1, bamaContinuation{Vehicle: "benelli,tnt180s", Index: 0})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if city == "iran" && r.URL.Query().Has("region") {
					t.Errorf("national search still has a region: %s", r.URL)
				}
				if city == "tehran" && r.URL.Query().Get("region") != "tehran" {
					t.Errorf("city filter lost: %s", r.URL)
				}
				w.Write([]byte(`{"status":true,"metadata":{"has_next":false},"data":{"ads":[{"detail":{"code":"valid","title":"بنلی TNT 180S","url":"/motorcycle/detail-valid-benelli-tnt180s-1402"}}]}}`))
			}))
			defer srv.Close()
			page, err := PublicSearchPage(context.Background(), NewSharedClient(), srv.URL, vehicle.PlatformBama, c, token)
			if err != nil || len(page.Listings) != 1 {
				t.Fatalf("Bama location search failed: %+v %v", page, err)
			}
		})
	}
}

func TestBamaUsesSelectedNativeModelAndPageIndex(t *testing.T) {
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeCar, CitySlug: "tehran", Query: "پژو 206", BamaCategory: "peugeot-206ir"}
	token := encodeCursor(vehicle.PlatformBama, c, 2, bamaContinuation{Vehicle: "peugeot,206ir", Index: 1})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("vehicle") != "peugeot,206ir" || r.URL.Query().Get("region") != "tehran" || r.URL.Query().Get("pageIndex") != "1" {
			t.Errorf("lost native filters: %s", r.URL)
		}
		w.Write([]byte(`{"status":true,"metadata":{"has_next":false},"data":{"ads":[{"detail":{"code":"a","title":"پژو 206","url":"/car/detail-a-peugeot-206ir-type5-1398","year":"1398","mileage":"120,000 km"},"price":{"price":"900,000,000"}},{"detail":{"code":"promoted","title":"پژو 207","url":"/car/detail-promoted-peugeot-207-1398"},"price":{"price":"1000000000"}}]}}`))
	}))
	defer ts.Close()
	page, e := PublicSearchPage(context.Background(), NewSharedClient(), ts.URL, vehicle.PlatformBama, c, token)
	if e != nil || len(page.Listings) != 1 || page.Listings[0].ID != "bama-a" || page.NextCursor != "" {
		t.Fatalf("%+v %v", page, e)
	}
}

func TestCatalogUsesPublishedLinksAndCachesThem(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`<a href="/car/peugeot-206ir">خرید و فروش پژو 206</a><a href="/car/detail-x">listing</a><a href="https://example.org/car/peugeot-207">external</a><a href="/car/peugeot-206ir">duplicate</a>`))
	}))
	defer ts.Close()
	catalog := NewCategoryCatalog(NewSharedClient())
	catalog.BaseURL = ts.URL
	rows, e := catalog.Categories(context.Background(), vehicle.VehicleTypeCar, "peugeot")
	if e != nil || len(rows) != 1 || rows[0].ID != "peugeot-206ir" {
		t.Fatalf("%+v %v", rows, e)
	}
	_, e = catalog.Categories(context.Background(), vehicle.VehicleTypeCar, "peugeot")
	if e != nil || calls != 1 {
		t.Fatalf("cache calls=%d error=%v", calls, e)
	}
}

func TestSheypoorContinuationPreservesFilter(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v10.0.0/search/tehran/car" || r.URL.Query().Get("q") != "206" {
			t.Errorf("wrong category/query: %s", r.URL)
		}
		if calls == 1 {
			w.Write([]byte(`{"data":[{"id":"one","attributes":{"title":"206","url":"https://www.sheypoor.com/v/one.html"}}],"meta":{"f":"next"}}`))
		} else {
			if r.URL.Query().Get("p") != "2" || r.URL.Query().Get("f") != "next" {
				t.Errorf("lost cursor: %s", r.URL)
			}
			w.Write([]byte(`{"data":[{"id":"two","attributes":{"title":"206","url":"https://www.sheypoor.com/v/two.html"}}],"meta":{}}`))
		}
	}))
	defer ts.Close()
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeCar, CitySlug: "tehran", Query: "206"}
	first, e := PublicSearchPage(context.Background(), NewSharedClient(), ts.URL, vehicle.PlatformSheypoor, c, "")
	if e != nil || first.NextCursor == "" {
		t.Fatalf("%+v %v", first, e)
	}
	second, e := PublicSearchPage(context.Background(), NewSharedClient(), ts.URL, vehicle.PlatformSheypoor, c, first.NextCursor)
	if e != nil || len(second.Listings) != 1 || second.Listings[0].ID != "sheypoor-two" || second.NextCursor != "" {
		t.Fatalf("%+v %v", second, e)
	}
}

func TestLivePagination(t *testing.T) {
	if !*livePages {
		t.Skip("enable with -args -live-pages")
	}
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeCar, CitySlug: "tehran", Query: "پژو 206", BamaCategory: "peugeot-206ir"}
	for _, source := range []struct {
		p   vehicle.Platform
		url string
	}{{vehicle.PlatformDivar, "https://divar.ir"}, {vehicle.PlatformBama, "https://bama.ir"}, {vehicle.PlatformSheypoor, "https://www.sheypoor.com"}} {
		t.Run(string(source.p), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			client := NewSharedClient()
			first, e := PublicSearchPage(ctx, client, source.url, source.p, c, "")
			if e != nil {
				t.Fatal(e)
			}
			if first.NextCursor == "" {
				t.Fatalf("no continuation: rows=%d", len(first.Listings))
			}
			second, e := PublicSearchPage(ctx, client, source.url, source.p, c, first.NextCursor)
			if e != nil {
				t.Fatal(e)
			}
			ids := map[string]bool{}
			for _, l := range first.Listings {
				ids[l.ID] = true
			}
			newCount := 0
			for _, l := range second.Listings {
				if !ids[l.ID] {
					newCount++
				}
			}
			if newCount == 0 {
				t.Fatal("next page returned no new listings")
			}
			t.Logf("first=%d next=%d new=%d continuation=%t", len(first.Listings), len(second.Listings), newCount, second.NextCursor != "")
		})
	}
}

func TestLiveCatalog(t *testing.T) {
	if !*livePages {
		t.Skip("enable with -args -live-pages")
	}
	catalog := NewCategoryCatalog(NewSharedClient())
	rows, e := catalog.Categories(context.Background(), vehicle.VehicleTypeCar, "")
	if e != nil {
		t.Fatal(e)
	}
	models, e := catalog.Categories(context.Background(), vehicle.VehicleTypeCar, "peugeot")
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, m := range models {
		if m.ID == "peugeot-206ir" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing actual 206 category: %+v", models)
	}
	t.Logf("brands=%d Peugeot models=%d", len(rows), len(models))
}

func TestLiveMotorcyclePagination(t *testing.T) {
	if !*livePages {
		t.Skip("enable with -args -live-pages")
	}
	catalog := NewCategoryCatalog(NewSharedClient())
	brands, e := catalog.Categories(context.Background(), vehicle.VehicleTypeMotorcycle, "")
	if e != nil {
		t.Fatal(e)
	}
	models, e := catalog.Categories(context.Background(), vehicle.VehicleTypeMotorcycle, "honda")
	if e != nil {
		t.Fatal(e)
	}
	if len(models) == 0 {
		t.Fatal("Honda motorcycle model list is empty")
	}
	t.Logf("motorcycle brands=%d models=%d first=%+v", len(brands), len(models), models[0])
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeMotorcycle, CitySlug: "tehran", Query: "هوندا", BamaCategory: "honda"}
	page, e := PublicSearchPage(context.Background(), NewSharedClient(), "https://bama.ir", vehicle.PlatformBama, c, "")
	if e != nil {
		t.Fatal(e)
	}
	if page.NextCursor == "" {
		t.Fatalf("missing motorcycle continuation: %+v", page)
	}
	next, e := PublicSearchPage(context.Background(), NewSharedClient(), "https://bama.ir", vehicle.PlatformBama, c, page.NextCursor)
	if e != nil {
		t.Fatal(e)
	}
	if len(next.Listings) == 0 {
		t.Fatal("motorcycle next page empty")
	}
	t.Logf("motorcycle first=%d next=%d", len(page.Listings), len(next.Listings))
}

func TestLiveBenelliRecency(t *testing.T) {
	if !*livePages {
		t.Skip("enable with -args -live-pages")
	}
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeMotorcycle, CitySlug: "tehran", Query: "بنلی", BamaCategory: "benelli"}
	for _, source := range []struct {
		p   vehicle.Platform
		url string
	}{{vehicle.PlatformDivar, "https://divar.ir"}, {vehicle.PlatformBama, "https://bama.ir"}, {vehicle.PlatformSheypoor, "https://www.sheypoor.com"}} {
		t.Run(string(source.p), func(t *testing.T) {
			page, e := PublicSearchPage(context.Background(), NewSharedClient(), source.url, source.p, c, "")
			if e != nil {
				t.Fatal(e)
			}
			dated := 0
			for _, l := range page.Listings {
				if l.PublishedAt != "" {
					dated++
				}
			}
			if len(page.Listings) == 0 || dated == 0 {
				t.Fatalf("rows=%d dated=%d", len(page.Listings), dated)
			}
			t.Logf("Benelli rows=%d dated=%d more=%t", len(page.Listings), dated, page.NextCursor != "")
		})
	}
}
