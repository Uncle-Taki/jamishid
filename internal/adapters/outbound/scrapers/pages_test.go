package scrapers

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"golang.org/x/net/html"
)

var livePages = flag.Bool("live-pages", false, "fetch real marketplace public search pages")

func TestLivePublicPages(t *testing.T) {
	if !*livePages {
		t.Skip("enable with -args -live-pages")
	}
	for _, source := range []struct {
		platform vehicle.Platform
		url      string
	}{
		{vehicle.PlatformDivar, "https://divar.ir"}, {vehicle.PlatformBama, "https://bama.ir"}, {vehicle.PlatformSheypoor, "https://www.sheypoor.com"},
	} {
		t.Run(string(source.platform), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			rows, err := PublicPage(ctx, NewSharedClient(), source.url, source.platform, vehicle.SearchCriteria{Query: "پژو ۲۰۶", CitySlug: "tehran", Type: vehicle.VehicleTypeCar})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) == 0 {
				t.Fatal("no live listings returned")
			}
			t.Logf("live listings=%d first=%q priceToman=%.0f url=%s", len(rows), rows[0].Title, rows[0].PriceToman, rows[0].URL)
		})
	}
}

func TestUnknownMetadataIsNotInvented(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<script type="application/ld+json">[{"@type":"Car","name":"Real listing","url":"https://divar.ir/v/car/token"}]</script>`))
	rows, err := divarPage(doc)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].PriceToman != 0 || rows[0].Year != 0 || rows[0].MileageKm != -1 || rows[0].BodyStatus != vehicle.BodyStatusUnknown {
		t.Fatalf("invented source facts: %+v", rows[0])
	}
}

func TestExplicitEmptyResults(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		parse      func(*html.Node) ([]vehicle.NormalizedVehicleListing, error)
	}{
		{"divar", `<script type="application/ld+json">[]</script>`, divarPage},
		{"bama", `<script id="__NUXT_DATA__">[{"ads":1},[]]</script>`, bamaPage},
		{"sheypoor", `<p>آگهی یافت نشد</p>`, sheypoorPage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, _ := html.Parse(strings.NewReader(tc.body))
			rows, err := tc.parse(doc)
			if err != nil || len(rows) != 0 {
				t.Fatalf("rows=%v err=%v", rows, err)
			}
		})
	}
}

func TestRecognizedShellsWithoutListingSchemaAreNotEmptyInventory(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		parse      func(*html.Node) ([]vehicle.NormalizedVehicleListing, error)
	}{
		{"bama", `<script id="__NUXT_DATA__">[{"renamed_ads":1},[]]</script>`, bamaPage},
		{"divar", `<script type="application/ld+json">[{"@type":"Organization","name":"Website"}]</script>`, divarPage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, _ := html.Parse(strings.NewReader(tc.body))
			if rows, err := tc.parse(doc); err == nil {
				t.Fatalf("schema shell silently accepted as empty: %+v", rows)
			}
		})
	}
}

func TestPublicPage_MockServersAndValidation(t *testing.T) {
	ctx := context.Background()
	client := NewSharedClient()

	// 1. Invalid city slug
	_, err := PublicPage(ctx, client, "http://localhost", vehicle.PlatformDivar, vehicle.SearchCriteria{CitySlug: "invalid city!"})
	if err == nil || !strings.Contains(err.Error(), "unsupported city slug") {
		t.Fatalf("expected unsupported city slug error, got %v", err)
	}

	// 2. Invalid base URL
	_, err = PublicPage(ctx, client, "::not-a-url::", vehicle.PlatformDivar, vehicle.SearchCriteria{CitySlug: "tehran"})
	if err == nil {
		t.Fatalf("expected url.Parse error for bad base URL")
	}

	// 3. Bama brand unrecognized
	_, err = PublicPage(ctx, client, "http://localhost", vehicle.PlatformBama, vehicle.SearchCriteria{Query: "unrecognized-alien-vehicle-xyz"})
	if err == nil || !strings.Contains(err.Error(), "recognized vehicle brand") {
		t.Fatalf("expected recognized brand error, got %v", err)
	}

	// 4. Divar mock server
	divarServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><script type="application/ld+json">[{"@type":"Car","name":"پژو 206","url":"https://divar.ir/v/car/123"}]</script></head><body></body></html>`))
	}))
	defer divarServer.Close()

	rows, err := PublicPage(ctx, client, divarServer.URL, vehicle.PlatformDivar, vehicle.SearchCriteria{Query: "206", CitySlug: "tehran", Type: vehicle.VehicleTypeCar})
	if err != nil || len(rows) != 1 {
		t.Fatalf("Divar PublicPage failed: len=%d err=%v", len(rows), err)
	}

	// 5. Bama motorcycle mock server
	bamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><script id="__NUXT_DATA__">[{"ads":1},[]]</script></head><body></body></html>`))
	}))
	defer bamaServer.Close()

	rows, err = PublicPage(ctx, client, bamaServer.URL, vehicle.PlatformBama, vehicle.SearchCriteria{Query: "بنلی", Type: vehicle.VehicleTypeMotorcycle})
	if err != nil || len(rows) != 0 {
		t.Fatalf("Bama PublicPage empty failed: len=%d err=%v", len(rows), err)
	}

	// 6. Sheypoor mock server
	sheypoorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><body><p>آگهی یافت نشد</p></body></html>`))
	}))
	defer sheypoorServer.Close()

	rows, err = PublicPage(ctx, client, sheypoorServer.URL, vehicle.PlatformSheypoor, vehicle.SearchCriteria{Query: "206", CitySlug: "tehran"})
	if err != nil || len(rows) != 0 {
		t.Fatalf("Sheypoor PublicPage empty failed: len=%d err=%v", len(rows), err)
	}
}
