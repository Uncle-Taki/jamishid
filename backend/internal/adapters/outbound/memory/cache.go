package memory

import (
	"context"
	"sync"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type cacheItem struct {
	data      []vehicle.ScoredListing
	expiresAt time.Time
}

type Cache struct {
	items map[string]cacheItem
	mu    sync.RWMutex
	ttl   time.Duration
}

func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		items: make(map[string]cacheItem),
		ttl:   ttl,
	}
}

func (c *Cache) Get(ctx context.Context, key string) ([]vehicle.ScoredListing, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, found := c.items[key]
	if !found {
		return nil, false
	}

	if time.Now().After(item.expiresAt) {
		return nil, false
	}

	return item.data, true
}

func (c *Cache) Set(ctx context.Context, key string, listings []vehicle.ScoredListing) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = cacheItem{
		data:      listings,
		expiresAt: time.Now().Add(c.ttl),
	}

	return nil
}
