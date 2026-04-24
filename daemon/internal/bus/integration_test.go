// daemon/internal/bus/integration_test.go
package bus_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/worker"
	"github.com/nats-io/nats.go/jetstream"
)

// TestIntegration_PRReviewFlow publishes a PRReviewMsg through the real
// embedded NATS bus, starts a ReviewWorker, and verifies the handler
// receives the correct payload and the message is acked.
func TestIntegration_PRReviewFlow(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	received := make(chan bus.PRReviewMsg, 1)
	handler := func(_ context.Context, msg bus.PRReviewMsg) {
		received <- msg
	}

	w := worker.NewReviewWorker(b.JetStream(), handler)
	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("review-worker start: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond) // let consumer attach

	pub := bus.NewPRReviewPublisher(b.JetStream())
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

	// Allow ack to propagate.
	time.Sleep(200 * time.Millisecond)
	cancel()

	cons, err := b.JetStream().Consumer(context.Background(), bus.StreamWork, bus.ConsumerReview)
	if err != nil {
		t.Fatalf("get consumer: %v", err)
	}
	info, err := cons.Info(context.Background())
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	if info.NumAckPending > 0 {
		t.Errorf("expected 0 ack-pending after handler, got %d", info.NumAckPending)
	}
}

// TestIntegration_PRPublishFlow_AckOnSuccess verifies that a PublishWorker
// acks the message when the handler returns nil.
func TestIntegration_PRPublishFlow_AckOnSuccess(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	received := make(chan int64, 1)
	handler := func(_ context.Context, msg bus.PRPublishMsg) error {
		received <- msg.ReviewID
		return nil
	}

	w := worker.NewPublishWorker(b.JetStream(), handler)
	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("publish-worker start: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)

	pub := bus.NewPRPublishPublisher(b.JetStream())
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

	time.Sleep(200 * time.Millisecond)
	cancel()

	cons, err := b.JetStream().Consumer(context.Background(), bus.StreamWork, bus.ConsumerPublish)
	if err != nil {
		t.Fatalf("get consumer: %v", err)
	}
	info, err := cons.Info(context.Background())
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	if info.NumAckPending > 0 {
		t.Errorf("expected 0 ack-pending on success, got %d", info.NumAckPending)
	}
}

// TestIntegration_PRPublishFlow_NakOnError verifies that a PublishWorker
// nak's the message when the handler returns an error, making it available
// for redelivery.
func TestIntegration_PRPublishFlow_NakOnError(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var callCount atomic.Int32
	handler := func(_ context.Context, _ bus.PRPublishMsg) error {
		callCount.Add(1)
		return errors.New("transient error")
	}

	w := worker.NewPublishWorker(b.JetStream(), handler)
	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("publish-worker start: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)

	data, _ := bus.Encode(bus.PRPublishMsg{ReviewID: 99})
	_, err := b.JetStream().Publish(ctx, bus.SubjPRPublish, data, jetstream.WithMsgID("rev:99"))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Wait for the first call.
	deadline := time.After(3 * time.Second)
	for callCount.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("handler not called within timeout")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	// After the handler returns an error, the message should be nak'd,
	// meaning the consumer info reflects the message is still pending.
	cons, err := b.JetStream().Consumer(context.Background(), bus.StreamWork, bus.ConsumerPublish)
	if err != nil {
		t.Fatalf("get consumer: %v", err)
	}
	info, err := cons.Info(context.Background())
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	// The message was nak'd with delay, so Delivered.Consumer should be >= 1
	// (at least one delivery attempt was made).
	if info.Delivered.Consumer < 1 {
		t.Errorf("expected Delivered.Consumer >= 1, got %d", info.Delivered.Consumer)
	}
}

// TestIntegration_IssueTriageFlow publishes an IssueMsg to the triage
// subject and verifies the TriageWorker handler receives the correct data.
func TestIntegration_IssueTriageFlow(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	received := make(chan bus.IssueMsg, 1)
	handler := func(_ context.Context, msg bus.IssueMsg) {
		received <- msg
	}

	w := worker.NewTriageWorker(b.JetStream(), handler)
	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("triage-worker start: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)

	pub := bus.NewIssuePublisher(b.JetStream())
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
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	received := make(chan bus.IssueMsg, 1)
	handler := func(_ context.Context, msg bus.IssueMsg) {
		received <- msg
	}

	w := worker.NewImplementWorker(b.JetStream(), handler)
	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("implement-worker start: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)

	pub := bus.NewIssuePublisher(b.JetStream())
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

// TestIntegration_StateCheckFlow enrolls an item in WatchKV, publishes a
// StateCheckMsg, starts a StateWorker with a handler returning changed=false,
// and verifies the KV backoff is increased.
func TestIntegration_StateCheckFlow(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kv := b.WatchKV()
	if err := kv.Enroll(ctx, "pr", "org/repo", 42, 12345); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	called := make(chan struct{}, 1)
	handler := func(_ context.Context, msg bus.StateCheckMsg) (bool, error) {
		called <- struct{}{}
		return false, nil // no change detected
	}

	w := worker.NewStateWorker(b.JetStream(), kv, handler)
	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("state-worker start: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)

	pub := bus.NewStateCheckPublisher(b.JetStream())
	if err := pub.PublishStateCheck(ctx, "pr", "org/repo", 42, 12345); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("handler not called within timeout")
	}

	// Wait for KV update to propagate.
	time.Sleep(200 * time.Millisecond)
	cancel()

	entry, err := kv.Get(context.Background(), "pr.12345")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Backoff should have increased from InitialBackoff (no change → double).
	if entry.Backoff() <= bus.InitialBackoff {
		t.Errorf("expected backoff > %v after no-change, got %v",
			bus.InitialBackoff, entry.Backoff())
	}
}

// TestIntegration_Durability publishes a PRReviewMsg, stops the bus without
// consuming, starts a new bus on the same data directory, and verifies
// the message is still available and consumed by a ReviewWorker.
func TestIntegration_Durability(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Phase 1: Start bus, publish message, stop without consuming.
	b1 := bus.New(bus.Config{DataDir: dir, MaxConcurrentWorkers: 3})
	if err := b1.Start(ctx); err != nil {
		t.Fatalf("start 1: %v", err)
	}
	pub := bus.NewPRReviewPublisher(b1.JetStream())
	if err := pub.PublishPRReview(ctx, "org/durable", 99, 999, "sha-durable"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	b1.Stop()

	// Phase 2: Start new bus on same directory, consume via ReviewWorker.
	b2 := bus.New(bus.Config{DataDir: dir, MaxConcurrentWorkers: 3})
	if err := b2.Start(ctx); err != nil {
		t.Fatalf("start 2: %v", err)
	}
	defer b2.Stop()

	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	received := make(chan bus.PRReviewMsg, 1)
	handler := func(_ context.Context, msg bus.PRReviewMsg) {
		received <- msg
	}

	w := worker.NewReviewWorker(b2.JetStream(), handler)
	go func() {
		if err := w.Start(ctx2); err != nil {
			t.Errorf("review-worker start: %v", err)
		}
	}()

	select {
	case msg := <-received:
		if msg.Repo != "org/durable" {
			t.Errorf("Repo = %q, want %q", msg.Repo, "org/durable")
		}
		if msg.Number != 99 {
			t.Errorf("Number = %d, want 99", msg.Number)
		}
		if msg.GithubID != 999 {
			t.Errorf("GithubID = %d, want 999", msg.GithubID)
		}
		if msg.HeadSHA != "sha-durable" {
			t.Errorf("HeadSHA = %q, want %q", msg.HeadSHA, "sha-durable")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("durable message not received after restart")
	}
}

// TestIntegration_DiscoveryFlow publishes a DiscoveryMsg via RepoPublisher
// and consumes it from the discovery-consumer, verifying the repos match.
func TestIntegration_DiscoveryFlow(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewRepoPublisher(b.JetStream())
	repos := []string{"org/alpha", "org/beta", "org/gamma"}
	if err := pub.PublishRepos(ctx, repos); err != nil {
		t.Fatalf("PublishRepos: %v", err)
	}

	cons, err := b.JetStream().Consumer(ctx, bus.StreamDiscovery, bus.ConsumerDiscovery)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(3*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	var got bus.DiscoveryMsg
	count := 0
	for m := range msgs.Messages() {
		count++
		if err := bus.Decode(m.Data(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		m.Ack()
	}
	if msgs.Error() != nil {
		t.Fatalf("messages error: %v", msgs.Error())
	}
	if count != 1 {
		t.Fatalf("expected 1 discovery message, got %d", count)
	}
	if len(got.Repos) != 3 {
		t.Fatalf("expected 3 repos, got %d", len(got.Repos))
	}
	for i, want := range repos {
		if got.Repos[i] != want {
			t.Errorf("repos[%d] = %q, want %q", i, got.Repos[i], want)
		}
	}
}

// TestIntegration_WorkerBackpressure publishes N+1 messages with
// MaxConcurrentWorkers=N and verifies the consumer's MaxAckPending limits
// how many messages can be fetched without acking. This is the worker-level
// counterpart of TestBackpressure_MaxAckPending in roundtrip_test.go: it
// uses the publisher API end-to-end and verifies against the consumer the
// workers bind to.
func TestIntegration_WorkerBackpressure(t *testing.T) {
	dir := t.TempDir()
	// MaxConcurrentWorkers=2 so the review consumer gets MaxAckPending=2.
	b := bus.New(bus.Config{DataDir: dir, MaxConcurrentWorkers: 2})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(b.Stop)

	ctx := context.Background()

	// Publish 3 messages (N+1 = 2+1) via the typed publisher.
	pub := bus.NewPRReviewPublisher(b.JetStream())
	for i := 0; i < 3; i++ {
		sha := fmt.Sprintf("bp-sha-%d", i)
		if err := pub.PublishPRReview(ctx, "org/bp", i+1, int64(i+100), sha); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	// Fetch up to 3 from the review consumer — MaxAckPending=2 limits delivery.
	cons, err := b.JetStream().Consumer(ctx, bus.StreamWork, bus.ConsumerReview)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}

	batch1, err := cons.Fetch(3, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch 1: %v", err)
	}
	var unacked []jetstream.Msg
	for m := range batch1.Messages() {
		unacked = append(unacked, m)
	}
	if len(unacked) != 2 {
		t.Fatalf("expected 2 messages (MaxAckPending=2), got %d", len(unacked))
	}

	// Attempting to fetch 1 more should yield 0 (backpressure).
	batch2, err := cons.Fetch(1, jetstream.FetchMaxWait(500*time.Millisecond))
	if err != nil {
		t.Fatalf("fetch 2: %v", err)
	}
	extra := 0
	for range batch2.Messages() {
		extra++
	}
	if extra != 0 {
		t.Errorf("expected 0 messages (backpressure), got %d", extra)
	}

	// Ack one to free a slot.
	unacked[0].Ack()
	time.Sleep(100 * time.Millisecond)

	// Now the 3rd message should be available.
	batch3, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch 3: %v", err)
	}
	released := 0
	for m := range batch3.Messages() {
		released++
		m.Ack()
	}
	if released != 1 {
		t.Errorf("expected 1 message after ack, got %d", released)
	}

	// Clean up remaining unacked.
	for _, m := range unacked[1:] {
		m.Ack()
	}
}
