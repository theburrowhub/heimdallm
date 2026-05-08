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

func TestImplementWorker_ProcessesMessagesConcurrently(t *testing.T) {
	b := newTestBus(t)
	conn := b.Conn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	started := make(chan int, 2)
	release := make(chan struct{})
	handler := func(_ context.Context, msg bus.IssueMsg) {
		started <- msg.Number
		<-release
	}

	w := worker.NewImplementWorker(conn, 2, handler)
	go func() { w.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	pub := bus.NewIssuePublisher(conn)
	if err := pub.PublishIssueImplement(ctx, "org/repo", 101, 100101); err != nil {
		t.Fatalf("publish first issue: %v", err)
	}
	if err := pub.PublishIssueImplement(ctx, "org/repo", 102, 100102); err != nil {
		t.Fatalf("publish second issue: %v", err)
	}

	seen := map[int]bool{}
	for len(seen) < 2 {
		select {
		case number := <-started:
			seen[number] = true
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatalf("timed out waiting for concurrent implement handlers, seen=%v", seen)
		}
	}
	close(release)

	if !seen[101] || !seen[102] {
		t.Fatalf("expected both issue handlers to start, seen=%v", seen)
	}
}
