// daemon/internal/bus/watch.go
package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	kvBucketWatch  = "HEIMDALLM_WATCH"
	InitialBackoff = 1 * time.Minute
	MaxBackoff     = 15 * time.Minute
	EvictAfter     = 1 * time.Hour
)

// WatchEntry represents a monitored PR or issue tracked in the KV store.
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

// Key returns the KV key for this entry (e.g. "pr.12345").
func (e WatchEntry) Key() string { return fmt.Sprintf("%s.%d", e.Type, e.GithubID) }

// WatchKV wraps a NATS JetStream KeyValue bucket to store watch state durably.
type WatchKV struct {
	kv jetstream.KeyValue
}

// NewWatchKV creates a WatchKV from an existing KeyValue bucket.
func NewWatchKV(kv jetstream.KeyValue) *WatchKV { return &WatchKV{kv: kv} }

// Enroll adds a new item to the watch list with the initial backoff.
func (w *WatchKV) Enroll(ctx context.Context, typ, repo string, number int, githubID int64) error {
	entry := WatchEntry{
		Type: typ, Repo: repo, Number: number, GithubID: githubID,
		NextCheck: time.Now().Add(InitialBackoff),
		BackoffNs: int64(InitialBackoff),
		LastSeen:  time.Now(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("watch: marshal: %w", err)
	}
	_, err = w.kv.Put(ctx, entry.Key(), data)
	if err != nil {
		return fmt.Errorf("watch: put %s: %w", entry.Key(), err)
	}
	return nil
}

// Get retrieves a single watch entry by key.
func (w *WatchKV) Get(ctx context.Context, key string) (*WatchEntry, error) {
	kve, err := w.kv.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	var entry WatchEntry
	if err := json.Unmarshal(kve.Value(), &entry); err != nil {
		return nil, fmt.Errorf("watch: unmarshal %s: %w", key, err)
	}
	return &entry, nil
}

// ResetBackoff resets an entry's backoff to InitialBackoff and updates LastSeen.
func (w *WatchKV) ResetBackoff(ctx context.Context, key string, observedAt time.Time) error {
	entry, err := w.Get(ctx, key)
	if err != nil {
		return err
	}
	entry.BackoffNs = int64(InitialBackoff)
	entry.NextCheck = time.Now().Add(InitialBackoff)
	entry.LastSeen = observedAt
	data, _ := json.Marshal(entry)
	_, err = w.kv.Put(ctx, key, data)
	return err
}

// IncreaseBackoff doubles the entry's backoff, capping at MaxBackoff.
func (w *WatchKV) IncreaseBackoff(ctx context.Context, key string) error {
	entry, err := w.Get(ctx, key)
	if err != nil {
		return err
	}
	newBackoff := time.Duration(entry.BackoffNs) * 2
	if newBackoff > MaxBackoff {
		newBackoff = MaxBackoff
	}
	entry.BackoffNs = int64(newBackoff)
	entry.NextCheck = time.Now().Add(newBackoff)
	data, _ := json.Marshal(entry)
	_, err = w.kv.Put(ctx, key, data)
	return err
}

// Delete removes an entry from the watch list.
func (w *WatchKV) Delete(ctx context.Context, key string) error {
	return w.kv.Delete(ctx, key)
}

// ForceUpdate writes the entry directly (used in tests to set arbitrary state).
func (w *WatchKV) ForceUpdate(ctx context.Context, entry *WatchEntry) error {
	data, _ := json.Marshal(entry)
	_, err := w.kv.Put(ctx, entry.Key(), data)
	return err
}

// ScanReady returns all entries whose NextCheck is at or before now.
func (w *WatchKV) ScanReady(ctx context.Context) ([]WatchEntry, error) {
	keys, err := w.kv.Keys(ctx)
	if err != nil {
		if errors.Is(err, jetstream.ErrNoKeysFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("watch: keys: %w", err)
	}
	now := time.Now()
	var ready []WatchEntry
	for _, key := range keys {
		entry, err := w.Get(ctx, key)
		if err != nil {
			continue
		}
		if !entry.NextCheck.After(now) {
			ready = append(ready, *entry)
		}
	}
	return ready, nil
}

// EvictStale removes entries whose LastSeen is older than EvictAfter.
// Returns the number of entries evicted.
func (w *WatchKV) EvictStale(ctx context.Context) (int, error) {
	keys, err := w.kv.Keys(ctx)
	if err != nil {
		if errors.Is(err, jetstream.ErrNoKeysFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("watch: keys: %w", err)
	}
	cutoff := time.Now().Add(-EvictAfter)
	evicted := 0
	for _, key := range keys {
		entry, err := w.Get(ctx, key)
		if err != nil {
			continue
		}
		if entry.LastSeen.Before(cutoff) {
			if err := w.kv.Delete(ctx, key); err == nil {
				evicted++
			}
		}
	}
	return evicted, nil
}
