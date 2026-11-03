package rediscache

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"time"
)

// Revision is read from PostgreSQL before looking up cached results. Every listing
// mutation changes it in the same transaction, so worker/API restarts cannot lose invalidation.
func (c *Cache) GetInventory(ctx context.Context, key string) (crawljobs.InventoryResult, bool) {
	cacheCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	raw, err := c.client.Get(cacheCtx, inventoryKey(key)).Bytes()
	if err != nil {
		return crawljobs.InventoryResult{}, false
	}
	var result crawljobs.InventoryResult
	if json.Unmarshal(raw, &result) != nil {
		return result, false
	}
	return result, true
}
func (c *Cache) SetInventory(ctx context.Context, key string, result crawljobs.InventoryResult) {
	raw, err := json.Marshal(result)
	if err != nil {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_ = c.client.Set(cacheCtx, inventoryKey(key), raw, 2*time.Minute).Err()
}
func inventoryKey(key string) string {
	return fmt.Sprintf("%sinventory:v1:%x", prefix, sha256.Sum256([]byte(key)))
}
