package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStreamEventsReturnsWhenContextCanceledWithBlockedSend(t *testing.T) {
	eventFlushed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}
		fmt.Fprint(w, "event: heartbeat\ndata: {}\n\n")
		flusher.Flush()
		close(eventFlushed)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "")
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan SSEEvent)
	done := make(chan error, 1)
	go func() {
		done <- client.StreamEvents(ctx, events)
	}()

	select {
	case <-eventFlushed:
	case <-time.After(time.Second):
		t.Fatal("server did not flush SSE event")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected context cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("StreamEvents did not return after context cancellation")
	}
}

func TestGetHealthParsesDaemonVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","version":"0.8.0","started_at":"2026-08-04T09:20:26Z"}`)
	}))
	t.Cleanup(srv.Close)

	h, err := New(srv.URL, "").GetHealth()
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if h.Version != "0.8.0" {
		t.Errorf("Version = %q, want 0.8.0", h.Version)
	}
	if h.Status != "ok" {
		t.Errorf("Status = %q, want ok", h.Status)
	}
}

// A daemon built before version stamping omits the field entirely; GetHealth
// must succeed with an empty Version rather than failing the whole fetch.
func TestGetHealthToleratesMissingVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	t.Cleanup(srv.Close)

	h, err := New(srv.URL, "").GetHealth()
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if h.Version != "" {
		t.Errorf("Version = %q, want empty", h.Version)
	}
}
