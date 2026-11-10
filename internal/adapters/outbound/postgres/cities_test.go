package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestBamaCityMigrationRepairsCurrentFactsAndFiltersOldJobLinks(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	s, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("TRUNCATE crawl_searches,crawl_jobs,crawl_tasks,listings,search_listings,listing_observations CASCADE")
	p := vehicle.PlatformBama
	c := vehicle.SearchCriteria{Query: "پژو 206", CitySlug: "tehran", Type: vehicle.VehicleTypeCar, Platform: &p}
	job, err := s.Submit(ctx, c, false)
	if err != nil {
		t.Fatal(err)
	}
	task, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatal("claim", err)
	}
	rows := []vehicle.NormalizedVehicleListing{}
	for id, location := range map[string]string{"local": "تهران / آزادی", "wrong": "شیراز", "unknown": "پاکدشت"} {
		rows = append(rows, vehicle.NormalizedVehicleListing{ID: id, Platform: p, Title: "پژو 206", CityID: "tehran", NeighborhoodFa: location, VehicleType: vehicle.VehicleTypeCar, MileageKm: -1, URL: "https://bama.ir/car/detail-" + id})
	}
	observations, err := s.Prepare(ctx, rows)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Commit(ctx, task, observations, "", 1); err != nil {
		t.Fatal(err)
	}
	// Simulate upgrading an existing database with polluted city facts.
	exec("DELETE FROM schema_migrations WHERE version=4")
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	job, err = s.Get(ctx, job.ID)
	if err != nil || len(job.Data) != 1 || job.Data[0].ID != "local" || job.Sources[0].Count != 1 {
		t.Fatalf("old snapshot leaked other cities: %+v %v", job, err)
	}
	for _, city := range []string{"tehran", "shiraz"} {
		found, err := s.Search(ctx, crawljobs.InventoryQuery{City: city, Limit: 100})
		if err != nil || found.Total != 1 || found.Data[0].CityID != city {
			t.Fatalf("stored city %s: %+v %v", city, found, err)
		}
	}
	var snapshots int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM listing_observations").Scan(&snapshots); err != nil || snapshots != 3 {
		t.Fatalf("migration changed historical observations: %d %v", snapshots, err)
	}
	var city string
	if err = s.Pool.QueryRow(ctx, "SELECT city FROM listings WHERE id='unknown'").Scan(&city); err != nil || city != "" {
		t.Fatalf("unknown city was invented: %q %v", city, err)
	}
}
