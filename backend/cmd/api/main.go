package main

import (
	"context"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/postgres"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/telemetry"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	inboundHttp "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/inbound/http"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/memory"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/rediscache"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers/bama"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers/divar"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers/sheypoor"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/aggregation"
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
	scrapers.ConfigureObserver(observations)
	// Separate internal monitoring listener; it is not routed through public nginx.
	monitoring := &http.Server{Addr: ":9090", Handler: observations, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := monitoring.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Monitoring listener: %v", err)
		}
	}()
	sharedClient := scrapers.NewSharedClient()
	if path := os.Getenv("CATEGORY_CATALOG_PATH"); path != "" {
		if err := scrapers.DefaultProviderCatalog().LoadSnapshot(path); err != nil {
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
	rediscache.SetBudgetFloor(2 * time.Second)
	var cache ports.ListingCacheRepository = memory.NewCache(24 * time.Hour)
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		shared, err := rediscache.New(redisURL, 24*time.Hour, observations)
		if err != nil {
			log.Fatal(err)
		}
		defer shared.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err = shared.Ping(ctx); err != nil {
			cancel()
			log.Fatal("Redis cache unavailable at startup")
		}
		key, err := shared.CursorSigningKey(ctx)
		cancel()
		if err != nil {
			log.Fatal("Redis cursor signing key unavailable")
		}
		if err = scrapers.ConfigureCursorSigningKey(key); err != nil {
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
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		store, err = postgres.New(ctx, databaseURL)
		cancel()
		if err != nil {
			log.Fatalf("PostgreSQL initialization failed: %v", err)
		}
		defer store.Close()
		store.RetryDelay = time.Hour
		if value := os.Getenv("CRAWLER_RETRY_DELAY"); value != "" {
			delay, parseErr := time.ParseDuration(value)
			if parseErr != nil || delay < time.Hour || delay > 2*time.Hour {
				log.Fatal("CRAWLER_RETRY_DELAY must be between 1h and 2h")
			}
			store.RetryDelay = delay
		}
		store.InventoryCache = inventoryCache
		scrapers.ConfigureEgressPool(store)
	}
	if os.Getenv("APP_ROLE") == "worker" {
		if store == nil {
			log.Fatal("Worker requires DATABASE_URL")
		}
		if inventoryCache == nil {
			log.Fatal("Worker requires REDIS_URL for shared source budgets and cursor signing")
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		service := &crawljobs.Service{Store: store, Crawler: aggService, Observer: observations}
		if err = store.Schedule(ctx); err != nil {
			log.Printf("Initial schedule: %v", err)
		}
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); service.Run(ctx, func(err error) { log.Printf("Crawl worker: %v", err) }) }()
		}
		detailService := &crawljobs.DetailService{Store: store, Fetch: scrapers.FetchListingFacts, Merge: scrapers.MergeDetails}
		for i := 0; i < 2; i++ {
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
				if err := store.RefreshCoverageCatalog(ctx, scrapers.DefaultProviderCatalog().Categories); err != nil && ctx.Err() == nil {
					log.Printf("Coverage catalog: %v", err)
				}
				if err := store.ScheduleVerification(ctx); err != nil && ctx.Err() == nil {
					log.Printf("Listing verification: %v", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
		wg.Wait()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = monitoring.Shutdown(shutdown)
		return
	}
	handler := inboundHttp.NewAggregationHandler(aggService)
	if store != nil {
		handler.SetJobs(store)
	}
	router := inboundHttp.SetupRouter(handler)
	if store != nil {
		inboundHttp.ConfigureOperations(router, store, os.Getenv("ADMIN_API_TOKEN"))
	}

	srv := &http.Server{
		Addr:    ":8080",
		Handler: observations.HTTP(router),
	}

	go func() {
		log.Println("Starting server on :8080")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
		log.Fatal("Server forced to shutdown:", err)
	}
	_ = monitoring.Shutdown(ctx)

	log.Println("Server exiting")
}
