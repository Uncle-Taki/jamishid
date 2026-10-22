package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
