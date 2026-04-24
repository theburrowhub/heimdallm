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
)

func TestPublishWorker_CallsHandlerOnSuccess(t *testing.T) {
	b := newTestBus(t)
	conn := b.Conn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	called := make(chan int64, 1)
	handler := func(_ context.Context, msg bus.PRPublishMsg) error {
		called <- msg.ReviewID
		return nil
	}

	w := worker.NewPublishWorker(conn, 3, handler)
	go func() { w.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	pub := bus.NewPRPublishPublisher(conn)
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
}

func TestPublishWorker_HandlerErrorLogged(t *testing.T) {
	b := newTestBus(t)
	conn := b.Conn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var mu sync.Mutex
	callCount := 0
	handler := func(_ context.Context, _ bus.PRPublishMsg) error {
		mu.Lock()
		callCount++
		mu.Unlock()
		return errors.New("transient error")
	}

	w := worker.NewPublishWorker(conn, 3, handler)
	go func() { w.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	data, _ := bus.Encode(bus.PRPublishMsg{ReviewID: 99})
	conn.Publish(bus.SubjPRPublish, data)
	conn.Flush()

	// Wait for the handler to be called.
	time.Sleep(500 * time.Millisecond)
	cancel()

	mu.Lock()
	n := callCount
	mu.Unlock()
	// With core NATS, no retry — handler called exactly once.
	if n != 1 {
		t.Fatalf("expected handler called 1 time, got %d", n)
	}
}

func TestPublishWorker_PanicDoesNotCrash(t *testing.T) {
	b := newTestBus(t)
	conn := b.Conn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	panicked := make(chan struct{}, 1)
	handler := func(_ context.Context, _ bus.PRPublishMsg) error {
		panicked <- struct{}{}
		panic("simulated publish panic")
	}

	w := worker.NewPublishWorker(conn, 3, handler)
	go func() { w.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	data, _ := bus.Encode(bus.PRPublishMsg{ReviewID: 77})
	conn.Publish(bus.SubjPRPublish, data)
	conn.Flush()

	select {
	case <-panicked:
		// Worker survived panic.
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called")
	}
}
