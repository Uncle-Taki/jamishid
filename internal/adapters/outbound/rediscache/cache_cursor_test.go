package rediscache

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// writeDenial simulates a Redis instance that answers reads but rejects writes,
// mirroring a healthy-but-full instance under noeviction. It also records the
// name of every command it sees so a test can assert which operations a code
// path performs. The denial is returned by ProcessHook before the command is
// sent, so go-redis does not retry it.
type writeDenial struct {
	commands *[]string
}

func (writeDenial) DialHook(next redis.DialHook) redis.DialHook { return next }
func (r writeDenial) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		*r.commands = append(*r.commands, cmd.Name())
		switch cmd.Name() {
		case "set", "setnx":
			return errors.New("OOM command not allowed when used memory limit is reached")
		}
		return next(ctx, cmd)
	}
}
func (r writeDenial) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			*r.commands = append(*r.commands, cmd.Name())
			if cmd.Name() == "set" || cmd.Name() == "setnx" {
				return errors.New("OOM command not allowed when used memory limit is reached")
			}
		}
		return next(ctx, cmds)
	}
}

func randomSigningKey(t *testing.T) []byte {
	t.Helper()
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	return key[:]
}

// Reuse of an existing valid key must be a single read and must not attempt any
// write, so a Redis at its memory limit under noeviction cannot block startup.
func TestCursorSigningKeyReuseIssuesNoWrite(t *testing.T) {
	server, a, _ := testCaches(t)
	ctx := context.Background()
	seed := randomSigningKey(t)
	server.Set(cursorSigningKeyName, hex.EncodeToString(seed))

	var commands []string
	a.client.AddHook(writeDenial{&commands})
	got, err := a.CursorSigningKey(ctx)
	if err != nil {
		t.Fatalf("reuse under write denial: %v", err)
	}
	if !bytes.Equal(got, seed) {
		t.Fatalf("rotated key: got %x want %x", got, seed)
	}
	for _, name := range commands {
		if name == "set" || name == "setnx" {
			t.Fatalf("read path attempted a write: %v", commands)
		}
	}
	if len(commands) != 1 || commands[0] != "get" {
		t.Fatalf("expected exactly one read, got %v", commands)
	}
}

// First initialization still performs a write (SetNX) and persists the durable
// key; concurrent initializers all observe the same winning key.
func TestCursorSigningKeyInitializationPersistsAndShares(t *testing.T) {
	_, a, b := testCaches(t)
	ctx := context.Background()
	var mu sync.Mutex
	var keys [][]byte
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := a
			if i%2 == 1 {
				c = b
			}
			key, err := c.CursorSigningKey(ctx)
			if err != nil {
				mu.Lock()
				defer mu.Unlock()
				t.Errorf("init %d: %v", i, err)
				return
			}
			mu.Lock()
			keys = append(keys, key)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if len(keys) != 8 {
		t.Fatalf("expected 8 keys, got %d", len(keys))
	}
	for i := 1; i < len(keys); i++ {
		if !bytes.Equal(keys[0], keys[i]) {
			t.Fatalf("concurrent initializers diverged: %x vs %x", keys[0], keys[i])
		}
	}
}

// A missing key with a write-rejected (OOM) Redis must fail explicitly and
// leave nothing persisted, rather than silently initializing or hanging.
func TestCursorSigningKeyInitializationReportsWriteFailure(t *testing.T) {
	server, a, _ := testCaches(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var commands []string
	a.client.AddHook(writeDenial{&commands})
	_, err := a.CursorSigningKey(ctx)
	if err == nil {
		t.Fatal("init succeeded despite rejected write")
	}
	if !strings.Contains(err.Error(), "OOM") {
		t.Fatalf("expected OOM write failure, got: %v", err)
	}
	if server.Exists(cursorSigningKeyName) {
		t.Fatal("key persisted despite failed initialization")
	}
}

// A malformed stored key is refused and left in place, not silently rotated to
// a new key (which would invalidate cursors every instance already signed).
func TestCursorSigningKeyMalformedRefusedWithoutRotation(t *testing.T) {
	server, a, _ := testCaches(t)
	ctx := context.Background()
	server.Set(cursorSigningKeyName, "not-valid-hex")
	if _, err := a.CursorSigningKey(ctx); err == nil {
		t.Fatal("malformed signing key accepted")
	}
	if got, _ := server.Get(cursorSigningKeyName); got != "not-valid-hex" {
		t.Fatalf("malformed key was rotated: %q", got)
	}
}

// Restart preserves key identity: a new client against the same server reads
// back the original key without re-initializing.
func TestCursorSigningKeySurvivesRestartWithoutWrite(t *testing.T) {
	server, a, _ := testCaches(t)
	ctx := context.Background()
	first, err := a.CursorSigningKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New("redis://"+server.Addr(), 24*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var commands []string
	reopened.client.AddHook(writeDenial{&commands})
	second, err := reopened.CursorSigningKey(ctx)
	if err != nil {
		t.Fatalf("restart read: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("restart changed the signing key")
	}
	for _, name := range commands {
		if name == "set" || name == "setnx" {
			t.Fatalf("restart re-initialized the key: %v", commands)
		}
	}
}
