package sse

import (
	"encoding/json"
	"testing"
	"time"
)

type capturePublisher struct {
	events []Event
}

func (c *capturePublisher) Publish(e Event) {
	c.events = append(c.events, e)
}

func TestEmitPollingStarted_FormatsPayload(t *testing.T) {
	pub := &capturePublisher{}
	EmitPollingStarted(pub, "prs", []string{"acme/foo", "acme/bar"})

	if len(pub.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(pub.events))
	}
	got := pub.events[0]
	if got.Type != EventPollingStarted {
		t.Errorf("type: got %q want %q", got.Type, EventPollingStarted)
	}
	var payload struct {
		Kind  string   `json:"kind"`
		Repos []string `json:"repos"`
	}
	if err := json.Unmarshal([]byte(got.Data), &payload); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if payload.Kind != "prs" {
		t.Errorf("kind: got %q want %q", payload.Kind, "prs")
	}
	if len(payload.Repos) != 2 || payload.Repos[0] != "acme/foo" || payload.Repos[1] != "acme/bar" {
		t.Errorf("repos: got %v", payload.Repos)
	}
}

func TestEmitPollingCompleted_FormatsPayload(t *testing.T) {
	pub := &capturePublisher{}
	EmitPollingCompleted(pub, "issues", 7, 1234*time.Millisecond)

	if len(pub.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(pub.events))
	}
	got := pub.events[0]
	if got.Type != EventPollingCompleted {
		t.Errorf("type: got %q want %q", got.Type, EventPollingCompleted)
	}
	var payload struct {
		Kind       string `json:"kind"`
		Count      int    `json:"count"`
		DurationMs int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal([]byte(got.Data), &payload); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if payload.Kind != "issues" || payload.Count != 7 || payload.DurationMs != 1234 {
		t.Errorf("payload: got %+v", payload)
	}
}

func TestEmit_NilPublisherIsSafe(t *testing.T) {
	// Should not panic — production code may call before publisher is wired.
	EmitPollingStarted(nil, "prs", []string{"acme/foo"})
	EmitPollingCompleted(nil, "issues", 0, 0)
}
