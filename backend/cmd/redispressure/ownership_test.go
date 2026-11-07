package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestTargetRejectsTCPAndOtherSockets(t *testing.T) {
	e := ownership{socket: "/tmp/private/redis.sock"}
	for _, raw := range []string{"redis://localhost:6379", "redis://user:pass@redis:6379", "redis://127.0.0.1:6379", "unix:///tmp/other.sock", "unix:///tmp/private/redis.sock?db=1", "invalid"} {
		if _, err := targetOptions(raw, e); err == nil {
			t.Fatalf("accepted unknown target %q", raw)
		}
	}
}

// A real isolated process exercises the exact server/config identity checks.
// Refused fixtures contain only synthetic sentinel values, never main data.
func TestOwnershipVerificationIsReadOnly(t *testing.T) {
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server is required for isolated ownership integration tests")
	}
	dir, err := os.MkdirTemp("", "jamshid-redis-pressure.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	e := ownership{dir: dir, socket: filepath.Join(dir, "redis.sock"), token: "0123456789abcdef0123456789abcdef", maxmemory: "33554432"}
	cmd := exec.Command(binary, "--port", "0", "--unixsocket", e.socket, "--dir", dir, "--maxmemory", e.maxmemory, "--maxmemory-policy", "noeviction", "--save", "", "--appendonly", "no")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	e.pid = strconv.Itoa(cmd.Process.Pid)
	options, err := targetOptions("unix://"+e.socket, e)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for client.Ping(ctx).Err() != nil {
		if ctx.Err() != nil {
			t.Fatal("isolated Redis failed to start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	marker := pressureMarkerPrefix + e.token
	if err := client.Set(ctx, marker, e.token, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := verifyOwnership(ctx, client, e); err != nil {
		t.Fatalf("launcher-owned fresh instance refused: %v", err)
	}
	for _, name := range []string{signingKey, budgetPacingKey, "unrelated:sentinel"} {
		t.Run(name, func(t *testing.T) {
			if err := client.Set(ctx, name, "synthetic-sentinel", 0).Err(); err != nil {
				t.Fatal(err)
			}
			before := client.MGet(ctx, marker, name).Val()
			if err := verifyOwnership(ctx, client, e); err == nil {
				t.Fatal("nonempty application fixture accepted")
			}
			if after := client.MGet(ctx, marker, name).Val(); !reflect.DeepEqual(before, after) || client.DBSize(ctx).Val() != 2 {
				t.Fatal("refusal mutated the fixture")
			}
			client.Del(ctx, name)
		})
	}
	for _, mutate := range []func(*ownership){
		func(o *ownership) { o.pid = "1" },
		func(o *ownership) { o.token = "wrong-marker" },
		func(o *ownership) { o.maxmemory = "16777216" },
	} {
		other := e
		mutate(&other)
		if err := verifyOwnership(ctx, client, other); err == nil {
			t.Fatal("mismatched launcher identity accepted")
		}
		if client.DBSize(ctx).Val() != 1 || client.Get(ctx, marker).Val() != e.token {
			t.Fatal("identity refusal mutated the fixture")
		}
	}
	client.Del(ctx, marker)
	if err := verifyOwnership(ctx, client, e); err == nil || client.DBSize(ctx).Val() != 0 {
		t.Fatal("missing marker must be refused without creating one")
	}
}
