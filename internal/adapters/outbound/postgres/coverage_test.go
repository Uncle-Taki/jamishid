package postgres

import (
	"context"
	"fmt"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/rediscache"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/alicebob/miniredis/v2"
	"os"
	"strings"
	"testing"
	"time"
)

func coverageStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	if !strings.Contains(url, "_test") {
		t.Fatal("disposable test database required")
	}
	s, err := New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err = s.Pool.Exec(context.Background(), `TRUNCATE crawl_searches,listings,egress_proxies CASCADE; UPDATE coverage_catalogs SET next_refresh=now(),lease_until=NULL,token=NULL,categories='[]'`); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestCoveragePacingAndGlobalOverlap(t *testing.T) {
	s := coverageStore(t)
	ctx := context.Background()
	fetch := func(_ context.Context, p vehicle.Platform, k vehicle.VehicleType, _ string) ([]vehicle.Category, error) {
		return []vehicle.Category{{ID: "brand", Label: "Brand", Type: k}}, nil
	}
	for i := 0; i < 6; i++ {
		if err := s.RefreshCoverageCatalog(ctx, fetch); err != nil {
			t.Fatal(err)
		}
	}
	var partitions int
	var soonest, latest time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT count(*),min(next_refresh),max(next_refresh) FROM crawl_searches WHERE coverage`).Scan(&partitions, &soonest, &latest); err != nil || partitions != 6 || !latest.After(soonest) || latest.After(time.Now().Add(48*time.Hour)) {
		t.Fatalf("staggered discovery: %d %v %v", partitions, latest, err)
	}
	if err := s.RefreshCoverageCatalog(ctx, func(context.Context, vehicle.Platform, vehicle.VehicleType, string) ([]vehicle.Category, error) {
		t.Fatal("catalog fetched twice")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Pool.Exec(ctx, `UPDATE crawl_searches SET next_refresh=now() WHERE criteria->>'platform'='bama' AND criteria->>'type'='car'`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	task, ok, err := s.Claim(ctx)
	if err != nil || !ok || task.Mode != "full" || task.Criteria.NativeCategory != "brand" || task.Criteria.CitySlug != "iran" {
		t.Fatalf("baseline: %+v %v", task, err)
	}
	observations := []crawljobs.Observation{}
	for i := 0; i < 25; i++ {
		row := vehicle.NormalizedVehicleListing{ID: fmt.Sprintf("bama-%d", i), Platform: vehicle.PlatformBama, VehicleType: vehicle.VehicleTypeCar, Title: "Test brand", CityID: "tehran", URL: fmt.Sprintf("https://bama.ir/car/%d", i)}
		observations = append(observations, crawljobs.Observation{Listing: row, SummaryHash: crawljobs.SummaryHash(row)})
	}
	if err = s.Commit(ctx, task, observations, "persistent-cursor", 1); err != nil {
		t.Fatal(err)
	}
	if _, claimed, e := s.Claim(ctx); e != nil || claimed {
		t.Fatalf("baseline drained without pacing: %v", e)
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE crawl_tasks SET available_at=now()`)
	resumed, ok, err := s.Claim(ctx)
	if err != nil || !ok || resumed.Cursor != "persistent-cursor" {
		t.Fatalf("checkpoint: %+v %v", resumed, err)
	}
	if err = s.Commit(ctx, resumed, nil, "", 2); err != nil {
		t.Fatal(err)
	}
	coverage, err := s.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if coverage[0].Complete != 1 {
		t.Fatalf("completion not visible: %+v", coverage)
	}
	platform := vehicle.PlatformBama
	job, err := s.Submit(ctx, vehicle.SearchCriteria{Query: "Test brand", CitySlug: "tehran", Type: vehicle.VehicleTypeCar, Platform: &platform}, false)
	if err != nil {
		t.Fatal(err)
	}
	// A familiar promoted listing alone cannot stop the crawl. Neither can repeating the same page.
	pages := [][]crawljobs.Observation{observations[:1], observations[1:11], observations[1:11], observations[11:16], observations[16:21]}
	for index, rows := range pages {
		_, _ = s.Pool.Exec(ctx, `UPDATE crawl_tasks SET available_at=now()`)
		task, ok, err = s.Claim(ctx)
		if err != nil || !ok || task.JobID != job.ID {
			t.Fatalf("delta claim %d: %+v %v", index, task, err)
		}
		if err = s.Commit(ctx, task, rows, "more", index+1); err != nil {
			t.Fatal(err)
		}
		snapshot, e := s.Get(ctx, job.ID)
		if e != nil {
			t.Fatal(e)
		}
		if snapshot.CrawlDone != (index == len(pages)-1) {
			t.Fatalf("overlap ended at wrong page %d: %+v", index, snapshot)
		}
	}
}
func TestEgressPoolHealthAndRemoval(t *testing.T) {
	s := coverageStore(t)
	ctx := context.Background()
	if _, _, err := s.AcquireEgress(ctx); err != nil {
		t.Fatal(err)
	}
	proxyID, err := s.PutProxy(ctx, "one", "http://operator:secret@198.51.100.1:9999", true)
	if err != nil {
		t.Fatal(err)
	}
	got, endpoint, err := s.AcquireEgress(ctx)
	if err != nil || got != proxyID || !strings.Contains(endpoint, "secret") {
		t.Fatal("proxy not acquired", err)
	}
	if err = s.ReportEgress(ctx, proxyID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AcquireEgress(ctx); err == nil {
		t.Fatal("unhealthy pool fell back to direct access")
	}
	rows, err := s.Proxies(ctx)
	if err != nil || strings.Contains(rows[0].Address, "secret") {
		t.Fatal("proxy credentials exposed", err)
	}
	if err = s.DeleteProxy(ctx, proxyID); err != nil {
		t.Fatal(err)
	}
	if _, endpoint, err = s.AcquireEgress(ctx); err != nil || endpoint != "" {
		t.Fatal("removed proxy still used", err)
	}
}

func TestInventoryCacheTracksCommittedFactsAndSurvivesRedisFailure(t *testing.T) {
	s := coverageStore(t)
	ctx := context.Background()
	server := miniredis.RunT(t)
	cache, err := rediscache.New("redis://"+server.Addr(), time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	s.InventoryCache = cache
	platform := vehicle.PlatformDivar
	_, err = s.Submit(ctx, vehicle.SearchCriteria{Query: "206", Type: vehicle.VehicleTypeCar, Platform: &platform}, false)
	if err != nil {
		t.Fatal(err)
	}
	task, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatal("claim", err)
	}
	row := vehicle.NormalizedVehicleListing{ID: "divar-cached", Platform: platform, VehicleType: vehicle.VehicleTypeCar, Title: "206", CityID: "tehran", PriceToman: 100}
	if err = s.Commit(ctx, task, []crawljobs.Observation{{Listing: row, SummaryHash: crawljobs.SummaryHash(row)}}, "", 1); err != nil {
		t.Fatal(err)
	}
	q := crawljobs.InventoryQuery{Type: "car", Model: "206", Category: "peugeot-206ir"}
	before, err := s.Search(ctx, q)
	if err != nil || before.Total != 1 || before.Data[0].PriceToman != 100 {
		t.Fatal("initial inventory", err)
	}
	if len(server.Keys()) == 0 {
		t.Fatal("query was not cached")
	}
	_, err = s.Pool.Exec(ctx, `UPDATE listings SET price=200,data=jsonb_set(data,'{priceToman}','200')`)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Search(ctx, q)
	if err != nil || after.Data[0].PriceToman != 200 {
		t.Fatal("stale inventory after committed update", err)
	}
	server.Close()
	fallback, err := s.Search(ctx, q)
	if err != nil || fallback.Total != 1 || fallback.Data[0].PriceToman != 200 {
		t.Fatal("Redis failure lost persisted results", err)
	}
}

func TestRateLimitDeferralsPreserveRetryBudgetAndEvidence(t *testing.T) {
	s := coverageStore(t)
	ctx := context.Background()
	platform := vehicle.PlatformDivar
	_, err := s.Submit(ctx, vehicle.SearchCriteria{Query: "206", Type: vehicle.VehicleTypeCar, Platform: &platform}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, `UPDATE crawl_tasks SET attempts=2`)
	if err != nil {
		t.Fatal(err)
	}
	task, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatal("claim", err)
	}
	if err = s.Fail(ctx, task, "rate_limited"); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	if err = s.Pool.QueryRow(ctx, `SELECT status,attempts FROM crawl_tasks WHERE job_id=$1`, task.JobID).Scan(&status, &attempts); err != nil || status != "retry" || attempts != 2 {
		t.Fatalf("page rate limit exhausted attempts: %s %d %v", status, attempts, err)
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE crawl_tasks SET available_at=now()`)
	task, ok, err = s.Claim(ctx)
	if err != nil || !ok {
		t.Fatal("reclaim", err)
	}
	row := vehicle.NormalizedVehicleListing{ID: "divar-limited", Platform: platform, VehicleType: vehicle.VehicleTypeCar, Title: "206", CityID: "tehran", PriceToman: 100}
	if err = s.Commit(ctx, task, []crawljobs.Observation{{Listing: row, SummaryHash: crawljobs.SummaryHash(row), NeedsDetails: true}}, "", 1); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE detail_tasks SET attempts=2`)
	detail, ok, err := s.ClaimDetail(ctx)
	if err != nil || !ok {
		t.Fatal("detail", err)
	}
	if err = s.CompleteDetail(ctx, detail, detail.Listing, 429, "rate_limited"); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT status,attempts FROM detail_tasks WHERE listing_id=$1`, row.ID).Scan(&status, &attempts); err != nil || status != "retry" || attempts != 2 {
		t.Fatalf("detail rate limit exhausted attempts: %s %d %v", status, attempts, err)
	}
	var events int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM listing_events WHERE listing_id=$1 AND status_code=429`, row.ID).Scan(&events); err != nil || events != 1 {
		t.Fatal("rate limit evidence missing", err)
	}
	history, err := s.History(ctx, platform, row.ID, 0)
	if err != nil || history.Listing.PriceToman != 100 || history.Listing.Lifecycle.Availability != "active" || history.Listing.Lifecycle.LastVerified != nil {
		t.Fatal("rate limit changed source facts", err)
	}
}
