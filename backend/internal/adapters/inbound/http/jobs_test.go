package http

import (
	"bytes"
	"context"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"net/http/httptest"
	"testing"
)

type jobStore struct {
	crawljobs.Repository
	criteria vehicle.SearchCriteria
	query    crawljobs.InventoryQuery
}

func (s *jobStore) Submit(_ context.Context, c vehicle.SearchCriteria, _ bool) (crawljobs.Job, error) {
	s.criteria = c
	return crawljobs.Job{ID: "persisted-job", Status: "queued", Sources: []vehicle.SourceStatus{}, Data: []vehicle.NormalizedVehicleListing{}}, nil
}
func (s *jobStore) Search(_ context.Context, q crawljobs.InventoryQuery) (crawljobs.InventoryResult, error) {
	s.query = q
	return crawljobs.InventoryResult{Data: []vehicle.NormalizedVehicleListing{}, Total: 0}, nil
}
func TestJobAndInventoryContracts(t *testing.T) {
	store := &jobStore{}
	handler := &AggregationHandler{}
	handler.SetJobs(store)
	router := setupTestRouter(handler)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/jobs", `{"query":"بنلی","type":"motorcycle","citySlug":"iran"}`, 202},
		{"POST", "/api/v1/jobs", `{"query":"","platform":"unknown"}`, 400},
		{"GET", "/api/v1/inventory?q=180&type=motorcycle&minPrice=100&limit=25&offset=25", "", 200},
		{"GET", "/api/v1/inventory?minPrice=NaN", "", 400},
		{"GET", "/api/v1/inventory?limit=101", "", 400},
		{"POST", "/api/v1/jobs/id/actions", `{"action":"delete"}`, 400},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	if store.criteria.CitySlug != "iran" || store.criteria.Type != vehicle.VehicleTypeMotorcycle {
		t.Fatal("crawl scope lost")
	}
	if store.query.Limit != 25 || store.query.Offset != 25 || store.query.MinPrice == nil || *store.query.MinPrice != 100 {
		t.Fatal("inventory filters lost")
	}
}
