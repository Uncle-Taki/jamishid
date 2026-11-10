// Package errorlog emits value-free developer diagnostics to stdout. Collection
// is asynchronous through Docker/Alloy; the application never calls Loki.
package errorlog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/trace"
)

type Reporter struct {
	logger        *slog.Logger
	role, release string
}

func New(w io.Writer) *Reporter {
	role := "api"
	if os.Getenv("APP_ROLE") == "worker" {
		role = "worker"
	}
	return &Reporter{logger: slog.New(slog.NewJSONHandler(w, nil)), role: role, release: os.Getenv("JAMSHID_RELEASE")}
}

func Context(ctx context.Context) context.Context {
	if ports.HasDiagnostics(ctx) {
		return ctx
	}
	return ports.WithDiagnostics(ctx, New(os.Stdout))
}

func (r *Reporter) Context(ctx context.Context) context.Context {
	if ports.HasDiagnostics(ctx) {
		return ctx
	}
	return ports.WithDiagnostics(ctx, r)
}

func (r *Reporter) Report(ctx context.Context, d ports.Diagnostic, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	class := d.ErrorClass
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		d.SQLState = pg.Code
		if class == "" {
			class = "postgres_" + pg.Code
		}
	}
	if class == "" {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			class = "timeout"
		default:
			var source *ports.SourceError
			var network net.Error
			if errors.As(err, &source) {
				class = source.Kind
			} else if errors.As(err, &network) {
				class = "network"
				if network.Timeout() {
					class = "timeout"
				}
			} else {
				class = reflect.TypeOf(err).String()
			}
		}
	}
	category := "internal"
	if d.Component == "source" {
		category = "upstream"
	}
	if class == "source_wait" || class == "budget_unavailable" || class == "egress_unavailable" || class == "rate_limited" || class == "http_429" || class == "lease_lost" {
		category = "expected"
	}
	key := strings.Join([]string{"v1", d.Component, d.Operation, d.Source, d.QueryName, class, d.SQLState}, "|")
	sum := sha256.Sum256([]byte(key))
	var id [16]byte
	_, _ = rand.Read(id[:])
	var pcs [24]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	var stack []string
	for {
		f, more := frames.Next()
		// Keep real call sites but drop the diagnostic plumbing itself (both the
		// exported wrappers and the unexported helper), which would otherwise be
		// inlined into every event's stack. Match by file so inlining cannot hide
		// a plumbing frame behind a renamed symbol.
		if strings.Contains(f.Function, "/backend/") && !strings.Contains(f.File, "/domain/ports/diagnostic.go") {
			stack = append(stack, strings.Split(f.Function, "/backend/")[1]+" ("+filepath.Base(f.File)+":"+strconv.Itoa(f.Line)+")")
		}
		if !more {
			break
		}
	}
	// Error(), PgError.Detail, request bodies, SQL arguments and URLs are never
	// serialized. The safe summary is constructed exclusively from code fields.
	r.logger.ErrorContext(ctx, "developer error", "event_kind", "error", "schema_version", "v1",
		"event_id", hex.EncodeToString(id[:]), "signature", hex.EncodeToString(sum[:16]),
		"component", d.Component, "operation", d.Operation, "source", d.Source,
		"query_name", d.QueryName, "query_shape", d.QueryShape, "error_class", class, "sqlstate", d.SQLState,
		"summary", d.Component+" / "+d.Operation+" / "+class, "category", category,
		"role", r.role, "release", r.release, "trace_id", trace.SpanContextFromContext(ctx).TraceID().String(), "stack", stack)
}

// Heartbeats distinguish a quiet application from a missing log pipeline.
func (r *Reporter) Heartbeat(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		r.logger.Info("logging heartbeat", "event_kind", "heartbeat", "schema_version", "v1", "role", r.role, "release", r.release)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
