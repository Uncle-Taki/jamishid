package telemetry

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/gin-gonic/gin"
)

func newTestObserver(t *testing.T) *Observer {
	t.Helper()
	observer, err := New(&bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Shutdown(context.Background()) })
	return observer
}

func scrape(t *testing.T, observer *Observer) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	observer.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 200 {
		t.Fatalf("metrics status %d", recorder.Code)
	}
	return recorder.Body.String()
}

func countSeries(t *testing.T, body, prefix string) int {
	t.Helper()
	count := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

// testRouter mirrors the production router's middleware order: route capture
// first, then the CORS middleware that aborts OPTIONS preflights with 204.
func testRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.HandleMethodNotAllowed = true
	r.Use(func(c *gin.Context) {
		if capture := ports.RouteCaptureFrom(c.Request.Context()); capture != nil {
			capture.Template = c.FullPath()
		}
		c.Next()
	})
	r.Use(func(c *gin.Context) {
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})
	r.GET("/api/v1/health", func(c *gin.Context) { c.Status(200) })
	r.GET("/jobs/:id", func(c *gin.Context) { c.Status(200) })
	r.POST("/jobs/:id/actions", func(c *gin.Context) { c.Status(202) })
	r.GET("/inventory/:platform/:listingID/history", func(c *gin.Context) { c.Status(200) })
	r.GET("/admin/proxies", func(c *gin.Context) { c.Status(401) })
	return r
}

// TestHTTPRouteNormalization proves route templates collapse parameterized
// paths, that OPTIONS/404/405 stay bounded, and that no identifiers leak.
func TestHTTPRouteNormalization(t *testing.T) {
	observer := newTestObserver(t)
	server := httptest.NewServer(observer.HTTP(testRouter()))
	defer server.Close()

	requests := []struct {
		method, path string
	}{
		{"GET", "/api/v1/health"},
		{"GET", "/jobs/abc123"},
		{"GET", "/jobs/def456"},
		{"GET", "/jobs/xyz789/sub"},
		{"POST", "/jobs/abc123/actions"},
		{"OPTIONS", "/jobs/abc123"},
		{"GET", "/nope"},
		{"DELETE", "/jobs/abc123"}, // method not allowed
		{"GET", "/inventory/divar/1/history"},
		{"GET", "/inventory/bama/2/history"},
		{"GET", "/admin/proxies"},
		{"BREW", "/api/v1/health"}, // arbitrary method token: bounded label
	}
	for _, request := range requests {
		req, err := http.NewRequest(request.method, server.URL+request.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	body := scrape(t, observer)
	for _, expected := range []string{
		`jamshid_http_requests_total{method="GET",route="/api/v1/health",status_class="2xx"} 1`,
		`jamshid_http_requests_total{method="GET",route="/jobs/:id",status_class="2xx"} 2`,
		`jamshid_http_requests_total{method="POST",route="/jobs/:id/actions",status_class="2xx"} 1`,
		`jamshid_http_requests_total{method="OPTIONS",route="preflight",status_class="2xx"} 1`,
		`jamshid_http_requests_total{method="GET",route="unmatched",status_class="4xx"} 2`,
		`jamshid_http_requests_total{method="DELETE",route="method_not_allowed",status_class="4xx"} 1`,
		`jamshid_http_requests_total{method="GET",route="/inventory/:platform/:listingID/history",status_class="2xx"} 2`,
		`jamshid_http_requests_total{method="GET",route="/admin/proxies",status_class="4xx"} 1`,
		// The arbitrary BREW method is normalized to the bounded "other" bucket;
		// Gin classifies it as method-not-allowed on a valid route.
		`jamshid_http_requests_total{method="other",route="method_not_allowed",status_class="4xx"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected series %q in:\n%s", expected, body)
		}
	}
	// 12 requests produce 9 series: the two /jobs/:id requests collapse into
	// one series, /jobs/xyz789/sub shares the unmatched bucket with /nope, and
	// the arbitrary BREW method collapses into the bounded "other" bucket.
	if got := countSeries(t, body, "jamshid_http_requests_total{"); got != 9 {
		t.Fatalf("route series count %d", got)
	}
	if !strings.Contains(body, "jamshid_http_requests_in_flight 0") {
		t.Fatal("in-flight gauge did not drain")
	}
	if !strings.Contains(body, `jamshid_http_request_duration_seconds_bucket{method="GET",route="/jobs/:id",le="+Inf"}`) {
		t.Fatal("duration histogram missing normalized route")
	}
	for _, secret := range []string{"abc123", "def456", "xyz789", "divar/1"} {
		if strings.Contains(body, secret) {
			t.Fatalf("identifier %q leaked into metrics", secret)
		}
	}
}

// TestHTTPWrapperPreservesLegacySeries keeps the original telemetry contract
// intact alongside the new counters.
func TestHTTPWrapperPreservesLegacySeries(t *testing.T) {
	observer := newTestObserver(t)
	server := httptest.NewServer(observer.HTTP(testRouter()))
	defer server.Close()
	resp, err := http.Get(server.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	body := scrape(t, observer)
	if !strings.Contains(body, `jamshid_operation_duration_seconds_count{cache="",failure="none",operation="http_api_v1_health",outcome="ok",source="",status="200"} 1`) {
		t.Fatalf("legacy operation series missing:\n%s", body)
	}
}

// TestMetricsConcurrentScrapeAndObserve races observations, task starts and
// metric scrapes against each other.
func TestMetricsConcurrentScrapeAndObserve(t *testing.T) {
	observer := newTestObserver(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				finish := observer.TaskStart("page", "divar")
				finish("success", "none")
				observer.LoopTick("page", "1")
				observer.ClaimError("page")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 20; j++ {
			scrape(t, observer)
		}
	}()
	wg.Wait()
	body := scrape(t, observer)
	if !strings.Contains(body, `jamshid_worker_task_attempts_total{failure="none",outcome="success",pool="page",source="divar"} 200`) {
		t.Fatalf("attempt counter wrong:\n%s", body)
	}
	if !strings.Contains(body, "jamshid_worker_active{pool=\"page\"} 0") {
		t.Fatal("active gauge did not drain")
	}
}
