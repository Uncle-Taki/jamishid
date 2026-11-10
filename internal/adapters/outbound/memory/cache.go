package memory

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type cacheItem struct {
	data      vehicle.ListingPage
	expiresAt time.Time
}

type Cache struct {
	items map[string]cacheItem
	mu    sync.RWMutex
	ttl   time.Duration
	now   func() time.Time
}

const maxCacheEntries = 1000

func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		items: make(map[string]cacheItem),
		ttl:   ttl,
		now:   time.Now,
	}
}

func (c *Cache) Get(ctx context.Context, key string) (vehicle.ListingPage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item, found := c.items[key]

	if !found {
		return vehicle.ListingPage{}, false
	}

	if !c.now().Before(item.expiresAt) {
		delete(c.items, key)
		return vehicle.ListingPage{}, false
	}

	return clonePage(item.data), true
}

func (c *Cache) Set(ctx context.Context, key string, page vehicle.ListingPage) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if _, exists := c.items[key]; !exists && len(c.items) >= maxCacheEntries {
		c.cleanup(now)
		if len(c.items) >= maxCacheEntries {
			// Keep the process bounded during a day of distinct searches.
			var oldest string
			var expires time.Time
			for key, item := range c.items {
				if expires.IsZero() || item.expiresAt.Before(expires) {
					oldest, expires = key, item.expiresAt
				}
			}
			delete(c.items, oldest)
		}
	}
	c.items[key] = cacheItem{
		data:      clonePage(page),
		expiresAt: now.Add(c.ttl),
	}

	return nil
}

func (c *Cache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cleanup(c.now())
}

func (c *Cache) cleanup(now time.Time) {
	for key, item := range c.items {
		if !now.Before(item.expiresAt) {
			delete(c.items, key)
		}
	}
}

func clonePage(page vehicle.ListingPage) vehicle.ListingPage {
	page.Listings = slices.Clone(page.Listings)
	for i := range page.Listings {
		page.Listings[i].Attributes = slices.Clone(page.Listings[i].Attributes)
	}
	return page
}
