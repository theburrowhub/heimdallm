// daemon/internal/worker/implement_test.go
package worker_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/worker"
)

func TestImplementWorker_ConsumesAndCallsHandler(t *testing.T) {
	b := newTestBus(t)
	conn := b.Conn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		received []bus.IssueMsg
	)
	handler := func(_ context.Context, msg bus.IssueMsg) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, msg)
	}

	w := worker.NewImplementWorker(conn, 3, handler)
	go func() { w.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	pub := bus.NewIssuePublisher(conn)
	if err := pub.PublishIssueImplement(ctx, "org/repo", 77, 99999); err != nil {
		t.Fatalf("publish: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1, got %d", len(received))
	}
	msg := received[0]
	if msg.Repo != "org/repo" || msg.Number != 77 || msg.GithubID != 99999 {
		t.Errorf("unexpected: %+v", msg)
	}
}
