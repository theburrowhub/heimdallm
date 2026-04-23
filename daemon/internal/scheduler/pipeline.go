package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"
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

	// Tier 2
	PRFetcher      Tier2PRFetcher
	PRProcessor    Tier2PRProcessor
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
//
// coldStart controls whether Tier 2 runs its first processTick immediately.
// Pass true on initial daemon startup, false on a pipeline reload triggered
// by config change. See RunTier2 for the rationale — in short, a config
// reload can come from a UI PATCH and firing Tier 2 before backoff state
// settles would amplify any in-flight review loop.
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

	// Tier 1: Discovery
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		RunTier1(ctx, Tier1Deps{
			Discovery: p.deps.Discovery,
			Limiter:   p.limiter,
			ReposChan: reposChan,
			ConfigFn:  p.deps.Tier1ConfigFn,
			Interval:  p.cfg.DiscoveryInterval,
		})
	}()

	// Tier 2: Per-repo
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		RunTier2(ctx, Tier2Deps{
			Limiter:        p.limiter,
			WatchQueue:     p.queue,
			PRFetcher:      p.deps.PRFetcher,
			PRProcessor:    p.deps.PRProcessor,
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

// Stop cancels all goroutines and waits for them to finish.
// It is idempotent — calling Stop multiple times is safe (e.g. the reload
// path stops the old pipeline, and the deferred shutdown may also call Stop
// if it reads a stale pointer).
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
