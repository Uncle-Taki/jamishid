package http

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/postgres"
	"github.com/gin-gonic/gin"
)

func TestProxyAdministrationRequiresConfiguredToken(t *testing.T) {
	for _, token := range []string{"", "admin-secret"} {
		router := gin.New()
		ConfigureOperations(router, nil, token)
		for _, authorization := range []string{"", "Bearer wrong"} {
			request := httptest.NewRequest("PUT", "/api/v1/admin/proxies/primary", strings.NewReader(`{"endpoint":"http://host:3128"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", authorization)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			want := 401
			if token == "" {
				want = 404
			}
			if response.Code != want {
				t.Fatalf("token configured=%t: %d", token != "", response.Code)
			}
		}
	}
	router := gin.New()
	ConfigureOperations(router, nil, "admin-secret")
	request := httptest.NewRequest("PUT", "/api/v1/admin/proxies/primary", strings.NewReader(`{"endpoint":"file:///tmp/proxy"}`))
	request.Header.Set("Authorization", "Bearer admin-secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 400 {
		t.Fatalf("invalid endpoint: %d", response.Code)
	}
}

type mockOpStore struct{}

func (m *mockOpStore) Coverage(ctx context.Context) ([]postgres.CoverageStatus, error) {
	return []postgres.CoverageStatus{{Platform: "divar", Partitions: 10}}, nil
}
func (m *mockOpStore) Proxies(ctx context.Context) ([]postgres.EgressProxy, error) {
	return []postgres.EgressProxy{{ID: "proxy-1", Address: "http://127.0.0.1:8080"}}, nil
}
func (m *mockOpStore) PutProxy(ctx context.Context, proxyID, endpoint string, enabled bool) (string, error) {
	return proxyID, nil
}
func (m *mockOpStore) DeleteProxy(ctx context.Context, proxyID string) error {
	return nil
}

func TestOperations_FullLifecycle(t *testing.T) {
	store := &mockOpStore{}
	router := gin.New()
	ConfigureOperations(router, store, "admin-token")

	// 1. Coverage
	req := httptest.NewRequest("GET", "/api/v1/coverage", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 on coverage, got %d", w.Code)
	}

	// 2. Proxies GET
	req = httptest.NewRequest("GET", "/api/v1/admin/proxies", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 on admin proxies, got %d", w.Code)
	}

	// 3. Put Proxy valid
	req = httptest.NewRequest("PUT", "/api/v1/admin/proxies/proxy-1", strings.NewReader(`{"endpoint":"http://10.0.0.1:8080"}`))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 on put proxy, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Delete Proxy
	req = httptest.NewRequest("DELETE", "/api/v1/admin/proxies/proxy-1", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Errorf("expected 204 on delete proxy, got %d", w.Code)
	}

	// 5. Coverage with nil store returns 503
	nilRouter := gin.New()
	ConfigureOperations(nilRouter, nil, "admin-token")
	req = httptest.NewRequest("GET", "/api/v1/coverage", nil)
	w = httptest.NewRecorder()
	nilRouter.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Errorf("expected 503 on coverage with nil store, got %d", w.Code)
	}

	// 6. Invalid proxyID characters
	req = httptest.NewRequest("PUT", "/api/v1/admin/proxies/invalid!proxy@id", strings.NewReader(`{"endpoint":"http://10.0.0.1:8080"}`))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for invalid proxy ID, got %d", w.Code)
	}

	// 7. Put proxy with enabled explicitly false
	req = httptest.NewRequest("PUT", "/api/v1/admin/proxies/proxy-disabled", strings.NewReader(`{"endpoint":"http://10.0.0.1:8080","enabled":false}`))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 for put proxy with enabled=false, got %d", w.Code)
	}
}

type errOpStore struct{}

func (m *errOpStore) Coverage(ctx context.Context) ([]postgres.CoverageStatus, error) {
	return nil, errors.New("db error")
}
func (m *errOpStore) Proxies(ctx context.Context) ([]postgres.EgressProxy, error) {
	return nil, errors.New("db error")
}
func (m *errOpStore) PutProxy(ctx context.Context, proxyID, endpoint string, enabled bool) (string, error) {
	return "", errors.New("db error")
}
func (m *errOpStore) DeleteProxy(ctx context.Context, proxyID string) error {
	return errors.New("db error")
}

func TestOperations_Errors(t *testing.T) {
	router := gin.New()
	ConfigureOperations(router, &errOpStore{}, "admin-token")

	// Coverage error
	req := httptest.NewRequest("GET", "/api/v1/coverage", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Errorf("expected 503 on coverage error, got %d", w.Code)
	}

	// Proxies GET error
	req = httptest.NewRequest("GET", "/api/v1/admin/proxies", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Errorf("expected 503 on proxies error, got %d", w.Code)
	}

	// Put Proxy error
	req = httptest.NewRequest("PUT", "/api/v1/admin/proxies/proxy-1", strings.NewReader(`{"endpoint":"http://10.0.0.1:8080"}`))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Errorf("expected 503 on put proxy error, got %d", w.Code)
	}

	// Delete Proxy error
	req = httptest.NewRequest("DELETE", "/api/v1/admin/proxies/proxy-1", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Errorf("expected 503 on delete proxy error, got %d", w.Code)
	}
}
