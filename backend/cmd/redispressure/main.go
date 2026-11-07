// Command redispressure is a disposable OOM-resilience acceptance harness for
// the shared cursor signing key, the cache fallback, and the write probe. It is
// not a production entrypoint.
//
// run.sh is the only supported launcher. It creates an isolated Redis on a
// private Unix socket and seeds a random marker. All ownership checks are
// read-only; an unknown target is rejected before any application-key mutation.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/rediscache"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/redis/go-redis/v9"
)

const signingKey = "jamshid:cache:v1:cursor-signing-key"

const (
	pressureMarkerPrefix = "pressure:marker:"
	fillerPrefix         = "pressure:filler:"
	budgetPacingKey      = "jamshid:cache:v1:budget:divar:interval"
)

func fail(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(1)
}
func pass(format string, args ...any) { fmt.Printf("PASS: "+format+"\n", args...) }

func main() {
	raw := os.Getenv("REDIS_URL")
	expected, err := ownershipFromEnv()
	if err != nil {
		fail("%v", err)
	}
	adminOpts, err := targetOptions(raw, expected)
	if err != nil {
		fail("refusing target: %v", err)
	}
	adminOpts.DialTimeout = time.Second
	adminOpts.ReadTimeout = time.Second
	adminOpts.WriteTimeout = time.Second
	adminOpts.ContextTimeoutEnabled = true
	adminOpts.MaxRetries = -1
	admin := redis.NewClient(adminOpts)
	defer admin.Close()
	c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := verifyOwnership(c, admin, expected); err != nil {
		fail("refusing target: %v", err)
	}
	maxmem, _ := strconv.Atoi(expected.maxmemory) // range-checked by ownershipFromEnv

	cache, err := rediscache.New(raw, 24*time.Hour, nil)
	if err != nil {
		fail("cache: %v", err)
	}
	defer cache.Close()

	// Seed a no-TTL budget pacing key BEFORE the fill so the OOM phase can prove
	// this critical coordination state survives a full instance.
	if err := admin.Set(c, budgetPacingKey, "300", 0).Err(); err != nil {
		fail("seed budget pacing key: %v", err)
	}
	coordination := map[string]string{
		"jamshid:cache:v1:budget:divar:cooldown": "1",
		"jamshid:cache:v1:budget:divar:slot":     "1",
		"jamshid:cache:v1:page:pressure:lease":   "synthetic-owner",
	}
	for name, value := range coordination {
		if err := admin.Set(c, name, value, 2*time.Minute).Err(); err != nil {
			fail("seed coordination fixture: %v", err)
		}
	}

	// --- Healthy instance ------------------------------------------
	// Case 0: fresh initialization persists a 32-byte durable key.
	key, err := cache.CursorSigningKey(c)
	if err != nil || len(key) != 32 {
		fail("case 0 init: %v", err)
	}
	pass("case 0: fresh initialization persists a 32-byte durable key")

	// Case 7a: a restarted process reuses the identical key. The real
	// previously-issued-cursor-survives contract is proven by a Go test that
	// uses the application cursor code; here we assert key identity across a
	// fresh client (restart) without any write.
	restarted, err := rediscache.New(raw, 24*time.Hour, nil)
	if err != nil {
		fail("restart client: %v", err)
	}
	again, err := restarted.CursorSigningKey(c)
	restarted.Close()
	if err != nil || !bytes.Equal(key, again) {
		fail("case 7a restart: %v", err)
	}
	pass("case 7a: fresh client reuses the identical durable key")

	// Case 3: an invalid stored key is refused and left in place, never
	// silently rotated.
	admin.Set(c, signingKey, "corrupt-not-hex", 0)
	if _, err := cache.CursorSigningKey(c); err == nil {
		fail("case 3: corrupt signing key accepted")
	}
	if got, _ := admin.Get(c, signingKey).Result(); got != "corrupt-not-hex" {
		fail("case 3: corrupt key was rotated to %q", got)
	}
	pass("case 3: invalid stored key is refused without silent rotation")
	admin.Set(c, signingKey, hex.EncodeToString(key), 0)

	// Case 2: concurrent first initializations converge on one key.
	admin.Del(c, signingKey)
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client, err := rediscache.New(raw, 24*time.Hour, nil)
			if err != nil {
				mu.Lock()
				defer mu.Unlock()
				fail("case 2 client: %v", err)
			}
			defer client.Close()
			k, err := client.CursorSigningKey(c)
			if err != nil {
				mu.Lock()
				defer mu.Unlock()
				fail("case 2 init: %v", err)
			}
			mu.Lock()
			seen[string(k)] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != 1 {
		fail("case 2: concurrent initializers diverged into %d keys", len(seen))
	}
	pass("case 2: 12 concurrent first initializations converge on one key")

	// Case 7b: after concurrent init, reads return the winner. Rebind the
	// reference to the live durable key so the OOM cases compare against what
	// is actually stored (the concurrent winner, not the first key).
	if got, err := cache.CursorSigningKey(c); err != nil || !seen[string(got)] {
		fail("case 7b: post-concurrent read diverged: %v", err)
	} else {
		key = got
	}

	// --- Fill the instance to its real noeviction limit -------------
	fillCtx, fillCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer fillCancel()
	// Filler blocks match the write-probe payload (64 KB, in
	// rediscache.monitoring). Filling until a write of THAT size fails puts the
	// headroom strictly below the probe size, so case 8's probe is rejected
	// deterministically rather than flapping on the memory boundary.
	const blockBytes = 64 * 1024 // must equal rediscache.writeProbePayload
	maxBlocks := (maxmem / blockBytes) + 1
	if maxBlocks < 2 {
		maxBlocks = 2
	}
	fillerBytes := 0
	oomSeen := false
	for i := 0; i < maxBlocks; i++ {
		name := fmt.Sprintf("%s%04d", fillerPrefix, i)
		err := admin.Set(fillCtx, name, bytes.Repeat([]byte{1}, blockBytes), 0).Err()
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "oom") {
				oomSeen = true
				break
			}
			fail("fill: unexpected error: %v", err)
		}
		fillerBytes += blockBytes
	}
	if !oomSeen {
		fail("fill: no OOM after %d MB of filler; raise the instance maxmemory or check policy", fillerBytes/(1024*1024))
	}
	info, _ := admin.Info(fillCtx, "memory").Result()
	pass("instance filled to its noeviction limit (real OOM, %d MB filler): %s", fillerBytes/(1024*1024), firstKV(info, "used_memory_human"))

	// The fill consumed the 3s baseline deadline; the OOM-phase cases get a
	// fresh one.
	oCtx, oCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer oCancel()
	c = oCtx

	// Case 1: existing signing key + real OOM => reuse succeeds. The old
	// write-first implementation would attempt SetNX and fail here; the
	// read-first path issues only a GET and returns the stored key unchanged,
	// in bounded time (a full instance must not stall startup).
	start := time.Now()
	got, err := cache.CursorSigningKey(c)
	elapsed := time.Since(start)
	if err != nil || !bytes.Equal(got, key) {
		fail("case 1: reuse under real OOM: %v", err)
	}
	if elapsed > 3*time.Second {
		fail("case 1: reuse under OOM took too long (would stall startup): %s", elapsed)
	}
	pass("case 1: existing key reused under real OOM, not rotated, in %s", elapsed.Round(time.Millisecond))

	// Case 4: signing and coordination state survived; noeviction never
	// evicts, so a full instance rejects instead of deleting. The pre-fill
	// budget pacing key (no TTL) must still be readable unchanged.
	if got, _ := admin.Get(c, signingKey).Result(); got != hex.EncodeToString(key) {
		fail("case 4: signing key altered under pressure")
	}
	if got, _ := admin.Get(c, budgetPacingKey).Result(); got != "300" {
		fail("case 4: budget pacing key lost or altered under pressure")
	}
	for name, value := range coordination {
		if got, err := admin.Get(c, name).Result(); err != nil || got != value {
			fail("case 4: coordination fixture lost or changed")
		}
		if ttl := admin.PTTL(c, name).Val(); ttl <= 0 || ttl > 2*time.Minute {
			fail("case 4: coordination fixture expiry changed")
		}
	}
	if got := firstKV(admin.Info(c, "stats").Val(), "evicted_keys"); got != "0" {
		fail("case 4: evictions under noeviction: %s", got)
	}
	pass("case 4: signing key, pacing, cooldown, slot and lease survived real OOM; evicted_keys=0")

	// Case 6: under real OOM a cache load must not report a cache hit, must
	// return in bounded time, and the fetcher must run at most once (no endless
	// fetch/retry loop). The final publish may fit in residual free bytes
	// (success) or be deferred to the documented cache_unavailable fallback;
	// both are correct, bounded outcomes. A spurious cache hit, a hang, or an
	// undocumented error is not.
	var fetchCalls int32
	loadStart := time.Now()
	page, cached, loadErr := cache.LoadPage(c, "pressure:page", func(context.Context) (vehicle.ListingPage, error) {
		fetchCalls++
		time.Sleep(20 * time.Millisecond)
		return vehicle.ListingPage{Page: 1}, nil
	})
	loadElapsed := time.Since(loadStart)
	if cached {
		fail("case 6: OOM load reported a cache hit")
	}
	if loadElapsed > 10*time.Second {
		fail("case 6: OOM load hung: %s", loadElapsed)
	}
	if fetchCalls > 1 {
		fail("case 6: OOM load looped the fetcher %d times", fetchCalls)
	}
	if loadErr != nil && !isCacheUnavailable(loadErr) {
		fail("case 6: undocumented OOM load error: %v", loadErr)
	}
	if loadErr == nil && (fetchCalls != 1 || page.Page != 1) {
		fail("case 6: successful load did not return the fresh fetched value")
	}
	pass("case 6: OOM load bounded (%s), no spurious hit, fetcher called %d time(s), outcome=%s",
		loadElapsed.Round(time.Millisecond), fetchCalls, outcomeName(loadErr))

	// Case 5b is intentionally NOT a harness case: near the memory limit a
	// small lease may fit while a payload does not. Thus a controlled "publish
	// denial" is not deterministically reachable here — its outcome depends
	// on incidental free bytes at the boundary (exactly what the review rejected).
	// The controlled lease-denial (fetcher never runs) and publish-denial (fetch
	// runs once, publish rejected) scenarios are proven deterministically in the
	// rediscache Go tests, where a hook can deny either the lease or the publish
	// while the instance is otherwise healthy.

	// Case 8: the write probe reports rejection while the instance still
	// answers reads, through the real MonitoringStats path.
	stats, err := cache.MonitoringStats(c)
	if err != nil || !stats.Up {
		fail("case 8: INFO must still succeed under OOM: %v", err)
	}
	if stats.WriteProbeOK {
		fail("case 8: write probe reported accepted under real OOM")
	}
	if pingErr := admin.Ping(c).Err(); pingErr != nil {
		fail("case 8: instance unreachable: %v", pingErr)
	}
	pass("case 8: reachable-but-OOM: up=true, write probe rejected (jamshid_redis_write_accepts=0)")

	// --- Cleanup the disposable pressure keys -----------------------
	// noeviction permits DEL of existing keys, so we can release the filler we
	// added and restore the instance to (near) empty for the launcher to tear
	// down.
	for _, k := range admin.Keys(c, fillerPrefix+"*").Val() {
		admin.Del(c, k)
	}
	admin.Del(c, budgetPacingKey)
	admin.Del(c, signingKey)
	pass("acceptance complete: launcher will destroy this private disposable instance")
}

func isCacheUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var se *ports.SourceError
	return errors.As(err, &se) && se != nil && se.Kind == "cache_unavailable"
}

func outcomeName(err error) string {
	if err == nil {
		return "success"
	}
	if isCacheUnavailable(err) {
		return "cache_unavailable(deferred)"
	}
	return "other"
}

func firstKV(info, field string) string {
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, field+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, field+":"))
		}
	}
	return "<missing>"
}
