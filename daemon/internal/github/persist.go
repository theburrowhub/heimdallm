package github

import (
	"fmt"
	"log/slog"
	"time"
)

// PersistedETag is one ETag cache entry as a CacheBackend stores it.
type PersistedETag struct {
	Key      string
	ETag     string
	Body     []byte
	StoredAt time.Time
}

// CacheBackend persists the conditional (ETag) cache so a daemon restart does
// not turn every warm poll back into a full 200 response. The github package
// only sees this interface; the SQLite store implements it. Implementations
// must be safe for concurrent use.
type CacheBackend interface {
	LoadETags() ([]PersistedETag, error)
	SaveETag(PersistedETag) error
	DeleteETag(key string) error
}

// DiffCacheBackend persists diffs keyed by the commits they span. Implementations
// must be safe for concurrent use and may drop entries at any time.
type DiffCacheBackend interface {
	GetDiff(key string) (body string, ok bool)
	PutDiff(key, body string)
}

// AttachCacheBackend loads the persisted ETag entries into the in-memory cache
// and writes every later store and eviction through to b. Load failures are
// returned but leave the cache usable (empty) and still attached, so the next
// responses repopulate it.
func (c *Client) AttachCacheBackend(b CacheBackend) error {
	if b == nil {
		return nil
	}
	entries, err := b.LoadETags()
	c.cache.load(entries)
	c.cache.setBackend(b)
	if err != nil {
		return fmt.Errorf("github: load etag cache: %w", err)
	}
	return nil
}

// AttachDiffCache enables the persistent diff cache.
func (c *Client) AttachDiffCache(b DiffCacheBackend) {
	if b == nil {
		c.diffs.Store(nil)
		return
	}
	c.diffs.Store(&b)
}

func (c *Client) diffCache() DiffCacheBackend {
	if p := c.diffs.Load(); p != nil {
		return *p
	}
	return nil
}

// FetchDiffAt returns the PR's diff like FetchDiff, served from the diff cache
// when the same (base, head) pair was fetched before. The PR diff depends on
// the merge base, so the key includes the base branch's tip as the Pulls API
// reported it: a moved base is a new key even if the merge base did not move,
// which only costs a re-download. Invalid or empty SHAs bypass the cache.
func (c *Client) FetchDiffAt(repo string, number int, baseSHA, headSHA string) (string, error) {
	cache := c.diffCache()
	if cache == nil || !commitSHAPattern.MatchString(baseSHA) || !commitSHAPattern.MatchString(headSHA) {
		return c.FetchDiff(repo, number)
	}
	key := fmt.Sprintf("pr\n%s#%d\n%s...%s", repo, number, baseSHA, headSHA)
	if body, ok := cache.GetDiff(key); ok {
		slog.Debug("github: diff served from cache", "repo", repo, "pr", number)
		return body, nil
	}
	diff, err := c.FetchDiff(repo, number)
	if err != nil {
		return "", err
	}
	cache.PutDiff(key, diff)
	return diff, nil
}

func compareCacheKey(repo, base, head string) string {
	return fmt.Sprintf("compare\n%s\n%s...%s", repo, base, head)
}
