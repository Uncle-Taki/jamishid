package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/aggregation"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/gin-gonic/gin"
)

type mockAggregationService struct {
	listings []vehicle.ScoredListing
	err      error
}

func (m *mockAggregationService) AggregateListings(ctx context.Context, criteria vehicle.SearchCriteria) ([]vehicle.ScoredListing, error) {
	return m.listings, m.err
}

func setupTestRouter(handler *AggregationHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	return SetupRouter(handler)
}

func TestHealth(t *testing.T) {
	router := setupTestRouter(&AggregationHandler{})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/health", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

func TestCrawl_OptionsCORS(t *testing.T) {
	router := setupTestRouter(&AggregationHandler{})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("OPTIONS", "/api/v1/crawl", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status %d, got %d", http.StatusNoContent, w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected CORS origin *")
	}
}

func TestCrawl_BadRequest(t *testing.T) {
	router := setupTestRouter(&AggregationHandler{})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/crawl", bytes.NewBuffer([]byte(`{invalid}`)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestCrawl_IntegrationSuccess(t *testing.T) {
	svc := &mockAggregationService{
		listings: []vehicle.ScoredListing{
			{
				NormalizedVehicleListing: vehicle.NormalizedVehicleListing{
					ID:         "1",
					Platform:   vehicle.PlatformDivar,
					Title:      "Test Car",
					PriceToman: 500,
					MileageKm:  10000,
					Year:       1400,
					BodyStatus: vehicle.BodyStatusPerfect,
					ScrapedAt:  time.Now().Format(time.RFC3339),
				},
				Score: vehicle.ListingScoreBreakdown{
					TotalScore: 90,
				},
			},
		},
	}
	handler := NewAggregationHandler(svc)
	router := setupTestRouter(handler)

	reqBody := `{"query": "Test", "weights": {"price": 50, "condition": 50}}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/crawl", bytes.NewBuffer([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp CrawlResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if !resp.Success {
		t.Errorf("expected success true")
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 result")
	}
	if resp.Data[0].ID != "1" {
		t.Errorf("expected listing ID 1")
	}
}

func TestCrawl_InternalServerError(t *testing.T) {
	svc := &mockAggregationService{
		err: errors.New("hard error"),
	}
	handler := NewAggregationHandler(svc)
	router := setupTestRouter(handler)

	reqBody := `{"query": "Test"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/crawl", bytes.NewBuffer([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

type reportingService struct{ mockAggregationService }

func (s *reportingService) AggregateWithSources(context.Context, vehicle.SearchCriteria) (vehicle.CrawlResult, error) {
	return vehicle.CrawlResult{Sources: []vehicle.SourceStatus{{Platform: vehicle.PlatformDivar, Status: "unavailable", Error: "blocked"}}}, aggregation.ErrSourcesUnavailable
}

func TestCrawl_UpstreamFailureIs502WithNoSyntheticData(t *testing.T) {
	router := setupTestRouter(NewAggregationHandler(&reportingService{}))
	req := httptest.NewRequest("POST", "/api/v1/crawl", bytes.NewBufferString(`{"query":"206"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var response CrawlResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 502 || response.Success || response.Data == nil || len(response.Data) != 0 || len(response.Sources) != 1 {
		t.Fatalf("status=%d response=%+v", w.Code, response)
	}
}

func TestCrawl_EmptySearchReturnsArray(t *testing.T) {
	router := setupTestRouter(NewAggregationHandler(&mockAggregationService{}))
	req := httptest.NewRequest("POST", "/api/v1/crawl", bytes.NewBufferString(`{"query":"206"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var response CrawlResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !response.Success || response.Data == nil || len(response.Data) != 0 {
		t.Fatalf("status=%d response=%+v", w.Code, response)
	}
}

type mockCategoryCatalog struct{}

func (m *mockCategoryCatalog) Categories(_ context.Context, _ vehicle.VehicleType, _ string) ([]vehicle.Category, error) {
	return []vehicle.Category{{ID: "cat1", Label: "Category 1"}}, nil
}

type mockProviderCatalog struct{}

func (m *mockProviderCatalog) Categories(_ context.Context, _ vehicle.Platform, _ vehicle.VehicleType, _ string) ([]vehicle.Category, error) {
	return []vehicle.Category{{ID: "pcat1", Label: "Provider Cat 1"}}, nil
}

func TestListingAndCategoriesEndpoints(t *testing.T) {
	handler := NewAggregationHandler(&mockAggregationService{})
	handler.SetCategoryCatalog(&mockCategoryCatalog{})
	handler.SetProviderCatalog(&mockProviderCatalog{})
	handler.SetListingFetcher(func(ctx context.Context, p vehicle.Platform, u string) (vehicle.NormalizedVehicleListing, error) {
		if u == "https://divar.ir/fail" {
			return vehicle.NormalizedVehicleListing{}, errors.New("upstream failure")
		}
		return vehicle.NormalizedVehicleListing{ID: "123", Platform: p, URL: u}, nil
	})
	router := setupTestRouter(handler)

	// Listing success
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/listing?platform=divar&url=https://divar.ir/v/123", nil)
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Listing invalid platform
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/listing?platform=invalid&url=https://divar.ir/v/123", nil)
	router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for invalid platform, got %d", w.Code)
	}

	// Listing fetch failure
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/listing?platform=divar&url=https://divar.ir/fail", nil)
	router.ServeHTTP(w, req)
	if w.Code != 502 {
		t.Errorf("expected 502 for fetch failure, got %d", w.Code)
	}

	// Categories general
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/categories?type=car", nil)
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 for categories, got %d", w.Code)
	}

	// Categories provider source
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/categories?type=car&source=bama", nil)
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 for provider categories, got %d", w.Code)
	}

	// Categories invalid source
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/categories?type=car&source=invalid", nil)
	router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for invalid source, got %d", w.Code)
	}

	// Listing URL too long > 2048
	w = httptest.NewRecorder()
	longURL := "https://divar.ir/" + strings.Repeat("a", 2040)
	req, _ = http.NewRequest("GET", "/api/v1/listing?platform=divar&url="+longURL, nil)
	router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for long URL, got %d", w.Code)
	}

	// Categories invalid type
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/categories?type=truck", nil)
	router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for invalid category type, got %d", w.Code)
	}

	// Categories invalid parent regex
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/categories?type=car&parent=invalid!@#$", nil)
	router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for invalid parent, got %d", w.Code)
	}

	// Categories provider with invalid parent regex
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/categories?type=car&source=bama&parent=invalid!@#$", nil)
	router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for invalid provider parent, got %d", w.Code)
	}
}

type errCategoryCatalog struct{}

func (m *errCategoryCatalog) Categories(_ context.Context, _ vehicle.VehicleType, _ string) ([]vehicle.Category, error) {
	return nil, errors.New("catalog db down")
}

type errProviderCatalog struct{}

func (m *errProviderCatalog) Categories(_ context.Context, _ vehicle.Platform, _ vehicle.VehicleType, _ string) ([]vehicle.Category, error) {
	return nil, errors.New("provider db down")
}

func TestCategories_CatalogErrors(t *testing.T) {
	handler := NewAggregationHandler(&mockAggregationService{})
	handler.SetCategoryCatalog(&errCategoryCatalog{})
	handler.SetProviderCatalog(&errProviderCatalog{})
	router := setupTestRouter(handler)

	// General categories error -> 502
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/categories?type=car", nil)
	router.ServeHTTP(w, req)
	if w.Code != 502 {
		t.Errorf("expected 502 on category catalog error, got %d", w.Code)
	}

	// Provider categories error -> 502
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/categories?type=car&source=bama", nil)
	router.ServeHTTP(w, req)
	if w.Code != 502 {
		t.Errorf("expected 502 on provider catalog error, got %d", w.Code)
	}
}

func TestCrawl_ValidationEdges(t *testing.T) {
	handler := NewAggregationHandler(&mockAggregationService{})
	router := setupTestRouter(handler)

	cases := []struct {
		name string
		body string
	}{
		{"keywords too long", `{"query":"206","keywords":"` + strings.Repeat("x", 201) + `"}`},
		{"empty query", `{"query":"   "}`},
		{"query too long", `{"query":"` + strings.Repeat("x", 201) + `"}`},
		{"invalid type", `{"query":"206","type":"boat"}`},
		{"invalid platform", `{"query":"206","platform":"unknown"}`},
		{"invalid cutoff hours", `{"query":"206","dateCutoffHours":-1}`},
		{"negative score weight", `{"query":"206","weights":{"price":-5}}`},
		{"zero total weights", `{"query":"206","weights":{"price":0,"condition":0,"mileage":0,"year":0}}`},
		{"invalid bamaCategory regex", `{"query":"206","bamaCategory":"bad!category"}`},
		{"invalid citySlug regex", `{"query":"206","citySlug":"bad_city!"}`},
		{"too many cursors", `{"query":"206","cursors":{"divar":"1","bama":"2","sheypoor":"3","extra":"4"}}`},
		{"invalid cursor platform", `{"query":"206","cursors":{"unknown":"1"}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/v1/crawl", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			if w.Code != 400 {
				t.Errorf("%s: expected 400, got %d", tc.name, w.Code)
			}
		})
	}
}
