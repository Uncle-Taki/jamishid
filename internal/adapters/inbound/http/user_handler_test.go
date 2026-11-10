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

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/memory"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/tokens"
	userApp "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/user"
	domainUser "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/user"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/gin-gonic/gin"
)

func setupTestUserRouter(t *testing.T) (*gin.Engine, *UserHandler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	memStore := memory.NewUserStore()
	tm, err := tokens.NewHMACTokenManager([]byte("test-secret-key-0123456789abcdef"))
	if err != nil {
		t.Fatalf("failed to create token manager: %v", err)
	}

	userSvc := userApp.NewService(memStore, memStore, tm)
	userHandler := NewUserHandler(userSvc, tm)

	router := gin.New()
	ConfigureUserRoutes(router, userHandler)
	return router, userHandler
}

func TestUserHandler_AuthAndSavedListings(t *testing.T) {
	router, _ := setupTestUserRouter(t)

	// 1. Register
	regBody, _ := json.Marshal(AuthRequest{Identifier: "testuser", Password: "password123"})
	req := httptest.NewRequest("POST", "/api/v1/auth/register", bytes.NewReader(regBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on register, got %d: %s", w.Code, w.Body.String())
	}

	var authResp AuthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &authResp); err != nil {
		t.Fatalf("failed to parse auth response: %v", err)
	}
	if authResp.Token == "" || authResp.User.Identifier != "testuser" {
		t.Fatalf("unexpected auth response: %+v", authResp)
	}
	token := authResp.Token

	// 1b. Validation limits for identifier and password length
	longIdBody, _ := json.Marshal(AuthRequest{Identifier: strings.Repeat("x", 255), Password: "password123"})
	req = httptest.NewRequest("POST", "/api/v1/auth/register", bytes.NewReader(longIdBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for identifier > 254 chars, got %d", w.Code)
	}

	longPassBody, _ := json.Marshal(AuthRequest{Identifier: "validuser", Password: strings.Repeat("p", 73)})
	req = httptest.NewRequest("POST", "/api/v1/auth/register", bytes.NewReader(longPassBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for password > 72 chars, got %d", w.Code)
	}

	// 2. Duplicate Register
	req = httptest.NewRequest("POST", "/api/v1/auth/register", bytes.NewReader(regBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 Conflict for duplicate register, got %d", w.Code)
	}

	// 3. Login
	loginBody, _ := json.Marshal(AuthRequest{Identifier: "testuser", Password: "password123"})
	req = httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on login, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Me endpoint without auth
	req = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for /me without token, got %d", w.Code)
	}

	// 5. Me endpoint with auth
	req = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for /me with token, got %d", w.Code)
	}

	// 6. Save Listing
	saveBody, _ := json.Marshal(SaveListingRequest{
		Platform:  "divar",
		ListingID: "item-101",
		Listing:   json.RawMessage(`{"id":"item-101","platform":"divar","title":"پژو پارس","priceToman":400000000}`),
	})
	req = httptest.NewRequest("POST", "/api/v1/user/saved-listings", bytes.NewReader(saveBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on save listing, got %d: %s", w.Code, w.Body.String())
	}

	// 7. Get Saved Listing IDs
	req = httptest.NewRequest("GET", "/api/v1/user/saved-listing-ids", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on get saved listing IDs, got %d: %s", w.Code, w.Body.String())
	}
	var idsResp struct {
		SavedIDs []string `json:"savedIds"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &idsResp)
	if len(idsResp.SavedIDs) != 1 || idsResp.SavedIDs[0] != "divar:item-101" {
		t.Errorf("expected ['divar:item-101'], got %v", idsResp.SavedIDs)
	}

	// 8. Get Saved Listings (and test pagination clamping)
	req = httptest.NewRequest("GET", "/api/v1/user/saved-listings?limit=5000&offset=-1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on get saved listings, got %d: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Data   []map[string]any `json:"data"`
		Total  int              `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	if listResp.Total != 1 || len(listResp.Data) != 1 {
		t.Errorf("expected 1 saved listing, got total=%d, len=%d", listResp.Total, len(listResp.Data))
	}
	if listResp.Limit != 50 {
		t.Errorf("expected limit clamped to 50, got %d", listResp.Limit)
	}
	if listResp.Offset != 0 {
		t.Errorf("expected offset clamped to 0, got %d", listResp.Offset)
	}

	// 9. Remove Saved Listing
	req = httptest.NewRequest("DELETE", "/api/v1/user/saved-listings/divar/item-101", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on remove saved listing, got %d: %s", w.Code, w.Body.String())
	}

	// Verify removal
	req = httptest.NewRequest("GET", "/api/v1/user/saved-listing-ids", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	_ = json.Unmarshal(w.Body.Bytes(), &idsResp)
	if len(idsResp.SavedIDs) != 0 {
		t.Errorf("expected 0 saved IDs after removal, got %v", idsResp.SavedIDs)
	}

	// 10. Error cases for Register and Login
	// Bad JSON register
	req = httptest.NewRequest("POST", "/api/v1/auth/register", bytes.NewReader([]byte("{invalid-json")))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on bad json register, got %d", w.Code)
	}

	// Bad JSON login
	req = httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader([]byte("{invalid-json")))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on bad json login, got %d", w.Code)
	}

	// Invalid credentials login
	badLogin, _ := json.Marshal(AuthRequest{Identifier: "testuser", Password: "wrongpassword"})
	req = httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(badLogin))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 on bad credentials, got %d", w.Code)
	}

	// 11. AuthMiddleware errors
	// Missing header
	req = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 on missing auth header, got %d", w.Code)
	}

	// Invalid token format
	req = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer invalid.token.value")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 on invalid token, got %d", w.Code)
	}

	// 12. SaveListing error cases
	// Missing listing_id
	badSave, _ := json.Marshal(SaveListingRequest{Platform: "divar", ListingID: ""})
	req = httptest.NewRequest("POST", "/api/v1/user/saved-listings", bytes.NewReader(badSave))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on empty listing id save, got %d", w.Code)
	}

	// Empty platform on save
	badPlatSave, _ := json.Marshal(SaveListingRequest{Platform: "", ListingID: "123"})
	req = httptest.NewRequest("POST", "/api/v1/user/saved-listings", bytes.NewReader(badPlatSave))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on empty platform save, got %d", w.Code)
	}

	// 13. Additional Edge Cases
	// Malformed JSON on register
	req = httptest.NewRequest("POST", "/api/v1/auth/register", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on bad register json, got %d", w.Code)
	}

	// Password too short on register
	shortPass, _ := json.Marshal(AuthRequest{Identifier: "another_user", Password: "123"})
	req = httptest.NewRequest("POST", "/api/v1/auth/register", bytes.NewReader(shortPass))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on short password, got %d", w.Code)
	}

	// Duplicate user registration (409 Conflict)
	dupUser, _ := json.Marshal(AuthRequest{Identifier: "testuser", Password: "password123"})
	req = httptest.NewRequest("POST", "/api/v1/auth/register", bytes.NewReader(dupUser))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 on duplicate register, got %d", w.Code)
	}

	// Malformed JSON on login
	req = httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on bad login json, got %d", w.Code)
	}

	// GetSavedListings with limit > 100 and offset < 0
	req = httptest.NewRequest("GET", "/api/v1/user/saved-listings?limit=999&offset=-5", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 on clamped query params, got %d", w.Code)
	}

	// SaveListing with malformed json
	req = httptest.NewRequest("POST", "/api/v1/user/saved-listings", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on malformed json save, got %d", w.Code)
	}
}

type mockErrUserStore struct{}

func (m *mockErrUserStore) CreateUser(_ context.Context, _ *domainUser.User) error {
	return errors.New("db error")
}
func (m *mockErrUserStore) FindUserByID(_ context.Context, _ string) (*domainUser.User, error) {
	return nil, errors.New("db error")
}
func (m *mockErrUserStore) FindUserByIdentifier(_ context.Context, _ string) (*domainUser.User, error) {
	return nil, errors.New("db error")
}
func (m *mockErrUserStore) GetSavedListings(_ context.Context, _ string, _, _ int) ([]vehicle.NormalizedVehicleListing, int, error) {
	return nil, 0, errors.New("db error")
}
func (m *mockErrUserStore) GetSavedListingIDs(_ context.Context, _ string) (map[string]bool, error) {
	return nil, errors.New("db error")
}
func (m *mockErrUserStore) SaveListing(_ context.Context, _ string, _ vehicle.Platform, _ string, _ []byte) error {
	return errors.New("db error")
}
func (m *mockErrUserStore) RemoveSavedListing(_ context.Context, _ string, _ vehicle.Platform, _ string) error {
	return errors.New("db error")
}

func TestUserHandler_ServiceErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tm, _ := tokens.NewHMACTokenManager([]byte("test-secret-key-0123456789abcdef"))
	token, _ := tm.GenerateToken("user-1", 10*time.Minute)

	errStore := &mockErrUserStore{}
	userSvc := userApp.NewService(errStore, errStore, tm)
	handler := NewUserHandler(userSvc, tm)
	router := gin.New()
	ConfigureUserRoutes(router, handler)

	// Me error -> 404
	req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 on Me error, got %d", w.Code)
	}

	// GetSavedListings error -> 500
	req = httptest.NewRequest("GET", "/api/v1/user/saved-listings", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on GetSavedListings error, got %d", w.Code)
	}

	// GetSavedListingIDs error -> 500
	req = httptest.NewRequest("GET", "/api/v1/user/saved-listing-ids", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on GetSavedListingIDs error, got %d", w.Code)
	}

	// SaveListing error -> 500
	saveBody := `{"platform":"divar","listingId":"123"}`
	req = httptest.NewRequest("POST", "/api/v1/user/saved-listings", strings.NewReader(saveBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on SaveListing error, got %d", w.Code)
	}

	// RemoveSavedListing error -> 500
	req = httptest.NewRequest("DELETE", "/api/v1/user/saved-listings/divar/123", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on RemoveSavedListing error, got %d", w.Code)
	}
}
