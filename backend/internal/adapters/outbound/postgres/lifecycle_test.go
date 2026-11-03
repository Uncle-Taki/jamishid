package postgres

import (
	"context"
	"errors"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"os"
	"testing"
)

func TestSummaryPublicationDetailRecoveryAndLifecycle(t *testing.T) {
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
		if _, e := s.Pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec("TRUNCATE crawl_searches,crawl_jobs,crawl_tasks,listings,search_listings,listing_observations,listing_events,detail_tasks,job_details CASCADE")
	platform := vehicle.PlatformBama
	job, err := s.Submit(ctx, vehicle.SearchCriteria{Query: "بنلی", Platform: &platform}, true)
	if err != nil {
		t.Fatal(err)
	}
	task, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	row := vehicle.NormalizedVehicleListing{ID: "durable-bike", Platform: platform, CityID: "tehran", NeighborhoodFa: "تهران، آزادی", Title: "بنلی", PriceToman: 100, MileageKm: -1, BodyStatus: vehicle.BodyStatusUnknown, URL: "https://bama.ir/motorcycle/durable-bike", VehicleType: vehicle.VehicleTypeMotorcycle}
	observations, err := s.Prepare(ctx, []vehicle.NormalizedVehicleListing{row})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Commit(ctx, task, observations, "", 1); err != nil {
		t.Fatal(err)
	}
	initial, err := s.Get(ctx, job.ID)
	if err != nil || len(initial.Data) != 1 || initial.Data[0].DetailsStatus != "partial" || initial.DetailsPending != 1 || initial.Done || !initial.CrawlDone {
		t.Fatalf("summaries must be visible before details: %+v %v", initial, err)
	}
	first, ok, err := s.ClaimDetail(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	exec("UPDATE detail_tasks SET lease_until=now()-interval '1 second'")
	recovered, ok, err := s.ClaimDetail(ctx)
	if err != nil || !ok || first.Token == recovered.Token {
		t.Fatal("detail recovery failed", err)
	}
	facts := row
	facts.MileageKm = 2000
	facts.BodyStatus = vehicle.BodyStatusPerfect
	if err = s.CompleteDetail(ctx, first, facts, 200, ""); !errors.Is(err, crawljobs.ErrLeaseLost) {
		t.Fatal("stale owner wrote", err)
	}
	detailFacts := vehicle.NormalizedVehicleListing{MileageKm: 2000, BodyStatus: vehicle.BodyStatusPerfect}
	recovered.Facts = &detailFacts
	if err = s.CompleteDetail(ctx, recovered, facts, 200, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteDetail(ctx, recovered, facts, 200, ""); !errors.Is(err, crawljobs.ErrLeaseLost) {
		t.Fatal("duplicate detail published", err)
	}
	complete, err := s.Get(ctx, job.ID)
	if err != nil || !complete.Done || complete.Data[0].MileageKm != 2000 || complete.Data[0].Lifecycle.LastVerified == nil {
		t.Fatalf("enrichment not published %+v %v", complete, err)
	}
	history, err := s.History(ctx, platform, row.ID, 0)
	if err != nil || len(history.Events) != 2 || history.Listing.Provenance["title"].Kind != "summary" || history.Listing.Provenance["mileageKm"].Kind != "detail" {
		t.Fatalf("provenance/history %+v %v", history, err)
	}
	check := func(code int, kind string) {
		t.Helper()
		if e := s.Recheck(ctx, platform, row.ID); e != nil {
			t.Fatal(e)
		}
		d, claimed, e := s.ClaimDetail(ctx)
		if e != nil || !claimed {
			t.Fatal(e)
		}
		if e = s.CompleteDetail(ctx, d, d.Listing, code, kind); e != nil {
			t.Fatal(e)
		}
	}
	// Reverification becomes due after 48 hours, using the persisted source URL.
	exec("UPDATE listings SET last_checked=now()-interval '47 hours'")
	if err = s.ScheduleVerification(ctx); err != nil {
		t.Fatal(err)
	}
	if _, claimed, e := s.ClaimDetail(ctx); e != nil || claimed {
		t.Fatalf("fresh listing was requeued: %v", e)
	}
	exec("UPDATE listings SET last_checked=now()-interval '49 hours'")
	if err = s.ScheduleVerification(ctx); err != nil {
		t.Fatal(err)
	}
	verification, claimed, e := s.ClaimDetail(ctx)
	if e != nil || !claimed || verification.Purpose != "verify" || verification.Listing.URL != row.URL {
		t.Fatalf("48-hour verification lost source identity: %+v %v", verification, e)
	}
	if err = s.ScheduleVerification(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteDetail(ctx, verification, facts, 200, ""); err != nil {
		t.Fatal("scheduler replaced running verification", err)
	}
	for _, failure := range []struct {
		code int
		kind string
	}{{0, "timeout"}, {429, "http"}, {503, "http"}, {200, "parser"}} {
		check(failure.code, failure.kind)
		h, e := s.History(ctx, platform, row.ID, 0)
		if e != nil || h.Listing.Lifecycle.Availability != "active" || h.Listing.MileageKm != 2000 {
			t.Fatal("transient failure removed listing", e)
		}
	}
	check(404, "http")
	h, err := s.History(ctx, platform, row.ID, 0)
	if err != nil || h.Listing.Lifecycle.Availability != "unavailable" {
		t.Fatal("explicit absence not recorded", err)
	}
	inventory, err := s.Search(ctx, crawljobs.InventoryQuery{})
	if err != nil || inventory.Total != 0 {
		t.Fatal("removed listing returned in inventory", err)
	}
	if err = s.Recheck(ctx, platform, row.ID); err != nil {
		t.Fatal(err)
	}
	verify, ok, err := s.ClaimDetail(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	facts.PriceToman = 120
	if err = s.CompleteDetail(ctx, verify, facts, 200, ""); err != nil {
		t.Fatal(err)
	}
	h, err = s.History(ctx, platform, row.ID, 0)
	if err != nil || h.Listing.Lifecycle.Availability != "active" || h.Listing.PriceToman != 120 || h.Events[0].Data.PriceToman != 120 {
		t.Fatal("reappearance/price observation lost", err)
	}
	// A changed summary fences an in-flight detail response from an older revision.
	if err = s.Recheck(ctx, platform, row.ID); err != nil {
		t.Fatal(err)
	}
	old, ok, err := s.ClaimDetail(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	job, err = s.Submit(ctx, vehicle.SearchCriteria{Query: "بنلی", Platform: &platform}, true)
	if err != nil {
		t.Fatal(err)
	}
	page, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	row.PriceToman = 140
	obs, err := s.Prepare(ctx, []vehicle.NormalizedVehicleListing{row})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Commit(ctx, page, obs, "", 1); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteDetail(ctx, old, facts, 200, ""); !errors.Is(err, crawljobs.ErrLeaseLost) {
		t.Fatal("old revision overwrote summary", err)
	}
	if err = s.ScheduleVerification(ctx); err != nil {
		t.Fatal(err)
	}
}
