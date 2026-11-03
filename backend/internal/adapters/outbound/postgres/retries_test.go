package postgres

import (
	"context"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"testing"
	"time"
)

func TestDelayedPageRetryPreservesCursorAndPause(t *testing.T) {
	s := coverageStore(t)
	ctx := context.Background()
	s.RetryDelay = 2 * time.Hour
	platform := vehicle.PlatformDivar
	job, err := s.Submit(ctx, vehicle.SearchCriteria{Query: "206", Type: vehicle.VehicleTypeCar, Platform: &platform}, false)
	if err != nil {
		t.Fatal(err)
	}
	task, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = s.Commit(ctx, task, nil, "checkpoint", 1); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE crawl_tasks SET available_at=now(),attempts=2`)
	task, ok, err = s.Claim(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = s.Fail(ctx, task, "timeout"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Status(ctx, job.ID)
	if err != nil || snapshot.RetryAt == nil || snapshot.RetriesPending != 1 || !snapshot.Done || time.Until(*snapshot.RetryAt) < 119*time.Minute {
		t.Fatalf("delayed metadata %+v %v", snapshot, err)
	}
	if err = s.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = s.Claim(ctx); err != nil || ok {
		t.Fatal("retry claimed before cooldown", err)
	}
	// Pausing does not lose the ticket or allow the scheduler to bypass it.
	if err = s.Action(ctx, job.ID, "pause", ""); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE crawler_retries SET available_at=now()-interval '1 second'`)
	if err = s.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = s.Claim(ctx); err != nil || ok {
		t.Fatal("paused retry claimed", err)
	}
	if err = s.Action(ctx, job.ID, "resume", ""); err != nil {
		t.Fatal(err)
	}
	// A second Store instance models a restarted scheduler using the same durable queue.
	restarted := &Store{Pool: s.Pool}
	if err = restarted.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	task, ok, err = restarted.Claim(ctx)
	if err != nil || !ok || task.Cursor != "checkpoint" || task.Attempts != 1 || task.JobID != job.ID {
		t.Fatalf("retry lost checkpoint %+v %v", task, err)
	}
	if err = restarted.Commit(ctx, task, nil, "", 2); err != nil {
		t.Fatal(err)
	}
	var count int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM crawler_retries`).Scan(&count)
	if count != 0 {
		t.Fatal("ticket not consumed")
	}
}
func TestDelayedDetailsPreserveFactsAndCannotBeRequeuedByVerification(t *testing.T) {
	s := coverageStore(t)
	ctx := context.Background()
	platform := vehicle.PlatformBama
	_, err := s.Submit(ctx, vehicle.SearchCriteria{Query: "206", Type: vehicle.VehicleTypeCar, Platform: &platform}, false)
	if err != nil {
		t.Fatal(err)
	}
	page, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	row := vehicle.NormalizedVehicleListing{ID: "bama-retry", Platform: platform, Title: "206", VehicleType: vehicle.VehicleTypeCar, PriceToman: 123, URL: "https://bama.ir/car/test"}
	obs := crawljobs.Observation{Listing: row, SummaryHash: crawljobs.SummaryHash(row), NeedsDetails: true}
	if err = s.Commit(ctx, page, []crawljobs.Observation{obs}, "", 1); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE detail_tasks SET attempts=2`)
	detail, ok, err := s.ClaimDetail(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = s.CompleteDetail(ctx, detail, detail.Listing, 503, "http"); err != nil {
		t.Fatal(err)
	}
	var count int
	var due time.Time
	if err = s.Pool.QueryRow(ctx, `SELECT count(*),min(available_at) FROM crawler_retries`).Scan(&count, &due); err != nil || count != 1 || time.Until(due) < 59*time.Minute {
		t.Fatal("missing hour retry", err)
	}
	if err = s.Recheck(ctx, platform, row.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, e := s.Status(ctx, page.JobID)
	if e != nil || !snapshot.Done || snapshot.RetriesPending != 1 {
		t.Fatalf("delayed-only polling did not stop %+v %v", snapshot, e)
	}
	if err = s.ScheduleVerification(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = s.ClaimDetail(ctx); err != nil || ok {
		t.Fatal("verification bypassed delayed queue", err)
	}
	history, err := s.History(ctx, platform, row.ID, 0)
	if err != nil || history.Listing.PriceToman != 123 || history.Listing.Lifecycle.Availability != "active" {
		t.Fatal("facts lost", err)
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE crawler_retries SET available_at=now()-interval '1 second'`)
	if err = s.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	detail, ok, err = s.ClaimDetail(ctx)
	if err != nil || !ok || detail.Attempts != 1 || detail.Listing.URL != row.URL {
		t.Fatalf("retry lost source %+v %v", detail, err)
	}
	// Deletion evidence ends retries rather than entering the delayed queue.
	if err = s.CompleteDetail(ctx, detail, detail.Listing, 410, "http"); err != nil {
		t.Fatal(err)
	}
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM crawler_retries`).Scan(&count)
	if count != 0 {
		t.Fatal("410 queued again")
	}
}
