// daemon/internal/worker/state_test.go
package worker_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/worker"
	_ "modernc.org/sqlite"
)

func newTestWatch(t *testing.T) *bus.WatchStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	w, err := bus.NewWatchStore(db)
	if err != nil {
		t.Fatalf("NewWatchStore: %v", err)
	}
	return w
}

func TestStateWorker_ConsumesAndCallsHandler(t *testing.T) {
	b := newTestBus(t)
	conn := b.Conn()
	ws := newTestWatch(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws.Enroll(ctx, "pr", "org/repo", 42, 12345)

	var mu sync.Mutex
	var calls []bus.StateCheckMsg
	handler := func(_ context.Context, msg bus.StateCheckMsg) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, msg)
		return false, nil // no change
	}

	w := worker.NewStateWorker(conn, 3, ws, handler)
	go func() { w.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	pub := bus.NewStateCheckPublisher(conn)
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

	// Verify backoff was increased (no change -> increase).
	entry, err := ws.Get(context.Background(), "pr.12345")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.Backoff() <= bus.InitialBackoff {
		t.Errorf("expected backoff > initial after no-change, got %v", entry.Backoff())
	}
}
