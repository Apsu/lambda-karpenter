package lambdaclient

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// InstanceTypeCache caches instance types with TTL, singleflight coalescing,
// and stale-while-revalidate behavior.
type InstanceTypeCache struct {
	client *Client
	TTL    time.Duration

	mu       sync.RWMutex
	cachedAt time.Time
	cache    map[string]InstanceTypesItem
	group    singleflight.Group
}

func NewInstanceTypeCache(client *Client, ttl time.Duration) *InstanceTypeCache {
	return &InstanceTypeCache{
		client: client,
		TTL:    ttl,
	}
}

func (c *InstanceTypeCache) Get(ctx context.Context) (map[string]InstanceTypesItem, error) {
	c.mu.RLock()
	if time.Since(c.cachedAt) < c.TTL && len(c.cache) > 0 {
		data := c.cache
		c.mu.RUnlock()
		return data, nil
	}
	stale := c.cache
	c.mu.RUnlock()

	result, err, _ := c.group.Do("instance-types", func() (any, error) {
		items, err := c.client.ListInstanceTypes(ctx)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.cache = items
		c.cachedAt = time.Now()
		c.mu.Unlock()
		return items, nil
	})

	if err != nil {
		// Stale-while-revalidate: return stale data on refresh failure.
		if len(stale) > 0 {
			return stale, nil
		}
		return nil, fmt.Errorf("instance type cache refresh failed: %w", err)
	}
	return result.(map[string]InstanceTypesItem), nil
}
