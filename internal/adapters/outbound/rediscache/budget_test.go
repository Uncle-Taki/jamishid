package rediscache

import (
	"context"
	"testing"
	"time"
)

func TestProviderBudgetSharedAcrossInstances(t *testing.T) {
	server, a, b := testCaches(t)
	ctx := context.Background()
	if err := a.Wait(ctx, "bama"); err != nil {
		t.Fatal(err)
	}
	blocked, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if err := b.Wait(blocked, "bama"); err == nil {
		t.Fatal("second instance bypassed shared slot")
	}
	if err := a.Backoff(ctx, "bama", 2000); err != nil {
		t.Fatal(err)
	}
	if err := b.Backoff(ctx, "bama", 1000); err != nil {
		t.Fatal(err)
	}
	keys := budgetKeys("bama")
	if server.TTL(keys[0]) != 2*time.Second {
		t.Fatal("shorter retry-after erased cooldown")
	}
	interval, _ := server.Get(keys[2])
	if interval != "1000" {
		t.Fatalf("adaptive interval %q", interval)
	}
	if err := b.Success(ctx, "bama"); err != nil {
		t.Fatal(err)
	}
	interval, _ = server.Get(keys[2])
	if interval != "900" {
		t.Fatalf("recovery interval %q", interval)
	}
	server.FastForward(3 * time.Second)
	if err := b.Wait(ctx, "bama"); err != nil {
		t.Fatal(err)
	}
	if err := b.Wait(ctx, "divar"); err != nil {
		t.Fatal("one provider blocked another", err)
	}
}
