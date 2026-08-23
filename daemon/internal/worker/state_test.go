// daemon/internal/worker/state_test.go
package worker_test

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/worker"
	"github.com/heimdallm/daemon/internal/workgate"
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

func TestStateWorker_UpdateDrainDefersWithoutHandlerOrBackoffMutation(t *testing.T) {
	b := newTestBus(t)
	conn := b.Conn()
	ws := newTestWatch(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.Enroll(ctx, "pr", "org/repo", 42, 12345); err != nil {
		t.Fatal(err)
	}
	before, err := ws.Get(ctx, "pr.12345")
	if err != nil {
		t.Fatal(err)
	}

	called := make(chan struct{}, 1)
	w := worker.NewStateWorker(conn, 1, ws, func(context.Context, bus.StateCheckMsg) (bool, error) {
		called <- struct{}{}
		return true, nil
	})
	gate := workgate.New(time.Minute)
	if _, err := gate.Prepare("update-owner"); err != nil {
		t.Fatal(err)
	}
	w.SetWorkGate(gate)
	go func() { _ = w.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	pub := bus.NewStateCheckPublisher(conn)
	if err := pub.PublishStateCheck(ctx, "pr", "org/repo", 42, 12345); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	select {
	case <-called:
		t.Fatal("state handler ran during update drain")
	default:
	}
	after, err := ws.Get(ctx, "pr.12345")
	if err != nil {
		t.Fatal(err)
	}
	if after.Backoff() != before.Backoff() || !after.LastSeen.Equal(before.LastSeen) {
		t.Fatalf("drain mutated watch state: before=%+v after=%+v", before, after)
	}
}

func TestStateWorker_DeletedWatchDoesNotLogBackoffFailure(t *testing.T) {
	b := newTestBus(t)
	conn := b.Conn()
	ws := newTestWatch(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ws.Enroll(ctx, "pr", "org/disabled", 42, 9876); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	handled := make(chan struct{})
	handler := func(ctx context.Context, _ bus.StateCheckMsg) (bool, error) {
		if err := ws.Delete(ctx, "pr.9876"); err != nil {
			return false, err
		}
		close(handled)
		return false, nil
	}

	w := worker.NewStateWorker(conn, 1, ws, handler)
	go func() { _ = w.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	pub := bus.NewStateCheckPublisher(conn)
	if err := pub.PublishStateCheck(ctx, "pr", "org/disabled", 42, 9876); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case <-handled:
	case <-ctx.Done():
		t.Fatal("state handler was not called")
	}

	// Give the worker time to apply its post-handler backoff update. A queued
	// state check may legitimately outlive the watch row it references.
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(logs.String(), "increase backoff failed") {
		t.Fatalf("deleted watch produced a misleading warning: %s", logs.String())
	}
	if _, err := ws.Get(ctx, "pr.9876"); err == nil {
		t.Fatal("deleted watch was unexpectedly recreated")
	}
}
