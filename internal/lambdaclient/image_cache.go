package lambdaclient

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ImageCache caches images with TTL, singleflight coalescing,
// and stale-while-revalidate behavior.
type ImageCache struct {
	client *Client
	TTL    time.Duration

	mu       sync.RWMutex
	cachedAt time.Time
	cache    []Image
	group    singleflight.Group
}

func NewImageCache(client *Client, ttl time.Duration) *ImageCache {
	return &ImageCache{
		client: client,
		TTL:    ttl,
	}
}

// Seed pre-populates the cache with the given data. Used in tests.
func (c *ImageCache) Seed(data []Image) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = data
	c.cachedAt = time.Now()
}

// ListImages returns all cached images, refreshing if expired.
// Satisfies the controller.ImageResolver interface.
func (c *ImageCache) ListImages(ctx context.Context) ([]Image, error) {
	return c.Get(ctx)
}

func (c *ImageCache) Get(ctx context.Context) ([]Image, error) {
	c.mu.RLock()
	if time.Since(c.cachedAt) < c.TTL && len(c.cache) > 0 {
		data := c.cache
		c.mu.RUnlock()
		return data, nil
	}
	stale := c.cache
	c.mu.RUnlock()

	result, err, _ := c.group.Do("images", func() (any, error) {
		items, err := c.client.ListImages(ctx)
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
		return nil, fmt.Errorf("image cache refresh failed: %w", err)
	}
	return result.([]Image), nil
}
