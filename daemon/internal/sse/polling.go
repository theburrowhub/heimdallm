package sse

import (
	"encoding/json"
	"log/slog"
	"time"
)

// Publisher is the minimal interface the polling emitters need from the
// SSE broker. *Broker satisfies this; tests can pass a capturing fake.
type Publisher interface {
	Publish(Event)
}

// EmitPollingStarted publishes a polling_started event. Safe to call with a
// nil publisher (no-op).
func EmitPollingStarted(pub Publisher, kind string, repos []string) {
	if pub == nil {
		return
	}
	if repos == nil {
		repos = []string{}
	}
	data, err := json.Marshal(map[string]any{
		"kind":  kind,
		"repos": repos,
	})
	if err != nil {
		slog.Error("sse: marshal polling_started", "err", err)
		return
	}
	pub.Publish(Event{Type: EventPollingStarted, Data: string(data)})
}

// EmitPollingCompleted publishes a polling_completed event with the cycle's
// item count and elapsed duration. Safe to call with a nil publisher.
func EmitPollingCompleted(pub Publisher, kind string, count int, duration time.Duration) {
	if pub == nil {
		return
	}
	data, err := json.Marshal(map[string]any{
		"kind":        kind,
		"count":       count,
		"duration_ms": duration.Milliseconds(),
	})
	if err != nil {
		slog.Error("sse: marshal polling_completed", "err", err)
		return
	}
	pub.Publish(Event{Type: EventPollingCompleted, Data: string(data)})
}
