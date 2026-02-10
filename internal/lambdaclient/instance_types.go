package lambdaclient

import (
	"context"
	"sync"
	"time"
)

// InstanceTypeCache caches instance types with TTL.
type InstanceTypeCache struct {
	client *Client
	TTL    time.Duration

	mu       sync.Mutex
	cachedAt time.Time
	cache    map[string]InstanceTypesItem
}

func NewInstanceTypeCache(client *Client, ttl time.Duration) *InstanceTypeCache {
	return &InstanceTypeCache{
		client: client,
		TTL:    ttl,
	}
}

func (c *InstanceTypeCache) Get(ctx context.Context) (map[string]InstanceTypesItem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if time.Since(c.cachedAt) < c.TTL && len(c.cache) > 0 {
		return c.cache, nil
	}

	items, err := c.client.ListInstanceTypes(ctx)
	if err != nil {
		return nil, err
	}
	c.cache = items
	c.cachedAt = time.Now()
	return items, nil
}
