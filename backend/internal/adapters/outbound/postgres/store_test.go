package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

// Run against a dedicated, disposable database; never point this at application data.
func TestDurableInventoryWorkflow(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
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
	platform := vehicle.PlatformBama
	criteria := vehicle.SearchCriteria{Query: "بنلی ۱۸۰", CitySlug: "tehran", Type: vehicle.VehicleTypeMotorcycle, Platform: &platform}
	job, err := s.Submit(ctx, criteria, false)
	if err != nil {
		t.Fatal(err)
	}
	// Concurrent submissions reuse the same active job.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other, e := s.Submit(ctx, criteria, false)
			if e != nil {
				t.Error(e)
			} else if other.ID != job.ID {
				t.Error("duplicate active job")
			}
		}()
	}
	wg.Wait()
	task, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if _, ok, err = s.Claim(ctx); err != nil || ok {
		t.Fatalf("leased page claimed twice: %v %v", ok, err)
	}
	exec("UPDATE crawl_tasks SET lease_until=now()-interval '1 second' WHERE job_id=$1", job.ID)
	recovered, ok, err := s.Claim(ctx)
	if err != nil || !ok || recovered.Token == task.Token {
		t.Fatalf("recover: %v %v", ok, err)
	}
	if err = s.Commit(ctx, task, nil, "", 1); !errors.Is(err, crawljobs.ErrLeaseLost) {
		t.Fatalf("old owner committed: %v", err)
	}
	row := vehicle.NormalizedVehicleListing{ID: "bike-1", Platform: platform, Title: "بنلي TNT ۱۸۰ سالم", BrandModel: "Benelli TNT 180", VehicleType: vehicle.VehicleTypeMotorcycle, CityID: "tehran", PriceToman: 180000000, MileageKm: 12000, Year: 1402, BodyStatus: vehicle.BodyStatusPerfect, DetailsStatus: "complete", URL: "https://bama.ir/motorcycle/bike-1"}
	observations, err := s.Prepare(ctx, []vehicle.NormalizedVehicleListing{row})
	if err != nil || !observations[0].NeedsDetails {
		t.Fatalf("new listing: %v", err)
	}
	if err = s.Commit(ctx, recovered, observations, "next-page", 1); err != nil {
		t.Fatal(err)
	}
	if err = s.Commit(ctx, recovered, observations, "next-page", 1); !errors.Is(err, crawljobs.ErrLeaseLost) {
		t.Fatalf("duplicate commit accepted: %v", err)
	}
	detail, claimed, detailErr := s.ClaimDetail(ctx)
	if detailErr != nil || !claimed {
		t.Fatalf("detail claim: %v", detailErr)
	}
	if err = s.CompleteDetail(ctx, detail, row, 200, ""); err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.Prepare(ctx, []vehicle.NormalizedVehicleListing{row})
	if err != nil || unchanged[0].NeedsDetails {
		t.Fatalf("unchanged listing enriched again: %v", err)
	}
	row.PriceToman += 1000000
	changed, err := s.Prepare(ctx, []vehicle.NormalizedVehicleListing{row})
	if err != nil || !changed[0].NeedsDetails {
		t.Fatalf("changed listing missed: %v", err)
	}
	if err = s.Action(ctx, job.ID, "pause", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = s.Claim(ctx); err != nil || ok {
		t.Fatalf("paused task claimed: %v %v", ok, err)
	}
	if err = s.Action(ctx, job.ID, "resume", ""); err != nil {
		t.Fatal(err)
	}
	task, ok, err = s.Claim(ctx)
	if err != nil || !ok || task.Cursor != "next-page" {
		t.Fatalf("checkpoint lost: %+v %v", task, err)
	}
	if err = s.Commit(ctx, task, changed, "", 2); err != nil {
		t.Fatal(err)
	}
	done, err := s.Get(ctx, job.ID)
	if err != nil || !done.CrawlDone || done.Status != "complete" || done.New != 1 || done.Changed != 1 || len(done.Data) != 1 {
		t.Fatalf("completed job %+v %v", done, err)
	}
	if err = s.Action(ctx, job.ID, "resume", ""); err != nil {
		t.Fatal(err)
	}
	done, err = s.Get(ctx, job.ID)
	if err != nil || done.Status != "complete" {
		t.Fatalf("resume finished job became stuck: %+v %v", done, err)
	}
	for _, query := range []string{"بنلی", "بنلي", "۱۸۰", "180", "Beneli", "Benelli TNT180", "بنلی TNT ۱۸۰"} {
		found, e := s.Search(ctx, crawljobs.InventoryQuery{Text: query, Type: "motorcycle", City: "tehran", Sort: "relevance", Limit: 10})
		if e != nil || found.Total != 1 {
			t.Errorf("query %q: %+v %v", query, found, e)
		}
	}
	for _, query := range []string{"1800", "۱۸۰۰", "Benelli 1800"} {
		found, e := s.Search(ctx, crawljobs.InventoryQuery{Text: query, Limit: 10})
		if e != nil || found.Total != 0 {
			t.Errorf("distinct numeric model %q matched: %+v %v", query, found, e)
		}
	}
	// An old stored index is rebuilt from original display facts by migration 003.
	exec("UPDATE listings SET normalized_text='legacy index',normalization_version='legacy'")
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = reindexNormalization(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var indexed, version string
	if err = s.Pool.QueryRow(ctx, "SELECT normalized_text,normalization_version FROM listings WHERE id='bike-1'").Scan(&indexed, &version); err != nil || indexed != vehicle.NormalizeModelText(row.Title+" "+row.BrandModel) || version != vehicle.NormalizationVersion() {
		t.Fatalf("reindex contract: %q %q %v", indexed, version, err)
	}
	max := 100000000.0
	filtered, err := s.Search(ctx, crawljobs.InventoryQuery{MaxPrice: &max})
	if err != nil || filtered.Total != 0 {
		t.Fatalf("price filter: %+v %v", filtered, err)
	}
	foreign, err := s.Search(ctx, crawljobs.InventoryQuery{City: "shiraz"})
	if err != nil || foreign.Total != 0 {
		t.Fatalf("city filter: %+v %v", foreign, err)
	}
	exec("UPDATE listings SET details_at=now()-interval '49 hours'")
	stale, err := s.Prepare(ctx, []vehicle.NormalizedVehicleListing{row})
	if err != nil || !stale[0].NeedsDetails {
		t.Fatalf("stale detail not refreshed: %v", err)
	}
	var observationCount int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM listing_observations").Scan(&observationCount); err != nil || observationCount != 2 {
		t.Fatalf("observations duplicated: %d %v", observationCount, err)
	}
	// Repeated searches reuse persisted results for five minutes rather than crawling again.
	exec("UPDATE crawl_jobs SET updated_at=now()-interval '4 minutes' WHERE id=$1", job.ID)
	reused, err := s.Submit(ctx, criteria, false)
	if err != nil || reused.ID != job.ID {
		t.Fatalf("recent stored job not reused: %+v %v", reused, err)
	}
	// Demand refreshes are bounded; baseline coverage runs full scans every 48 hours.
	exec("UPDATE crawl_searches SET next_refresh=now()-interval '1 second',demand=12,demand_updated_at=now()")
	if err = s.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	for page := 1; page <= 20; page++ {
		exec("UPDATE crawl_tasks SET available_at=now()")
		task, ok, err = s.Claim(ctx)
		if err != nil || !ok || task.Mode != "incremental" {
			t.Fatalf("incremental: %+v %v", task, err)
		}
		if err = s.Commit(ctx, task, changed, "more", page); err != nil {
			t.Fatal(err)
		}
	}
	var hours float64
	if err = s.Pool.QueryRow(ctx, "SELECT extract(epoch FROM(next_refresh-now()))/3600 FROM crawl_searches").Scan(&hours); err != nil || hours < 47.9 || hours > 48.1 {
		t.Fatalf("popular interval: %f %v", hours, err)
	}
	limited, err := s.Get(ctx, task.JobID)
	if err != nil || limited.Status != "partial" || !limited.CrawlDone || limited.Sources[0].NextCursor != "more" {
		t.Fatalf("incremental limit: %+v %v", limited, err)
	}
	exec("UPDATE crawl_searches SET next_refresh=now()-interval '1 second',last_full=now()-interval '49 hours',coverage=true")
	if err = s.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	task, ok, err = s.Claim(ctx)
	if err != nil || !ok || task.Mode != "full" {
		t.Fatalf("periodic full scan: %+v %v", task, err)
	}
	// Recoverable failures retry only the failed source, then become visible as partial.
	for attempt := 1; attempt <= 3; attempt++ {
		if err = s.Fail(ctx, task, "network"); err != nil {
			t.Fatal(err)
		}
		if attempt < 3 {
			exec("UPDATE crawl_tasks SET available_at=now() WHERE job_id=$1", task.JobID)
			task, ok, err = s.Claim(ctx)
			if err != nil || !ok {
				t.Fatalf("retry: %v", err)
			}
		}
	}
	failed, err := s.Get(ctx, task.JobID)
	if err != nil || failed.RetriesPending != 1 || failed.RetryAt == nil || !failed.Done || failed.CrawlDone {
		t.Fatalf("failed job: %+v %v", failed, err)
	}
}
