package telemetry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var bounds = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 15, 30, 60}

type series struct {
	labels          string
	count, listings uint64
	sum             float64
	buckets         []uint64
}
type Observer struct {
	mu       sync.Mutex
	series   map[string]*series
	tracer   trace.Tracer
	provider *sdktrace.TracerProvider
	logger   *slog.Logger
}

// JSON span output can be redirected to a file alongside the structured event log.
func New(logs io.Writer, traces io.Writer) (*Observer, error) {
	exporter, err := stdouttrace.New(stdouttrace.WithWriter(traces))
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "jamshid-backend"))))
	return &Observer{series: map[string]*series{}, tracer: provider.Tracer("jamshid"), provider: provider, logger: slog.New(slog.NewJSONHandler(logs, nil))}, nil
}
func (o *Observer) Shutdown(ctx context.Context) error { return o.provider.Shutdown(ctx) }

func (o *Observer) Start(ctx context.Context, operation string, platform vehicle.Platform) (context.Context, func(ports.Observation)) {
	start := time.Now()
	ctx, span := o.tracer.Start(ctx, operation, trace.WithAttributes(attribute.String("source", string(platform))))
	var once sync.Once
	return ctx, func(event ports.Observation) {
		once.Do(func() {
			defer span.End()
			outcome := event.Outcome
			if outcome == "" {
				outcome = "ok"
				if event.Error != nil || event.StatusCode >= 400 {
					outcome = "error"
				}
			}
			failure := ports.FailureKind(event.Error)
			attrs := []attribute.KeyValue{attribute.String("outcome", outcome), attribute.String("failure", failure), attribute.String("cache", event.Cache), attribute.Int("http.status_code", event.StatusCode), attribute.Int("listings", event.Listings)}
			span.SetAttributes(attrs...)
			if event.Error != nil || event.StatusCode >= 400 {
				span.SetStatus(codes.Error, failure)
			}
			elapsed := time.Since(start).Seconds()
			// Labels contain only controlled categories: no query, item URL, cursor or ID.
			labels := fmt.Sprintf("operation=%q,source=%q,outcome=%q,failure=%q,cache=%q,status=%q", operation, platform, outcome, failure, event.Cache, fmt.Sprint(event.StatusCode))
			o.mu.Lock()
			s := o.series[labels]
			if s == nil {
				s = &series{labels: labels, buckets: make([]uint64, len(bounds))}
				o.series[labels] = s
			}
			s.count++
			s.sum += elapsed
			s.listings += uint64(max(0, event.Listings))
			for i, b := range bounds {
				if elapsed <= b {
					s.buckets[i]++
				}
			}
			o.mu.Unlock()
			o.logger.Info("operation completed", "operation", operation, "source", platform, "outcome", outcome, "failure", failure, "cache", event.Cache, "status", event.StatusCode, "duration_seconds", elapsed, "listings", event.Listings, "trace_id", span.SpanContext().TraceID().String())
		})
	}
}

func (o *Observer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	o.mu.Lock()
	defer o.mu.Unlock()
	io.WriteString(w, "# HELP jamshid_operation_duration_seconds Operation duration by source and cache state.\n# TYPE jamshid_operation_duration_seconds histogram\n# HELP jamshid_valid_listings_total Valid listings returned by operation.\n# TYPE jamshid_valid_listings_total counter\n")
	keys := make([]string, 0, len(o.series))
	for key := range o.series {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		s := o.series[key]
		for i, b := range bounds {
			fmt.Fprintf(w, "jamshid_operation_duration_seconds_bucket{%s,le=%q} %d\n", s.labels, fmt.Sprint(b), s.buckets[i])
		}
		fmt.Fprintf(w, "jamshid_operation_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", s.labels, s.count)
		fmt.Fprintf(w, "jamshid_operation_duration_seconds_sum{%s} %g\njamshid_operation_duration_seconds_count{%s} %d\njamshid_valid_listings_total{%s} %d\n", s.labels, s.sum, s.labels, s.count, s.labels, s.listings)
	}
}

func (o *Observer) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := r.URL.Path
		switch route {
		case "/api/v1/crawl", "/api/v1/listing", "/api/v1/categories", "/api/v1/health":
		default:
			route = "other"
		}
		ctx, finish := o.Start(r.Context(), "http"+strings.ReplaceAll(route, "/", "_"), "")
		recorder := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(recorder, r.WithContext(ctx))
		finish(ports.Observation{StatusCode: recorder.status})
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
