// daemon/internal/bus/watch.go
package bus

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	InitialBackoff = 1 * time.Minute
	MaxBackoff     = 15 * time.Minute
	EvictAfter     = 1 * time.Hour
)

// watchStateSchema is applied on construction to create the table if needed.
const watchStateSchema = `
CREATE TABLE IF NOT EXISTS watch_state (
    key        TEXT PRIMARY KEY,
    type       TEXT NOT NULL,
    repo       TEXT NOT NULL,
    number     INTEGER NOT NULL,
    github_id  INTEGER NOT NULL,
    next_check TEXT NOT NULL,
    backoff_ns INTEGER NOT NULL,
    last_seen  TEXT NOT NULL
);
`

// WatchEntry represents a monitored PR or issue tracked in the watch_state table.
type WatchEntry struct {
	Type      string    `json:"type"`
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	GithubID  int64     `json:"github_id"`
	NextCheck time.Time `json:"next_check"`
	BackoffNs int64     `json:"backoff_ns"`
	LastSeen  time.Time `json:"last_seen"`
}

// Backoff returns the current backoff duration.
func (e WatchEntry) Backoff() time.Duration { return time.Duration(e.BackoffNs) }

// Key returns the key for this entry (e.g. "pr.12345").
func (e WatchEntry) Key() string { return fmt.Sprintf("%s.%d", e.Type, e.GithubID) }

// WatchStore wraps a SQLite table to store watch state durably.
// Replaces the JetStream KV bucket that was used previously.
type WatchStore struct {
	db *sql.DB
}

// NewWatchStore creates a WatchStore, ensuring the watch_state table exists.
func NewWatchStore(db *sql.DB) (*WatchStore, error) {
	if _, err := db.Exec(watchStateSchema); err != nil {
		return nil, fmt.Errorf("watch: create table: %w", err)
	}
	return &WatchStore{db: db}, nil
}

const timeFormat = time.RFC3339Nano

// Enroll adds a new item to the watch list with the initial backoff.
func (w *WatchStore) Enroll(_ context.Context, typ, repo string, number int, githubID int64) error {
	key := fmt.Sprintf("%s.%d", typ, githubID)
	now := time.Now()
	nextCheck := now.Add(InitialBackoff)
	_, err := w.db.Exec(`
		INSERT INTO watch_state (key, type, repo, number, github_id, next_check, backoff_ns, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			repo = excluded.repo,
			number = excluded.number,
			next_check = excluded.next_check,
			backoff_ns = excluded.backoff_ns,
			last_seen = excluded.last_seen`,
		key, typ, repo, number, githubID,
		nextCheck.Format(timeFormat), int64(InitialBackoff), now.Format(timeFormat),
	)
	if err != nil {
		return fmt.Errorf("watch: enroll %s: %w", key, err)
	}
	return nil
}

// EnrollIfAbsent adds an item only if it's not already in the watch list.
// Returns true if enrolled, false if already present.
func (w *WatchStore) EnrollIfAbsent(_ context.Context, typ, repo string, number int, githubID int64) (bool, error) {
	key := fmt.Sprintf("%s.%d", typ, githubID)
	now := time.Now()
	nextCheck := now.Add(InitialBackoff)
	res, err := w.db.Exec(`
		INSERT OR IGNORE INTO watch_state (key, type, repo, number, github_id, next_check, backoff_ns, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		key, typ, repo, number, githubID,
		nextCheck.Format(timeFormat), int64(InitialBackoff), now.Format(timeFormat),
	)
	if err != nil {
		return false, fmt.Errorf("watch: enroll-if-absent %s: %w", key, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Get retrieves a single watch entry by key.
func (w *WatchStore) Get(_ context.Context, key string) (*WatchEntry, error) {
	var entry WatchEntry
	var nextCheckStr, lastSeenStr string
	err := w.db.QueryRow(`
		SELECT type, repo, number, github_id, next_check, backoff_ns, last_seen
		FROM watch_state WHERE key = ?`, key,
	).Scan(&entry.Type, &entry.Repo, &entry.Number, &entry.GithubID,
		&nextCheckStr, &entry.BackoffNs, &lastSeenStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("watch: key %s not found", key)
		}
		return nil, fmt.Errorf("watch: get %s: %w", key, err)
	}
	entry.NextCheck, _ = time.Parse(timeFormat, nextCheckStr)
	entry.LastSeen, _ = time.Parse(timeFormat, lastSeenStr)
	return &entry, nil
}

// ResetBackoff resets an entry's backoff to InitialBackoff and updates LastSeen.
func (w *WatchStore) ResetBackoff(_ context.Context, key string, observedAt time.Time) error {
	nextCheck := time.Now().Add(InitialBackoff)
	res, err := w.db.Exec(`
		UPDATE watch_state SET
			backoff_ns = ?,
			next_check = ?,
			last_seen = ?
		WHERE key = ?`,
		int64(InitialBackoff), nextCheck.Format(timeFormat), observedAt.Format(timeFormat), key,
	)
	if err != nil {
		return fmt.Errorf("watch: reset %s: %w", key, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("watch: key %s not found", key)
	}
	return nil
}

// IncreaseBackoff doubles the entry's backoff, capping at MaxBackoff.
func (w *WatchStore) IncreaseBackoff(_ context.Context, key string) error {
	entry, err := w.Get(context.Background(), key)
	if err != nil {
		return err
	}
	newBackoff := time.Duration(entry.BackoffNs) * 2
	if newBackoff > MaxBackoff {
		newBackoff = MaxBackoff
	}
	nextCheck := time.Now().Add(newBackoff)
	_, err = w.db.Exec(`
		UPDATE watch_state SET backoff_ns = ?, next_check = ? WHERE key = ?`,
		int64(newBackoff), nextCheck.Format(timeFormat), key,
	)
	if err != nil {
		return fmt.Errorf("watch: increase %s: %w", key, err)
	}
	return nil
}

// Delete removes an entry from the watch list.
func (w *WatchStore) Delete(_ context.Context, key string) error {
	_, err := w.db.Exec(`DELETE FROM watch_state WHERE key = ?`, key)
	return err
}

// ForceUpdate writes the entry directly (used in tests to set arbitrary state).
func (w *WatchStore) ForceUpdate(_ context.Context, entry *WatchEntry) error {
	_, err := w.db.Exec(`
		INSERT INTO watch_state (key, type, repo, number, github_id, next_check, backoff_ns, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			type = excluded.type,
			repo = excluded.repo,
			number = excluded.number,
			github_id = excluded.github_id,
			next_check = excluded.next_check,
			backoff_ns = excluded.backoff_ns,
			last_seen = excluded.last_seen`,
		entry.Key(), entry.Type, entry.Repo, entry.Number, entry.GithubID,
		entry.NextCheck.Format(timeFormat), entry.BackoffNs, entry.LastSeen.Format(timeFormat),
	)
	if err != nil {
		return fmt.Errorf("watch: force update %s: %w", entry.Key(), err)
	}
	return nil
}

// ScanReady returns all entries whose NextCheck is at or before now.
func (w *WatchStore) ScanReady(_ context.Context) ([]WatchEntry, error) {
	now := time.Now().Format(timeFormat)
	rows, err := w.db.Query(`
		SELECT type, repo, number, github_id, next_check, backoff_ns, last_seen
		FROM watch_state WHERE next_check <= ?`, now,
	)
	if err != nil {
		return nil, fmt.Errorf("watch: scan ready: %w", err)
	}
	defer rows.Close()

	var ready []WatchEntry
	for rows.Next() {
		var entry WatchEntry
		var nextCheckStr, lastSeenStr string
		if err := rows.Scan(&entry.Type, &entry.Repo, &entry.Number, &entry.GithubID,
			&nextCheckStr, &entry.BackoffNs, &lastSeenStr); err != nil {
			continue
		}
		entry.NextCheck, _ = time.Parse(timeFormat, nextCheckStr)
		entry.LastSeen, _ = time.Parse(timeFormat, lastSeenStr)
		ready = append(ready, entry)
	}
	return ready, rows.Err()
}

// EvictStale removes entries whose LastSeen is older than EvictAfter.
// Returns the number of entries evicted.
func (w *WatchStore) EvictStale(_ context.Context) (int, error) {
	cutoff := time.Now().Add(-EvictAfter).Format(timeFormat)
	res, err := w.db.Exec(`DELETE FROM watch_state WHERE last_seen < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("watch: evict stale: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
