package scrapers

import (
	"context"
	"errors"
	"fmt"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/rediscache"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/alicebob/miniredis/v2"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestDetailEnrichmentUsesSharedRedisAcrossClients(t *testing.T) {
	server := miniredis.RunT(t)
	a, err := rediscache.New("redis://"+server.Addr(), 24*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := rediscache.New("redis://"+server.Addr(), 24*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	body, err := os.ReadFile("testdata/bama-detail.html")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	client := detailFixtureClient(t, func() (string, int) { calls.Add(1); return string(body), 200 })
	ConfigureDetailCache(a)
	defer ConfigureDetailCache(nil)
	rawURL := "https://bama.ir/car/detail-redis-integration"
	first := []vehicle.NormalizedVehicleListing{{ID: "one", Platform: vehicle.PlatformBama, URL: rawURL, MileageKm: -1}}
	enrichListings(context.Background(), client, first)
	ConfigureDetailCache(b)
	second := []vehicle.NormalizedVehicleListing{{ID: "two", CityID: "mashhad", Platform: vehicle.PlatformBama, URL: rawURL, MileageKm: -1}}
	enrichListings(context.Background(), client, second)
	if calls.Load() != 1 || first[0].DetailsStatus != "complete" || second[0].DetailsStatus != "complete" || second[0].ID != "two" || second[0].CityID != "mashhad" {
		t.Fatalf("shared item integration failed: calls=%d first=%+v second=%+v", calls.Load(), first, second)
	}
}

func detailFixtureClient(t *testing.T, fetch func() (string, int)) *SharedClient {
	t.Helper()
	return &SharedClient{client: &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
		body, status := fetch()
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
}

func TestDetailCacheReusesItemFor24HoursAndRefetchesAfterExpiry(t *testing.T) {
	body, err := os.ReadFile("testdata/bama-detail.html")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	client := detailFixtureClient(t, func() (string, int) { calls.Add(1); return string(body), 200 })
	rawURL := "https://bama.ir/car/detail-cache-ttl"
	key := string(vehicle.PlatformBama) + ":" + rawURL
	first, err := listingDetails(context.Background(), client, vehicle.PlatformBama, rawURL)
	if err != nil || first.DetailsStatus != "complete" {
		t.Fatalf("first detail: %+v %v", first, err)
	}
	detailCache.Lock()
	entry := detailCache.entries[key]
	detailCache.Unlock()
	remaining := time.Until(entry.expires)
	if remaining < 24*time.Hour-time.Minute || remaining > 24*time.Hour {
		t.Fatalf("wrong detail TTL: %v", remaining)
	}
	first.Attributes[0].Value = "changed by caller"
	again, err := listingDetails(context.Background(), client, vehicle.PlatformBama, "https://www.bama.ir/car/detail-cache-ttl?tracking=one#photos")
	if err != nil || calls.Load() != 1 || again.Attributes[0].Value == "changed by caller" {
		t.Fatalf("cache or ownership failed: calls=%d err=%v", calls.Load(), err)
	}
	detailCache.Lock()
	entry.expires = time.Now().Add(-time.Second)
	detailCache.entries[key] = entry
	detailCache.Unlock()
	if _, err := listingDetails(context.Background(), client, vehicle.PlatformBama, rawURL); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expired item not refreshed: %d", calls.Load())
	}
}

func TestInvalidAndFailedDetailsAreRetried(t *testing.T) {
	body, err := os.ReadFile("testdata/bama-detail.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []struct {
		name, body string
		status     int
	}{
		{"http", "unavailable", 403}, {"invalid", "<html>not a vehicle detail</html>", 200},
	} {
		t.Run(failure.name, func(t *testing.T) {
			var calls atomic.Int32
			client := detailFixtureClient(t, func() (string, int) {
				if calls.Add(1) == 1 {
					return failure.body, failure.status
				}
				return string(body), 200
			})
			rawURL := "https://bama.ir/car/detail-cache-failure-" + failure.name
			if _, err := listingDetails(context.Background(), client, vehicle.PlatformBama, rawURL); err == nil {
				t.Fatal("invalid detail accepted")
			}
			if _, err := listingDetails(context.Background(), client, vehicle.PlatformBama, rawURL); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 {
				t.Fatalf("failure cached: %d", calls.Load())
			}
		})
	}
}

func TestConcurrentDetailRequestsShareFetchAndPreserveListingIdentity(t *testing.T) {
	body, err := os.ReadFile("testdata/divar-detail.html")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	client := detailFixtureClient(t, func() (string, int) { calls.Add(1); return string(body), 200 })
	rows := make([]vehicle.NormalizedVehicleListing, 12)
	for i := range rows {
		rows[i] = vehicle.NormalizedVehicleListing{ID: "original", Title: "بنلی", CityID: "tehran", MileageKm: -1,
			Platform: vehicle.PlatformDivar, URL: "https://divar.ir/v/original/cache-concurrent"}
	}
	enrichListings(context.Background(), client, rows)
	for _, row := range rows {
		if row.DetailsStatus != "complete" || row.ID != "original" || row.CityID != "tehran" || row.Title != "بنلی" || row.MileageKm != 30000 {
			t.Fatalf("lost identity or facts: %+v", row)
		}
	}
	// A different source slug, city and query string still identify the same post token.
	next := []vehicle.NormalizedVehicleListing{{ID: "other", CityID: "karaj", MileageKm: -1, Platform: vehicle.PlatformDivar,
		URL: "https://divar.ir/v/another-slug/cache-concurrent?tracking=two"}}
	enrichListings(context.Background(), client, next)
	if calls.Load() != 1 || next[0].ID != "other" || next[0].CityID != "karaj" || next[0].DetailsStatus != "complete" {
		t.Fatalf("same item fetched again: calls=%d next=%+v", calls.Load(), next)
	}
}

func TestCanceledDetailWaiterDoesNotStartAnotherFetch(t *testing.T) {
	body, err := os.ReadFile("testdata/bama-detail.html")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	client := detailFixtureClient(t, func() (string, int) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return string(body), 200
	})
	rawURL := "https://bama.ir/car/detail-cache-cancel"
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := listingDetails(context.Background(), client, vehicle.PlatformBama, rawURL); err != nil {
			t.Error(err)
		}
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := listingDetails(ctx, client, vehicle.PlatformBama, rawURL); err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("extra request: %d", calls.Load())
	}
}

func TestLifecycleFactsRetainHTTPAbsenceStatus(t *testing.T) {
	for _, code := range []int{404, 410} {
		client := detailFixtureClient(t, func() (string, int) { return "missing", code })
		_, err := listingDetails(ports.WithCrawlPolicy(context.Background(), ports.CrawlPolicy{RefreshDetails: true}), client, vehicle.PlatformBama, "https://bama.ir/car/detail-removed-"+fmt.Sprint(code))
		var source *ports.SourceError
		if !errors.As(err, &source) || source.StatusCode != code {
			t.Fatalf("status lost: %v", err)
		}
	}
}
