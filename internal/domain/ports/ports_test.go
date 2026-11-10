package ports

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type mockReporter struct {
	reports []Diagnostic
}

func (m *mockReporter) Report(ctx context.Context, d Diagnostic, err error) {
	m.reports = append(m.reports, d)
}

type mockNetErr struct {
	timeout bool
}

var _ net.Error = mockNetErr{}

func (m mockNetErr) Error() string   { return "net err" }
func (m mockNetErr) Timeout() bool   { return m.timeout }
func (m mockNetErr) Temporary() bool { return false }

func TestPortsCrawlPolicy(t *testing.T) {
	ctx := context.Background()
	empty := CrawlPolicyFrom(ctx)
	if empty.Refresh || empty.SkipDetails {
		t.Fatalf("expected empty policy, got %+v", empty)
	}

	policy := CrawlPolicy{Refresh: true, SkipDetails: true, RefreshDetails: true}
	ctxWithPolicy := WithCrawlPolicy(ctx, policy)
	got := CrawlPolicyFrom(ctxWithPolicy)
	if got != policy {
		t.Fatalf("expected %+v, got %+v", policy, got)
	}
}

func TestPortsDiagnostics(t *testing.T) {
	ctx := context.Background()
	if HasDiagnostics(ctx) {
		t.Fatalf("expected no diagnostics initially")
	}
	if DiagnosticRecorded(ctx) {
		t.Fatalf("expected not recorded")
	}

	// DiagnosticScope without diagnostics returns original ctx
	if DiagnosticScope(ctx) != ctx {
		t.Fatalf("expected same ctx")
	}

	reporter := &mockReporter{}
	diagCtx := WithDiagnostics(ctx, reporter)
	if !HasDiagnostics(diagCtx) {
		t.Fatalf("expected diagnostics to be present")
	}

	scopedCtx := DiagnosticScope(diagCtx)
	if !HasDiagnostics(scopedCtx) {
		t.Fatalf("expected diagnostics in scoped ctx")
	}

	// Nil error or canceled error should be ignored
	ReportDiagnostic(scopedCtx, Diagnostic{Component: "test"}, nil)
	ReportDiagnostic(scopedCtx, Diagnostic{Component: "test"}, context.Canceled)
	if len(reporter.reports) != 0 {
		t.Fatalf("expected 0 reports for nil or canceled")
	}

	// Normal diagnostic report
	testErr := errors.New("something broke")
	ReportDiagnostic(scopedCtx, Diagnostic{Component: "test1"}, testErr)
	if len(reporter.reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reporter.reports))
	}
	if !DiagnosticRecorded(scopedCtx) {
		t.Fatalf("expected recorded to be true")
	}

	// Duplicate error is deduplicated when occurrence=false
	ReportDiagnostic(scopedCtx, Diagnostic{Component: "test2"}, testErr)
	if len(reporter.reports) != 1 {
		t.Fatalf("expected still 1 report due to deduplication, got %d", len(reporter.reports))
	}

	// Occurrence=true reports even duplicate error
	ReportDiagnosticOccurrence(scopedCtx, Diagnostic{Component: "test3"}, testErr)
	if len(reporter.reports) != 2 {
		t.Fatalf("expected 2 reports, got %d", len(reporter.reports))
	}

	// Report on unconfigured context should be safe no-op
	ReportDiagnostic(context.Background(), Diagnostic{Component: "noop"}, testErr)

	// Context with nil reporter
	nilReporterCtx := WithDiagnostics(ctx, nil)
	ReportDiagnostic(nilReporterCtx, Diagnostic{Component: "noop"}, testErr)

	// Bounding test: exceed 64 errors
	for i := 0; i < 70; i++ {
		ReportDiagnosticOccurrence(scopedCtx, Diagnostic{Component: "bulk"}, errors.New("err"))
	}
}

func TestPortsObservationAndFailureKind(t *testing.T) {
	ctx := context.Background()
	noop := NoopObserver{}
	ctx2, done := noop.Start(ctx, "test", vehicle.PlatformDivar)
	if ctx2 == nil || done == nil {
		t.Fatalf("noop observer returned nil")
	}
	done(Observation{Outcome: "ok"})

	// Route capture
	if RouteCaptureFrom(ctx) != nil {
		t.Fatalf("expected nil capture")
	}
	capture := &RouteCapture{Template: "/api/test"}
	ctxCapture := WithRouteCapture(ctx, capture)
	if RouteCaptureFrom(ctxCapture) != capture {
		t.Fatalf("expected matching route capture")
	}

	// SourceError
	wrapped := errors.New("underlying")
	srcErr := &SourceError{Kind: "bad_status", StatusCode: 502, Err: wrapped}
	if srcErr.Error() != "underlying" {
		t.Fatalf("expected underlying error message, got %s", srcErr.Error())
	}
	if srcErr.Unwrap() != wrapped {
		t.Fatalf("unwrap failed")
	}

	bareErr := &SourceError{Kind: "bad_status", StatusCode: 502}
	if bareErr.Error() != "source request failed: bad_status" {
		t.Fatalf("expected fallback error message, got %s", bareErr.Error())
	}

	// FailureKind
	if got := FailureKind(nil); got != "none" {
		t.Fatalf("expected none, got %s", got)
	}
	if got := FailureKind(&SourceError{Kind: "source_wait"}); got != "source_wait" {
		t.Fatalf("expected source_wait, got %s", got)
	}
	if got := FailureKind(&SourceError{Kind: "budget_unavailable"}); got != "budget_unavailable" {
		t.Fatalf("expected budget_unavailable, got %s", got)
	}
	if got := FailureKind(&SourceError{Kind: "egress_unavailable"}); got != "egress_unavailable" {
		t.Fatalf("expected egress_unavailable, got %s", got)
	}
	if got := FailureKind(context.Canceled); got != "canceled" {
		t.Fatalf("expected canceled, got %s", got)
	}
	if got := FailureKind(context.DeadlineExceeded); got != "timeout" {
		t.Fatalf("expected timeout, got %s", got)
	}
	if got := FailureKind(&SourceError{Kind: "custom_kind"}); got != "custom_kind" {
		t.Fatalf("expected custom_kind, got %s", got)
	}
	if got := FailureKind(mockNetErr{timeout: true}); got != "timeout" {
		t.Fatalf("expected timeout for net error, got %s", got)
	}
	if got := FailureKind(mockNetErr{timeout: false}); got != "network" {
		t.Fatalf("expected network for non-timeout net error, got %s", got)
	}
	if got := FailureKind(errors.New("random err")); got != "other" {
		t.Fatalf("expected other, got %s", got)
	}
}
