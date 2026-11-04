package rediscache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/alicebob/miniredis/v2"
)

func testCaches(t *testing.T) (*miniredis.Miniredis, *Cache, *Cache) {
	t.Helper()
	server := miniredis.RunT(t)
	a, err := New("redis://"+server.Addr(), 24*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New("redis://"+server.Addr(), 24*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return server, a, b
}

func TestSharedPageTTLAndOwnership(t *testing.T) {
	server, a, b := testCaches(t)
	ctx := context.Background()
	page := vehicle.ListingPage{Page: 2, NextCursor: "next", CategoryMatched: true, Listings: []vehicle.NormalizedVehicleListing{{ID: "one", Attributes: []vehicle.SourceAttribute{{Key: "color", Value: "black"}}}}}
	if err := a.Set(ctx, "search", page); err != nil {
		t.Fatal(err)
	}
	page.Listings[0].ID = "mutated"
	got, ok := b.Get(ctx, "search")
	if !ok || got.Page != 2 || got.NextCursor != "next" || !got.CategoryMatched || got.Listings[0].ID != "one" {
		t.Fatalf("shared page mismatch: %+v", got)
	}
	got.Listings[0].Attributes[0].Value = "mutated"
	again, _ := a.Get(ctx, "search")
	if again.Listings[0].Attributes[0].Value != "black" {
		t.Fatal("caller changed cached attributes")
	}
	server.FastForward(23 * time.Hour)
	if _, ok := b.Get(ctx, "search"); !ok {
		t.Fatal("expired early")
	}
	if ttl := server.TTL(cacheKey("page", "search")); ttl != time.Hour {
		t.Fatalf("read extended TTL: %v", ttl)
	}
	server.FastForward(time.Hour)
	if _, ok := a.Get(ctx, "search"); ok {
		t.Fatal("did not expire at 24 hours")
	}
}

func TestConcurrentInstancesFetchPageOnce(t *testing.T) {
	_, a, b := testCaches(t)
	ctx := context.Background()
	var calls atomic.Int32
	var wg sync.WaitGroup
	fetch := func(context.Context) (vehicle.ListingPage, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return vehicle.ListingPage{Page: 1, NextCursor: "next"}, nil
	}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cache := a
			if i%2 == 1 {
				cache = b
			}
			page, _, err := cache.LoadPage(ctx, "same", fetch)
			if err != nil || page.NextCursor != "next" {
				t.Errorf("load: %+v %v", page, err)
			}
		}(i)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("duplicate upstream calls: %d", calls.Load())
	}
}

func TestSharedDetailsAndFailedFetchRetry(t *testing.T) {
	server, a, b := testCaches(t)
	ctx := context.Background()
	var calls atomic.Int32
	fetch := func(context.Context) (vehicle.NormalizedVehicleListing, error) {
		if calls.Add(1) == 1 {
			return vehicle.NormalizedVehicleListing{}, errors.New("blocked")
		}
		return vehicle.NormalizedVehicleListing{DetailsStatus: "complete", MileageKm: 12000, Attributes: []vehicle.SourceAttribute{{Key: "mileage", Value: "12000"}}}, nil
	}
	if _, _, err := a.LoadDetail(ctx, "item", fetch); err == nil {
		t.Fatal("failure accepted")
	}
	if _, ok := b.GetDetail(ctx, "item"); ok {
		t.Fatal("failure cached")
	}
	facts, cached, err := b.LoadDetail(ctx, "item", fetch)
	if err != nil || cached || facts.MileageKm != 12000 {
		t.Fatalf("retry: %+v %v", facts, err)
	}
	facts, cached, err = a.LoadDetail(ctx, "item", fetch)
	if err != nil || !cached || calls.Load() != 2 {
		t.Fatalf("shared details refetched: %v %v calls=%d", cached, err, calls.Load())
	}
	server.FastForward(24 * time.Hour)
	if _, ok := b.GetDetail(ctx, "item"); ok {
		t.Fatal("details did not expire")
	}
	if _, _, err := b.LoadDetail(ctx, "item", fetch); err != nil || calls.Load() != 3 {
		t.Fatalf("expiry refresh: %v calls=%d", err, calls.Load())
	}
}

func TestLeaseSurvivesCanceledWaiterAndLateOwnerCannotOverwrite(t *testing.T) {
	server, a, b := testCaches(t)
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, _, err := a.LoadPage(ctx, "slow", func(context.Context) (vehicle.ListingPage, error) {
			close(started)
			<-release
			return vehicle.ListingPage{Page: 1}, nil
		})
		finished <- err
	}()
	<-started
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, _, err := b.LoadPage(waitCtx, "slow", func(context.Context) (vehicle.ListingPage, error) {
		t.Error("waiting process fetched")
		return vehicle.ListingPage{}, nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting cancel lost: %v", err)
	}
	server.FastForward(leaseTTL + time.Second)
	if _, _, err := b.LoadPage(ctx, "slow", func(context.Context) (vehicle.ListingPage, error) { return vehicle.ListingPage{Page: 2}, nil }); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-finished; err == nil {
		t.Fatal("expired owner published")
	}
	page, ok := b.Get(ctx, "slow")
	if !ok || page.Page != 2 {
		t.Fatalf("new owner overwritten: %+v", page)
	}
}

func TestReleaseOnlyDeletesOwnLease(t *testing.T) {
	server, a, _ := testCaches(t)
	ctx := context.Background()
	key := cacheKey("page", "owner") + ":lease"
	server.Set(key, "new-owner")
	if err := releaseScript.Run(ctx, a.client, []string{key}, "old-owner").Err(); err != nil {
		t.Fatal(err)
	}
	if got, _ := server.Get(key); got != "new-owner" {
		t.Fatal("deleted another owner's lease")
	}
}

func TestCursorKeySharedAcrossClientsAndNoTTL(t *testing.T) {
	server, a, b := testCaches(t)
	ctx := context.Background()
	first, err := a.CursorSigningKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.CursorSigningKey(ctx)
	if err != nil || string(first) != string(second) {
		t.Fatalf("keys differ: %v", err)
	}
	server.FastForward(72 * time.Hour)
	third, err := b.CursorSigningKey(ctx)
	if err != nil || string(first) != string(third) {
		t.Fatalf("key expired: %v", err)
	}
	server.Set(prefix+"cursor-signing-key", "corrupt")
	if _, err := a.CursorSigningKey(ctx); err == nil {
		t.Fatal("corrupt signing key accepted")
	}
}

func TestMalformedCacheIsRefetchedAndRedisOutageDoesNotFetch(t *testing.T) {
	server, a, _ := testCaches(t)
	ctx := context.Background()
	var calls int
	server.Set(cacheKey("page", "bad"), `{"version":0,"data":{}}`)
	fetch := func(context.Context) (vehicle.ListingPage, error) { calls++; return vehicle.ListingPage{Page: 1}, nil }
	if _, cached, err := a.LoadPage(ctx, "bad", fetch); err != nil || cached || calls != 1 {
		t.Fatalf("corruption not refreshed: cached=%v err=%v", cached, err)
	}
	server.Close()
	if _, _, err := a.LoadPage(ctx, "unavailable", fetch); err == nil {
		t.Fatal("cache outage hidden")
	}
	if calls != 1 {
		t.Fatal("Redis outage triggered uncoordinated fetch")
	}
}

func TestInventoryCacheRevisionExpiryAndOutcomeClassification(t *testing.T) {
	server, a, b := testCaches(t)
	ctx := context.Background()
	result := crawljobs.InventoryResult{Data: []vehicle.NormalizedVehicleListing{{ID: "persisted"}, {ID: "junk"}}, Total: 2, Limit: 50}
	a.SetInventory(ctx, "revision-1:query", result)
	if got, outcome := b.GetInventory(ctx, "revision-1:query"); outcome != "hit" || got.Data[0].ID != "persisted" {
		t.Fatalf("inventory not shared: outcome=%s", outcome)
	}
	if _, outcome := b.GetInventory(ctx, "revision-2:query"); outcome != "miss" {
		t.Fatalf("new revision expected miss, got %s", outcome)
	}
	server.Set(inventoryKey("revision-3:query"), `{not json`)
	if _, outcome := b.GetInventory(ctx, "revision-3:query"); outcome != "error" {
		t.Fatalf("corrupt payload expected error, got %s", outcome)
	}
	server.FastForward(2 * time.Minute)
	if _, outcome := b.GetInventory(ctx, "revision-1:query"); outcome != "miss" {
		t.Fatalf("inventory did not expire: outcome=%s", outcome)
	}
}
