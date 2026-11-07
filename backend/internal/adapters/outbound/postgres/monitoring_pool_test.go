package postgres

import (
	"context"
	"os"
	"regexp"
	"testing"
)

// TestMonitoringPoolIsReadOnlyBoundedAndIndependent proves the dedicated
// monitoring pool (review findings R7/C5): it is capped at two connections,
// opens read-only sessions, does NOT run application migrations (the schema
// must already exist), and cannot starve the application pool because it is a
// separate pool. A write through it must be rejected at the session level.
func TestMonitoringPoolIsReadOnlyBoundedAndIndependent(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()

	// The schema must be created by the application pool (migrations), not the
	// monitoring pool.
	app, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	mon, err := NewMonitoring(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer mon.Close()

	// Bounded at two connections, distinct pool from the application.
	if got := mon.Pool.Stat().MaxConns(); got != 2 {
		t.Fatalf("monitoring pool MaxConns = %d, want 2", got)
	}
	if mon.Pool == app.Pool {
		t.Fatal("monitoring pool must be independent of the application pool")
	}

	// A write must be rejected by the read-only session parameter.
	_, err = mon.Pool.Exec(ctx, `CREATE TABLE jamshid_monitoring_write_probe (id int)`)
	if err == nil {
		t.Fatal("monitoring pool accepted a write; session must be read-only")
	}
	if !regexp.MustCompile(`read.only`).MatchString(err.Error()) {
		t.Fatalf("expected a read-only rejection, got: %v", err)
	}

	// A read must succeed (the pool is usable for monitoring queries).
	var n int
	if err := mon.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("schema_migrations empty; migrations were not applied by the app pool")
	}
}
