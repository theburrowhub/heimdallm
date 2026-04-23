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
	entry, err := kv.Get(context.Background(), "pr.12345")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.Backoff() <= bus.InitialBackoff {
		t.Errorf("expected backoff > initial after no-change, got %v", entry.Backoff())
	}
}
