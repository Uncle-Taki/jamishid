package crawljobs

import (
	"context"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"testing"
)

type testStore struct {
	Repository
	task         Task
	committed    []Observation
	next, failed string
}

func (s *testStore) Claim(context.Context) (Task, bool, error) { return s.task, true, nil }
func (s *testStore) Prepare(_ context.Context, rows []vehicle.NormalizedVehicleListing) ([]Observation, error) {
	return []Observation{{Listing: rows[0], NeedsDetails: false}, {Listing: rows[1], NeedsDetails: true}}, nil
}
func (s *testStore) Commit(_ context.Context, _ Task, rows []Observation, next string, _ int) error {
	s.committed = rows
	s.next = next
	return nil
}
func (s *testStore) Fail(_ context.Context, _ Task, kind string) error { s.failed = kind; return nil }

type crawlFunc func(context.Context, vehicle.SearchCriteria) (vehicle.CrawlResult, error)

func (f crawlFunc) AggregateWithSources(ctx context.Context, c vehicle.SearchCriteria) (vehicle.CrawlResult, error) {
	return f(ctx, c)
}
func TestWorkerCommitsSummariesWithoutWaitingForEnrichment(t *testing.T) {
	store := &testStore{task: Task{Platform: vehicle.PlatformBama, Cursor: "saved", Attempts: 1}}
	service := Service{Store: store, Crawler: crawlFunc(func(ctx context.Context, c vehicle.SearchCriteria) (vehicle.CrawlResult, error) {
		p := ports.CrawlPolicyFrom(ctx)
		if !p.Refresh || !p.SkipDetails || c.Cursors[vehicle.PlatformBama] != "saved" {
			t.Fatal("worker fetch policy/checkpoint lost")
		}
		return vehicle.CrawlResult{Listings: []vehicle.ScoredListing{{NormalizedVehicleListing: vehicle.NormalizedVehicleListing{ID: "old"}}, {NormalizedVehicleListing: vehicle.NormalizedVehicleListing{ID: "changed"}}}, Sources: []vehicle.SourceStatus{{Platform: vehicle.PlatformBama, Status: "ok", NextCursor: "next", Page: 2}}}, nil
	}), Enrich: func(ctx context.Context, rows []vehicle.NormalizedVehicleListing) {
		t.Fatal("page worker must not block on detail requests")
		if len(rows) != 1 || rows[0].ID != "changed" || !ports.CrawlPolicyFrom(ctx).RefreshDetails {
			t.Fatal("unchanged item enriched")
		}
		rows[0].DetailsStatus = "complete"
	}}
	if _, err := service.WorkOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.committed) != 2 || store.next != "next" || store.committed[1].Listing.DetailsStatus == "complete" {
		t.Fatal("page not persisted")
	}
}
func TestSearchIdentityExcludesDisplayFilters(t *testing.T) {
	a := vehicle.SearchCriteria{Query: "كيا ١٨٠"}
	b := vehicle.SearchCriteria{Query: "کیا 180"}
	price := 123.0
	b.MinPrice = &price
	first, _ := SearchIdentity(a)
	second, _ := SearchIdentity(b)
	if first != second {
		t.Fatal("normalization or display filters changed identity")
	}
	b.CitySlug = "shiraz"
	third, _ := SearchIdentity(b)
	if first == third {
		t.Fatal("city scope lost")
	}
}

func TestSummaryHashIgnoresObservationAndEstimatedPublicationTimes(t *testing.T) {
	a := vehicle.NormalizedVehicleListing{ID: "one", Title: "بنلی", PriceToman: 100, PublishedAt: "2026-11-01T12:00:00Z", ScrapedAt: "2026-11-02T12:00:00Z"}
	b := a
	b.PublishedAt = "2026-11-01T12:01:00Z"
	b.ScrapedAt = "2026-11-02T12:01:00Z"
	if SummaryHash(a) != SummaryHash(b) {
		t.Fatal("relative timestamps caused a false edit")
	}
	b.PriceToman = 200
	if SummaryHash(a) == SummaryHash(b) {
		t.Fatal("price edit was ignored")
	}
}
