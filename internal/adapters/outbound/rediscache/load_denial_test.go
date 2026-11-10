package rediscache

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/redis/go-redis/v9"
)

// denyCommands is a hook that records every command name and rejects a
// configured set of commands with a non-NOSCRIPT error. It lets a test deny a
// specific step (the lease SetNX, or the publish EVAL/EVALSHA) while the rest of
// the instance behaves normally — the controlled denial scenarios the review
// asked for, as opposed to relying on incidental free bytes at a real OOM
// boundary. The deny set is read-only after construction and the command log is
// appended sequentially, so (like the existing writeDenial hook) no lock is
// needed and the type is safe to add with a value receiver.
type denyCommands struct {
	deny     map[string]bool
	commands *[]string
}

func (d denyCommands) DialHook(next redis.DialHook) redis.DialHook { return next }
func (d denyCommands) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		name := strings.ToLower(cmd.Name())
		*d.commands = append(*d.commands, name)
		if d.deny[name] {
			return errors.New("command denied by test: " + name)
		}
		return next(ctx, cmd)
	}
}
func (d denyCommands) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			name := strings.ToLower(cmd.Name())
			*d.commands = append(*d.commands, name)
			if d.deny[name] {
				return errors.New("command denied by test: " + name)
			}
		}
		return next(ctx, cmds)
	}
}

// TestLeaseHeldByOtherOwnerNeverFetches is the controlled LEASE-denial scenario:
// while another worker holds the fetch lease, a concurrent load must NOT fetch
// (no uncoordinated upstream call) and must give up on its bounded deadline.
func TestLeaseHeldByOtherOwnerNeverFetches(t *testing.T) {
	server, a, _ := testCaches(t)
	var fetchCalls int
	// A concurrent owner already holds the lease for this page.
	server.Set(cacheKey("page", "search")+":lease", "other-owner-token")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_, _, err := a.LoadPage(ctx, "search", func(context.Context) (vehicle.ListingPage, error) {
		fetchCalls++
		return vehicle.ListingPage{Page: 1}, nil
	})
	if fetchCalls != 0 {
		t.Fatalf("fetcher ran %d time(s) while another owner held the lease (uncoordinated fetch)", fetchCalls)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the bounded deadline to end the wait, got: %v", err)
	}
}

// TestPublishDenialRunsFetcherOnceAndDefers is the controlled PUBLISH-denial
// scenario: the lease is acquired and the fetch runs exactly once, but the
// publish (the lease-checked EVAL) is rejected. The load must surface the
// documented cache_unavailable deferral (not a panic, not a false success) and
// must not cache a stale value.
func TestPublishDenialRunsFetcherOnceAndDefers(t *testing.T) {
	_, a, _ := testCaches(t)
	var commands []string
	a.client.AddHook(denyCommands{deny: map[string]bool{"eval": true, "evalsha": true}, commands: &commands})
	var fetchCalls int
	ctx := context.Background()
	_, cached, err := a.LoadPage(ctx, "search", func(context.Context) (vehicle.ListingPage, error) {
		fetchCalls++
		return vehicle.ListingPage{Page: 1, NextCursor: "next"}, nil
	})
	if fetchCalls != 1 {
		t.Fatalf("fetcher called %d time(s) on publish denial, want exactly 1", fetchCalls)
	}
	if cached {
		t.Fatalf("publish-denied load reported a cache hit")
	}
	if !isSourceCacheUnavailable(err) {
		t.Fatalf("publish denial must defer to the documented cache_unavailable fallback, got: %v", err)
	}
	// A failed publish must not leave a cached payload behind.
	if _, ok := a.Get(ctx, "search"); ok {
		t.Fatalf("a denied publish left a cached value")
	}
	// (The lease release is not asserted here: this controlled scenario denies
	// every eval, including the release script, so a residual lease is a test
	// artifact, not the production contract. Release-only behavior is covered by
	// TestReleaseOnlyDeletesOwnLease.)
}

// isSourceCacheUnavailable reports whether err is the documented
// cache_unavailable SourceError (the load deferral on a failed lease or
// publish).
func isSourceCacheUnavailable(err error) bool {
	var se *ports.SourceError
	return errors.As(err, &se) && se != nil && se.Kind == "cache_unavailable"
}

func TestRejectedLeaseWriteNeverFetches(t *testing.T) {
	_, cache, _ := testCaches(t)
	var commands []string
	cache.client.AddHook(writeDenial{commands: &commands})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	calls := 0
	_, cached, err := cache.LoadPage(ctx, "lease-denied", func(context.Context) (vehicle.ListingPage, error) {
		calls++
		return vehicle.ListingPage{}, nil
	})
	if !isSourceCacheUnavailable(err) || cached || calls != 0 || ctx.Err() != nil {
		t.Fatalf("denied lease must defer promptly without fetching: error=%v cached=%v calls=%d", err, cached, calls)
	}
}
