// daemon/internal/worker/publish_test.go
package worker_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/worker"
	"github.com/nats-io/nats.go/jetstream"
)

func TestPublishWorker_AcksOnSuccess(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	called := make(chan int64, 1)
	handler := func(_ context.Context, msg bus.PRPublishMsg) error {
		called <- msg.ReviewID
		return nil
	}

	w := worker.NewPublishWorker(b.JetStream(), handler)
	go func() { w.Start(ctx) }()
	time.Sleep(200 * time.Millisecond)

	pub := bus.NewPRPublishPublisher(b.JetStream())
	if err := pub.PublishPRPublish(ctx, 42); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case id := <-called:
		if id != 42 {
			t.Errorf("ReviewID = %d, want 42", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called")
	}

	time.Sleep(200 * time.Millisecond)
	cancel()

	cons, err := b.JetStream().Consumer(context.Background(), bus.StreamWork, bus.ConsumerPublish)
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

func TestPublishWorker_NaksOnError(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var mu sync.Mutex
	callCount := 0
	handler := func(_ context.Context, msg bus.PRPublishMsg) error {
		mu.Lock()
		callCount++
		mu.Unlock()
		return errors.New("transient error")
	}

	w := worker.NewPublishWorker(b.JetStream(), handler)
	go func() { w.Start(ctx) }()
	time.Sleep(200 * time.Millisecond)

	data, _ := bus.Encode(bus.PRPublishMsg{ReviewID: 99})
	_, err := b.JetStream().Publish(ctx, bus.SubjPRPublish, data, jetstream.WithMsgID("rev:99"))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Wait for at least the first call
	time.Sleep(1 * time.Second)
	cancel()

	mu.Lock()
	n := callCount
	mu.Unlock()
	if n < 1 {
		t.Fatalf("expected handler to be called at least once, got %d", n)
	}
}

func TestPublishWorker_NaksOnPanic(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	panicked := make(chan struct{}, 1)
	handler := func(_ context.Context, _ bus.PRPublishMsg) error {
		panicked <- struct{}{}
		panic("simulated publish panic")
	}

	w := worker.NewPublishWorker(b.JetStream(), handler)
	go func() { w.Start(ctx) }()
	time.Sleep(200 * time.Millisecond)

	data, _ := bus.Encode(bus.PRPublishMsg{ReviewID: 77})
	_, err := b.JetStream().Publish(ctx, bus.SubjPRPublish, data, jetstream.WithMsgID("rev:77"))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-panicked:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called")
	}

	// Panic → safeHandle returns error → NakWithDelay (not ack)
	time.Sleep(300 * time.Millisecond)
	cancel()
}
