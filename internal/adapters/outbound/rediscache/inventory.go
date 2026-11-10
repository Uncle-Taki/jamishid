package rediscache

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/redis/go-redis/v9"
)

// Revision is read from PostgreSQL before looking up cached results. Every listing
// mutation changes it in the same transaction, so worker/API restarts cannot lose invalidation.
func (c *Cache) GetInventory(ctx context.Context, key string) (crawljobs.InventoryResult, string) {
	cacheCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	raw, err := c.client.Get(cacheCtx, inventoryKey(key)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return crawljobs.InventoryResult{}, "miss"
		}
		return crawljobs.InventoryResult{}, "error"
	}
	var result crawljobs.InventoryResult
	if json.Unmarshal(raw, &result) != nil {
		// Corrupt payloads are cache errors, not legitimate misses.
		return crawljobs.InventoryResult{}, "error"
	}
	return result, "hit"
}
func (c *Cache) SetInventory(ctx context.Context, key string, result crawljobs.InventoryResult) string {
	raw, err := json.Marshal(result)
	if err != nil {
		return "error"
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if err := c.client.Set(cacheCtx, inventoryKey(key), raw, 2*time.Minute).Err(); err != nil {
		return "error"
	}
	return "ok"
}
func inventoryKey(key string) string {
	return fmt.Sprintf("%sinventory:v1:%x", prefix, sha256.Sum256([]byte(key)))
}
