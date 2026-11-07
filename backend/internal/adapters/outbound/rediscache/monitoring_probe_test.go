package rediscache

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The per-poll write probe must succeed on a healthy instance, clean up after
// itself (the key is deleted; the TTL is only the crash backstop), and report
// failure when the instance rejects writes.
func TestMonitoringWriteProbeHealthyAndRejected(t *testing.T) {
	server, a, _ := testCaches(t)
	ctx := context.Background()

	// Healthy instance: the probe is one tiny, fresh-key, self-deleting write.
	for i := 0; i < 3; i++ {
		if err := a.probeWrite(ctx); err != nil {
			t.Fatalf("poll %d: probe rejected on healthy instance: %v", i, err)
		}
	}
	// The probe deletes its key; nothing accumulates between polls.
	var left []string
	for _, k := range server.Keys() {
		if strings.HasPrefix(k, writeProbeKeyPrefix) {
			left = append(left, k)
		}
	}
	if len(left) != 0 {
		t.Fatalf("probe keys accumulated: %v", left)
	}

	// Reachable-but-full instance: a probe write is denied, which is exactly the
	// memory-pressure signal PING/INFO cannot show. The pre-seeded key keeps the
	// check deterministic (no first-poll write to deny).
	b, err := New("redis://"+server.Addr(), 24*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	server.Set(writeProbeKeyPrefix+"test", "1")
	var commands []string
	b.probeClient.AddHook(writeDenial{&commands})
	if err := b.probeWrite(ctx); err == nil {
		t.Fatal("OOM probe reported accepted")
	}
	if len(commands) != 1 || commands[0] != "set" {
		t.Fatalf("expected a single denied probe write, got %v", commands)
	}
}
