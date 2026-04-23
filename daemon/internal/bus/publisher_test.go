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
