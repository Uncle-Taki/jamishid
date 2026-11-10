package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	inboundHttp "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/inbound/http"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/memory"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/postgres"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/rediscache"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers/bama"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers/divar"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers/sheypoor"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/telemetry"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/tokens"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/aggregation"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	userApp "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/user"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

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
	loggingCtx, stopLogging := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopLogging()
	go observations.LoggingHeartbeat(loggingCtx)
	report := func(ctx context.Context, operation string, err error) {
		ports.ReportDiagnostic(observations.DiagnosticContext(ctx), ports.Diagnostic{Component: "runtime", Operation: operation}, err)
	}
	scrapers.ConfigureObserver(observations)
	sharedClient := scrapers.NewSharedClient()
	// Separate internal monitoring listener; it is not routed through public nginx.
	// One owner shuts it down: this defer, on both the API and worker paths.
	monitoring := &http.Server{Addr: ":9090", Handler: observations, ReadHeaderTimeout: 5 * time.Second}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = monitoring.Shutdown(ctx)
	}()
	go func() {
		if err := monitoring.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			report(context.Background(), "monitoring.listen", err)
			log.Printf("Monitoring listener: %v", err)
		}
	}()
	if path := os.Getenv("CATEGORY_CATALOG_PATH"); path != "" {
		if err := scrapers.DefaultProviderCatalog().LoadSnapshot(path); err != nil {
			report(context.Background(), "catalog.load", err)
			log.Printf("Category snapshot not loaded; using live discovery: %v", err)
		}
	}

	divarScraper := divar.NewScraper(sharedClient)
	bamaScraper := bama.NewScraper(sharedClient)
	sheypoorScraper := sheypoor.NewScraper(sharedClient)

	scrapersList := []ports.MarketplaceScraper{
		divarScraper,
		bamaScraper,
		sheypoorScraper,
	}

	var inventoryCache crawljobs.InventoryCache
	var sharedCache *rediscache.Cache
	rediscache.SetSourceBudgetFloor("divar", 500*time.Millisecond)
	rediscache.SetSourceBudgetFloor("bama", 1200*time.Millisecond)
	rediscache.SetSourceBudgetFloor("sheypoor", 1500*time.Millisecond)
	var cache ports.ListingCacheRepository = memory.NewCache(24 * time.Hour)
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		shared, err := rediscache.New(redisURL, 24*time.Hour, observations)
		if err != nil {
			report(context.Background(), "redis.configure", err)
			log.Fatal(err)
		}
		sharedCache = shared
		defer shared.Close()
		ctx, cancel := context.WithTimeout(observations.DiagnosticContext(context.Background()), 5*time.Second)
		if err = shared.Ping(ctx); err != nil {
			report(ctx, "redis.startup", err)
			cancel()
			log.Fatal("Redis cache unavailable at startup")
		}
		key, err := shared.CursorSigningKey(ctx)
		cancel()
		if err != nil {
			report(ctx, "cursor_signing_key", err)
			log.Fatal("Redis cursor signing key unavailable")
		}
		if err = scrapers.ConfigureCursorSigningKey(key); err != nil {
			report(ctx, "cursor.configure", err)
			log.Fatal(err)
		}
		cache = shared
		inventoryCache = shared
		scrapers.ConfigureDetailCache(shared)
		scrapers.ConfigureProviderBudget(shared)
		log.Println("Shared Redis page/detail cache enabled (24h TTL)")
	}

	weights := vehicle.ScoreWeights{
		PriceWeight:     40.0,
		ConditionWeight: 30.0,
		MileageWeight:   20.0,
		YearWeight:      10.0,
	}

	aggService := aggregation.NewAggregationService(scrapersList, cache, weights)
	aggService.SetObserver(observations)

	var store *postgres.Store
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		ctx, cancel := context.WithTimeout(observations.DiagnosticContext(context.Background()), 30*time.Second)
		store, err = postgres.New(ctx, databaseURL)
		cancel()
		if err != nil {
			report(ctx, "postgres.startup", err)
			log.Fatalf("PostgreSQL initialization failed: %v", err)
		}
		defer store.Close()
		store.RetryDelay = time.Hour
		if value := os.Getenv("CRAWLER_RETRY_DELAY"); value != "" {
			delay, parseErr := time.ParseDuration(value)
			if parseErr != nil || delay < time.Hour || delay > 2*time.Hour {
				report(context.Background(), "retry.configure", errors.New("invalid retry delay"))
				log.Fatal("CRAWLER_RETRY_DELAY must be between 1h and 2h")
			}
			store.RetryDelay = delay
		}
		store.InventoryCache = inventoryCache
		store.Metrics = observations
		scrapers.ConfigureEgressPool(store)
	}
	if os.Getenv("APP_ROLE") == "worker" {
		if store == nil {
			report(context.Background(), "worker.configure_database", errors.New("database not configured"))
			log.Fatal("Worker requires DATABASE_URL")
		}
		if inventoryCache == nil {
			report(context.Background(), "worker.configure_cache", errors.New("cache not configured"))
			log.Fatal("Worker requires REDIS_URL for shared source budgets and cursor signing")
		}
		runWorker(observations, store, aggService)
		return
	}

	// API role: optionally run the single designated shared snapshot collector.
	// The collector uses its own small read-only pool (two connections, no
	// migrations) so monitoring scans never compete with API handlers for
	// application connections; its lifetime is bound to the collector context.
	if store != nil && os.Getenv("JAMSHID_MONITORING_COLLECTOR") == "1" {
		interval := 15 * time.Second
		if value := os.Getenv("JAMSHID_MONITORING_INTERVAL"); value != "" {
			if parsed, parseErr := time.ParseDuration(value); parseErr == nil && parsed >= time.Second {
				interval = parsed
			}
		}
		collectorCtx, stopCollector := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stopCollector()
		monitoringStore, err := postgres.NewMonitoring(collectorCtx, os.Getenv("DATABASE_URL"))
		if err != nil {
			// Optional monitoring must not take down a working API. A failed
			// dedicated-pool initialization is logged and the API continues
			// serving requests without the shared snapshot collector (its
			// /metrics stays up, serving worker/HTTP signals; the poll-health
			// series simply stay absent because no collector runs).
			report(collectorCtx, "monitoring.initialize", err)
			log.Print("Monitoring collector disabled (dedicated pool init failed)")
		} else {
			defer monitoringStore.Close()
			source := &snapshotSource{
				store:        monitoringStore,
				pgTimeout:    defaultPGProbeTimeout,
				redisTimeout: defaultRedisProbeTimeout,
			}
			// Only wrap Redis when configured: a nil *rediscache.Cache would
			// become a non-nil interface and be probed (nil dereference).
			if sharedCache != nil {
				source.cache = sharedCache
			}
			// The poll deadline is the sum of the two independent probe
			// deadlines plus headroom, so each dependency gets its own bounded
			// budget and total collection stays bounded.
			collectorDone := observations.RunSnapshotCollector(collectorCtx, source, interval, defaultPGProbeTimeout+defaultRedisProbeTimeout+time.Second)
			// Cancel and join before the dedicated pool's deferred Close.
			defer func() { stopCollector(); <-collectorDone }()
			log.Printf("Monitoring snapshot collector enabled (interval %s, dedicated read-only pool); this process is the designated collector", interval)
		}
	}
	if store != nil {
		appCtx, stopApp := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stopApp()
		startPoolStats(appCtx, observations, store)
	}
	handler := inboundHttp.NewAggregationHandler(aggService)
	if store != nil {
		handler.SetJobs(store)
	}
	router := inboundHttp.SetupRouter(handler)
	if store != nil {
		inboundHttp.ConfigureOperations(router, store, os.Getenv("ADMIN_API_TOKEN"))
	}

	tokenManager, err := tokens.NewHMACTokenManager([]byte(os.Getenv("AUTH_SECRET_KEY")))
	if err != nil {
		report(context.Background(), "tokens.configure", err)
		log.Fatalf("Token manager initialization failed: %v", err)
	}

	var userRepo ports.UserRepository
	var savedRepo ports.SavedListingRepository
	if store != nil {
		userRepo = store
		savedRepo = store
	} else {
		memStore := memory.NewUserStore()
		userRepo = memStore
		savedRepo = memStore
	}

	userSvc := userApp.NewService(userRepo, savedRepo, tokenManager)
	userHandler := inboundHttp.NewUserHandler(userSvc, tokenManager)
	inboundHttp.ConfigureUserRoutes(router, userHandler)

	srv := &http.Server{
		Addr:    ":8080",
		Handler: observations.HTTP(router),
	}

	go func() {
		log.Println("Starting server on :8080")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			report(context.Background(), "http.listen", err)
			log.Fatalf("listen: %s\n", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		report(ctx, "http.shutdown", err)
		log.Fatal("Server forced to shutdown:", err)
	}
	_ = monitoring.Shutdown(ctx)

	log.Println("Server exiting")
}

// loopCount reads a bounded worker loop count from the environment.
func loopCount(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value < 1 || value > 16 {
		return fallback
	}
	return value
}

// startPoolStats periodically records application pool statistics until ctx
// is canceled; the goroutine always stops with the process's signal context.
func startPoolStats(ctx context.Context, observations *telemetry.Observer, store *postgres.Store) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			stats := store.Pool.Stat()
			observations.ObservePostgresPool(stats.AcquiredConns(), stats.IdleConns(), stats.MaxConns())
		}
	}()
}

func runWorker(observations *telemetry.Observer, store *postgres.Store, crawler interface {
	AggregateWithSources(context.Context, vehicle.SearchCriteria) (vehicle.CrawlResult, error)
}) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx = observations.DiagnosticContext(ctx)
	pageLoops := loopCount("WORKER_PAGE_LOOPS", 3)
	detailLoops := loopCount("WORKER_DETAIL_LOOPS", 2)
	observations.SetWorkerCapacity("page", pageLoops)
	observations.SetWorkerCapacity("detail", detailLoops)
	if err := store.Schedule(ctx); err != nil {
		ports.ReportDiagnostic(ctx, ports.Diagnostic{Component: "scheduler", Operation: "initial_schedule"}, err)
		log.Printf("Initial schedule: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < pageLoops; i++ {
		service := &crawljobs.Service{Store: store, Crawler: crawler, Observer: observations, Metrics: observations, Loop: strconv.Itoa(i + 1)}
		wg.Add(1)
		go func() { defer wg.Done(); service.Run(ctx, func(err error) { log.Printf("Crawl worker: %v", err) }) }()
	}
	for i := 0; i < detailLoops; i++ {
		detailService := &crawljobs.DetailService{Store: store, Fetch: scrapers.FetchListingFacts, Merge: scrapers.MergeDetails, Metrics: observations, Loop: strconv.Itoa(i + 1)}
		wg.Add(1)
		go func() {
			defer wg.Done()
			detailService.Run(ctx, func(err error) { log.Printf("Detail worker: %v", err) })
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			cycleCtx := ports.DiagnosticScope(ctx)
			observations.LoopTick("maintenance", "1")
			coverageErr := store.RefreshCoverageCatalog(cycleCtx, scrapers.DefaultProviderCatalog().Categories)
			observations.ScheduleAttempt("coverage")(coverageErr)
			if coverageErr != nil && ctx.Err() == nil {
				ports.ReportDiagnostic(cycleCtx, ports.Diagnostic{Component: "scheduler", Operation: "coverage"}, coverageErr)
				log.Printf("Coverage catalog: %v", coverageErr)
			}
			cycleCtx = ports.DiagnosticScope(ctx)
			verificationErr := store.ScheduleVerification(cycleCtx)
			observations.ScheduleAttempt("verification")(verificationErr)
			if verificationErr != nil && ctx.Err() == nil {
				ports.ReportDiagnostic(cycleCtx, ports.Diagnostic{Component: "scheduler", Operation: "verification"}, verificationErr)
				log.Printf("Listing verification: %v", verificationErr)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	startPoolStats(ctx, observations, store)
	wg.Wait()
	// Trace exporter shutdown has one owner: main's defer. The monitoring
	// listener is also shut down by main's defer on this path.
}
