package client

import (
	"context"
	"strings"
	"sync"
	"time"
)

// listCache keeps list reads for a while. Concurrent reads of one key wait for
// a single fetch, so Terraform's parallel refresh of many resources in one
// repository sends one request.
type listCache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*cacheEntry
}

type cacheEntry struct {
	// fetching serializes the fetch of this key.
	fetching sync.Mutex
	// value and expires are guarded by listCache.mu.
	value   any
	expires time.Time
	valid   bool
}

func newListCache(ttl time.Duration, now func() time.Time) *listCache {
	return &listCache{ttl: ttl, now: now, entries: map[string]*cacheEntry{}}
}

func (c *listCache) entry(key string) *cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		e = &cacheEntry{}
		c.entries[key] = e
	}
	return e
}

func (c *listCache) lookup(e *cacheEntry) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.valid && c.now().Before(e.expires) {
		return e.value, true
	}
	return nil, false
}

// get returns the cached value of key, or fetches and caches it. Errors are not cached.
func (c *listCache) get(ctx context.Context, key string, fetch func(context.Context) (any, error)) (any, error) {
	e := c.entry(key)
	if value, ok := c.lookup(e); ok {
		return value, nil
	}
	e.fetching.Lock()
	defer e.fetching.Unlock()
	if value, ok := c.lookup(e); ok {
		return value, nil
	}
	value, err := fetch(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	// An invalidation while fetching replaced the entry: do not resurrect stale data.
	if c.entries[key] == e {
		e.value, e.expires, e.valid = value, c.now().Add(c.ttl), true
	}
	c.mu.Unlock()
	return value, nil
}

// invalidate drops every key that starts with prefix.
func (c *listCache) invalidate(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if strings.HasPrefix(key, prefix) {
			delete(c.entries, key)
		}
	}
}
