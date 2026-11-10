package scrapers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestSourceDetailFacts(t *testing.T) {
	for _, tt := range []struct {
		source  vehicle.Platform
		mileage float64
		year    int
		price   float64
	}{
		{vehicle.PlatformDivar, 30000, 1400, 320000000},
		{vehicle.PlatformBama, 22000, 1402, 385000000},
		{vehicle.PlatformSheypoor, 12500, 1399, 315000000},
	} {
		t.Run(string(tt.source), func(t *testing.T) {
			body, err := os.ReadFile("testdata/" + string(tt.source) + "-detail.html")
			if err != nil {
				t.Fatal(err)
			}
			l, err := parseListingDetails(body, tt.source)
			if err != nil {
				t.Fatal(err)
			}
			if l.MileageKm != tt.mileage || l.Year != tt.year || l.PriceToman != tt.price || l.ColorFa != "مشکی" {
				t.Fatalf("wrong source values: %+v", l)
			}
			if tt.source == vehicle.PlatformDivar && (l.BodyStatus != vehicle.BodyStatusPerfect || l.TechnicalStatus != "سالم و پلمپ" || l.DocumentStatus == "") {
				t.Fatalf("labelled facts lost: %+v", l)
			}
			if tt.source == vehicle.PlatformSheypoor && l.EngineVolumeCc != 0 {
				t.Fatal("engine range fabricated as a single number")
			}
			if len(l.Attributes) < 4 {
				t.Fatalf("source labels were not preserved: %+v", l.Attributes)
			}
		})
	}
}

func TestUnknownAttributesStayUnknown(t *testing.T) {
	l := missingListing()
	for _, pair := range [][2]string{{"کارکرد", "نامشخص"}, {"سال تولید", "نامشخص"}, {"حجم موتور", "(175cc - 199cc)"}, {"وضعیت فنی موتور", "سالم و پلمپ"}} {
		applyAttribute(&l, pair[0], pair[1])
	}
	if l.MileageKm != -1 || l.Year != 0 || l.EngineVolumeCc != 0 || l.BodyStatus != vehicle.BodyStatusUnknown {
		t.Fatalf("unknown facts invented or engine health treated as body condition: %+v", l)
	}
	applyAttribute(&l, "کارکرد", "صفر کیلومتر")
	if l.MileageKm != 0 {
		t.Fatal("explicit zero mileage lost")
	}
	if _, err := parseListingDetails([]byte(`<html>listing unavailable</html>`), vehicle.PlatformDivar); err == nil {
		t.Fatal("unavailable detail page recognized as valid")
	}
}

type detailTransport func(*http.Request) (*http.Response, error)

func (f detailTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDetailFailureKeepsListingAndKnownFacts(t *testing.T) {
	client := &SharedClient{client: &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable")), Header: make(http.Header)}, nil
	})}}
	rows := []vehicle.NormalizedVehicleListing{{ID: "known", Platform: vehicle.PlatformDivar, URL: "https://divar.ir/v/known/failure-test", Title: "known", MileageKm: 12345, Year: 1400, PriceToman: 320000000}}
	enrichListings(context.Background(), client, rows)
	if len(rows) != 1 || rows[0].MileageKm != 12345 || rows[0].PriceToman != 320000000 || rows[0].DetailsStatus != "partial" {
		t.Fatalf("detail failure corrupted listing: %+v", rows)
	}
}

func TestDetailURLIsLimitedToPublicListingRoutes(t *testing.T) {
	for _, raw := range []string{"http://divar.ir/v/a/b", "https://divar.ir.example/v/a/b", "https://divar.ir/admin", "https://divar.ir:8080/v/a/b", "https://user@divar.ir/v/a/b", "https://127.0.0.1/v/a/b"} {
		if validDetailURL(raw, vehicle.PlatformDivar) {
			t.Errorf("invalid detail URL accepted: %s", raw)
		}
	}
}

func TestDivarDisplayedCardPriceOverridesInconsistentJSONLD(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<script type="application/ld+json">[{"@type":"Motorcycle","name":"بنلی 180","url":"https://divar.ir/v/test/price-test","offers":{"price":"5400000","priceCurrency":"IRR"}}]</script><script>window.__PRELOADED_STATE__ = {"search":{"searchData":{"formData":{}}},"nb":{"listWidgets":[{"data":{"dto":{"data":{"token":"price-test","middle_description_text":"۵۴۰,۰۰۰,۰۰۰ تومان"}}}}]}};</script>`)
	}))
	defer srv.Close()
	page, err := PublicSearchPage(context.Background(), NewSharedClient(), srv.URL, vehicle.PlatformDivar, vehicle.SearchCriteria{Type: vehicle.VehicleTypeMotorcycle, Query: "بنلی"}, "")
	if err != nil || len(page.Listings) != 1 || page.Listings[0].PriceToman != 540000000 {
		t.Fatalf("wrong displayed card price: %+v %v", page, err)
	}
}

func TestDivarPublicDetailEndpointFields(t *testing.T) {
	body := []byte(`{"sections":[{"section_name":"LIST_DATA","widgets":[{"widget_type":"GROUP_INFO_ROW","data":{"items":[{"title":"کارکرد","value":"۳۰۰۰۰"},{"title":"مدل (سال تولید)","value":"۱۴۰۰"}]}},{"widget_type":"UNEXPANDABLE_ROW","data":{"title":"قیمت","value":"۳۲۰,۰۰۰,۰۰۰ تومان"}}]}],"contact":{"phone":"ignored"}}`)
	l, err := parseListingDetails(body, vehicle.PlatformDivar)
	if err != nil || l.MileageKm != 30000 || l.Year != 1400 || l.PriceToman != 320000000 || len(l.Attributes) != 3 {
		t.Fatalf("public detail fields incorrect: %+v %v", l, err)
	}
}

func TestExportedHelpersAndDetails(t *testing.T) {
	ctx := context.Background()

	// DefaultProviderCatalog
	if DefaultProviderCatalog() == nil {
		t.Fatal("expected DefaultProviderCatalog to return catalog")
	}

	// ConfigureProviderBudget
	ConfigureProviderBudget(nil)

	// MergeDetails
	target := vehicle.NormalizedVehicleListing{ID: "car1", Title: "Original Title", MileageKm: -1}
	facts := vehicle.NormalizedVehicleListing{ID: "car1", Title: "New Title", MileageKm: 15000, Year: 1401}
	MergeDetails(&target, facts)
	if target.MileageKm != 15000 || target.Year != 1401 || target.Title != "Original Title" {
		t.Fatalf("unexpected MergeDetails result: %+v", target)
	}

	// FetchListingDetails invalid URL
	if _, err := FetchListingDetails(ctx, vehicle.PlatformDivar, "https://invalid-host.com/123"); err == nil {
		t.Fatal("expected error on invalid detail URL")
	}

	// FetchListingFacts invalid URL
	if _, err := FetchListingFacts(ctx, vehicle.PlatformDivar, "https://invalid-host.com/123"); err == nil {
		t.Fatal("expected error on invalid facts URL")
	}

	// EnrichListings empty and invalid
	emptyRows := []vehicle.NormalizedVehicleListing{}
	EnrichListings(ctx, emptyRows)

	invalidRows := []vehicle.NormalizedVehicleListing{{URL: "https://invalid.com/v/1"}}
	EnrichListings(ctx, invalidRows)
}
