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
