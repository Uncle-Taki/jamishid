package scrapers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestSelectedModelRelevance(t *testing.T) {
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeMotorcycle, BamaCategory: "benelli-tnt180s", Query: "بنلی TNT 180S"}
	for _, title := range []string{"بنلی TNT 180S", "بنلی ۱۸۰ اس نیو / Benelli 180 S New", "بنلي۱۸۰s", "Benelli 180 S", "موتورسیکلت بنلی ۱۸۰", "بنلی۱۸۰ مدل۹۹"} {
		if !matchesSelectedVehicle(title, c) {
			t.Errorf("rejected model alias: %s", title)
		}
	}
	for _, title := range []string{"بنلی 150 TNT", "بنلی TNT 135", "بنلی ستا 125", "بنلی پانارائا", "بنلی", "بنلی TNT 1800S", "هوندا 180", "بنلی طرح ای دی وی ۱۸۰", "بنلی ۱۸۰ اپاچی", "باسلام یه عددکله چراغ جلوبنلی180فابریک واصلی است", "فروش قطعات بنلی 180", "اگزوز بنلی 180"} {
		if matchesSelectedVehicle(title, c) {
			t.Errorf("accepted unrelated listing: %s", title)
		}
	}
	c.Keywords = "مشکی"
	c.Query += " مشکی"
	if matchesSelectedVehicle("بنلی 180 سفید", c) || !matchesSelectedVehicle("بنلی 180 مشکی", c) {
		t.Fatal("optional keywords must narrow the selected model")
	}
}

func TestOtherSelectedModelsDoNotMatchNumericPrefixes(t *testing.T) {
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeCar, BamaCategory: "peugeot-206ir", Query: "پژو 206"}
	if !matchesSelectedVehicle("پژو۲۰۶ سالم", c) || !matchesSelectedVehicle("Peugeot 206", c) || matchesSelectedVehicle("پژو ۲۰۷", c) || matchesSelectedVehicle("پژو 2060", c) {
		t.Fatal("selected car model and brand must match whole tokens")
	}
}

func TestRejectedPageRetainsContinuation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("p") == "1" {
			fmt.Fprint(w, `{"data":[{"id":"wrong","attributes":{"title":"بنلی 150","url":"https://www.sheypoor.com/v/wrong"}}],"meta":{"f":"next","items_per_page":1,"normal_count":2}}`)
		} else {
			fmt.Fprint(w, `{"data":[{"id":"right","attributes":{"title":"بنلی ۱۸۰ اس","url":"https://www.sheypoor.com/v/right"}}],"meta":{"items_per_page":1,"normal_count":2}}`)
		}
	}))
	defer srv.Close()
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeMotorcycle, CitySlug: "tehran", BamaCategory: "benelli-tnt180s", Query: "بنلی TNT 180S"}
	first, err := PublicSearchPage(context.Background(), NewSharedClient(), srv.URL, vehicle.PlatformSheypoor, c, "")
	if err != nil || len(first.Listings) != 0 || first.NextCursor == "" {
		t.Fatalf("empty filtered page must remain pageable: %+v, %v", first, err)
	}
	next, err := PublicSearchPage(context.Background(), NewSharedClient(), srv.URL, vehicle.PlatformSheypoor, c, first.NextCursor)
	if err != nil || len(next.Listings) != 1 || next.Listings[0].ID != "sheypoor-right" || next.NextCursor != "" {
		t.Fatalf("matching later page lost: %+v, %v", next, err)
	}
}
