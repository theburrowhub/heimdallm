// daemon/internal/worker/review_test.go
package worker_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/worker"
)

func newTestBus(t *testing.T) *bus.Bus {
	t.Helper()
	b := bus.New(bus.Config{MaxConcurrentWorkers: 3})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("bus start: %v", err)
	}
	t.Cleanup(b.Stop)
	return b
}

func TestReviewWorker_ConsumesAndCallsHandler(t *testing.T) {
	b := newTestBus(t)
	conn := b.Conn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		received []bus.PRReviewMsg
	)
	handler := func(_ context.Context, msg bus.PRReviewMsg) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, msg)
	}

	w := worker.NewReviewWorker(conn, 3, handler)
	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("worker start: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)

	pub := bus.NewPRReviewPublisher(conn)
	if err := pub.PublishPRReview(ctx, "org/repo", 42, 12345, "abc123"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 message handled, got %d", len(received))
	}
	msg := received[0]
	if msg.Repo != "org/repo" || msg.Number != 42 || msg.GithubID != 12345 || msg.HeadSHA != "abc123" {
		t.Errorf("unexpected message: %+v", msg)
	}
}

func TestReviewWorker_HandlerPanicDoesNotCrash(t *testing.T) {
	b := newTestBus(t)
	conn := b.Conn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	panicked := make(chan struct{}, 1)
	handler := func(_ context.Context, _ bus.PRReviewMsg) {
		panicked <- struct{}{}
		panic("simulated handler panic")
	}

	w := worker.NewReviewWorker(conn, 3, handler)
	go func() { w.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	data, _ := bus.Encode(bus.PRReviewMsg{Repo: "a/b", Number: 1, GithubID: 1, HeadSHA: "p1"})
	conn.Publish(bus.SubjPRReview, data)
	conn.Flush()

	select {
	case <-panicked:
		// Worker survived panic — test passes.
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called within timeout")
	}
}
