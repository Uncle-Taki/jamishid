// Command smokeworker runs the real crawljobs loops, PostgreSQL queue
// semantics and telemetry against deterministic in-process fake marketplace
// fetchers. It exists only for the isolated monitoring smoke test; it must
// never be used in production and never contacts real marketplaces.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/postgres"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/telemetry"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

// fakeCrawler returns deterministic listings per page and exhausts after
// FAKE_PAGES pages, mirroring a real marketplace pagination contract.
type fakeCrawler struct {
	pages    int
	failures int // every Nth fetch fails with a source error (0 = never)
	hangAt   time.Time
	calls    int
}

func (f *fakeCrawler) AggregateWithSources(ctx context.Context, criteria vehicle.SearchCriteria) (vehicle.CrawlResult, error) {
	f.calls++
	if f.failures > 0 && f.calls%f.failures == 0 {
		return vehicle.CrawlResult{Sources: []vehicle.SourceStatus{{Platform: *criteria.Platform, Status: "error", FailureKind: "source_wait"}}}, fmt.Errorf("fake source cooling down")
	}
	// A hang after the configured delay freezes this loop inside a task: the
	// active gauge sticks, the lease expires and another loop reclaims it.
	if !f.hangAt.IsZero() && time.Now().After(f.hangAt) {
		log.Printf("fake crawler hanging forever for stale-loop testing")
		<-make(chan struct{})
	}
	page := 1
	if criteria.Cursors != nil && criteria.Cursors[*criteria.Platform] != "" {
		page, _ = strconv.Atoi(criteria.Cursors[*criteria.Platform])
	}
	count := envInt("FAKE_LISTINGS", 8)
	listings := []vehicle.ScoredListing{}
	for i := 0; i < count; i++ {
		row := vehicle.NormalizedVehicleListing{
			ID:          fmt.Sprintf("smoke-%s-%d-%d", *criteria.Platform, page, i),
			Platform:    *criteria.Platform,
			Title:       "دودوپه پیکان",
			BrandModel:  "Smoke Test Model",
			VehicleType: criteria.Type,
			CityID:      criteria.CitySlug,
			PriceToman:  float64(100000000 + page*1000 + i),
			MileageKm:   float64(10000 + i),
			Year:        1400,
			BodyStatus:  vehicle.BodyStatusPerfect,
			URL:         fmt.Sprintf("https://smoke.invalid/%s/%d/%d", *criteria.Platform, page, i),
			ScrapedAt:   time.Now().UTC().Format(time.RFC3339),
		}
		if row.VehicleType == "" {
			row.VehicleType = vehicle.VehicleTypeCar
		}
		if row.CityID == "" {
			row.CityID = "tehran"
		}
		listings = append(listings, vehicle.ScoredListing{NormalizedVehicleListing: row})
	}
	status := vehicle.SourceStatus{Platform: *criteria.Platform, Status: "ok", Page: page}
	if page < f.pages {
		status.NextCursor = strconv.Itoa(page + 1)
	}
	return vehicle.CrawlResult{Listings: listings, Sources: []vehicle.SourceStatus{status}}, nil
}

// fakeFetch enriches a listing deterministically without network access.
func fakeFetch(ctx context.Context, platform vehicle.Platform, url string) (vehicle.NormalizedVehicleListing, error) {
	if kind := os.Getenv("FAKE_DETAIL_ERROR"); kind != "" {
		return vehicle.NormalizedVehicleListing{}, &fakeSourceError{kind: kind}
	}
	if seconds := envInt("FAKE_DETAIL_HANG_SECONDS", 0); seconds > 0 {
		select {
		case <-time.After(time.Duration(seconds) * time.Second):
		case <-ctx.Done():
			return vehicle.NormalizedVehicleListing{}, ctx.Err()
		}
	}
	return vehicle.NormalizedVehicleListing{
		ID:         "facts",
		Platform:   platform,
		URL:        url,
		PriceToman: 1,
		MileageKm:  1,
		Year:       1400,
		BodyStatus: vehicle.BodyStatusPerfect,
	}, nil
}

type fakeSourceError struct{ kind string }

func (e *fakeSourceError) Error() string { return "fake detail failure: " + e.kind }

func fakeMerge(row *vehicle.NormalizedVehicleListing, facts vehicle.NormalizedVehicleListing) {
	if facts.PriceToman > 0 {
		row.PriceToman = facts.PriceToman
	}
	if facts.MileageKm > 0 {
		row.MileageKm = facts.MileageKm
	}
}

func main() {
	observations, err := telemetry.New(os.Stdout, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = observations.Shutdown(ctx)
	}()
	monitoring := &http.Server{Addr: ":9090", Handler: observations, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := monitoring.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Monitoring listener: %v", err)
		}
	}()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("smokeworker requires DATABASE_URL")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	store, err := postgres.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("PostgreSQL initialization failed: %v", err)
	}
	defer store.Close()
	store.RetryDelay = time.Hour
	if value := os.Getenv("CRAWLER_RETRY_DELAY"); value != "" {
		if delay, parseErr := time.ParseDuration(value); parseErr == nil {
			store.RetryDelay = delay
		}
	}
	store.Metrics = observations

	pageLoops := envInt("WORKER_PAGE_LOOPS", 3)
	detailLoops := envInt("WORKER_DETAIL_LOOPS", 2)
	observations.SetWorkerCapacity("page", pageLoops)
	observations.SetWorkerCapacity("detail", detailLoops)
	crawler := &fakeCrawler{pages: envInt("FAKE_PAGES", 2), failures: envInt("FAKE_FAIL_EVERY", 0)}
	if seconds := envInt("FAKE_PAGE_HANG_SECONDS", 0); seconds > 0 {
		crawler.hangAt = time.Now().Add(time.Duration(seconds) * time.Second)
	}

	var wg sync.WaitGroup
	for i := 0; i < pageLoops; i++ {
		service := &crawljobs.Service{Store: store, Crawler: crawler, Observer: observations, Metrics: observations, Loop: strconv.Itoa(i + 1)}
		wg.Add(1)
		go func() {
			defer wg.Done()
			service.Run(ctx, func(err error) { log.Printf("Crawl worker: %v", err) })
		}()
	}
	for i := 0; i < detailLoops; i++ {
		detailService := &crawljobs.DetailService{Store: store, Fetch: fakeFetch, Merge: fakeMerge, Metrics: observations, Loop: strconv.Itoa(i + 1)}
		wg.Add(1)
		go func() {
			defer wg.Done()
			detailService.Run(ctx, func(err error) { log.Printf("Detail worker: %v", err) })
		}()
	}
	// Maintenance loop with the schedule scheduler, mirroring the real worker
	// minus marketplace catalog discovery.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			observations.LoopTick("maintenance", "1")
			observations.ScheduleAttempt("schedule")(store.Schedule(ctx))
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			stats := store.Pool.Stat()
			observations.ObservePostgresPool(stats.AcquiredConns(), stats.IdleConns(), stats.MaxConns())
		}
	}()
	wg.Wait()
	log.Println("smokeworker exiting")
}
