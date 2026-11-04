package rediscache

import (
	"context"
	"strconv"
)

// MonitoringStats is a bounded read-only snapshot of Redis server state and the
// shared source budget cooldowns. Cooldowns are observed pacing state, never
// provider quota remaining.
type MonitoringStats struct {
	Up         bool
	UsedMemory float64
	MaxMemory  float64
	Hits       float64
	Misses     float64
	Evicted    float64
	Clients    float64
	// Cooldowns maps each marketplace source to its observed shared budget
	// cooldown remaining in seconds.
	Cooldowns map[string]float64
}

// Sources are the fixed marketplace budget namespaces used by the scrapers.
var Sources = []string{"divar", "bama", "sheypoor"}

func (c *Cache) MonitoringStats(ctx context.Context) (MonitoringStats, error) {
	stats := MonitoringStats{Cooldowns: map[string]float64{}}
	info, err := c.client.Info(ctx, "memory", "stats", "clients").Result()
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
	return stats, nil
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
