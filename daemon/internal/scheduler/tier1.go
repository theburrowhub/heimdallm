package scheduler

import (
	"context"
	"log/slog"
	"time"
)

// Tier1Discovery is the interface the discovery tier needs.
type Tier1Discovery interface {
	Discovered() []string
}

// Tier1Config provides the repo lists for merging.
type Tier1Config struct {
	StaticRepos    []string
	NonMonitored   []string
	DiscoveryTopic string
	DiscoveryOrgs  []string
}

// Tier1Deps holds all dependencies for the discovery tier.
type Tier1Deps struct {
	Discovery Tier1Discovery
	Limiter   *RateLimiter
	ReposChan chan<- []string
	ConfigFn  func() Tier1Config
	Interval  time.Duration
}

// RunTier1 runs the discovery tier. It periodically merges static repos
// with discovered repos and sends the full list to reposChan.
func RunTier1(ctx context.Context, deps Tier1Deps) {
	// Send initial repos immediately
	sendRepos(ctx, deps)

	ticker := time.NewTicker(deps.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := deps.Limiter.Acquire(ctx, TierDiscovery); err != nil {
				return
			}
			sendRepos(ctx, deps)
		}
	}
}

func sendRepos(ctx context.Context, deps Tier1Deps) {
	cfg := deps.ConfigFn()
	discovered := deps.Discovery.Discovered()

	// Merge static + discovered, exclude non-monitored
	nonMon := make(map[string]struct{}, len(cfg.NonMonitored))
	for _, r := range cfg.NonMonitored {
		nonMon[r] = struct{}{}
	}
	seen := make(map[string]struct{})
	var repos []string
	for _, r := range cfg.StaticRepos {
		if _, skip := nonMon[r]; skip {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		repos = append(repos, r)
	}
	for _, r := range discovered {
		if _, skip := nonMon[r]; skip {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		repos = append(repos, r)
	}

	slog.Info("tier1: discovery complete", "repos", len(repos))
	select {
	case deps.ReposChan <- repos:
	case <-ctx.Done():
	}
}
