package scheduler

import (
	"context"
	"log/slog"
	"time"
)

// ItemSnapshot is the freshly-fetched subset of a watched item's state that
// HandleChange needs to decide whether to run the review. Tier 3 returns it
// from CheckItem so HandleChange does not re-fetch from GitHub.
//
// Fields are optional — only those relevant to the item's type are populated
// (e.g. Draft is always false for issues, HeadSHA is empty for issues).
// A nil snapshot signals "no change detected" and is ignored by HandleChange.
//
// HeadSHA is populated for PR snapshots and is load-bearing for the
// persistent in-flight claim (#258): the Tier 3 path was one of the two
// call sites that passed an empty SHA to runReview in #264, bypassing the
// claim and re-opening the #243 double-review pattern for PRs that had
// already been pushed into the watch queue.
type ItemSnapshot struct {
	State     string
	Draft     bool
	Author    string
	UpdatedAt time.Time
	HeadSHA   string
}

// Tier3ItemChecker checks a single item for state changes.
type Tier3ItemChecker interface {
	// CheckItem returns whether the item changed since LastSeen and, when
	// changed, a fresh snapshot of the item's state. An unchanged item
	// returns (false, nil, nil).
	CheckItem(ctx context.Context, item *WatchItem) (changed bool, snap *ItemSnapshot, err error)
	// HandleChange processes a detected change. snap is the snapshot returned
	// by CheckItem on the same tick; callers can rely on it being non-nil
	// because RunTier3 only invokes HandleChange when changed == true.
	HandleChange(ctx context.Context, item *WatchItem, snap *ItemSnapshot) error
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

				changed, snap, err := deps.Checker.CheckItem(ctx, item)
				if err != nil {
					slog.Warn("tier3: check failed", "type", item.Type,
						"repo", item.Repo, "number", item.Number, "err", err)
					deps.Queue.ReEnqueue(item)
					continue
				}

				if changed {
					slog.Info("tier3: change detected",
						"type", item.Type, "repo", item.Repo, "number", item.Number)
					if err := deps.Checker.HandleChange(ctx, item, snap); err != nil {
						slog.Error("tier3: handle change", "err", err)
					}
					// Pass the observed timestamp so LastSeen matches
					// GitHub-side time, not wall clock — see
					// theburrowhub/heimdallm#243.
					observed := time.Time{}
					if snap != nil {
						observed = snap.UpdatedAt
					}
					deps.Queue.ResetBackoff(item, observed)
				} else {
					deps.Queue.ReEnqueue(item)
				}
			}
		}
	}
}
