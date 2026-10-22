package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	inboundHttp "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/inbound/http"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/memory"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers/bama"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers/divar"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers/sheypoor"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/aggregation"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func main() {
	sharedClient := scrapers.NewSharedClient()

	divarScraper := divar.NewScraper(sharedClient)
	bamaScraper := bama.NewScraper(sharedClient)
	sheypoorScraper := sheypoor.NewScraper(sharedClient)

	scrapersList := []ports.MarketplaceScraper{
		divarScraper,
		bamaScraper,
		sheypoorScraper,
	}

	cache := memory.NewCache(5 * time.Minute)

	weights := vehicle.ScoreWeights{
		PriceWeight:     40.0,
		ConditionWeight: 30.0,
		MileageWeight:   20.0,
		YearWeight:      10.0,
	}

	aggService := aggregation.NewAggregationService(scrapersList, cache, weights)

	handler := inboundHttp.NewAggregationHandler(aggService)
	router := inboundHttp.SetupRouter(handler)

	srv := &http.Server{
		Addr:    ":8080",
		Handler: router,
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

	log.Println("Server exiting")
}
