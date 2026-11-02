package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/rediscache"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/sourcecheck"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"os"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if path := os.Getenv("CATEGORY_CATALOG_PATH"); path != "" {
		if err := scrapers.DefaultProviderCatalog().LoadSnapshot(path); err != nil {
			fmt.Fprintln(os.Stderr, "Catalog snapshot unavailable; using live discovery")
		}
	}
	if url := os.Getenv("REDIS_URL"); url != "" {
		cache, err := rediscache.New(url, 24*time.Hour, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Shared provider budget unavailable")
			os.Exit(2)
		}
		defer cache.Close()
		if err = cache.Ping(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "Shared provider budget unavailable")
			os.Exit(2)
		}
		scrapers.ConfigureProviderBudget(cache)
	}
	client := scrapers.NewSharedClient()
	hosts := map[vehicle.Platform]string{vehicle.PlatformDivar: "https://divar.ir", vehicle.PlatformBama: "https://bama.ir", vehicle.PlatformSheypoor: "https://www.sheypoor.com"}
	report := sourcecheck.Run(ctx, func(ctx context.Context, p vehicle.Platform, c vehicle.SearchCriteria) (vehicle.ListingPage, error) {
		return scrapers.PublicSearchPage(ctx, client, hosts[p], p, c, "")
	})
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		os.Exit(2)
	}
	if len(report.Alerts) > 0 {
		os.Exit(1)
	}
}
