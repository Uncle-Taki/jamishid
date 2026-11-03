package http

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
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
