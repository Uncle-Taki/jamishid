package ports

import (
	"context"
	"errors"
	"sync"
)

// Diagnostic contains code-owned descriptions, never request or query values.
type Diagnostic struct {
	Component, Operation, Source, QueryName, QueryShape, ErrorClass, SQLState string
}

type DiagnosticReporter interface {
	Report(context.Context, Diagnostic, error)
}

type diagnosticKey struct{}
type diagnosticScope struct {
	sync.Mutex
	reporter DiagnosticReporter
	errors   []error
}

func WithDiagnostics(ctx context.Context, reporter DiagnosticReporter) context.Context {
	return context.WithValue(ctx, diagnosticKey{}, &diagnosticScope{reporter: reporter})
}

// DiagnosticScope starts an independent occurrence boundary (one request/task).
func DiagnosticScope(ctx context.Context) context.Context {
	if state, ok := ctx.Value(diagnosticKey{}).(*diagnosticScope); ok {
		return WithDiagnostics(ctx, state.reporter)
	}
	return ctx
}

func HasDiagnostics(ctx context.Context) bool {
	_, ok := ctx.Value(diagnosticKey{}).(*diagnosticScope)
	return ok
}

// ReportDiagnostic deduplicates propagation of the same error within a boundary.
// Different failing operations are still separate occurrences, not unique bugs.
func ReportDiagnostic(ctx context.Context, event Diagnostic, err error) {
	reportDiagnostic(ctx, event, err, false)
}

// ReportDiagnosticOccurrence is called once by a leaf command/query hook. Each
// actual execution counts, even if a client reuses the same sentinel error.
// Parent boundaries still use ReportDiagnostic to suppress propagation.
func ReportDiagnosticOccurrence(ctx context.Context, event Diagnostic, err error) {
	reportDiagnostic(ctx, event, err, true)
}

func reportDiagnostic(ctx context.Context, event Diagnostic, err error, occurrence bool) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	state, ok := ctx.Value(diagnosticKey{}).(*diagnosticScope)
	if !ok || state.reporter == nil {
		return
	}
	state.Lock()
	for _, previous := range state.errors {
		if !occurrence && (errors.Is(err, previous) || errors.Is(previous, err)) {
			state.Unlock()
			return
		}
	}
	// Bound memory even for requests with many failing downstream operations.
	if len(state.errors) < 64 {
		state.errors = append(state.errors, err)
	}
	state.Unlock()
	state.reporter.Report(ctx, event, err)
}

func DiagnosticRecorded(ctx context.Context) bool {
	state, ok := ctx.Value(diagnosticKey{}).(*diagnosticScope)
	if !ok {
		return false
	}
	state.Lock()
	defer state.Unlock()
	return len(state.errors) != 0
}
