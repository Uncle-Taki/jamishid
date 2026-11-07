package telemetry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/errorlog"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Histogram buckets shared by operation and HTTP duration series.
var bounds = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 15, 30, 60}

// operationLabels preserves the legacy exposition contract exactly:
// jamshid_operation_duration_seconds{operation,source,outcome,failure,cache,status}.
var operationLabels = []string{"operation", "source", "outcome", "failure", "cache", "status"}

type Observer struct {
	tracer      trace.Tracer
	provider    *sdktrace.TracerProvider
	logger      *slog.Logger
	diagnostics *errorlog.Reporter

	registry *prometheus.Registry

	operations *prometheus.HistogramVec
	listings   *prometheus.CounterVec

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	httpInFlight prometheus.Gauge

	workerCapacity    *prometheus.GaugeVec
	workerActive      *prometheus.GaugeVec
	workerAttempts    *prometheus.CounterVec
	workerDuration    *prometheus.HistogramVec
	workerClaimErrors *prometheus.CounterVec
	workerLoopTick    *prometheus.GaugeVec
	workerCompleted   *prometheus.GaugeVec
	schedulerAttempt  *prometheus.GaugeVec
	schedulerSuccess  *prometheus.GaugeVec
	schedulerErrors   *prometheus.CounterVec

	retryTickets         *prometheus.CounterVec
	inventoryCache       *prometheus.CounterVec
	inventoryCacheWrites *prometheus.CounterVec
	inventoryQueries     prometheus.Counter
	postgresPoolConns    *prometheus.GaugeVec
	postgresPoolMax      prometheus.Gauge
	snapshotCollector    *snapshotCollector
}

// JSON span output can be redirected to a file alongside the structured event log.
func New(logs io.Writer, traces io.Writer) (*Observer, error) {
	exporter, err := stdouttrace.New(stdouttrace.WithWriter(traces))
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "jamshid-backend"))))
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	o := &Observer{
		tracer:      provider.Tracer("jamshid"),
		provider:    provider,
		logger:      slog.New(slog.NewJSONHandler(logs, nil)),
		diagnostics: errorlog.New(logs),
		registry:    registry,
	}
	o.operations = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "jamshid_operation_duration_seconds",
		Help:    "Operation duration by source and cache state.",
		Buckets: bounds,
	}, operationLabels)
	o.listings = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "jamshid_valid_listings_total",
		Help: "Valid listings returned by operation.",
	}, operationLabels)
	o.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "jamshid_http_requests_total",
		Help: "HTTP requests by normalized route template, method and status class.",
	}, []string{"route", "method", "status_class"})
	o.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "jamshid_http_request_duration_seconds",
		Help:    "HTTP request duration by normalized route template and method.",
		Buckets: bounds,
	}, []string{"route", "method"})
	o.httpInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "jamshid_http_requests_in_flight",
		Help: "HTTP requests currently being served.",
	})
	o.workerCapacity = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "jamshid_worker_capacity",
		Help: "Configured worker loops per pool.",
	}, []string{"pool"})
	o.workerActive = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "jamshid_worker_active",
		Help: "Worker loops currently processing a claimed task.",
	}, []string{"pool"})
	o.workerAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "jamshid_worker_task_attempts_total",
		Help: "Worker task attempts (not unique listings) by pool, source, outcome and failure kind.",
	}, []string{"pool", "source", "outcome", "failure"})
	o.workerDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "jamshid_worker_task_duration_seconds",
		Help:    "Worker task attempt duration by pool.",
		Buckets: bounds,
	}, []string{"pool"})
	o.workerClaimErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "jamshid_worker_claim_errors_total",
		Help: "Task claim failures, reported even when no task was acquired.",
	}, []string{"pool"})
	o.workerLoopTick = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "jamshid_worker_loop_last_tick_timestamp_seconds",
		Help: "Unix timestamp of the last actual loop iteration, including idle polls.",
	}, []string{"pool", "loop"})
	o.workerCompleted = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "jamshid_worker_last_completed_timestamp_seconds",
		Help: "Unix timestamp of the last successfully committed task.",
	}, []string{"pool"})
	o.schedulerAttempt = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "jamshid_scheduler_last_attempt_timestamp_seconds",
		Help: "Unix timestamp of the last scheduler cycle attempt.",
	}, []string{"scheduler"})
	o.schedulerSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "jamshid_scheduler_last_success_timestamp_seconds",
		Help: "Unix timestamp of the last successful scheduler cycle.",
	}, []string{"scheduler"})
	o.schedulerErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "jamshid_scheduler_errors_total",
		Help: "Scheduler cycle failures.",
	}, []string{"scheduler"})
	o.retryTickets = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "jamshid_retry_tickets_total",
		Help: "Durable retry ticket transitions counted after successful persistence.",
	}, []string{"kind", "action"})
	o.inventoryCache = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "jamshid_inventory_cache_requests_total",
		Help: "Inventory cache lookups by outcome: hit, miss, error or bypass.",
	}, []string{"outcome"})
	o.inventoryCacheWrites = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "jamshid_inventory_cache_writes_total",
		Help: "Inventory cache writes by outcome: ok or error (noeviction/write failures stay visible; fallback semantics are unchanged).",
	}, []string{"outcome"})
	o.inventoryQueries = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "jamshid_inventory_queries_total",
		Help: "Inventory searches served from PostgreSQL.",
	})
	o.postgresPoolConns = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "jamshid_postgres_pool_connections",
		Help: "Application PostgreSQL pool connections by state.",
	}, []string{"state"})
	o.postgresPoolMax = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "jamshid_postgres_pool_max_connections",
		Help: "Application PostgreSQL pool size limit.",
	})
	o.snapshotCollector = newSnapshotCollector()
	registry.MustRegister(
		o.operations, o.listings,
		o.httpRequests, o.httpDuration, o.httpInFlight,
		o.workerCapacity, o.workerActive, o.workerAttempts, o.workerDuration,
		o.workerClaimErrors, o.workerLoopTick, o.workerCompleted,
		o.schedulerAttempt, o.schedulerSuccess, o.schedulerErrors,
		o.retryTickets, o.inventoryCache, o.inventoryCacheWrites, o.inventoryQueries,
		o.postgresPoolConns, o.postgresPoolMax,
		o.snapshotCollector,
	)
	return o, nil
}

func (o *Observer) Shutdown(ctx context.Context) error { return o.provider.Shutdown(ctx) }

// Registry exposes the process-wide Prometheus registry for additional collectors.
func (o *Observer) Registry() *prometheus.Registry { return o.registry }

// ObservePostgresPool records current application pool statistics; it is cheap
// and called periodically from main wiring and tests.
func (o *Observer) ObservePostgresPool(acquired, idle, max int32) {
	o.postgresPoolConns.WithLabelValues("acquired").Set(float64(acquired))
	o.postgresPoolConns.WithLabelValues("idle").Set(float64(idle))
	o.postgresPoolMax.Set(float64(max))
}

func (o *Observer) Start(ctx context.Context, operation string, platform vehicle.Platform) (context.Context, func(ports.Observation)) {
	ctx = o.diagnostics.Context(ctx)
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
			// Only leaf operations emit developer events. Parent aggregation/page
			// observations would multiply one downstream failure.
			if event.Error != nil || event.StatusCode >= 400 {
				if operation == "fetch" || operation == "parser_contract" || strings.HasPrefix(operation, "redis_") {
					err := event.Error
					if err == nil {
						err = errors.New("upstream status")
					}
					component := "source"
					if strings.HasPrefix(operation, "redis_") {
						component = "redis"
					}
					class := failure
					if class == "none" {
						class = "http_" + strconv.Itoa(event.StatusCode)
					}
					ports.ReportDiagnostic(ctx, ports.Diagnostic{Component: component, Operation: operation, Source: string(platform), ErrorClass: class}, err)
				}
			}
			attrs := []attribute.KeyValue{attribute.String("outcome", outcome), attribute.String("failure", failure), attribute.String("cache", event.Cache), attribute.Int("http.status_code", event.StatusCode), attribute.Int("listings", event.Listings)}
			span.SetAttributes(attrs...)
			if event.Error != nil || event.StatusCode >= 400 {
				span.SetStatus(codes.Error, failure)
			}
			elapsed := time.Since(start).Seconds()
			// Labels contain only controlled categories: no query, item URL, cursor or ID.
			o.operations.WithLabelValues(operation, string(platform), outcome, failure, event.Cache, strconv.Itoa(event.StatusCode)).Observe(elapsed)
			o.listings.WithLabelValues(operation, string(platform), outcome, failure, event.Cache, strconv.Itoa(event.StatusCode)).Add(float64(max(0, event.Listings)))
			o.logger.Info("operation completed", "operation", operation, "source", platform, "outcome", outcome, "failure", failure, "cache", event.Cache, "status", event.StatusCode, "duration_seconds", elapsed, "listings", event.Listings, "trace_id", span.SpanContext().TraceID().String())
		})
	}
}

func (o *Observer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	promhttp.HandlerFor(o.registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

// HTTP wraps an HTTP handler with request metrics. Normalized routes come from
// ports.RouteCapture (set by the router middleware after route matching); see
// router.go. Legacy operation classification is preserved unchanged.
func (o *Observer) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(ports.DiagnosticScope(o.diagnostics.Context(r.Context())))
		capture := &ports.RouteCapture{}
		r = r.WithContext(ports.WithRouteCapture(r.Context(), capture))
		start := time.Now()
		o.httpInFlight.Inc()
		defer o.httpInFlight.Dec()
		// Legacy four-path classification, preserved verbatim.
		legacy := r.URL.Path
		switch legacy {
		case "/api/v1/crawl", "/api/v1/listing", "/api/v1/categories", "/api/v1/health":
		default:
			legacy = "other"
		}
		ctx, finish := o.Start(r.Context(), "http"+strings.ReplaceAll(legacy, "/", "_"), "")
		recorder := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(recorder, r.WithContext(ctx))
		finish(ports.Observation{StatusCode: recorder.status})
		route := capture.Template
		if route == "" {
			switch {
			case r.Method == http.MethodOptions:
				route = "preflight"
			case recorder.status == http.StatusMethodNotAllowed:
				route = "method_not_allowed"
			default:
				route = "unmatched"
			}
		}
		if recorder.status >= 500 && !ports.DiagnosticRecorded(ctx) {
			ports.ReportDiagnostic(ctx, ports.Diagnostic{Component: "http", Operation: normalizeMethod(r.Method) + " " + route, ErrorClass: "http_" + strconv.Itoa(recorder.status)}, errors.New("server response"))
		}
		o.httpRequests.WithLabelValues(route, normalizeMethod(r.Method), statusClass(recorder.status)).Inc()
		o.httpDuration.WithLabelValues(route, normalizeMethod(r.Method)).Observe(time.Since(start).Seconds())
	})
}

func (o *Observer) DiagnosticContext(ctx context.Context) context.Context {
	return o.diagnostics.Context(ctx)
}
func (o *Observer) LoggingHeartbeat(ctx context.Context) { o.diagnostics.Heartbeat(ctx) }

// normalizeMethod maps the raw request method to a bounded label set so an
// arbitrary or malformed method token cannot create unbounded cardinality.
func normalizeMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodTrace:
		return method
	default:
		return "other"
	}
}

func statusClass(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500 && code < 600:
		return "5xx"
	default:
		return "other"
	}
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
