package errorlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestValueFreeGroupingAndOccurrences(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf)
	d := ports.Diagnostic{Component: "postgres", Operation: "inventory.search", QueryName: "inventory.search:shape1", QueryShape: "select * from listings where id = ?"}
	for i := 0; i < 20; i++ {
		ctx := r.Context(context.Background())
		err := &pgconn.PgError{Code: "23505", Message: fmt.Sprintf("secret-user-%d", i), Detail: "password=do-not-store", Where: "SQL with values"}
		ports.ReportDiagnostic(ctx, d, err)
		ports.ReportDiagnostic(ctx, ports.Diagnostic{Component: "http", Operation: "GET /inventory"}, fmt.Errorf("outer: %w", err))
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 20 {
		t.Fatalf("want 20 occurrences, got %d", len(lines))
	}
	signatures := map[string]bool{}
	for _, line := range lines {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		signatures[e["signature"].(string)] = true
	}
	if len(signatures) != 1 {
		t.Fatal("values split signatures", signatures)
	}
	for _, secret := range []string{"secret-user", "do-not-store", "SQL with values"} {
		if strings.Contains(buf.String(), secret) {
			t.Fatal("secret emitted", secret)
		}
	}
	buf.Reset()
	r.Report(context.Background(), d, &pgconn.PgError{Code: "40P01"})
	var other map[string]any
	_ = json.Unmarshal(buf.Bytes(), &other)
	if signatures[other["signature"].(string)] {
		t.Fatal("different SQLSTATE merged")
	}
}

func TestCanceledOperationsAreNotBugs(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf)
	ports.ReportDiagnostic(r.Context(context.Background()), ports.Diagnostic{Component: "worker", Operation: "shutdown"}, fmt.Errorf("wrap: %w", context.Canceled))
	if buf.Len() != 0 {
		t.Fatal(buf.String())
	}
	r.Report(context.Background(), ports.Diagnostic{Component: "source", Operation: "fetch"}, &ports.SourceError{Kind: "rate_limited", Err: errors.New("URL with token")})
	if !strings.Contains(buf.String(), `"category":"expected"`) || strings.Contains(buf.String(), "URL with token") {
		t.Fatal(buf.String())
	}
}

// probeStackCall is a real call site that must survive stack capture.
func probeStackCall(r *Reporter) {
	ctx := r.Context(context.Background())
	ports.ReportDiagnostic(ctx, ports.Diagnostic{Component: "redis", Operation: "probe"}, errors.New("probe error"))
}

// TestStackOmitsDiagnosticPlumbing verifies that the diagnostic reporting
// plumbing (exported wrappers and the unexported helper in diagnostic.go) is
// filtered out of the captured stack, while a real application call site is
// retained. Matching by file keeps this stable even when frames are inlined.
func TestStackOmitsDiagnosticPlumbing(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf)
	probeStackCall(r)
	var e map[string]any
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	stack, ok := e["stack"].([]any)
	if !ok {
		t.Fatal("missing stack field")
	}
	if len(stack) == 0 {
		t.Fatal("stack lost all call sites")
	}
	text := ""
	for _, f := range stack {
		text += f.(string) + "\n"
	}
	if strings.Contains(text, "diagnostic.go") ||
		strings.Contains(text, "reportDiagnostic") ||
		strings.Contains(text, "ReportDiagnostic") ||
		strings.Contains(text, "ReportDiagnosticOccurrence") {
		t.Fatalf("diagnostic plumbing leaked into stack: %s", text)
	}
	// A real call site from the application (not the plumbing) is retained.
	if !strings.Contains(text, "errorlog") {
		t.Fatalf("no real call site retained in stack: %s", text)
	}
}

func TestRepeatedLeafExecutionsStillCountWithSameSentinel(t *testing.T) {
	var buf bytes.Buffer
	ctx := New(&buf).Context(context.Background())
	err := errors.New("same reused error with private value")
	d := ports.Diagnostic{Component: "redis", Operation: "get"}
	for i := 0; i < 3; i++ {
		ports.ReportDiagnosticOccurrence(ctx, d, err)
	}
	ports.ReportDiagnostic(ctx, ports.Diagnostic{Component: "http", Operation: "GET /inventory"}, fmt.Errorf("outer: %w", err))
	if strings.Count(buf.String(), `"event_kind":"error"`) != 3 {
		t.Fatal(buf.String())
	}
}

func TestReporter_ContextAndHeartbeat(t *testing.T) {
	t.Setenv("APP_ROLE", "worker")
	t.Setenv("JAMSHID_RELEASE", "v1.2.3")
	var buf bytes.Buffer
	r := New(&buf)
	if r.role != "worker" || r.release != "v1.2.3" {
		t.Fatalf("expected worker role and v1.2.3 release, got %s / %s", r.role, r.release)
	}

	// Top level Context
	ctx := Context(context.Background())
	if !ports.HasDiagnostics(ctx) {
		t.Fatal("expected diagnostics in ctx")
	}
	// Calling again returns same ctx
	if ctx2 := Context(ctx); ctx2 != ctx {
		t.Fatal("expected identical ctx when diagnostics already present")
	}

	// Reporter.Context with existing diagnostics
	if ctx3 := r.Context(ctx); ctx3 != ctx {
		t.Fatal("expected identical ctx when already configured")
	}

	// Report with nil error does nothing
	r.Report(ctx, ports.Diagnostic{Component: "test"}, nil)

	// Heartbeat runs and cancels cleanly
	hbCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	r.Heartbeat(hbCtx)
	if !strings.Contains(buf.String(), "logging heartbeat") {
		t.Fatalf("expected heartbeat log, got %s", buf.String())
	}
}

type mockNetErr struct {
	timeout bool
}

func (m mockNetErr) Error() string   { return "network failure" }
func (m mockNetErr) Timeout() bool   { return m.timeout }
func (m mockNetErr) Temporary() bool { return false }

func TestReporter_TimeoutAndNetworkClassification(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf)
	ctx := context.Background()

	// 1. DeadlineExceeded -> class "timeout"
	r.Report(ctx, ports.Diagnostic{Component: "client"}, context.DeadlineExceeded)
	if !strings.Contains(buf.String(), `"error_class":"timeout"`) {
		t.Fatalf("expected timeout class for DeadlineExceeded, got %s", buf.String())
	}

	// 2. net.Error without timeout -> class "network"
	buf.Reset()
	r.Report(ctx, ports.Diagnostic{Component: "client"}, mockNetErr{timeout: false})
	if !strings.Contains(buf.String(), `"error_class":"network"`) {
		t.Fatalf("expected network class, got %s", buf.String())
	}

	// 3. net.Error with timeout -> class "timeout"
	buf.Reset()
	r.Report(ctx, ports.Diagnostic{Component: "client"}, mockNetErr{timeout: true})
	if !strings.Contains(buf.String(), `"error_class":"timeout"`) {
		t.Fatalf("expected timeout class for network timeout, got %s", buf.String())
	}
}
