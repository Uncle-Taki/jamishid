package rediscache

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"
)

// writeProbePayload is the canary write size for the per-poll write probe. It
// is sized near the dominant application writes (about 32 KB page payloads) so
// the probe starts rejecting at the same memory pressure the application feels,
// not only when the instance is nearly completely full. A 1-byte probe would
// report "healthy" while real payload writes are already failing.
const writeProbePayload = 64 * 1024

// writeProbeKeyPrefix targets the per-poll write probe. A FRESH key is used on
// purpose: overwriting an existing same-size key would fit even on a full
// instance (no new allocation), so the probe would never reject. Adding a fresh
// key forces a real allocation that noeviction rejects at the limit. The short
// TTL self-cleans the key if the process dies between the probe SET and DEL.
const writeProbeKeyPrefix = prefix + "monitoring:write-probe:"

// MonitoringStats reads server state and source budget cooldowns and performs
// a bounded canary write. Cooldowns are pacing state, never provider quota.
type MonitoringStats struct {
	Up         bool
	UsedMemory float64
	MaxMemory  float64
	Hits       float64
	Misses     float64
	Evicted    float64
	Clients    float64
	// WriteProbeOK marks whether the last per-poll probe write was accepted. A
	// full instance under noeviction answers INFO and PING while still
	// rejecting writes, so Up alone cannot expose memory pressure.
	WriteProbeOK bool
	// Cooldowns maps each marketplace source to its observed shared budget
	// cooldown remaining in seconds.
	Cooldowns map[string]float64
}

// Sources are the fixed marketplace budget namespaces used by the scrapers.
var Sources = []string{"divar", "bama", "sheypoor"}

func (c *Cache) MonitoringStats(ctx context.Context) (MonitoringStats, error) {
	stats := MonitoringStats{Cooldowns: map[string]float64{}}
	info, err := c.client.Info(ctx, "memory", "stats", "clients").Result()
	if err != nil && strings.Contains(err.Error(), "ERR wrong number of arguments") {
		info, err = c.client.Info(ctx).Result()
	}
	if err != nil {
		return stats, err
	}
	fields := parseInfo(info)
	stats.Up = true
	stats.UsedMemory = fields["used_memory"]
	stats.MaxMemory = fields["maxmemory"]
	stats.Hits = fields["keyspace_hits"]
	stats.Misses = fields["keyspace_misses"]
	stats.Evicted = fields["evicted_keys"]
	stats.Clients = fields["connected_clients"]
	for _, source := range Sources {
		ttl, err := c.client.PTTL(ctx, budgetKeys(source)[0]).Result()
		if err != nil || ttl < 0 {
			// No cooldown key or already expired: healthy pacing.
			stats.Cooldowns[source] = 0
			continue
		}
		stats.Cooldowns[source] = ttl.Seconds()
	}
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	stats.WriteProbeOK = c.probeWrite(probeCtx) == nil
	cancel()
	return stats, nil
}

// probeWrite performs one bounded write that forces a real allocation (a fresh
// key of representative payload size), then deletes it. This observes one
// canary outcome; other payload sizes or concurrent writes can behave differently. It never
// touches application keys, so it cannot delete or evict signing or
// coordination state. The probe key self-expires via its TTL.
func (c *Cache) probeWrite(ctx context.Context) error {
	name := writeProbeKeyPrefix + strconv.FormatInt(time.Now().UnixMilli(), 10)
	if err := c.probeClient.Set(ctx, name, bytes.Repeat([]byte{1}, writeProbePayload), 60*time.Second).Err(); err != nil {
		return err
	}
	// Best-effort cleanup; the TTL is the backstop if the process dies in
	// between. A DEL failure does not mean the write was not accepted.
	_ = c.probeClient.Del(ctx, name).Err()
	return nil
}

// parseInfo converts the Redis INFO text response into a flat numeric map.
// Missing or non-numeric fields become 0.
func parseInfo(info string) map[string]float64 {
	result := map[string]float64{}
	for _, line := range splitLines(info) {
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		for i := 0; i < len(line); i++ {
			if line[i] == ':' {
				value, err := strconv.ParseFloat(line[i+1:], 64)
				if err == nil {
					result[line[:i]] = value
				}
				break
			}
		}
	}
	return result
}

func splitLines(info string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(info); i++ {
		if info[i] == '\n' {
			end := i
			if end > start && info[end-1] == '\r' {
				end--
			}
			lines = append(lines, info[start:end])
			start = i + 1
		}
	}
	if start < len(info) {
		lines = append(lines, info[start:])
	}
	return lines
}
