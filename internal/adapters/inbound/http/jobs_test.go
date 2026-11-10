package http

import (
	"bytes"
	"context"
	"errors"
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
func (s *jobStore) SubmitStatus(_ context.Context, c vehicle.SearchCriteria, _ bool) (crawljobs.Job, error) {
	s.criteria = c
	return crawljobs.Job{ID: "status-only-job", Status: "queued"}, nil
}
func (s *jobStore) Status(_ context.Context, id string) (crawljobs.Job, error) {
	if id == "not-found" {
		return crawljobs.Job{}, crawljobs.ErrNotFound
	}
	return crawljobs.Job{ID: id, Status: "status-only"}, nil
}
func (s *jobStore) Search(_ context.Context, q crawljobs.InventoryQuery) (crawljobs.InventoryResult, error) {
	s.query = q
	return crawljobs.InventoryResult{Data: []vehicle.NormalizedVehicleListing{}, Total: 0}, nil
}
func (s *jobStore) Get(_ context.Context, id string) (crawljobs.Job, error) {
	if id == "not-found" {
		return crawljobs.Job{}, crawljobs.ErrNotFound
	}
	return crawljobs.Job{ID: id, Status: "running"}, nil
}
func (s *jobStore) Action(_ context.Context, id, action string, platform vehicle.Platform) error {
	if id == "not-found" {
		return crawljobs.ErrNotFound
	}
	return nil
}
func (s *jobStore) History(_ context.Context, platform vehicle.Platform, listingID string, offset int) (crawljobs.ListingHistory, error) {
	if listingID == "not-found" {
		return crawljobs.ListingHistory{}, crawljobs.ErrNotFound
	}
	return crawljobs.ListingHistory{Listing: vehicle.NormalizedVehicleListing{ID: listingID, Platform: platform}, Offset: offset}, nil
}
func (s *jobStore) Recheck(_ context.Context, platform vehicle.Platform, listingID string) error {
	if listingID == "not-found" {
		return crawljobs.ErrNotFound
	}
	return nil
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
		{"POST", "/api/v1/jobs?statusOnly=1", `{"query":"بنلی","type":"motorcycle","citySlug":"iran"}`, 202},
		{"POST", "/api/v1/jobs", `{"query":"","platform":"unknown"}`, 400},
		{"GET", "/api/v1/inventory?q=180&type=motorcycle&minPrice=100&limit=25&offset=25", "", 200},
		{"GET", "/api/v1/inventory?minPrice=NaN", "", 400},
		{"GET", "/api/v1/inventory?limit=101", "", 400},
		{"GET", "/api/v1/inventory?minYear=1200", "", 400},
		{"GET", "/api/v1/inventory?type=invalid", "", 400},
		{"POST", "/api/v1/jobs/id/actions", `{"action":"delete"}`, 400},
		{"POST", "/api/v1/jobs/persisted-job/actions", `{"action":"pause","platform":"divar"}`, 200},
		{"POST", "/api/v1/jobs/not-found/actions", `{"action":"pause"}`, 404},
		{"GET", "/api/v1/jobs/persisted-job", "", 200},
		{"GET", "/api/v1/jobs/persisted-job?statusOnly=1", "", 200},
		{"GET", "/api/v1/jobs/not-found", "", 404},
		{"GET", "/api/v1/inventory/divar/item1/history?offset=0", "", 200},
		{"GET", "/api/v1/inventory/unknown/item1/history", "", 400},
		{"GET", "/api/v1/inventory/divar/not-found/history", "", 404},
		{"POST", "/api/v1/inventory/divar/item1/recheck", "", 202},
		{"POST", "/api/v1/inventory/unknown/item1/recheck", "", 400},
		{"POST", "/api/v1/inventory/divar/not-found/recheck", "", 404},
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

	// Test nil jobs repository returns 503
	nilHandler := &AggregationHandler{}
	nilRouter := setupTestRouter(nilHandler)
	for _, tc := range []struct {
		method, path, body string
	}{
		{"POST", "/api/v1/jobs", `{"query":"test"}`},
		{"GET", "/api/v1/jobs/123", ""},
		{"POST", "/api/v1/jobs/123/actions", `{"action":"pause"}`},
		{"GET", "/api/v1/inventory?q=test", ""},
		{"GET", "/api/v1/inventory/divar/item1/history", ""},
		{"POST", "/api/v1/inventory/divar/item1/recheck", ""},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", "application/json")
		nilRouter.ServeHTTP(w, req)
		if w.Code != 503 {
			t.Fatalf("expected 503 for nil jobs on %s, got %d", tc.path, w.Code)
		}
	}
}

type errJobStore struct {
	crawljobs.Repository
}

func (s *errJobStore) Submit(_ context.Context, _ vehicle.SearchCriteria, _ bool) (crawljobs.Job, error) {
	return crawljobs.Job{}, errors.New("db error")
}
func (s *errJobStore) Search(_ context.Context, _ crawljobs.InventoryQuery) (crawljobs.InventoryResult, error) {
	return crawljobs.InventoryResult{}, errors.New("db error")
}
func (s *errJobStore) Action(_ context.Context, _, _ string, _ vehicle.Platform) error {
	return nil
}
func (s *errJobStore) Get(_ context.Context, _ string) (crawljobs.Job, error) {
	return crawljobs.Job{}, errors.New("db error")
}

func TestJobAndInventoryErrors(t *testing.T) {
	store := &errJobStore{}
	handler := &AggregationHandler{}
	handler.SetJobs(store)
	router := setupTestRouter(handler)

	// Submit error -> 503
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/jobs", bytes.NewBufferString(`{"query":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Errorf("expected 503 on Submit error, got %d", w.Code)
	}

	// Search error -> 503
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/v1/inventory?q=test", nil)
	router.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Errorf("expected 503 on Search error, got %d", w.Code)
	}

	// Action succeeds but Get fails -> 503
	w = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/v1/jobs/job-1/actions", bytes.NewBufferString(`{"action":"pause"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Errorf("expected 503 on Get error after action, got %d", w.Code)
	}
}
