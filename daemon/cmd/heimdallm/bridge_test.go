package main

import (
	"fmt"
	"slices"
	"testing"

	"github.com/heimdallm/daemon/internal/bus"
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
