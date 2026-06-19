package main

import (
	"fmt"
	"testing"

	"github.com/heimdallm/daemon/internal/sse"
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
		if subject == "heimdallm.events.boom" {
			panic("simulated nats publish panic")
		}
		got = append(got, subject)
		return nil
	}

	// Returns (does not propagate the panic) once the channel is drained.
	publishBridgeEvents(events, publish)

	want := []string{"heimdallm.events.a", "heimdallm.events.c"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("expected bridge to process %v after recovering, got %v", want, got)
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
