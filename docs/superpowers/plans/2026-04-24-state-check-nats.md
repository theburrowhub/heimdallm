# State Check → NATS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the in-memory WatchQueue with a NATS KV bucket for durable watch state, a state poller that publishes ready items, and a state worker consumer that checks GitHub state.

**Architecture:** A NATS KV bucket `HEIMDALLM_WATCH` stores per-item backoff state (survives restart). A poller goroutine scans the bucket every 30s and publishes `StateCheckMsg` for items due for check. The `state-worker` consumer handles the GitHub API call and updates backoff state. Workers enroll items via KV Put instead of `WatchQueue.Push`.

**Tech Stack:** Go, NATS JetStream KV (embedded), existing GitHub client + scheduler interfaces

**Spec:** `docs/superpowers/specs/2026-04-24-state-check-nats-design.md`

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Create | `daemon/internal/bus/watch.go` | WatchEntry type + WatchKV wrapper |
| Create | `daemon/internal/bus/watch_test.go` | KV CRUD + ScanReady + EvictStale tests |
| Modify | `daemon/internal/bus/bus.go` | Create KV bucket in Start(), expose WatchKV() |
| Modify | `daemon/internal/bus/publisher.go` | Add StateCheckPublisher |
| Modify | `daemon/internal/bus/publisher_test.go` | Test |
| Create | `daemon/internal/worker/state.go` | StateWorker consumer |
| Create | `daemon/internal/worker/state_test.go` | Test |
| Modify | `daemon/cmd/heimdallm/main.go` | State poller, stateHandler, StateWorker, replace Queue().Push |

---

### Task 1: WatchKV wrapper + KV bucket

**Files:**
- Create: `daemon/internal/bus/watch.go`
- Create: `daemon/internal/bus/watch_test.go`
- Modify: `daemon/internal/bus/bus.go`

- [ ] **Step 1: Create watch.go with WatchEntry and WatchKV**

```go
// daemon/internal/bus/watch.go
package bus

import (
	"context"
	"encoding/json"
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

// WatchEntry represents a PR or issue being watched for state changes.
// Stored as JSON in the NATS KV bucket.
type WatchEntry struct {
	Type      string    `json:"type"`       // "pr" or "issue"
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	GithubID  int64     `json:"github_id"`
	NextCheck time.Time `json:"next_check"`
	BackoffNs int64     `json:"backoff_ns"` // time.Duration stored as nanoseconds
	LastSeen  time.Time `json:"last_seen"`
}

// Backoff returns the backoff duration.
func (e WatchEntry) Backoff() time.Duration {
	return time.Duration(e.BackoffNs)
}

// Key returns the KV key for this entry.
func (e WatchEntry) Key() string {
	return fmt.Sprintf("%s:%d", e.Type, e.GithubID)
}

// WatchKV wraps a NATS KeyValue bucket for watch state management.
type WatchKV struct {
	kv jetstream.KeyValue
}

// NewWatchKV wraps an existing KeyValue bucket.
func NewWatchKV(kv jetstream.KeyValue) *WatchKV {
	return &WatchKV{kv: kv}
}

// Enroll adds or updates an item in the watch bucket with initial backoff.
// If the item already exists, it is overwritten (re-enrollment resets state).
func (w *WatchKV) Enroll(ctx context.Context, typ, repo string, number int, githubID int64) error {
	entry := WatchEntry{
		Type:      typ,
		Repo:      repo,
		Number:    number,
		GithubID:  githubID,
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

// Get returns the watch entry for the given key, or nil if not found.
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

// ResetBackoff resets the backoff to initial and updates LastSeen.
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

// IncreaseBackoff doubles the backoff (capped at MaxBackoff) and schedules next check.
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

// Delete removes an item from the watch bucket.
func (w *WatchKV) Delete(ctx context.Context, key string) error {
	return w.kv.Delete(ctx, key)
}

// ScanReady returns all entries whose NextCheck is at or before now.
func (w *WatchKV) ScanReady(ctx context.Context) ([]WatchEntry, error) {
	keys, err := w.kv.Keys(ctx)
	if err != nil {
		// jetstream.ErrNoKeysFound means the bucket is empty
		if err.Error() == "nats: no keys found" {
			return nil, nil
		}
		return nil, fmt.Errorf("watch: keys: %w", err)
	}
	now := time.Now()
	var ready []WatchEntry
	for _, key := range keys {
		entry, err := w.Get(ctx, key)
		if err != nil {
			continue // skip unreadable entries
		}
		if !entry.NextCheck.After(now) {
			ready = append(ready, *entry)
		}
	}
	return ready, nil
}

// EvictStale removes entries not seen for EvictAfter. Returns count deleted.
func (w *WatchKV) EvictStale(ctx context.Context) (int, error) {
	keys, err := w.kv.Keys(ctx)
	if err != nil {
		if err.Error() == "nats: no keys found" {
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
```

- [ ] **Step 2: Add KV bucket creation in bus.go**

In `daemon/internal/bus/bus.go`, in the `Start` method, after `ensureConsumers(ctx)` and before the success log, add:

```go
	kv, err := b.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: kvBucketWatch,
	})
	if err != nil {
		conn.Close()
		srv.Shutdown()
		return fmt.Errorf("bus: create KV bucket %s: %w", kvBucketWatch, err)
	}
	b.watchKV = NewWatchKV(kv)
```

Add the `watchKV` field to the `Bus` struct:

```go
type Bus struct {
	server  *natsserver.Server
	conn    *nats.Conn
	js      jetstream.JetStream
	cfg     Config
	watchKV *WatchKV

	stopOnce sync.Once
}
```

Add the accessor:

```go
// WatchKV returns the watch state KV wrapper.
func (b *Bus) WatchKV() *WatchKV {
	return b.watchKV
}
```

- [ ] **Step 3: Create watch_test.go**

```go
// daemon/internal/bus/watch_test.go
package bus_test

import (
	"context"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
)

func TestWatchKV_EnrollAndGet(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()
	kv := b.WatchKV()

	if err := kv.Enroll(ctx, "pr", "org/repo", 42, 12345); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	entry, err := kv.Get(ctx, "pr:12345")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.Type != "pr" || entry.Repo != "org/repo" || entry.Number != 42 || entry.GithubID != 12345 {
		t.Errorf("unexpected entry: %+v", entry)
	}
	if entry.Backoff() != bus.InitialBackoff {
		t.Errorf("backoff = %v, want %v", entry.Backoff(), bus.InitialBackoff)
	}
}

func TestWatchKV_ResetBackoff(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()
	kv := b.WatchKV()

	kv.Enroll(ctx, "pr", "org/repo", 1, 100)
	// Increase backoff first
	kv.IncreaseBackoff(ctx, "pr:100")

	entry, _ := kv.Get(ctx, "pr:100")
	if entry.Backoff() != 2*bus.InitialBackoff {
		t.Fatalf("backoff after increase = %v, want %v", entry.Backoff(), 2*bus.InitialBackoff)
	}

	// Reset
	observed := time.Now()
	kv.ResetBackoff(ctx, "pr:100", observed)

	entry, _ = kv.Get(ctx, "pr:100")
	if entry.Backoff() != bus.InitialBackoff {
		t.Errorf("backoff after reset = %v, want %v", entry.Backoff(), bus.InitialBackoff)
	}
}

func TestWatchKV_IncreaseBackoff_CapsAtMax(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()
	kv := b.WatchKV()

	kv.Enroll(ctx, "pr", "org/repo", 1, 100)
	// Increase many times
	for i := 0; i < 20; i++ {
		kv.IncreaseBackoff(ctx, "pr:100")
	}
	entry, _ := kv.Get(ctx, "pr:100")
	if entry.Backoff() > bus.MaxBackoff {
		t.Errorf("backoff %v exceeds max %v", entry.Backoff(), bus.MaxBackoff)
	}
}

func TestWatchKV_ScanReady(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()
	kv := b.WatchKV()

	// Enroll with default NextCheck = now + 1m (not ready)
	kv.Enroll(ctx, "pr", "org/repo", 1, 100)

	ready, err := kv.ScanReady(ctx)
	if err != nil {
		t.Fatalf("ScanReady: %v", err)
	}
	if len(ready) != 0 {
		t.Errorf("expected 0 ready, got %d", len(ready))
	}

	// Manually set NextCheck to past
	entry, _ := kv.Get(ctx, "pr:100")
	entry.NextCheck = time.Now().Add(-1 * time.Minute)
	kv.ForceUpdate(ctx, entry)

	ready, err = kv.ScanReady(ctx)
	if err != nil {
		t.Fatalf("ScanReady: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready, got %d", len(ready))
	}
	if ready[0].GithubID != 100 {
		t.Errorf("unexpected ready item: %+v", ready[0])
	}
}

func TestWatchKV_EvictStale(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()
	kv := b.WatchKV()

	kv.Enroll(ctx, "pr", "org/repo", 1, 100)

	// Manually set LastSeen to >1h ago
	entry, _ := kv.Get(ctx, "pr:100")
	entry.LastSeen = time.Now().Add(-2 * time.Hour)
	kv.ForceUpdate(ctx, entry)

	evicted, err := kv.EvictStale(ctx)
	if err != nil {
		t.Fatalf("EvictStale: %v", err)
	}
	if evicted != 1 {
		t.Errorf("expected 1 evicted, got %d", evicted)
	}

	_, err = kv.Get(ctx, "pr:100")
	if err == nil {
		t.Error("expected Get to fail after eviction")
	}
}

func TestWatchKV_Delete(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()
	kv := b.WatchKV()

	kv.Enroll(ctx, "issue", "org/repo", 5, 555)
	if err := kv.Delete(ctx, "issue:555"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := kv.Get(ctx, "issue:555")
	if err == nil {
		t.Error("expected Get to fail after Delete")
	}
}
```

Note: The test uses `kv.ForceUpdate` which we need to add as a test helper. Add this method to watch.go:

```go
// ForceUpdate writes the entry directly (used in tests to set arbitrary state).
func (w *WatchKV) ForceUpdate(ctx context.Context, entry *WatchEntry) error {
	data, _ := json.Marshal(entry)
	_, err := w.kv.Put(ctx, entry.Key(), data)
	return err
}
```

- [ ] **Step 4: Run tests**

```bash
cd daemon
go test ./internal/bus/ -run "TestWatchKV" -v -count=1
```

Expected: All 6 KV tests pass.

- [ ] **Step 5: Commit**

```bash
cd daemon
git add internal/bus/watch.go internal/bus/watch_test.go internal/bus/bus.go
git commit -m "feat(bus): add WatchKV wrapper with NATS KV bucket for state check (#308)"
```

---

### Task 2: StateCheckPublisher

**Files:**
- Modify: `daemon/internal/bus/publisher.go`
- Modify: `daemon/internal/bus/publisher_test.go`

- [ ] **Step 1: Write failing test**

Append to `daemon/internal/bus/publisher_test.go`:

```go
func TestStateCheckPublisher_Publish(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewStateCheckPublisher(b.JetStream())
	if err := pub.PublishStateCheck(ctx, "pr", "org/repo", 42, 12345); err != nil {
		t.Fatalf("PublishStateCheck: %v", err)
	}

	cons, err := b.JetStream().Consumer(ctx, bus.StreamWork, bus.ConsumerState)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var got bus.StateCheckMsg
	for m := range msgs.Messages() {
		if err := bus.Decode(m.Data(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		m.Ack()
	}
	if got.Type != "pr" || got.Repo != "org/repo" || got.Number != 42 || got.GithubID != 12345 {
		t.Errorf("unexpected: %+v", got)
	}
}
```

- [ ] **Step 2: Implement**

Append to `daemon/internal/bus/publisher.go`:

```go
// StateCheckPublisher publishes state check requests to NATS JetStream.
type StateCheckPublisher struct {
	js jetstream.JetStream
}

// NewStateCheckPublisher creates a publisher for state check requests.
func NewStateCheckPublisher(js jetstream.JetStream) *StateCheckPublisher {
	return &StateCheckPublisher{js: js}
}

// PublishStateCheck publishes a state check request for a watched item.
func (p *StateCheckPublisher) PublishStateCheck(ctx context.Context, typ, repo string, number int, githubID int64) error {
	data, err := Encode(StateCheckMsg{
		Type:     typ,
		Repo:     repo,
		Number:   number,
		GithubID: githubID,
	})
	if err != nil {
		return fmt.Errorf("bus: encode state check: %w", err)
	}
	_, err = p.js.Publish(ctx, SubjStateCheck, data)
	if err != nil {
		return fmt.Errorf("bus: publish state check: %w", err)
	}
	return nil
}
```

No dedup — the poller is the single publisher and explicitly manages timing via KV.

- [ ] **Step 3: Run tests, commit**

```bash
cd daemon
go test ./internal/bus/ -run TestStateCheckPublisher -v
git add internal/bus/publisher.go internal/bus/publisher_test.go
git commit -m "feat(bus): add StateCheckPublisher (#308)"
```

---

### Task 3: StateWorker

**Files:**
- Create: `daemon/internal/worker/state.go`
- Create: `daemon/internal/worker/state_test.go`

- [ ] **Step 1: Create state.go**

```go
// daemon/internal/worker/state.go
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

// StateHandler is invoked for each state check message. It should check the
// item's GitHub state and return whether a change was detected. The handler
// is responsible for calling HandleChange when changed==true.
type StateHandler func(ctx context.Context, msg bus.StateCheckMsg) (changed bool, err error)

// StateWorker consumes state check requests from NATS.
type StateWorker struct {
	js      jetstream.JetStream
	watchKV *bus.WatchKV
	handler StateHandler
}

// NewStateWorker creates a worker that consumes from the state-worker
// durable consumer. After each handler call, it updates the KV backoff
// state: reset on change, increase on no change.
func NewStateWorker(js jetstream.JetStream, watchKV *bus.WatchKV, handler StateHandler) *StateWorker {
	return &StateWorker{js: js, watchKV: watchKV, handler: handler}
}

// Start begins consuming. Blocks until ctx is cancelled.
func (w *StateWorker) Start(ctx context.Context) error {
	cons, err := w.js.Consumer(ctx, bus.StreamWork, bus.ConsumerState)
	if err != nil {
		return err
	}

	iter, err := cons.Messages(jetstream.PullMaxMessages(1))
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	for {
		msg, err := iter.Next()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				return nil
			}
			return fmt.Errorf("state-worker: iter.Next: %w", err)
		}

		var checkMsg bus.StateCheckMsg
		if err := bus.Decode(msg.Data(), &checkMsg); err != nil {
			slog.Error("state-worker: decode message", "err", err)
			msg.Ack()
			continue
		}

		slog.Debug("state-worker: checking",
			"type", checkMsg.Type, "repo", checkMsg.Repo,
			"number", checkMsg.Number, "github_id", checkMsg.GithubID)

		changed, handlerErr := w.safeHandle(ctx, checkMsg)

		key := fmt.Sprintf("%s:%d", checkMsg.Type, checkMsg.GithubID)
		if handlerErr != nil {
			slog.Warn("state-worker: check failed",
				"type", checkMsg.Type, "repo", checkMsg.Repo,
				"number", checkMsg.Number, "err", handlerErr)
			// Re-enqueue with increased backoff
			w.watchKV.IncreaseBackoff(ctx, key)
		} else if changed {
			slog.Info("state-worker: change detected",
				"type", checkMsg.Type, "repo", checkMsg.Repo,
				"number", checkMsg.Number)
			// Handler already processed the change — reset backoff
			w.watchKV.ResetBackoff(ctx, key, time.Now())
		} else {
			// No change — increase backoff
			w.watchKV.IncreaseBackoff(ctx, key)
		}

		msg.Ack()
	}
}

func (w *StateWorker) safeHandle(ctx context.Context, msg bus.StateCheckMsg) (changed bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("state-worker: handler panic",
				"type", msg.Type, "repo", msg.Repo, "number", msg.Number,
				"panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return w.handler(ctx, msg)
}
```

Note: needs `"time"` import for `time.Now()` in `Start`.

- [ ] **Step 2: Create state_test.go**

```go
// daemon/internal/worker/state_test.go
package worker_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/worker"
)

func TestStateWorker_ConsumesAndCallsHandler(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kv := b.WatchKV()
	// Enroll item so the worker can update backoff
	kv.Enroll(ctx, "pr", "org/repo", 42, 12345)

	var mu sync.Mutex
	var calls []bus.StateCheckMsg
	handler := func(_ context.Context, msg bus.StateCheckMsg) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, msg)
		return false, nil // no change
	}

	w := worker.NewStateWorker(b.JetStream(), kv, handler)
	go func() { w.Start(ctx) }()
	time.Sleep(200 * time.Millisecond)

	pub := bus.NewStateCheckPublisher(b.JetStream())
	if err := pub.PublishStateCheck(ctx, "pr", "org/repo", 42, 12345); err != nil {
		t.Fatalf("publish: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Type != "pr" || calls[0].GithubID != 12345 {
		t.Errorf("unexpected: %+v", calls[0])
	}

	// Verify backoff was increased (no change → increase)
	entry, err := kv.Get(context.Background(), "pr:12345")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.Backoff() <= bus.InitialBackoff {
		t.Errorf("expected backoff > initial after no-change, got %v", entry.Backoff())
	}
}
```

- [ ] **Step 3: Run tests, commit**

```bash
cd daemon
go test ./internal/worker/ -run TestStateWorker -v -count=1
git add internal/worker/state.go internal/worker/state_test.go
git commit -m "feat(worker): add StateWorker with KV backoff management (#308)"
```

---

### Task 4: Wire into main.go

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go`

This task has three parts: state poller, state handler + worker, and replacing Queue().Push.

- [ ] **Step 1: Replace Queue().Push in reviewHandler with KV Enroll**

In the `reviewHandler` closure (around line 541-547), replace:

```go
		// Maintain Tier 3 watching (Task 9 replaces WatchQueue entirely).
		cfgMu.Lock()
		q := pipe.Queue()
		cfgMu.Unlock()
		q.Push(&scheduler.WatchItem{
			Type: "pr", Repo: pr.Repo, Number: pr.Number, GithubID: pr.ID,
		})
```

With:

```go
		// Enroll for state watching via NATS KV.
		if err := eventBus.WatchKV().Enroll(ctx, "pr", pr.Repo, pr.Number, pr.ID); err != nil {
			slog.Warn("review-worker: failed to enroll watch",
				"repo", pr.Repo, "pr", pr.Number, "err", err)
		}
```

- [ ] **Step 2: Add state poller goroutine**

After the implementWorker startup block, add:

```go
	// ── State check poller ──────────────────────────────────────────────
	// Scans the NATS KV watch bucket every 30s and publishes StateCheckMsg
	// for items due for a state check. Replaces the in-memory WatchQueue.
	stateCheckPub := bus.NewStateCheckPublisher(js)
	statePollerCtx, statePollerCancel := context.WithCancel(context.Background())
	defer statePollerCancel()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-statePollerCtx.Done():
				return
			case <-ticker.C:
				watchKV := eventBus.WatchKV()
				if evicted, err := watchKV.EvictStale(statePollerCtx); err != nil {
					slog.Warn("state-poller: evict failed", "err", err)
				} else if evicted > 0 {
					slog.Debug("state-poller: evicted stale items", "count", evicted)
				}

				ready, err := watchKV.ScanReady(statePollerCtx)
				if err != nil {
					slog.Warn("state-poller: scan failed", "err", err)
					continue
				}
				for _, entry := range ready {
					if err := stateCheckPub.PublishStateCheck(statePollerCtx, entry.Type, entry.Repo, entry.Number, entry.GithubID); err != nil {
						slog.Warn("state-poller: publish failed",
							"type", entry.Type, "repo", entry.Repo, "number", entry.Number, "err", err)
					}
				}
			}
		}
	}()
```

- [ ] **Step 3: Add stateHandler and StateWorker startup**

After the state poller block, add:

```go
	// ── NATS state check worker ─────────────────────────────────────────
	// Consumes state check requests, calls GitHub API, updates KV backoff.
	// Reuses the existing CheckItem/HandleChange logic from tier2Adapter.
	stateHandler := func(ctx context.Context, msg bus.StateCheckMsg) (bool, error) {
		item := &scheduler.WatchItem{
			Type:     msg.Type,
			Repo:     msg.Repo,
			Number:   msg.Number,
			GithubID: msg.GithubID,
		}

		// Read LastSeen from KV for the dedup check inside CheckItem
		key := fmt.Sprintf("%s:%d", msg.Type, msg.GithubID)
		entry, err := eventBus.WatchKV().Get(ctx, key)
		if err == nil {
			item.LastSeen = entry.LastSeen
		}

		changed, snap, err := adapter.CheckItem(ctx, item)
		if err != nil {
			return false, err
		}
		if !changed {
			return false, nil
		}
		if err := adapter.HandleChange(ctx, item, snap); err != nil {
			return true, err
		}
		return true, nil
	}

	stateW := worker.NewStateWorker(js, eventBus.WatchKV(), stateHandler)
	stateWCtx, stateWCancel := context.WithCancel(context.Background())
	defer stateWCancel()
	go func() {
		if err := stateW.Start(stateWCtx); err != nil {
			slog.Error("state worker stopped", "err", err)
		}
	}()
```

- [ ] **Step 4: Verify build**

```bash
cd daemon
go build ./cmd/heimdallm/
```

- [ ] **Step 5: Run full test suite**

```bash
cd daemon
go test ./... -count=1
```

- [ ] **Step 6: Commit**

```bash
git add cmd/heimdallm/main.go
git commit -m "feat: wire state check poller and worker into daemon (#308)"
```

---

### Task 5: Final validation

- [ ] **Step 1: Run affected packages with race detector**

```bash
cd daemon
go test ./internal/bus/ ./internal/worker/ ./cmd/heimdallm/ -race -count=1
```

- [ ] **Step 2: Build binary and smoke test**

```bash
cd daemon
go build -o bin/heimdallm ./cmd/heimdallm/
HEIMDALLM_DATA_DIR=$(mktemp -d) HEIMDALLM_AI_PRIMARY=claude-code timeout 10 ./bin/heimdallm 2>&1 | head -25
```

Expected: Daemon starts. After a PR review completes, the review handler enrolls the PR in the KV bucket. The state poller (30s tick) will eventually publish a StateCheckMsg. The state worker will consume and check GitHub state.

- [ ] **Step 3: Verify KV persistence across restart**

```bash
# Start daemon, wait for some watch items to be enrolled
# Stop daemon (Ctrl+C), restart
# The KV bucket should still have entries — state watching survives restart
```

- [ ] **Step 4: Commit if adjustments needed**
