// daemon/internal/worker/triage_test.go
package worker_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/worker"
)

func TestTriageWorker_ConsumesAndCallsHandler(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		received []bus.IssueMsg
	)
	handler := func(_ context.Context, msg bus.IssueMsg) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, msg)
	}

	w := worker.NewTriageWorker(b.JetStream(), handler)
	go func() { w.Start(ctx) }()
	time.Sleep(200 * time.Millisecond)

	pub := bus.NewIssuePublisher(b.JetStream())
	if err := pub.PublishIssueTriage(ctx, "org/repo", 42, 12345); err != nil {
		t.Fatalf("publish: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1, got %d", len(received))
	}
	msg := received[0]
	if msg.Repo != "org/repo" || msg.Number != 42 || msg.GithubID != 12345 {
		t.Errorf("unexpected: %+v", msg)
	}
}
