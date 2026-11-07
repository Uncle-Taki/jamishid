package rediscache

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/redis/go-redis/v9"
)

// diagRecorder captures the Diagnostic events a scope would forward to a
// reporter, so a test can assert exactly which redis errors are surfaced as
// developer errors.
type diagRecorder struct {
	mu     sync.Mutex
	events []ports.Diagnostic
}

func (r *diagRecorder) Report(_ context.Context, d ports.Diagnostic, _ error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, d)
}

func (r *diagRecorder) count(class string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if class == "" {
		return len(r.events)
	}
	n := 0
	for _, d := range r.events {
		if d.ErrorClass == class {
			n++
		}
	}
	return n
}

// TestRedisScriptFallbackIsNotReported is the review R4 regression: a scripted
// operation whose first EVALSHA misses the server's script cache (NOSCRIPT) and
// is transparently retried with EVAL still succeeds, and the intermediate
// NOSCRIPT command error must NOT be surfaced as a developer error. A fresh
// miniredis has an empty script cache, so the very first Run is exactly this
// cold NOSCRIPT-then-EVAL path.
func TestRedisScriptFallbackIsNotReported(t *testing.T) {
	_, a, _ := testCaches(t)
	rec := &diagRecorder{}
	ctx := ports.WithDiagnostics(context.Background(), rec)

	// releaseScript on a lease that does not exist: GET returns nothing, the
	// script returns 0 without error. The transport path is
	// EVALSHA (NOSCRIPT on the cold cache) then EVAL (success).
	if _, err := runScript(ctx, releaseScript, a.client, []string{"pressure:lease"}, "tok").Int(); err != nil {
		t.Fatalf("release script run: %v", err)
	}
	if got := rec.count("redis_NOSCRIPT"); got != 0 {
		t.Fatalf("cold-script NOSCRIPT fallback was reported as a developer error %d time(s): %+v", got, rec.events)
	}
	// The whole operation succeeded, so nothing redis-related should be reported.
	if got := rec.count(""); got != 0 {
		t.Fatalf("successful cold-script operation reported %d diagnostic(s): %+v", got, rec.events)
	}
}

// TestRedisTerminalScriptFailureIsReported proves a genuine script failure that
// the NOSCRIPT retry does not recover is still reported, exactly once, with a
// value-free classification (the synthetic message must not leak into the
// diagnostic classification). The NOSCRIPT miss of the initial EVALSHA is
// suppressed; only the terminal EVAL failure is reported.
func TestRedisTerminalScriptFailureIsReported(t *testing.T) {
	_, a, _ := testCaches(t)
	rec := &diagRecorder{}
	ctx := ports.WithDiagnostics(context.Background(), rec)

	failing := redis.NewScript("error('synthetic-terminal-failure-XYZ')")
	if _, err := runScript(ctx, failing, a.client, nil).Result(); err == nil {
		t.Fatal("expected the terminal script failure to surface an error")
	}

	// The recovered NOSCRIPT miss is suppressed; exactly one terminal failure is
	// reported.
	if got := rec.count("redis_NOSCRIPT"); got != 0 {
		t.Fatalf("recovered NOSCRIPT miss must be suppressed, reported %d time(s)", got)
	}
	if got := rec.count(""); got != 1 {
		t.Fatalf("expected exactly one diagnostic for the terminal failure, got %d: %+v", got, rec.events)
	}
	d := rec.events[0]
	if d.Component != "redis" || d.ErrorClass == "" {
		t.Fatalf("diagnostic must be a classified redis event: %+v", d)
	}
	// The classification is value-free: it must not embed the synthetic message.
	for _, field := range []string{d.Operation, d.ErrorClass, d.Source, d.QueryName, d.QueryShape} {
		if strings.Contains(field, "synthetic-terminal-failure-XYZ") {
			t.Fatalf("diagnostic field %q leaked the terminal failure text", field)
		}
	}
}

// TestRedisGenuineCommandErrorIsReported is a control: a non-script command
// that fails with a real server error is still reported, so the NOSCRIPT
// suppression is narrow and does not hide genuine errors.
func TestRedisGenuineCommandErrorIsReported(t *testing.T) {
	_, a, _ := testCaches(t)
	rec := &diagRecorder{}
	ctx := ports.WithDiagnostics(context.Background(), rec)

	// SET a key then read it as a list: WRONGTYPE, a genuine protocol error.
	if err := a.client.Set(ctx, "pressure:typed", "scalar", 0).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := a.client.LRange(ctx, "pressure:typed", 0, 0).Result(); err == nil {
		t.Fatal("expected a WRONGTYPE error")
	}
	if got := rec.count(""); got == 0 {
		t.Fatalf("genuine command error was not reported")
	}
	if got := rec.count("redis_NOSCRIPT"); got != 0 {
		t.Fatalf("a non-script error must not be classified as NOSCRIPT")
	}
}

func TestDirectAndPipelinedNoScriptRemainVisible(t *testing.T) {
	for _, pipeline := range []bool{false, true} {
		_, cache, _ := testCaches(t)
		rec := &diagRecorder{}
		ctx := ports.WithDiagnostics(context.Background(), rec)
		if pipeline {
			pipe := cache.client.Pipeline()
			pipe.EvalSha(ctx, "missing-sha", nil)
			if _, err := pipe.Exec(ctx); !redis.HasErrorPrefix(err, "NOSCRIPT") {
				t.Fatalf("expected terminal NOSCRIPT: %v", err)
			}
		} else if err := cache.client.EvalSha(ctx, "missing-sha", nil).Err(); !redis.HasErrorPrefix(err, "NOSCRIPT") {
			t.Fatalf("expected terminal NOSCRIPT: %v", err)
		}
		if rec.count("redis_NOSCRIPT") != 1 {
			t.Fatalf("terminal NOSCRIPT must be reported once (pipeline=%v): %+v", pipeline, rec.events)
		}
	}
}
