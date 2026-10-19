package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/heimdallm/daemon/internal/github"
)

// GitHub response caches. Both are namespaced by a fingerprint of the token
// that fetched the data: a different token may see different repositories, so
// its entries are never served to — and are purged on attach by — another.

const (
	// diffCacheMaxAge drops diffs nobody reviewed for two weeks; a PR still
	// open after that has almost certainly moved to new commits.
	diffCacheMaxAge = 14 * 24 * time.Hour
	// diffCacheMaxBytes bounds the on-disk diff cache.
	diffCacheMaxBytes = 256 * 1024 * 1024
	// diffCachePruneEvery runs the age/size prune once per this many writes.
	diffCachePruneEvery = 32
)

func ensureGitHubCacheSchema(db *sql.DB) {
	db.Exec(`CREATE TABLE IF NOT EXISTS gh_etag_cache (
		ns        TEXT NOT NULL,
		key       TEXT NOT NULL,
		etag      TEXT NOT NULL,
		body      BLOB NOT NULL,
		stored_at TEXT NOT NULL,
		PRIMARY KEY (ns, key)
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS gh_diff_cache (
		ns        TEXT NOT NULL,
		key       TEXT NOT NULL,
		body      BLOB NOT NULL,
		stored_at TEXT NOT NULL,
		PRIMARY KEY (ns, key)
	)`)
	db.Exec("CREATE INDEX IF NOT EXISTS idx_gh_diff_cache_stored ON gh_diff_cache(stored_at)")
}

// ETagCache is the SQLite github.CacheBackend for one token namespace.
type ETagCache struct {
	db *sql.DB
	ns string
}

// ETagCache returns the persistent ETag cache for namespace ns and drops the
// entries of every other namespace.
func (s *Store) ETagCache(ns string) *ETagCache {
	if _, err := s.db.Exec(`DELETE FROM gh_etag_cache WHERE ns != ?`, ns); err != nil {
		slog.Warn("store: purge foreign etag cache entries", "err", err)
	}
	return &ETagCache{db: s.db, ns: ns}
}

var _ github.CacheBackend = (*ETagCache)(nil)

func (c *ETagCache) LoadETags() ([]github.PersistedETag, error) {
	rows, err := c.db.Query(`SELECT key, etag, body, stored_at FROM gh_etag_cache WHERE ns = ?`, c.ns)
	if err != nil {
		return nil, fmt.Errorf("store: load etag cache: %w", err)
	}
	defer rows.Close()
	var out []github.PersistedETag
	for rows.Next() {
		var e github.PersistedETag
		var storedAt string
		if err := rows.Scan(&e.Key, &e.ETag, &e.Body, &storedAt); err != nil {
			return out, fmt.Errorf("store: scan etag cache: %w", err)
		}
		e.StoredAt, _ = time.Parse(sqliteTimeFormat, storedAt)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (c *ETagCache) SaveETag(e github.PersistedETag) error {
	_, err := c.db.Exec(`INSERT INTO gh_etag_cache (ns, key, etag, body, stored_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(ns, key) DO UPDATE SET etag = excluded.etag, body = excluded.body, stored_at = excluded.stored_at`,
		c.ns, e.Key, e.ETag, e.Body, e.StoredAt.UTC().Format(sqliteTimeFormat))
	if err != nil {
		return fmt.Errorf("store: save etag cache: %w", err)
	}
	return nil
}

func (c *ETagCache) DeleteETag(key string) error {
	if _, err := c.db.Exec(`DELETE FROM gh_etag_cache WHERE ns = ? AND key = ?`, c.ns, key); err != nil {
		return fmt.Errorf("store: delete etag cache: %w", err)
	}
	return nil
}

// DiffCache is the SQLite github.DiffCacheBackend for one token namespace.
type DiffCache struct {
	db     *sql.DB
	ns     string
	writes atomic.Int64
	// maxAge and maxBytes default to diffCacheMaxAge / diffCacheMaxBytes.
	maxAge   time.Duration
	maxBytes int64
}

// DiffCache returns the persistent diff cache for namespace ns and drops the
// entries of every other namespace.
func (s *Store) DiffCache(ns string) *DiffCache {
	if _, err := s.db.Exec(`DELETE FROM gh_diff_cache WHERE ns != ?`, ns); err != nil {
		slog.Warn("store: purge foreign diff cache entries", "err", err)
	}
	return &DiffCache{db: s.db, ns: ns, maxAge: diffCacheMaxAge, maxBytes: diffCacheMaxBytes}
}

var _ github.DiffCacheBackend = (*DiffCache)(nil)

func (c *DiffCache) GetDiff(key string) (string, bool) {
	var body []byte
	err := c.db.QueryRow(`SELECT body FROM gh_diff_cache WHERE ns = ? AND key = ?`, c.ns, key).Scan(&body)
	if err != nil {
		if err != sql.ErrNoRows {
			slog.Debug("store: read diff cache", "err", err)
		}
		return "", false
	}
	return string(body), true
}

func (c *DiffCache) PutDiff(key, body string) {
	_, err := c.db.Exec(`INSERT INTO gh_diff_cache (ns, key, body, stored_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(ns, key) DO UPDATE SET body = excluded.body, stored_at = excluded.stored_at`,
		c.ns, key, []byte(body), time.Now().UTC().Format(sqliteTimeFormat))
	if err != nil {
		slog.Debug("store: write diff cache", "err", err)
		return
	}
	if c.writes.Add(1)%diffCachePruneEvery == 1 {
		c.Prune()
	}
}

// Prune drops entries older than the max age, then the oldest entries until
// the cache fits its byte budget.
func (c *DiffCache) Prune() {
	cutoff := time.Now().Add(-c.maxAge).UTC().Format(sqliteTimeFormat)
	if _, err := c.db.Exec(`DELETE FROM gh_diff_cache WHERE stored_at < ?`, cutoff); err != nil {
		slog.Debug("store: prune diff cache by age", "err", err)
		return
	}
	var total sql.NullInt64
	if err := c.db.QueryRow(`SELECT SUM(length(body)) FROM gh_diff_cache`).Scan(&total); err != nil || total.Int64 <= c.maxBytes {
		return
	}
	rows, err := c.db.Query(`SELECT ns, key, length(body) FROM gh_diff_cache ORDER BY stored_at ASC`)
	if err != nil {
		return
	}
	type victim struct{ ns, key string }
	var victims []victim
	excess := total.Int64 - c.maxBytes
	for rows.Next() && excess > 0 {
		var v victim
		var n int64
		if rows.Scan(&v.ns, &v.key, &n) != nil {
			break
		}
		victims = append(victims, v)
		excess -= n
	}
	rows.Close()
	for _, v := range victims {
		c.db.Exec(`DELETE FROM gh_diff_cache WHERE ns = ? AND key = ?`, v.ns, v.key)
	}
}
