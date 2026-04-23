// daemon/internal/bus/bus_test.go
package bus_test

import (
	"context"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

func TestBus_StartStop(t *testing.T) {
	b := newTestBus(t)

	if b.Conn() == nil {
		t.Fatal("Conn() returned nil after Start")
	}
	if b.JetStream() == nil {
		t.Fatal("JetStream() returned nil after Start")
	}
}

func TestBus_DoubleStop(t *testing.T) {
	dir := t.TempDir()
	b := bus.New(bus.Config{DataDir: dir, MaxConcurrentWorkers: 2})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b.Stop()
	b.Stop() // must not panic
}

func TestBus_DefaultWorkers(t *testing.T) {
	dir := t.TempDir()
	b := bus.New(bus.Config{DataDir: dir, MaxConcurrentWorkers: 0})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(b.Stop)
	// Verify it started without error (default of 5 applied internally)
}

func TestBus_StreamsCreated(t *testing.T) {
	b := newTestBus(t)
	js := b.JetStream()
	ctx := context.Background()

	for _, name := range []string{bus.StreamWork, bus.StreamDiscovery, bus.StreamEvents} {
		_, err := js.Stream(ctx, name)
		if err != nil {
			t.Errorf("stream %s not found: %v", name, err)
		}
	}
}

func TestBus_WorkStream_Config(t *testing.T) {
	b := newTestBus(t)
	js := b.JetStream()
	ctx := context.Background()

	s, err := js.Stream(ctx, bus.StreamWork)
	if err != nil {
		t.Fatalf("stream not found: %v", err)
	}
	info := s.CachedInfo()
	if info.Config.Retention != jetstream.WorkQueuePolicy {
		t.Errorf("retention = %v, want WorkQueuePolicy", info.Config.Retention)
	}
	if info.Config.Duplicates != 2*time.Minute {
		t.Errorf("dedup = %v, want 2m", info.Config.Duplicates)
	}
	if info.Config.MaxAge != 24*time.Hour {
		t.Errorf("max_age = %v, want 24h", info.Config.MaxAge)
	}
}

func TestBus_DiscoveryStream_Config(t *testing.T) {
	b := newTestBus(t)
	js := b.JetStream()
	ctx := context.Background()

	s, err := js.Stream(ctx, bus.StreamDiscovery)
	if err != nil {
		t.Fatalf("stream not found: %v", err)
	}
	info := s.CachedInfo()
	if info.Config.Retention != jetstream.InterestPolicy {
		t.Errorf("retention = %v, want InterestPolicy", info.Config.Retention)
	}
	if info.Config.Duplicates != 5*time.Minute {
		t.Errorf("dedup = %v, want 5m", info.Config.Duplicates)
	}
}

func TestBus_EventsStream_Config(t *testing.T) {
	b := newTestBus(t)
	js := b.JetStream()
	ctx := context.Background()

	s, err := js.Stream(ctx, bus.StreamEvents)
	if err != nil {
		t.Fatalf("stream not found: %v", err)
	}
	info := s.CachedInfo()
	if info.Config.Retention != jetstream.InterestPolicy {
		t.Errorf("retention = %v, want InterestPolicy", info.Config.Retention)
	}
	// StreamEvents has no explicit Duplicates configured; NATS applies its
	// server-side default (2m). Verify retention policy is the key property.
}

func TestBus_ConsumersCreated(t *testing.T) {
	b := newTestBus(t)
	js := b.JetStream()
	ctx := context.Background()

	cases := []struct {
		stream   string
		consumer string
	}{
		{bus.StreamWork, bus.ConsumerReview},
		{bus.StreamWork, bus.ConsumerPublish},
		{bus.StreamWork, bus.ConsumerTriage},
		{bus.StreamWork, bus.ConsumerImplement},
		{bus.StreamWork, bus.ConsumerState},
		{bus.StreamDiscovery, bus.ConsumerDiscovery},
	}
	for _, tc := range cases {
		_, err := js.Consumer(ctx, tc.stream, tc.consumer)
		if err != nil {
			t.Errorf("consumer %s on %s not found: %v", tc.consumer, tc.stream, err)
		}
	}
}

func TestBus_ReviewConsumer_MaxAckPending(t *testing.T) {
	b := newTestBus(t)
	js := b.JetStream()
	ctx := context.Background()

	c, err := js.Consumer(ctx, bus.StreamWork, bus.ConsumerReview)
	if err != nil {
		t.Fatalf("consumer not found: %v", err)
	}
	info := c.CachedInfo()
	// newTestBus uses MaxConcurrentWorkers=3
	if info.Config.MaxAckPending != 3 {
		t.Errorf("MaxAckPending = %d, want 3", info.Config.MaxAckPending)
	}
}

func TestBus_StateConsumer_DoubleMaxAck(t *testing.T) {
	b := newTestBus(t)
	js := b.JetStream()
	ctx := context.Background()

	c, err := js.Consumer(ctx, bus.StreamWork, bus.ConsumerState)
	if err != nil {
		t.Fatalf("consumer not found: %v", err)
	}
	info := c.CachedInfo()
	// state-worker gets MaxConcurrentWorkers * 2 = 6
	if info.Config.MaxAckPending != 6 {
		t.Errorf("MaxAckPending = %d, want 6", info.Config.MaxAckPending)
	}
}

func TestBus_DiscoveryConsumer_SinglePending(t *testing.T) {
	b := newTestBus(t)
	js := b.JetStream()
	ctx := context.Background()

	c, err := js.Consumer(ctx, bus.StreamDiscovery, bus.ConsumerDiscovery)
	if err != nil {
		t.Fatalf("consumer not found: %v", err)
	}
	info := c.CachedInfo()
	if info.Config.MaxAckPending != 1 {
		t.Errorf("MaxAckPending = %d, want 1", info.Config.MaxAckPending)
	}
}
