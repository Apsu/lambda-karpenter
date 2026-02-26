package lambdaclient

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// InstanceListCache provides a short-TTL cache for ListInstances, coalescing
// concurrent calls with singleflight.
type InstanceListCache struct {
	client *Client
	TTL    time.Duration

	mu       sync.RWMutex
	cachedAt time.Time
	cache    []Instance
	group    singleflight.Group
}

func NewInstanceListCache(client *Client, ttl time.Duration) *InstanceListCache {
	return &InstanceListCache{
		client: client,
		TTL:    ttl,
	}
}

// Invalidate forces the next List call to refresh from the API.
func (c *InstanceListCache) Invalidate() {
	c.mu.Lock()
	c.cachedAt = time.Time{}
	c.mu.Unlock()
}

func (c *InstanceListCache) List(ctx context.Context) ([]Instance, error) {
	c.mu.RLock()
	if time.Since(c.cachedAt) < c.TTL && c.cache != nil {
		data := c.cache
		c.mu.RUnlock()
		return data, nil
	}
	stale := c.cache
	c.mu.RUnlock()

	result, err, _ := c.group.Do("list-instances", func() (any, error) {
		items, err := c.client.ListInstances(ctx)
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
		if stale != nil {
			return stale, nil
		}
		return nil, fmt.Errorf("instance list cache refresh failed: %w", err)
	}
	return result.([]Instance), nil
}
