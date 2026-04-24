// daemon/internal/bus/integration_test.go
package bus_test

import (
	"context"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/worker"
	"github.com/nats-io/nats.go"
)

// TestIntegration_PRReviewFlow publishes a PRReviewMsg through the real
// embedded NATS bus, starts a ReviewWorker, and verifies the handler
// receives the correct payload.
func TestIntegration_PRReviewFlow(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	received := make(chan bus.PRReviewMsg, 1)
	handler := func(_ context.Context, msg bus.PRReviewMsg) {
		received <- msg
	}

	w := worker.NewReviewWorker(conn, 3, handler)
	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("review-worker start: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond) // let subscription attach

	pub := bus.NewPRReviewPublisher(conn)
	if err := pub.PublishPRReview(ctx, "org/repo", 42, 12345, "abc123"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case msg := <-received:
		if msg.Repo != "org/repo" {
			t.Errorf("Repo = %q, want %q", msg.Repo, "org/repo")
		}
		if msg.Number != 42 {
			t.Errorf("Number = %d, want 42", msg.Number)
		}
		if msg.GithubID != 12345 {
			t.Errorf("GithubID = %d, want 12345", msg.GithubID)
		}
		if msg.HeadSHA != "abc123" {
			t.Errorf("HeadSHA = %q, want %q", msg.HeadSHA, "abc123")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler not called within timeout")
	}
}

// TestIntegration_PRPublishFlow_AckOnSuccess verifies that a PublishWorker
// calls the handler and completes without error.
func TestIntegration_PRPublishFlow_AckOnSuccess(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	received := make(chan int64, 1)
	handler := func(_ context.Context, msg bus.PRPublishMsg) error {
		received <- msg.ReviewID
		return nil
	}

	w := worker.NewPublishWorker(conn, 3, handler)
	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("publish-worker start: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)

	pub := bus.NewPRPublishPublisher(conn)
	if err := pub.PublishPRPublish(ctx, 42); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case id := <-received:
		if id != 42 {
			t.Errorf("ReviewID = %d, want 42", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler not called within timeout")
	}
}

// TestIntegration_IssueTriageFlow publishes an IssueMsg to the triage
// subject and verifies the TriageWorker handler receives the correct data.
func TestIntegration_IssueTriageFlow(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	received := make(chan bus.IssueMsg, 1)
	handler := func(_ context.Context, msg bus.IssueMsg) {
		received <- msg
	}

	w := worker.NewTriageWorker(conn, 3, handler)
	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("triage-worker start: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)

	pub := bus.NewIssuePublisher(conn)
	if err := pub.PublishIssueTriage(ctx, "org/my-repo", 10, 555); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case msg := <-received:
		if msg.Repo != "org/my-repo" {
			t.Errorf("Repo = %q, want %q", msg.Repo, "org/my-repo")
		}
		if msg.Number != 10 {
			t.Errorf("Number = %d, want 10", msg.Number)
		}
		if msg.GithubID != 555 {
			t.Errorf("GithubID = %d, want 555", msg.GithubID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler not called within timeout")
	}
}

// TestIntegration_IssueImplementFlow publishes an IssueMsg to the implement
// subject and verifies the ImplementWorker handler receives the correct data.
func TestIntegration_IssueImplementFlow(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	received := make(chan bus.IssueMsg, 1)
	handler := func(_ context.Context, msg bus.IssueMsg) {
		received <- msg
	}

	w := worker.NewImplementWorker(conn, 3, handler)
	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("implement-worker start: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)

	pub := bus.NewIssuePublisher(conn)
	if err := pub.PublishIssueImplement(ctx, "org/impl-repo", 77, 99999); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case msg := <-received:
		if msg.Repo != "org/impl-repo" {
			t.Errorf("Repo = %q, want %q", msg.Repo, "org/impl-repo")
		}
		if msg.Number != 77 {
			t.Errorf("Number = %d, want 77", msg.Number)
		}
		if msg.GithubID != 99999 {
			t.Errorf("GithubID = %d, want 99999", msg.GithubID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler not called within timeout")
	}
}

// TestIntegration_StateCheckFlow enrolls an item in WatchStore, publishes a
// StateCheckMsg, starts a StateWorker with a handler returning changed=false,
// and verifies the backoff is increased.
func TestIntegration_StateCheckFlow(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws := env.watch
	if err := ws.Enroll(ctx, "pr", "org/repo", 42, 12345); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	called := make(chan struct{}, 1)
	handler := func(_ context.Context, msg bus.StateCheckMsg) (bool, error) {
		called <- struct{}{}
		return false, nil // no change detected
	}

	w := worker.NewStateWorker(conn, 3, ws, handler)
	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("state-worker start: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)

	pub := bus.NewStateCheckPublisher(conn)
	if err := pub.PublishStateCheck(ctx, "pr", "org/repo", 42, 12345); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("handler not called within timeout")
	}

	// Wait for backoff update to propagate.
	time.Sleep(200 * time.Millisecond)

	entry, err := ws.Get(context.Background(), "pr.12345")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Backoff should have increased from InitialBackoff (no change -> double).
	if entry.Backoff() <= bus.InitialBackoff {
		t.Errorf("expected backoff > %v after no-change, got %v",
			bus.InitialBackoff, entry.Backoff())
	}
}

// TestIntegration_DiscoveryFlow publishes a DiscoveryMsg via RepoPublisher
// and verifies it arrives via core NATS subscription.
func TestIntegration_DiscoveryFlow(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()
	ctx := context.Background()

	ch := make(chan bus.DiscoveryMsg, 1)
	sub, err := conn.Subscribe(bus.SubjDiscoveryRepos, func(msg *nats.Msg) {
		var dm bus.DiscoveryMsg
		if err := bus.Decode(msg.Data, &dm); err != nil {
			return
		}
		ch <- dm
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	pub := bus.NewRepoPublisher(conn)
	repos := []string{"org/alpha", "org/beta", "org/gamma"}
	if err := pub.PublishRepos(ctx, repos); err != nil {
		t.Fatalf("PublishRepos: %v", err)
	}
	conn.Flush()

	select {
	case got := <-ch:
		if len(got.Repos) != 3 {
			t.Fatalf("expected 3 repos, got %d", len(got.Repos))
		}
		for i, want := range repos {
			if got.Repos[i] != want {
				t.Errorf("repos[%d] = %q, want %q", i, got.Repos[i], want)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for discovery message")
	}
}
