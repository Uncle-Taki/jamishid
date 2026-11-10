package memory

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestCache_Miss(t *testing.T) {
	if _, found := NewCache(24*time.Hour).Get(context.Background(), "unknown"); found {
		t.Fatal("unexpected cache hit")
	}
}

func TestCache_HitPreservesPageAndOwnsItsData(t *testing.T) {
	cache := NewCache(24 * time.Hour)
	page := vehicle.ListingPage{Page: 2, NextCursor: "next", CategoryMatched: true,
		Listings: []vehicle.NormalizedVehicleListing{{ID: "one", Attributes: []vehicle.SourceAttribute{{Key: "color", Value: "black"}}}}}
	if err := cache.Set(context.Background(), "key", page); err != nil {
		t.Fatal(err)
	}
	page.Listings[0].ID = "changed"
	page.Listings[0].Attributes[0].Value = "changed"
	got, found := cache.Get(context.Background(), "key")
	if !found || got.Page != 2 || got.NextCursor != "next" || !got.CategoryMatched || got.Listings[0].ID != "one" || got.Listings[0].Attributes[0].Value != "black" {
		t.Fatalf("page or ownership lost: %+v", got)
	}
	got.Listings[0].Attributes[0].Value = "changed again"
	got.Listings[0].ID = "changed again"
	again, _ := cache.Get(context.Background(), "key")
	if again.Listings[0].ID != "one" || again.Listings[0].Attributes[0].Value != "black" {
		t.Fatal("caller mutated cached data")
	}
}

func TestCache_ExpirationAt24HoursIsNotExtendedByReads(t *testing.T) {
	cache := NewCache(24 * time.Hour)
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }
	_ = cache.Set(context.Background(), "key", vehicle.ListingPage{})
	now = now.Add(24*time.Hour - time.Nanosecond)
	if _, found := cache.Get(context.Background(), "key"); !found {
		t.Fatal("expired before 24 hours")
	}
	now = now.Add(time.Nanosecond)
	if _, found := cache.Get(context.Background(), "key"); found {
		t.Fatal("still valid at TTL boundary")
	}
	if len(cache.items) != 0 {
		t.Fatal("expired entry not removed")
	}
}

func TestCache_CleanupAndCapacity(t *testing.T) {
	cache := NewCache(24 * time.Hour)
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }
	for i := 0; i <= maxCacheEntries; i++ {
		_ = cache.Set(context.Background(), fmt.Sprint(i), vehicle.ListingPage{})
		now = now.Add(time.Second)
	}
	if len(cache.items) != maxCacheEntries {
		t.Fatalf("unbounded cache: %d", len(cache.items))
	}
	if _, found := cache.Get(context.Background(), "0"); found {
		t.Fatal("oldest entry not evicted")
	}
	if _, found := cache.Get(context.Background(), fmt.Sprint(maxCacheEntries)); !found {
		t.Fatal("new entry not cached")
	}
	now = now.Add(24 * time.Hour)
	cache.Cleanup()
	if len(cache.items) != 0 {
		t.Fatal("cleanup retained expired pages")
	}
}

func TestCache_Concurrency(t *testing.T) {
	cache := NewCache(24 * time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = cache.Set(context.Background(), "key", vehicle.ListingPage{Page: j})
				_, _ = cache.Get(context.Background(), "key")
				cache.Cleanup()
			}
		}()
	}
	wg.Wait()
}
