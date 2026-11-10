package telemetry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	inbound "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/inbound/http"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/gin-gonic/gin"
)

func TestHTTPDiagnosticFallbackAndPanic(t *testing.T) {
	var logs bytes.Buffer
	o, err := New(&logs, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Shutdown(context.Background())
	router := inbound.SetupRouter(nil)
	router.GET("/panic/:id", func(c *gin.Context) { panic("secret-panic-value") })
	router.GET("/failed/:id", func(c *gin.Context) { c.Status(503) })
	for _, path := range []string{"/panic/secret-id?token=secret-token", "/failed/secret-id"} {
		rec := httptest.NewRecorder()
		o.HTTP(router).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	}
	if strings.Contains(logs.String(), "secret") {
		t.Fatal("values leaked", logs.String())
	}
	if strings.Count(logs.String(), `"event_kind":"error"`) != 2 {
		t.Fatal(logs.String())
	}
}

func TestLeafErrorNotMultipliedByHTTPFallback(t *testing.T) {
	var logs bytes.Buffer
	o, _ := New(&logs, io.Discard)
	defer o.Shutdown(context.Background())
	h := o.HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, finish := o.Start(r.Context(), "fetch", "divar")
		_ = ctx
		finish(ports.Observation{Error: errors.New("secret-value")})
		w.WriteHeader(502)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/any", nil))
	if strings.Count(logs.String(), `"event_kind":"error"`) != 1 {
		t.Fatal(logs.String())
	}
}
