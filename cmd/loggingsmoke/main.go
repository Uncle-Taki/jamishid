// loggingsmoke deliberately generates errors against a DISPOSABLE database.
// It is a fixture executable, never used by the production image entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/errorlog"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/postgres"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	reporter := errorlog.New(os.Stdout)
	ctx = reporter.Context(ctx)
	go reporter.Heartbeat(ctx)
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	store, err := postgres.New(startup, os.Getenv("DATABASE_URL"))
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture database unavailable")
		os.Exit(1)
	}
	defer store.Close()
	for i := 0; i < 10; i++ {
		scope := ports.DiagnosticScope(ctx)
		_, err := store.Pool.Exec(scope, `SELECT missing_logging_fixture_column FROM listings WHERE id=$1`, fmt.Sprintf("private-fixture-value-%d", i))
		// Propagating the same error must not add another occurrence.
		ports.ReportDiagnostic(scope, ports.Diagnostic{Component: "http", Operation: "fixture.parent"}, err)
	}
	for i := 0; i < 2; i++ {
		_, _ = store.Pool.Exec(ports.DiagnosticScope(ctx), `SELECT 1 / $1::integer`, 0)
	}
	fmt.Println(`legacy log private-fixture-value-do-not-ingest`)
	fmt.Println(`{"level":"ERROR","msg":"private-fixture-value-do-not-ingest"}`)
	if os.Getenv("FIXTURE_EXIT") == "1" {
		return
	}
	<-ctx.Done()
}
