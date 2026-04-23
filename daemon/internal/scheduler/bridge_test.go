package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/scheduler"
)

func TestBridgeDiscovery_ForwardsRepos(t *testing.T) {
	dir := t.TempDir()
	b := bus.New(bus.Config{DataDir: dir, MaxConcurrentWorkers: 3})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("bus start: %v", err)
	}
	defer b.Stop()

	pub := bus.NewRepoPublisher(b.JetStream())

	pipe := scheduler.NewPipeline(scheduler.PipelineConfig{
		DiscoveryInterval: 1 * time.Hour,
		PollInterval:      1 * time.Hour,
		WatchInterval:     1 * time.Hour,
	}, scheduler.PipelineDeps{
		Discovery: &fakeDiscovery{repos: nil},
		Tier1ConfigFn: func() scheduler.Tier1Config {
			return scheduler.Tier1Config{}
		},
		Publisher:      pub,
		JS:             b.JetStream(),
		PRFetcher:      &noopPRFetcher{},
		PRProcessor:    &noopPRProcessor{},
		PRPublisher:    &noopPRPublisher{},
		IssueProcessor: &noopIssueProcessor{},
		Store:          &noopStore{},
		Tier2ConfigFn:  func() []string { return nil },
	})
	pipe.Start(context.Background(), false)
	defer pipe.Stop()

	// Give the bridge goroutine time to start its consumer iterator
	time.Sleep(200 * time.Millisecond)

	if err := pub.PublishRepos(context.Background(), []string{"org/repo1", "org/repo2"}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	ctx := context.Background()
	cons, err := b.JetStream().Consumer(ctx, bus.StreamDiscovery, bus.ConsumerDiscovery)
	if err != nil {
		t.Fatalf("get consumer: %v", err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	if info.NumPending > 0 {
		t.Errorf("expected 0 pending messages (bridge should have consumed), got %d", info.NumPending)
	}
	if info.NumAckPending > 0 {
		t.Errorf("expected 0 ack-pending (bridge should have acked), got %d", info.NumAckPending)
	}
}

// ── noop stubs for PipelineDeps (Tier 2 unused in bridge test) ──────────

type noopPRFetcher struct{}

func (n *noopPRFetcher) FetchPRsToReview() ([]scheduler.Tier2PR, error) { return nil, nil }

type noopPRProcessor struct{}

func (n *noopPRProcessor) ProcessPR(_ context.Context, _ scheduler.Tier2PR) error { return nil }
func (n *noopPRProcessor) PublishPending()                                        {}

type noopIssueProcessor struct{}

func (n *noopIssueProcessor) ProcessRepo(_ context.Context, _ string) (int, error) { return 0, nil }

type noopPRPublisher struct{}

func (n *noopPRPublisher) PublishPRReview(_ context.Context, _ string, _ int, _ int64, _ string) error {
	return nil
}

type noopStore struct{}

func (n *noopStore) PRAlreadyReviewed(_ int64, _ time.Time) bool { return true }
