package telemetry

import (
	"bytes"
	"context"
	"errors"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsLogsAndTraceContainSourceAndBoundedCategories(t *testing.T) {
	var logs, traces bytes.Buffer
	observer, err := New(&logs, &traces)
	if err != nil {
		t.Fatal(err)
	}
	ctx, finish := observer.Start(context.Background(), "source", vehicle.PlatformBama)
	_, child := observer.Start(ctx, "fetch", vehicle.PlatformBama)
	child(ports.Observation{StatusCode: 503, Error: &ports.SourceError{Kind: "http", StatusCode: 503, Err: errors.New("secret-url-query")}})
	finish(ports.Observation{Cache: "hit", Listings: 9})
	finish(ports.Observation{Listings: 100})
	recorder := httptest.NewRecorder()
	observer.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if err := observer.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{recorder.Body.String(), logs.String(), traces.String()} {
		if strings.Contains(text, "secret-url-query") {
			t.Fatal("raw error leaks URL data")
		}
		if !strings.Contains(text, "bama") {
			t.Fatal("source absent")
		}
	}
	if !strings.Contains(recorder.Body.String(), "jamshid_operation_duration_seconds_bucket") || !strings.Contains(recorder.Body.String(), "cache=\"hit\"") {
		t.Fatal("histogram or cache label missing")
	}
	if !strings.Contains(traces.String(), "Parent") || !strings.Contains(logs.String(), "trace_id") {
		t.Fatal("tracing correlation missing")
	}
}
