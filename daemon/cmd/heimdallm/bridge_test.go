package main

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/nats-io/nats.go"
)

// A panic while publishing one event must not propagate (which, in the real
// bare goroutine, would crash the whole daemon) and must not stop the bridge:
// the events before and after the panicking one are still published.
func TestPublishBridgeEvents_RecoversFromPanicAndContinues(t *testing.T) {
	events := make(chan sse.Event, 3)
	events <- sse.Event{Type: "a", Data: "1"}
	events <- sse.Event{Type: "boom", Data: "2"}
	events <- sse.Event{Type: "c", Data: "3"}
	close(events)

	var got []string
	publish := func(subject string, data []byte) error {
		if subject == bus.SubjEventPrefix+"boom" {
			panic("simulated nats publish panic")
		}
		got = append(got, subject)
		return nil
	}

	// Returns (does not propagate the panic) once the channel is drained.
	publishBridgeEvents(events, publish)

	want := []string{bus.SubjEventPrefix + "a", bus.SubjEventPrefix + "c"}
	if !slices.Equal(got, want) {
		t.Fatalf("expected bridge to process %v after recovering, got %v", want, got)
	}
}

// recover() must also catch a non-string / runtime panic (here, assignment to
// a nil map), not just explicit panic("string") calls.
func TestPublishBridgeEvents_RecoversFromRuntimePanic(t *testing.T) {
	events := make(chan sse.Event, 2)
	events <- sse.Event{Type: "nilmap", Data: "1"}
	events <- sse.Event{Type: "ok", Data: "2"}
	close(events)

	var got []string
	publish := func(subject string, data []byte) error {
		if subject == bus.SubjEventPrefix+"nilmap" {
			var m map[string]int
			m["boom"] = 1 // runtime panic: assignment to entry in nil map
		}
		got = append(got, subject)
		return nil
	}

	publishBridgeEvents(events, publish)

	if want := []string{bus.SubjEventPrefix + "ok"}; !slices.Equal(got, want) {
		t.Fatalf("expected bridge to recover from a runtime panic and continue; want %v, got %v", want, got)
	}
}

// A publish error is logged but must not stop the bridge — every event is
// still attempted.
func TestPublishBridgeEvents_ContinuesAfterPublishError(t *testing.T) {
	events := make(chan sse.Event, 2)
	events <- sse.Event{Type: "x", Data: "1"}
	events <- sse.Event{Type: "y", Data: "2"}
	close(events)

	attempts := 0
	publish := func(subject string, data []byte) error {
		attempts++
		return fmt.Errorf("publish failed")
	}

	publishBridgeEvents(events, publish)

	if attempts != 2 {
		t.Fatalf("expected both events attempted despite errors, got %d", attempts)
	}
}

func TestPublishBridgeEventsSkipsOnlySuccessfullyForwardedRepoDiscovery(t *testing.T) {
	events := make(chan sse.Event, 3)
	events <- sse.Event{Type: sse.EventRepoDiscovered, Data: `{"repo":"org/already"}`, NATSForwarded: true}
	events <- sse.Event{Type: sse.EventRepoDiscovered, Data: `{"repo":"org/fallback"}`}
	events <- sse.Event{Type: sse.EventReviewStarted, Data: `{}`}
	close(events)

	var got []string
	publishBridgeEvents(events, func(subject string, _ []byte) error {
		got = append(got, subject)
		return nil
	})
	want := []string{
		bus.SubjEventPrefix + sse.EventRepoDiscovered,
		bus.SubjEventPrefix + sse.EventReviewStarted,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("bridged subjects = %v, want %v", got, want)
	}
}

func TestOrderedNATSEventPublisherFlushesBatchInOrder(t *testing.T) {
	conn := newInProcessNATS(t)
	messages := make(chan *nats.Msg, 2)
	sub, err := conn.ChanSubscribe(bus.SubjEventPrefix+">", messages)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush subscription: %v", err)
	}

	publish := newOrderedNATSEventPublisher(conn)
	if err := publish([]sse.Event{{Type: "first", Data: "1"}, {Type: "second", Data: "2"}}); err != nil {
		t.Fatalf("publish ordered batch: %v", err)
	}
	for _, want := range []string{"first", "second"} {
		select {
		case got := <-messages:
			if got.Subject != bus.SubjEventPrefix+want {
				t.Fatalf("subject = %q, want %q", got.Subject, bus.SubjEventPrefix+want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for %s event", want)
		}
	}
}

func TestOrderedNATSEventPublisherReturnsPublishError(t *testing.T) {
	conn := newInProcessNATS(t)
	conn.Close()
	if err := newOrderedNATSEventPublisher(conn)([]sse.Event{{Type: "event", Data: "payload"}}); err == nil {
		t.Fatal("publish on closed NATS connection returned nil")
	}
}
