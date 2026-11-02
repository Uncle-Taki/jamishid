package sourcecheck

import (
	"context"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"testing"
)

func TestEmptySearchDoesNotBecomeParserAlarm(t *testing.T) {
	r := Run(context.Background(), func(context.Context, vehicle.Platform, vehicle.SearchCriteria) (vehicle.ListingPage, error) {
		return vehicle.ListingPage{}, nil
	})
	if len(r.Alerts) != 0 || len(r.Probes) != 6 {
		t.Fatalf("%+v", r)
	}
}
func TestRepeatedFailuresAndMissingFieldsAreActionable(t *testing.T) {
	r := Run(context.Background(), func(_ context.Context, p vehicle.Platform, _ vehicle.SearchCriteria) (vehicle.ListingPage, error) {
		if p == vehicle.PlatformDivar {
			return vehicle.ListingPage{}, &ports.SourceError{Kind: "parser"}
		}
		if p == vehicle.PlatformBama {
			return vehicle.ListingPage{Listings: []vehicle.NormalizedVehicleListing{{ID: "real"}}, Parser: vehicle.ParserReport{Candidates: 2, Valid: 1, Rejected: 1, MissingPrice: 1}}, nil
		}
		return vehicle.ListingPage{}, nil
	})
	if len(r.Alerts) != 2 {
		t.Fatalf("%+v", r)
	}
	if r.Probes[0].Failure != "parser" {
		t.Fatal("failure cause lost")
	}
}
