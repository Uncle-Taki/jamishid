package scrapers

import (
	"context"
	"flag"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"golang.org/x/net/html"
	"strings"
	"testing"
	"time"
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
		{"bama", `<script id="__NUXT_DATA__">[{}]</script>`, bamaPage},
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
