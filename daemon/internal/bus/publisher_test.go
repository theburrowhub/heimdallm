package bus_test

import (
	"context"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

func TestRepoPublisher_PublishRepos(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewRepoPublisher(b.JetStream())
	repos := []string{"org/repo1", "org/repo2", "org/repo3"}

	if err := pub.PublishRepos(ctx, repos); err != nil {
		t.Fatalf("PublishRepos: %v", err)
	}

	cons, err := b.JetStream().Consumer(ctx, bus.StreamDiscovery, bus.ConsumerDiscovery)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var got bus.DiscoveryMsg
	for m := range msgs.Messages() {
		if err := bus.Decode(m.Data(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		m.Ack()
	}

	if len(got.Repos) != 3 {
		t.Fatalf("expected 3 repos, got %d", len(got.Repos))
	}
	if got.Repos[0] != "org/repo1" || got.Repos[1] != "org/repo2" || got.Repos[2] != "org/repo3" {
		t.Errorf("unexpected repos: %v", got.Repos)
	}
}

func TestPRReviewPublisher_Publish(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewPRReviewPublisher(b.JetStream())

	err := pub.PublishPRReview(ctx, "org/repo", 42, 12345, "abc123")
	if err != nil {
		t.Fatalf("PublishPRReview: %v", err)
	}

	cons, err := b.JetStream().Consumer(ctx, bus.StreamWork, bus.ConsumerReview)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var got bus.PRReviewMsg
	for m := range msgs.Messages() {
		if err := bus.Decode(m.Data(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		m.Ack()
	}
	if got.Repo != "org/repo" || got.Number != 42 || got.GithubID != 12345 || got.HeadSHA != "abc123" {
		t.Errorf("unexpected payload: %+v", got)
	}
}

func TestPRReviewPublisher_Dedup(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewPRReviewPublisher(b.JetStream())

	if err := pub.PublishPRReview(ctx, "org/repo", 1, 100, "sha1"); err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	if err := pub.PublishPRReview(ctx, "org/repo", 1, 100, "sha1"); err != nil {
		t.Fatalf("publish 2: %v", err)
	}

	cons, err := b.JetStream().Consumer(ctx, bus.StreamWork, bus.ConsumerReview)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(2, jetstream.FetchMaxWait(1*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	count := 0
	for m := range msgs.Messages() {
		count++
		m.Ack()
	}
	if count != 1 {
		t.Errorf("expected 1 (dedup), got %d", count)
	}
}

func TestPRPublishPublisher_Publish(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewPRPublishPublisher(b.JetStream())

	if err := pub.PublishPRPublish(ctx, 42); err != nil {
		t.Fatalf("PublishPRPublish: %v", err)
	}

	cons, err := b.JetStream().Consumer(ctx, bus.StreamWork, bus.ConsumerPublish)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var got bus.PRPublishMsg
	for m := range msgs.Messages() {
		if err := bus.Decode(m.Data(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		m.Ack()
	}
	if got.ReviewID != 42 {
		t.Errorf("ReviewID = %d, want 42", got.ReviewID)
	}
}

func TestPRPublishPublisher_Dedup(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewPRPublishPublisher(b.JetStream())

	if err := pub.PublishPRPublish(ctx, 42); err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	if err := pub.PublishPRPublish(ctx, 42); err != nil {
		t.Fatalf("publish 2: %v", err)
	}

	cons, err := b.JetStream().Consumer(ctx, bus.StreamWork, bus.ConsumerPublish)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(2, jetstream.FetchMaxWait(1*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	count := 0
	for m := range msgs.Messages() {
		count++
		m.Ack()
	}
	if count != 1 {
		t.Errorf("expected 1 (dedup), got %d", count)
	}
}

func TestIssuePublisher_Triage(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewIssuePublisher(b.JetStream())
	if err := pub.PublishIssueTriage(ctx, "org/repo", 10, 555); err != nil {
		t.Fatalf("PublishIssueTriage: %v", err)
	}

	cons, err := b.JetStream().Consumer(ctx, bus.StreamWork, bus.ConsumerTriage)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var got bus.IssueMsg
	for m := range msgs.Messages() {
		if err := bus.Decode(m.Data(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		m.Ack()
	}
	if got.Repo != "org/repo" || got.Number != 10 || got.GithubID != 555 {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestIssuePublisher_Implement(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewIssuePublisher(b.JetStream())
	if err := pub.PublishIssueImplement(ctx, "org/repo", 20, 666); err != nil {
		t.Fatalf("PublishIssueImplement: %v", err)
	}

	cons, err := b.JetStream().Consumer(ctx, bus.StreamWork, bus.ConsumerImplement)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var got bus.IssueMsg
	for m := range msgs.Messages() {
		if err := bus.Decode(m.Data(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		m.Ack()
	}
	if got.Repo != "org/repo" || got.Number != 20 || got.GithubID != 666 {
		t.Errorf("unexpected: %+v", got)
	}
}
