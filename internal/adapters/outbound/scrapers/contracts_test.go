package scrapers

import (
	"context"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestSourceSchemaContracts(t *testing.T) {
	for _, platform := range []vehicle.Platform{vehicle.PlatformDivar, vehicle.PlatformBama, vehicle.PlatformSheypoor} {
		for _, kind := range []string{"valid", "empty", "drift"} {
			t.Run(string(platform)+"/"+kind, func(t *testing.T) {
				body, err := os.ReadFile("testdata/contracts/" + string(platform) + "-" + kind + ".json")
				if err != nil {
					t.Fatal(err)
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Write(body)
				}))
				defer srv.Close()
				criteria := vehicle.SearchCriteria{Query: "پژو 206", Type: vehicle.VehicleTypeCar, CitySlug: "tehran"}
				token := ""
				if platform == vehicle.PlatformDivar {
					token = encodeCursor(platform, criteria, 2, divarContinuation{})
				}
				if platform == vehicle.PlatformBama {
					token = encodeCursor(platform, criteria, 2, bamaContinuation{Vehicle: "peugeot,206ir", Index: 1})
				}
				page, err := PublicSearchPage(ports.WithCrawlPolicy(context.Background(), ports.CrawlPolicy{SkipDetails: true}), NewSharedClient(), srv.URL, platform, criteria, token)
				if kind == "drift" {
					if err == nil || ports.FailureKind(err) != "parser" {
						t.Fatalf("schema drift silently became empty: %+v %v", page, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				expected := 0
				if kind == "valid" {
					expected = 1
				}
				if len(page.Listings) != expected {
					t.Fatalf("listings %d, expected %d", len(page.Listings), expected)
				}
				if kind == "valid" && page.Parser.Valid != 1 {
					t.Fatalf("missing parser evidence %+v", page.Parser)
				}
			})
		}
	}
}
func TestParserMissingFieldsAreMeasuredWithoutInventingFacts(t *testing.T) {
	rows := []vehicle.NormalizedVehicleListing{{ID: "one", Title: "Real car", URL: "https://bama.ir/car/detail-one", MileageKm: -1}, {ID: "two", Title: "", URL: "https://bama.ir/car/detail-two", MileageKm: -1}}
	valid, report, err := validateCollection(context.Background(), vehicle.PlatformBama, 2, rows)
	if err != nil || len(valid) != 1 || report.Rejected != 1 || report.MissingPrice != 1 || report.MissingYear != 1 || report.MissingMileage != 1 {
		t.Fatalf("%+v %v", report, err)
	}
	if valid[0].PriceToman != 0 || valid[0].MileageKm != -1 {
		t.Fatal("invented missing source facts")
	}
}
