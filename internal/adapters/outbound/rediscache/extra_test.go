package rediscache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestInventoryCacheOperations(t *testing.T) {
	server, a, _ := testCaches(t)
	ctx := context.Background()

	// Miss on non-existent key
	_, status := a.GetInventory(ctx, "nonexistent-query")
	if status != "miss" {
		t.Fatalf("expected miss, got %s", status)
	}

	// Set valid inventory
	sample := crawljobs.InventoryResult{
		Total: 1,
		Data:  []vehicle.NormalizedVehicleListing{{ID: "inv-1", Title: "Pride"}},
	}
	setRes := a.SetInventory(ctx, "query-1", sample)
	if setRes != "ok" {
		t.Fatalf("expected ok, got %s", setRes)
	}

	// Hit on valid inventory
	got, status := a.GetInventory(ctx, "query-1")
	if status != "hit" || got.Total != 1 || len(got.Data) != 1 || got.Data[0].ID != "inv-1" {
		t.Fatalf("expected hit with correct payload, got status %s, %+v", status, got)
	}

	// Corrupted payload yields "error"
	rawKey := inventoryKey("query-1")
	server.Set(rawKey, "invalid json payload {{{{")
	_, status = a.GetInventory(ctx, "query-1")
	if status != "error" {
		t.Fatalf("expected error for corrupted JSON, got %s", status)
	}
}

func TestBudgetFloors(t *testing.T) {
	_, a, _ := testCaches(t)
	ctx := context.Background()

	// Default fallback for unknown
	if floor := getBudgetFloor("unknown_source"); floor != 2000 {
		t.Fatalf("expected 2000 default floor, got %v", floor)
	}
	// Source defaults
	if floor := getBudgetFloor("divar"); floor != 500 {
		t.Fatalf("expected 500 default floor for divar, got %v", floor)
	}
	if floor := getBudgetFloor("bama"); floor != 1200 {
		t.Fatalf("expected 1200 default floor for bama, got %v", floor)
	}
	if floor := getBudgetFloor("sheypoor"); floor != 1500 {
		t.Fatalf("expected 1500 default floor for sheypoor, got %v", floor)
	}
	// Source with proxy partition
	if floor := getBudgetFloor("divar:proxy1"); floor != 500 {
		t.Fatalf("expected 500 for divar:proxy1, got %v", floor)
	}

	// Set source-specific budget floor
	SetSourceBudgetFloor("bama", 750*time.Millisecond)
	if floor := getBudgetFloor("bama"); floor != 750 {
		t.Fatalf("expected 750ms source floor, got %v", floor)
	}

	// Set global budget floor
	SetBudgetFloor(300 * time.Millisecond)
	if floor := getBudgetFloor("other_src"); floor != 300 {
		t.Fatalf("expected 300ms global floor, got %v", floor)
	}

	// Wait with canceled context returns error
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := a.Wait(canceledCtx, "bama"); err == nil {
		t.Fatalf("expected error for canceled context in Wait")
	}
}

func TestCachePingCloseAndRefresh(t *testing.T) {
	_, a, b := testCaches(t)
	ctx := context.Background()

	if err := a.Ping(ctx); err != nil {
		t.Fatalf("expected ping to succeed, got %v", err)
	}

	// Test RefreshPage and RefreshDetail
	pageFetcher := func(ctx context.Context) (vehicle.ListingPage, error) {
		return vehicle.ListingPage{Page: 1, NextCursor: "n1", Listings: []vehicle.NormalizedVehicleListing{{ID: "refreshed-page"}}}, nil
	}
	p, _, err := a.RefreshPage(ctx, "refresh-page-key", pageFetcher)
	if err != nil || len(p.Listings) != 1 || p.Listings[0].ID != "refreshed-page" {
		t.Fatalf("RefreshPage failed: %v, %+v", err, p)
	}

	detailFetcher := func(ctx context.Context) (vehicle.NormalizedVehicleListing, error) {
		return vehicle.NormalizedVehicleListing{ID: "refreshed-detail", Title: "D"}, nil
	}
	d, _, err := a.RefreshDetail(ctx, "refresh-detail-key", detailFetcher)
	if err != nil || d.ID != "refreshed-detail" {
		t.Fatalf("RefreshDetail failed: %v, %+v", err, d)
	}

	// Test failing fetcher
	failFetcher := func(ctx context.Context) (vehicle.ListingPage, error) {
		return vehicle.ListingPage{}, errors.New("upstream failed")
	}
	if _, _, err := a.RefreshPage(ctx, "fail-page", failFetcher); err == nil {
		t.Fatalf("expected error from failFetcher")
	}

	// Test Close
	if err := b.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestMonitoringStatsAndParseInfo(t *testing.T) {
	_, a, _ := testCaches(t)
	ctx := context.Background()

	stats, err := a.MonitoringStats(ctx)
	if err != nil {
		t.Fatalf("MonitoringStats failed: %v", err)
	}
	if !stats.Up {
		t.Fatalf("expected Up = true")
	}
	if !stats.WriteProbeOK {
		t.Fatalf("expected WriteProbeOK = true")
	}

	// Test parseInfo and splitLines directly with CRLF and LF
	sampleInfo := "# Memory\r\nused_memory:1048576\r\nmaxmemory:268435456\r\n# Stats\nkeyspace_hits:42\nkeyspace_misses:5\nevicted_keys:0\nconnected_clients:3\ninvalid_field:not_a_number\n"

	fields := parseInfo(sampleInfo)
	if fields["used_memory"] != 1048576 {
		t.Fatalf("expected used_memory = 1048576, got %v", fields["used_memory"])
	}
	if fields["keyspace_hits"] != 42 {
		t.Fatalf("expected keyspace_hits = 42, got %v", fields["keyspace_hits"])
	}
	if fields["connected_clients"] != 3 {
		t.Fatalf("expected connected_clients = 3, got %v", fields["connected_clients"])
	}
}
