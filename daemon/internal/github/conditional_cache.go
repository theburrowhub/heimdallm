package github

import (
	"sync"
	"sync/atomic"
	"time"
)

// maxCacheEntries is the maximum number of entries the ConditionalCache will
// hold before evicting the oldest entries. This bounds memory usage to
// roughly maxCacheEntries × average-body-size (≤1 MiB each in practice).
const maxCacheEntries = 2000

// condEntry stores the ETag and response body for one cached GET response.
type condEntry struct {
	etag     string
	body     []byte
	storedAt time.Time
}

// ConditionalCache stores ETag/body pairs keyed by request path so that
// repeated GET requests can be made conditional (If-None-Match). Thread-safe.
type ConditionalCache struct {
	mu      sync.RWMutex
	entries map[string]condEntry

	// Observability counters — incremented atomically from doWithBody.
	cacheHits   atomic.Int64
	cacheMisses atomic.Int64
}

// NewConditionalCache returns an initialised, empty ConditionalCache.
func NewConditionalCache() *ConditionalCache {
	return &ConditionalCache{
		entries: make(map[string]condEntry),
	}
}

// Get retrieves the stored etag and body for key.
// Returns ok=false when no entry exists.
func (c *ConditionalCache) Get(key string) (etag string, body []byte, ok bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return "", nil, false
	}
	return e.etag, e.body, true
}

// Put stores or updates the etag and body for key.
// When the cache is at capacity it evicts the single oldest entry first.
func (c *ConditionalCache) Put(key, etag string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict the oldest entry if we're at the cap and this is a new key.
	if _, exists := c.entries[key]; !exists && len(c.entries) >= maxCacheEntries {
		c.evictOldestLocked()
	}
	c.entries[key] = condEntry{
		etag:     etag,
		body:     body,
		storedAt: time.Now(),
	}
}

// evictOldestLocked removes the entry with the earliest storedAt timestamp.
// Caller must hold c.mu.Lock().
func (c *ConditionalCache) evictOldestLocked() {
	var oldestKey string
	var oldestTime time.Time
	for k, e := range c.entries {
		if oldestKey == "" || e.storedAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = e.storedAt
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// CacheHits returns the number of 304-converted responses since the cache was created.
func (c *ConditionalCache) CacheHits() int64 { return c.cacheHits.Load() }

// CacheMisses returns the number of 200 responses that were stored since creation.
func (c *ConditionalCache) CacheMisses() int64 { return c.cacheMisses.Load() }

// cacheKey builds the lookup key for a given path (which already includes
// any query string). The method parameter is retained in the signature for
// clarity but the cache is only used for GET; the key itself is just the path
// because the method is always "GET" at call sites.
func cacheKey(_ string, path string) string {
	return path
}
