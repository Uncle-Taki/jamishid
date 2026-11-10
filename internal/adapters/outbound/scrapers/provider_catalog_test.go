package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestProviderCatalogResolvesNativeHierarchyAndCaches(t *testing.T) {
	for _, platform := range []vehicle.Platform{vehicle.PlatformDivar, vehicle.PlatformSheypoor} {
		t.Run(string(platform), func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				parent := strings.TrimPrefix(r.URL.Path, "/s/tehran/motorcycles")
				if platform == vehicle.PlatformSheypoor {
					parent = strings.TrimPrefix(r.URL.Path, "/api/v10.0.0/search/filters/tehran/motorcycles")
				}
				next, label := "benelli", "بنلی"
				switch parent {
				case "/benelli":
					next, label = "benelli/tnt", "تی ان تی"
				case "/benelli/tnt":
					next, label = "benelli/tnt/180s", "180 اس"
				case "":
				default:
					t.Fatalf("unexpected category request %s", r.URL.Path)
				}
				if platform == vehicle.PlatformSheypoor {
					fmt.Fprintf(w, `{"meta":{"links":[{"url":"/s/tehran/motorcycles/%s","name":"%s"},{"url":"https://foreign.example/s/tehran/motorcycles/fake","name":"fake"}]}}`, next, label)
				} else {
					fmt.Fprintf(w, `<script>window.__PRELOADED_STATE__ = {"search":{"searchData":{"formData":{"category":{"str":{"value":"motorcycles"}}}}}};</script><a href="/s/tehran/motorcycles/%s">%s</a><a href="https://foreign.example/s/tehran/motorcycles/fake">fake</a><a href="/s/tehran/car/benelli">wrong vehicle</a>`, next, label)
				}
			}))
			defer srv.Close()
			catalog := NewProviderCatalog(NewSharedClient())
			catalog.Bases[platform] = srv.URL
			criteria := vehicle.SearchCriteria{Type: vehicle.VehicleTypeMotorcycle, BamaCategory: "benelli-tnt180s", Query: "بنلی TNT 180S"}
			for i := 0; i < 2; i++ {
				path, err := catalog.Resolve(context.Background(), platform, criteria)
				if err != nil || path != "benelli/tnt/180s" {
					t.Fatalf("wrong native category %q: %v", path, err)
				}
			}
			if requests != 3 {
				t.Fatalf("cache failed: requests=%d", requests)
			}
			rows, err := catalog.Categories(context.Background(), platform, criteria.Type, "")
			if err != nil || len(rows) != 1 {
				t.Fatalf("foreign or wrong-type categories accepted: %+v %v", rows, err)
			}
		})
	}
}

func TestProviderCatalogDoesNotInventMissingModelOrHideFailures(t *testing.T) {
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(503)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/motorcycles") {
			fmt.Fprint(w, `{"meta":{"links":[{"url":"/s/tehran/motorcycles/benelli","name":"بنلی"}]}}`)
		} else {
			fmt.Fprint(w, `{"meta":{"links":[]}}`)
		}
	}))
	defer srv.Close()
	catalog := NewProviderCatalog(NewSharedClient())
	catalog.Bases[vehicle.PlatformSheypoor] = srv.URL
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeMotorcycle, BamaCategory: "benelli-tnt180s", Query: "بنلی TNT 180S"}
	path, err := catalog.Resolve(context.Background(), vehicle.PlatformSheypoor, c)
	if err != nil || path != "benelli" {
		t.Fatalf("must scope to verified brand: %s %v", path, err)
	}
	for _, parent := range []string{"../bad", "benelli/../../bad", "//foreign", "benelli?x=y"} {
		if _, err := catalog.Categories(context.Background(), vehicle.PlatformSheypoor, c.Type, parent); err == nil {
			t.Errorf("invalid path accepted: %s", parent)
		}
	}
	fail = true
	catalog = NewProviderCatalog(NewSharedClient())
	catalog.Bases[vehicle.PlatformSheypoor] = srv.URL
	if _, err := catalog.Resolve(context.Background(), vehicle.PlatformSheypoor, c); err == nil {
		t.Fatal("catalog failure silently broadened search")
	}
}

func TestDivarEmptyAppShellIsNotCachedAsAnEmptyCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<script>window.__PRELOADED_STATE__ = {"search":{"searchData":{"formData":{}}}};</script>`)
	}))
	defer srv.Close()
	catalog := NewProviderCatalog(NewSharedClient())
	catalog.Bases[vehicle.PlatformDivar] = srv.URL
	if _, err := catalog.Categories(context.Background(), vehicle.PlatformDivar, vehicle.VehicleTypeMotorcycle, "benelli/tnt"); err == nil {
		t.Fatal("empty shell treated as a leaf category")
	}
	if len(catalog.cache) != 0 {
		t.Fatal("failed catalogue response cached")
	}
}

func TestNativeSheypoorContinuationKeepsModelPathAndKeywords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v10.0.0/search/tehran/motorcycles/benelli/tnt/180s" || r.URL.Query().Get("q") != "مشکی" || r.URL.Query().Get("p") != "2" || r.URL.Query().Get("f") != "provider-next" {
			t.Errorf("native model filters lost: %s", r.URL)
		}
		fmt.Fprint(w, `{"data":[{"id":"matching","attributes":{"title":"بنلی ۱۸۰ مشکی","url":"https://www.sheypoor.com/v/matching"}}],"meta":{}}`)
	}))
	defer srv.Close()
	c := vehicle.SearchCriteria{Type: vehicle.VehicleTypeMotorcycle, CitySlug: "tehran", BamaCategory: "benelli-tnt180s", Query: "بنلی TNT 180S مشکی", Keywords: "مشکی"}
	token := encodeCursor(vehicle.PlatformSheypoor, c, 2, sheypoorContinuation{Page: 2, Filter: "provider-next", Category: "benelli/tnt/180s"})
	page, err := PublicSearchPage(context.Background(), NewSharedClient(), srv.URL, vehicle.PlatformSheypoor, c, token)
	if err != nil || len(page.Listings) != 1 || page.NextCursor != "" {
		t.Fatalf("model continuation failed: %+v %v", page, err)
	}
}

func TestIncompleteSnapshotReusesOnlySuccessfulBranches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	snapshot := map[string]any{"updatedAt": time.Now(), "complete": false, "errors": map[string]string{"divar:motorcycle:benelli/tnt": "unavailable"}, "nodes": map[string][]vehicle.Category{
		"divar:motorcycle:":        {{ID: "benelli", Label: "بنلی", Type: vehicle.VehicleTypeMotorcycle}},
		"divar:motorcycle:benelli": {{ID: "benelli/tnt", Label: "تی ان تی", ParentID: "benelli", Type: vehicle.VehicleTypeMotorcycle}},
	}}
	body, _ := json.Marshal(snapshot)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	catalog := NewProviderCatalog(NewSharedClient())
	catalog.Bases[vehicle.PlatformDivar] = "http://127.0.0.1:1"
	if err := catalog.LoadSnapshot(path); err != nil {
		t.Fatal(err)
	}
	rows, err := catalog.Categories(context.Background(), vehicle.PlatformDivar, vehicle.VehicleTypeMotorcycle, "benelli")
	if err != nil || len(rows) != 1 || rows[0].ID != "benelli/tnt" {
		t.Fatalf("snapshot not reused: %+v %v", rows, err)
	}
	if _, err := catalog.Categories(context.Background(), vehicle.PlatformDivar, vehicle.VehicleTypeMotorcycle, "benelli/tnt"); err == nil {
		t.Fatal("missing branch incorrectly cached as empty")
	}
	snapshot["updatedAt"] = time.Now().Add(-25 * time.Hour)
	body, _ = json.Marshal(snapshot)
	_ = os.WriteFile(path, body, 0600)
	if err := NewProviderCatalog(NewSharedClient()).LoadSnapshot(path); err == nil {
		t.Fatal("stale snapshot accepted")
	}
}
