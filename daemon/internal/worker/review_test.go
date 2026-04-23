// daemon/internal/worker/review_test.go
package worker_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/worker"
	"github.com/nats-io/nats.go/jetstream"
)

func newTestBus(t *testing.T) *bus.Bus {
	t.Helper()
	dir := t.TempDir()
	b := bus.New(bus.Config{DataDir: dir, MaxConcurrentWorkers: 3})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("bus start: %v", err)
	}
	t.Cleanup(b.Stop)
	return b
}

func TestReviewWorker_ConsumesAndCallsHandler(t *testing.T) {
	b := newTestBus(t)
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

	w := worker.NewReviewWorker(b.JetStream(), handler)

	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("worker start: %v", err)
		}
	}()

	time.Sleep(200 * time.Millisecond)

	pub := bus.NewPRReviewPublisher(b.JetStream())
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

	cons, err := b.JetStream().Consumer(context.Background(), bus.StreamWork, bus.ConsumerReview)
	if err != nil {
		t.Fatalf("get consumer: %v", err)
	}
	info, err := cons.Info(context.Background())
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	if info.NumAckPending > 0 {
		t.Errorf("expected 0 ack-pending, got %d", info.NumAckPending)
	}
}

func TestReviewWorker_AcksOnHandlerReturn(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	called := make(chan struct{}, 1)
	handler := func(_ context.Context, _ bus.PRReviewMsg) {
		called <- struct{}{}
	}

	w := worker.NewReviewWorker(b.JetStream(), handler)
	go func() { w.Start(ctx) }()
	time.Sleep(200 * time.Millisecond)

	data, _ := bus.Encode(bus.PRReviewMsg{Repo: "a/b", Number: 1, GithubID: 1, HeadSHA: "s1"})
	_, err := b.JetStream().Publish(ctx, bus.SubjPRReview, data, jetstream.WithMsgID("1:s1"))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called within timeout")
	}

	time.Sleep(200 * time.Millisecond)
	cancel()

	cons, err := b.JetStream().Consumer(context.Background(), bus.StreamWork, bus.ConsumerReview)
	if err != nil {
		t.Fatalf("get consumer: %v", err)
	}
	info, err := cons.Info(context.Background())
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	if info.NumAckPending > 0 {
		t.Errorf("message not acked after handler returned: ack-pending=%d", info.NumAckPending)
	}
}
