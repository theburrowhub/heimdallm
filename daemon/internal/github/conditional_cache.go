package github

import (
	"sync"
	"sync/atomic"
	"time"
)

// maxCacheEntries is the maximum number of entries the ConditionalCache will
// hold before evicting the oldest entries.
const maxCacheEntries = 2000

// maxCacheBytes is the aggregate body budget across all entries. The entry
// count alone does not bound memory: at the ≤1 MiB per-body ceiling,
// maxCacheEntries would permit ~2 GiB resident in a daemon that runs for weeks.
// Whichever limit is reached first drives eviction.
const maxCacheBytes = 128 * 1024 * 1024 // 128 MiB

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
	// bytes is the running sum of len(body) across entries, maintained under
	// mu so eviction can enforce maxCacheBytes without walking the map.
	bytes int

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

	// Replacing a key releases its old body before the new one is accounted.
	if prev, exists := c.entries[key]; exists {
		c.bytes -= len(prev.body)
		delete(c.entries, key)
	}

	// Evict oldest-first until both limits accommodate the incoming entry.
	// The byte budget is checked in a loop because one large body can displace
	// several small ones. The guard on len(c.entries) keeps this terminating
	// even if a single body somehow exceeds the whole budget — in that case the
	// cache ends up holding just that entry rather than spinning.
	for len(c.entries) >= maxCacheEntries ||
		(len(c.entries) > 0 && c.bytes+len(body) > maxCacheBytes) {
		c.evictOldestLocked()
	}

	c.entries[key] = condEntry{
		etag:     etag,
		body:     body,
		storedAt: time.Now(),
	}
	c.bytes += len(body)
}

// Bytes returns the aggregate size of all cached bodies. Intended for
// observability and tests.
func (c *ConditionalCache) Bytes() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.bytes
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
		c.bytes -= len(c.entries[oldestKey].body)
		delete(c.entries, oldestKey)
	}
}

// CacheHits returns the number of 304-converted responses since the cache was created.
func (c *ConditionalCache) CacheHits() int64 { return c.cacheHits.Load() }

// CacheMisses returns the number of 200 responses that were stored since creation.
func (c *ConditionalCache) CacheMisses() int64 { return c.cacheMisses.Load() }

// cacheKey builds the lookup key for a given GET request. The cache is keyed
// by both the Accept header and the path (including any query string) because
// the same path can legitimately be requested with different Accept values that
// produce different response bodies — for example, /repos/{}/pulls/{n} is
// fetched as application/vnd.github+json by getPR/GetPRSnapshot and as
// application/vnd.github.v3.diff by FetchDiff. Path-only keying caused cache
// collisions between those two callers (the diff body was served for the JSON
// request or vice-versa). The method parameter is retained in the signature for
// symmetry with its callers but is always "GET" at every call site.
func cacheKey(_ string, accept, path string) string {
	return accept + "\n" + path
}
