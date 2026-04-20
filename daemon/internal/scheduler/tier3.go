package scheduler

import (
	"context"
	"log/slog"
	"time"
)

// Tier3ItemChecker checks a single item for state changes.
type Tier3ItemChecker interface {
	// CheckItem returns true if the item has changed since LastSeen.
	CheckItem(ctx context.Context, item *WatchItem) (changed bool, err error)
	// HandleChange processes a detected change (re-review, re-triage, etc).
	HandleChange(ctx context.Context, item *WatchItem) error
}

// Tier3Deps holds all dependencies for the per-item watch tier.
type Tier3Deps struct {
	Limiter  *RateLimiter
	Queue    *WatchQueue
	Checker  Tier3ItemChecker
	Interval time.Duration
}

// RunTier3 runs the per-item watch tier. It pops ready items from the
// queue, checks them for changes, and re-enqueues with backoff.
func RunTier3(ctx context.Context, deps Tier3Deps) {
	ticker := time.NewTicker(deps.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Evict stale items first
			if evicted := deps.Queue.Evict(); evicted > 0 {
				slog.Debug("tier3: evicted stale items", "count", evicted)
			}

			// Pop all ready items
			ready := deps.Queue.PopReady()
			if len(ready) == 0 {
				continue
			}

			slog.Debug("tier3: checking items", "count", len(ready))
			for _, item := range ready {
				if err := deps.Limiter.Acquire(ctx, TierWatch); err != nil {
					// Context cancelled — re-enqueue and exit
					deps.Queue.ReEnqueue(item)
					return
				}

				changed, err := deps.Checker.CheckItem(ctx, item)
				if err != nil {
					slog.Warn("tier3: check failed", "type", item.Type,
						"repo", item.Repo, "number", item.Number, "err", err)
					deps.Queue.ReEnqueue(item)
					continue
				}

				if changed {
					slog.Info("tier3: change detected",
						"type", item.Type, "repo", item.Repo, "number", item.Number)
					if err := deps.Checker.HandleChange(ctx, item); err != nil {
						slog.Error("tier3: handle change", "err", err)
					}
					deps.Queue.ResetBackoff(item)
				} else {
					deps.Queue.ReEnqueue(item)
				}
			}
		}
	}
}
