package crawljobs

import (
	"context"
	"errors"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"testing"
)

type detailStore struct {
	DetailRepository
	task      DetailTask
	completed bool
	code      int
	kind      string
	row       vehicle.NormalizedVehicleListing
	facts     *vehicle.NormalizedVehicleListing
}

func (s *detailStore) ClaimDetail(context.Context) (DetailTask, bool, error) {
	return s.task, true, nil
}
func (s *detailStore) CompleteDetail(_ context.Context, task DetailTask, row vehicle.NormalizedVehicleListing, code int, kind string) error {
	s.completed = true
	s.code = code
	s.kind = kind
	s.row = row
	s.facts = task.Facts
	return nil
}
func TestDetailWorkerClassifiesFailuresAndPreservesFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
		kind string
	}{{"ok", nil, 200, ""}, {"gone", &ports.SourceError{StatusCode: 410, Kind: "http", Err: errors.New("gone")}, 410, "http"}, {"timeout", context.DeadlineExceeded, 0, "timeout"}, {"parser", &ports.SourceError{Kind: "parser"}, 0, "parser"}} {
		t.Run(tc.name, func(t *testing.T) {
			store := &detailStore{task: DetailTask{Listing: vehicle.NormalizedVehicleListing{ID: "stable", Platform: vehicle.PlatformBama, URL: "https://bama.ir/car/stable", PriceToman: 100}}}
			service := DetailService{Store: store, Fetch: func(ctx context.Context, p vehicle.Platform, url string) (vehicle.NormalizedVehicleListing, error) {
				if !ports.CrawlPolicyFrom(ctx).RefreshDetails {
					t.Fatal("verification served stale cache")
				}
				return vehicle.NormalizedVehicleListing{MileageKm: 10}, tc.err
			}, Merge: func(row *vehicle.NormalizedVehicleListing, facts vehicle.NormalizedVehicleListing) {
				row.MileageKm = facts.MileageKm
			}}
			_, err := service.WorkOne(context.Background())
			if err != nil || !store.completed || store.code != tc.code || store.kind != tc.kind || store.row.PriceToman != 100 {
				t.Fatalf("unexpected completion %+v %v", store, err)
			}
			if tc.err == nil && (store.facts == nil || store.row.MileageKm != 10) {
				t.Fatal("actual detail facts not retained for provenance")
			}
		})
	}
}
