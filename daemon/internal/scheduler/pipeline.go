package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

// PipelineConfig holds the interval configuration for each tier.
type PipelineConfig struct {
	DiscoveryInterval time.Duration // Tier 1 (default 15m)
	PollInterval      time.Duration // Tier 2 (default 5m)
	WatchInterval     time.Duration // Tier 3 base (default 1m)
	RateLimitPerHour  int           // total API budget (default 4500)
}

// PipelineDeps bundles all external dependencies the pipeline needs.
type PipelineDeps struct {
	// Tier 1
	Discovery     Tier1Discovery
	Tier1ConfigFn func() Tier1Config
	Publisher     Tier1Publisher // publishes discovery results to NATS

	// NATS bridge (interim — Task 4 removes when Tier 2 consumes directly)
	JS jetstream.JetStream

	// Tier 2
	PRFetcher      Tier2PRFetcher
	PRProcessor    Tier2PRProcessor
	PRPublisher    Tier2PRPublisher // publishes PR review requests to NATS
	IssueProcessor Tier2IssueProcessor
	Promoter       Tier2Promoter
	Store          Tier2Store
	Tier2ConfigFn  func() []string // monitored repos

	// Tier 3
	ItemChecker Tier3ItemChecker
}

// Pipeline orchestrates the 3-tier polling architecture.
type Pipeline struct {
	cfg  PipelineConfig
	deps PipelineDeps

	limiter *RateLimiter
	queue   *WatchQueue

	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewPipeline creates a new pipeline. Call Start to begin processing.
func NewPipeline(cfg PipelineConfig, deps PipelineDeps) *Pipeline {
	if cfg.RateLimitPerHour <= 0 {
		cfg.RateLimitPerHour = 4500
	}
	if cfg.DiscoveryInterval <= 0 {
		cfg.DiscoveryInterval = 15 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Minute
	}
	if cfg.WatchInterval <= 0 {
		cfg.WatchInterval = 1 * time.Minute
	}
	return &Pipeline{
		cfg:     cfg,
		deps:    deps,
		limiter: NewRateLimiter(cfg.RateLimitPerHour),
		queue:   NewWatchQueue(),
	}
}

// Start launches all 3 tiers and the rate limiter refill goroutine.
func (p *Pipeline) Start(parentCtx context.Context, coldStart bool) {
	ctx, cancel := context.WithCancel(parentCtx)
	p.cancel = cancel

	reposChan := make(chan []string, 1)

	// Rate limiter hourly refill
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.limiter.Refill()
				slog.Info("pipeline: rate limiter refilled")
			}
		}
	}()

	// Tier 1: Discovery — publishes to NATS
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		RunTier1(ctx, Tier1Deps{
			Discovery: p.deps.Discovery,
			Limiter:   p.limiter,
			Publisher: p.deps.Publisher,
			ConfigFn:  p.deps.Tier1ConfigFn,
			Interval:  p.cfg.DiscoveryInterval,
		})
	}()

	// Bridge: NATS discovery-consumer → reposChan (interim, Task 4 removes)
	if p.deps.JS != nil {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.bridgeDiscovery(ctx, reposChan)
		}()
	}

	// Tier 2: Per-repo
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		RunTier2(ctx, Tier2Deps{
			Limiter:        p.limiter,
			WatchQueue:     p.queue,
			PRFetcher:      p.deps.PRFetcher,
			PRProcessor:    p.deps.PRProcessor,
			PRPublisher:    p.deps.PRPublisher,
			IssueProcessor: p.deps.IssueProcessor,
			Promoter:       p.deps.Promoter,
			Store:          p.deps.Store,
			ConfigFn:       p.deps.Tier2ConfigFn,
			Interval:       p.cfg.PollInterval,
		}, reposChan, coldStart)
	}()

	// Tier 3: Per-item watch
	if p.deps.ItemChecker != nil {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			RunTier3(ctx, Tier3Deps{
				Limiter:  p.limiter,
				Queue:    p.queue,
				Checker:  p.deps.ItemChecker,
				Interval: p.cfg.WatchInterval,
			})
		}()
	}

	slog.Info("pipeline: started",
		"discovery", p.cfg.DiscoveryInterval,
		"poll", p.cfg.PollInterval,
		"watch", p.cfg.WatchInterval,
		"rate_limit", p.cfg.RateLimitPerHour)
}

// bridgeDiscovery consumes from the NATS discovery-consumer and forwards
// repo lists to the reposChan that Tier 2 reads. This is a transitional
// bridge — Task 4 will have Tier 2 consume from NATS directly.
func (p *Pipeline) bridgeDiscovery(ctx context.Context, out chan<- []string) {
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second

	for {
		if err := p.runBridgeConsumer(ctx, out); err != nil {
			slog.Error("bridge: consumer failed, retrying", "err", err, "backoff", backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func (p *Pipeline) runBridgeConsumer(ctx context.Context, out chan<- []string) error {
	cons, err := p.deps.JS.Consumer(ctx, bus.StreamDiscovery, bus.ConsumerDiscovery)
	if err != nil {
		return fmt.Errorf("get discovery consumer: %w", err)
	}
	iter, err := cons.Messages(jetstream.PullMaxMessages(1))
	if err != nil {
		return fmt.Errorf("start message iterator: %w", err)
	}
	defer iter.Stop()

	// Stop the iterator when context is cancelled so iter.Next() unblocks.
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	for {
		msg, err := iter.Next()
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return fmt.Errorf("iter.Next: %w", err)
		}

		var dm bus.DiscoveryMsg
		if err := bus.Decode(msg.Data(), &dm); err != nil {
			slog.Error("bridge: decode discovery msg", "err", err)
			msg.Ack()
			continue
		}

		// Ack after channel send. If the process crashes before Tier 2
		// processes the repos, the next discovery cycle will re-publish
		// the list — acceptable trade-off for simplicity.
		select {
		case out <- dm.Repos:
		case <-ctx.Done():
			return nil
		}
		msg.Ack()
	}
}

// Stop cancels all goroutines and waits for them to finish.
func (p *Pipeline) Stop() {
	p.stopOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
		p.wg.Wait()
		slog.Info("pipeline: stopped")
	})
}

// Queue returns the watch queue for external inspection/testing.
func (p *Pipeline) Queue() *WatchQueue {
	return p.queue
}

// Limiter returns the shared rate limiter for external callers (e.g. NATS workers).
func (p *Pipeline) Limiter() *RateLimiter {
	return p.limiter
}
