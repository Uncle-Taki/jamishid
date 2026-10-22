package memory

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestCache_Miss(t *testing.T) {
	cache := NewCache(1 * time.Minute)
	_, found := cache.Get(context.Background(), "unknown")
	if found {
		t.Error("expected to not find key")
	}
}

func TestCache_Hit(t *testing.T) {
	cache := NewCache(1 * time.Minute)
	key := "my-key"
	listings := []vehicle.ScoredListing{
		{NormalizedVehicleListing: vehicle.NormalizedVehicleListing{ID: "1"}},
	}

	err := cache.Set(context.Background(), key, listings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cachedListings, found := cache.Get(context.Background(), key)
	if !found {
		t.Fatal("expected to find key")
	}
	if len(cachedListings) != 1 || cachedListings[0].ID != "1" {
		t.Error("cached data mismatch")
	}
}

func TestCache_Expiration(t *testing.T) {
	cache := NewCache(1 * time.Millisecond)
	key := "my-key"
	listings := []vehicle.ScoredListing{}

	_ = cache.Set(context.Background(), key, listings)

	time.Sleep(5 * time.Millisecond)

	_, found := cache.Get(context.Background(), key)
	if found {
		t.Error("expected key to be expired and not returned")
	}
}

func TestCache_Cleanup(t *testing.T) {
	cache := NewCache(1 * time.Millisecond)
	key := "my-key"
	listings := []vehicle.ScoredListing{}

	_ = cache.Set(context.Background(), key, listings)

	time.Sleep(5 * time.Millisecond)

	cache.Cleanup()

	cache.mu.RLock()
	_, found := cache.items[key]
	cache.mu.RUnlock()

	if found {
		t.Error("expected key to be removed by cleanup")
	}
}

func TestCache_Concurrency(t *testing.T) {
	cache := NewCache(1 * time.Minute)
	key := "concurrent-key"
	listings := []vehicle.ScoredListing{{}}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = cache.Set(context.Background(), key, listings)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_, _ = cache.Get(context.Background(), key)
		}
	}()

	wg.Wait()
}
