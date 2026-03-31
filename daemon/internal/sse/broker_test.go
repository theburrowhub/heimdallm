package sse_test

import (
	"testing"
	"time"

	"github.com/heimdallr/daemon/internal/sse"
)

func TestBroker_PublishAndReceive(t *testing.T) {
	b := sse.NewBroker()
	b.Start()
	defer b.Stop()

	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	event := sse.Event{Type: "review_completed", Data: `{"pr_id":1}`}
	b.Publish(event)

	select {
	case got := <-ch:
		if got.Type != "review_completed" {
			t.Errorf("event type: got %q want %q", got.Type, "review_completed")
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for event")
	}
}

func TestBroker_MultipleSubscribers(t *testing.T) {
	b := sse.NewBroker()
	b.Start()
	defer b.Stop()

	ch1 := b.Subscribe()
	ch2 := b.Subscribe()
	defer b.Unsubscribe(ch1)
	defer b.Unsubscribe(ch2)

	b.Publish(sse.Event{Type: "pr_detected", Data: `{}`})

	for _, ch := range []chan sse.Event{ch1, ch2} {
		select {
		case e := <-ch:
			if e.Type != "pr_detected" {
				t.Errorf("expected pr_detected, got %q", e.Type)
			}
		case <-time.After(time.Second):
			t.Error("timeout for subscriber")
		}
	}
}
