package main

import (
	"context"
	"testing"
	"time"
)

func TestLoopCount(t *testing.T) {
	// 1. Unset env returns fallback
	if c := loopCount("TEST_NONEXISTENT_ENV", 5); c != 5 {
		t.Errorf("expected fallback 5, got %d", c)
	}

	// 2. Invalid integer returns fallback
	t.Setenv("TEST_LOOP_COUNT", "not-a-number")
	if c := loopCount("TEST_LOOP_COUNT", 4); c != 4 {
		t.Errorf("expected fallback 4 on invalid int, got %d", c)
	}

	// 3. Below min (< 1) returns fallback
	t.Setenv("TEST_LOOP_COUNT", "0")
	if c := loopCount("TEST_LOOP_COUNT", 3); c != 3 {
		t.Errorf("expected fallback 3 on < 1, got %d", c)
	}

	// 4. Above max (> 16) returns fallback
	t.Setenv("TEST_LOOP_COUNT", "17")
	if c := loopCount("TEST_LOOP_COUNT", 2); c != 2 {
		t.Errorf("expected fallback 2 on > 16, got %d", c)
	}

	// 5. Valid count (1..16)
	t.Setenv("TEST_LOOP_COUNT", "8")
	if c := loopCount("TEST_LOOP_COUNT", 1); c != 8 {
		t.Errorf("expected valid count 8, got %d", c)
	}
}

func TestSnapshotSource_Deadlines(t *testing.T) {
	sDefault := &snapshotSource{}
	if sDefault.pgDeadline() != defaultPGProbeTimeout {
		t.Errorf("expected default pg deadline, got %v", sDefault.pgDeadline())
	}
	if sDefault.redisDeadline() != defaultRedisProbeTimeout {
		t.Errorf("expected default redis deadline, got %v", sDefault.redisDeadline())
	}

	sCustom := &snapshotSource{
		pgTimeout:    2 * time.Second,
		redisTimeout: 1 * time.Second,
	}
	if sCustom.pgDeadline() != 2*time.Second {
		t.Errorf("expected custom pg deadline 2s, got %v", sCustom.pgDeadline())
	}
	if sCustom.redisDeadline() != 1*time.Second {
		t.Errorf("expected custom redis deadline 1s, got %v", sCustom.redisDeadline())
	}

	// probeContext already canceled
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, cancelFn, ok := sDefault.probeContext(canceledCtx, time.Second)
	cancelFn()
	if ok {
		t.Errorf("expected probeContext on canceled ctx to return ok=false")
	}

	// probeContext with past deadline
	pastCtx, pastCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer pastCancel()
	_, cancelFn2, ok := sDefault.probeContext(pastCtx, time.Second)
	cancelFn2()
	if ok {
		t.Errorf("expected probeContext on past deadline ctx to return ok=false")
	}

	// probeContext with deadline clamping (remaining < probe deadline)
	futureCtx, futureCancel := context.WithDeadline(context.Background(), time.Now().Add(50*time.Millisecond))
	defer futureCancel()
	_, cancelFn3, ok := sDefault.probeContext(futureCtx, time.Minute)
	defer cancelFn3()
	if !ok {
		t.Errorf("expected ok=true when remaining > 0")
	}
}
