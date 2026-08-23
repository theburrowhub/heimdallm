// daemon/internal/worker/review_test.go
package worker_test

import (
	"context"
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

	received := make(chan bus.PRReviewMsg, 1)
	handler := func(_ context.Context, msg bus.PRReviewMsg) {
		received <- msg
	}

	w := worker.NewReviewWorker(conn, 3, handler)
	ready := make(chan error, 1)
	go func() {
		if err := w.Start(ctx, ready); err != nil {
			t.Errorf("worker start: %v", err)
		}
	}()
	if err := <-ready; err != nil {
		t.Fatalf("worker readiness: %v", err)
	}

	pub := bus.NewPRReviewPublisher(conn)
	if err := pub.PublishPRReview(ctx, "org/repo", 42, 12345, "abc123"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case msg := <-received:
		if msg.Repo != "org/repo" || msg.Number != 42 || msg.GithubID != 12345 || msg.HeadSHA != "abc123" {
			t.Errorf("unexpected message: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called within timeout")
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
	ready := make(chan error, 1)
	go func() { _ = w.Start(ctx, ready) }()
	if err := <-ready; err != nil {
		t.Fatalf("worker readiness: %v", err)
	}

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
