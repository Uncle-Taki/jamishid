package crawljobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
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

type emptyDetailStore struct {
	DetailRepository
	err error
}

func (s *emptyDetailStore) ClaimDetail(context.Context) (DetailTask, bool, error) {
	if s.err != nil {
		return DetailTask{}, false, s.err
	}
	return DetailTask{}, false, nil
}

func TestDetailWorker_EmptyAndError(t *testing.T) {
	// 1. No task available
	service := DetailService{Store: &emptyDetailStore{}}
	worked, err := service.WorkOne(context.Background())
	if err != nil || worked {
		t.Fatalf("expected (worked=false, err=nil), got (%v, %v)", worked, err)
	}

	// 2. Claim error
	serviceErr := DetailService{Store: &emptyDetailStore{err: errors.New("db down")}}
	worked, err = serviceErr.WorkOne(context.Background())
	if err == nil || worked {
		t.Fatalf("expected claim error, got worked=%v err=%v", worked, err)
	}

	// 3. DetailService.Run exits cleanly on context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reported := false
	service.Run(ctx, func(err error) { reported = true })
	if reported {
		t.Errorf("expected no reports on clean cancel")
	}
}

type completeErrStore struct {
	detailStore
	completeErr error
}

func (s *completeErrStore) CompleteDetail(_ context.Context, _ DetailTask, _ vehicle.NormalizedVehicleListing, _ int, _ string) error {
	return s.completeErr
}

func TestDetailWorker_CompleteErrorsAndDeferrals(t *testing.T) {
	// 1. ErrLeaseLost on CompleteDetail
	task := DetailTask{Listing: vehicle.NormalizedVehicleListing{ID: "car1", Platform: vehicle.PlatformDivar, URL: "http://test"}}
	s1 := &completeErrStore{detailStore: detailStore{task: task}, completeErr: ErrLeaseLost}
	svc1 := DetailService{
		Store: s1,
		Fetch: func(ctx context.Context, p vehicle.Platform, url string) (vehicle.NormalizedVehicleListing, error) {
			return vehicle.NormalizedVehicleListing{}, nil
		},
		Merge: func(*vehicle.NormalizedVehicleListing, vehicle.NormalizedVehicleListing) {},
	}
	worked, err := svc1.WorkOne(context.Background())
	if !worked || !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost, got worked=%v err=%v", worked, err)
	}

	// 2. context.Canceled on CompleteDetail
	s2 := &completeErrStore{detailStore: detailStore{task: task}, completeErr: context.Canceled}
	svc2 := DetailService{
		Store: s2,
		Fetch: func(ctx context.Context, p vehicle.Platform, url string) (vehicle.NormalizedVehicleListing, error) {
			return vehicle.NormalizedVehicleListing{}, nil
		},
		Merge: func(*vehicle.NormalizedVehicleListing, vehicle.NormalizedVehicleListing) {},
	}
	worked, err = svc2.WorkOne(context.Background())
	if !worked || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got worked=%v err=%v", worked, err)
	}

	// 3. Generic DB error on CompleteDetail
	s3 := &completeErrStore{detailStore: detailStore{task: task}, completeErr: errors.New("db error")}
	svc3 := DetailService{
		Store: s3,
		Fetch: func(ctx context.Context, p vehicle.Platform, url string) (vehicle.NormalizedVehicleListing, error) {
			return vehicle.NormalizedVehicleListing{}, nil
		},
		Merge: func(*vehicle.NormalizedVehicleListing, vehicle.NormalizedVehicleListing) {},
	}
	worked, err = svc3.WorkOne(context.Background())
	if !worked || err == nil {
		t.Fatalf("expected db error, got worked=%v err=%v", worked, err)
	}

	// 4. Rate-limit 429 deferral outcome
	s4 := &detailStore{task: task}
	svc4 := DetailService{
		Store: s4,
		Fetch: func(ctx context.Context, p vehicle.Platform, url string) (vehicle.NormalizedVehicleListing, error) {
			return vehicle.NormalizedVehicleListing{}, &ports.SourceError{StatusCode: 429, Kind: "rate_limit"}
		},
		Merge: func(*vehicle.NormalizedVehicleListing, vehicle.NormalizedVehicleListing) {},
	}
	worked, err = svc4.WorkOne(context.Background())
	if !worked || err != nil || s4.code != 429 {
		t.Fatalf("expected 429 deferral, got worked=%v err=%v code=%d", worked, err, s4.code)
	}

	// 5. Canceled during fetch
	ctxCancel, cancel := context.WithCancel(context.Background())
	svc5 := DetailService{
		Store: s4,
		Fetch: func(ctx context.Context, p vehicle.Platform, url string) (vehicle.NormalizedVehicleListing, error) {
			cancel()
			return vehicle.NormalizedVehicleListing{}, ctxCancel.Err()
		},
		Merge: func(*vehicle.NormalizedVehicleListing, vehicle.NormalizedVehicleListing) {},
	}
	worked, err = svc5.WorkOne(ctxCancel)
	if !worked || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got worked=%v err=%v", worked, err)
	}
}

type toggleDetailStore struct {
	DetailRepository
	task  DetailTask
	calls int
}

func (s *toggleDetailStore) ClaimDetail(context.Context) (DetailTask, bool, error) {
	s.calls++
	if s.calls == 1 {
		return s.task, true, nil
	}
	if s.calls == 2 {
		return DetailTask{}, false, errors.New("worker loop err")
	}
	return DetailTask{}, false, nil
}

func (s *toggleDetailStore) CompleteDetail(_ context.Context, _ DetailTask, _ vehicle.NormalizedVehicleListing, _ int, _ string) error {
	return nil
}

func TestDetailWorker_RunActive(t *testing.T) {
	task := DetailTask{Listing: vehicle.NormalizedVehicleListing{ID: "run-car", Platform: vehicle.PlatformDivar, URL: "http://test"}}
	store := &toggleDetailStore{task: task}
	svc := DetailService{
		Store: store,
		Fetch: func(ctx context.Context, p vehicle.Platform, url string) (vehicle.NormalizedVehicleListing, error) {
			return vehicle.NormalizedVehicleListing{}, nil
		},
		Merge: func(*vehicle.NormalizedVehicleListing, vehicle.NormalizedVehicleListing) {},
	}

	ctx, cancel := context.WithCancel(context.Background())
	var reportedErr error
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	svc.Run(ctx, func(err error) {
		reportedErr = err
	})

	if reportedErr == nil || reportedErr.Error() != "worker loop err" {
		t.Fatalf("expected worker loop err reported, got %v", reportedErr)
	}
}
