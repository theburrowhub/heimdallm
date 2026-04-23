package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Tier2PRFetcher fetches PRs for review.
type Tier2PRFetcher interface {
	FetchPRsToReview() ([]Tier2PR, error)
}

// Tier2PR carries the PR fields that the review pipeline needs.
// FetchPRsToReview already fetches these from the GitHub Search API;
// passing them through avoids a per-PR re-fetch and prevents silent
// zero-value bugs in the pipeline's UpsertPR call.
//
// HeadSHA is resolved by the adapter after the review-guard filter (the
// Search Issues API does not populate head.sha, so it costs one extra
// /pulls/N lookup per PR that passed the gate). Carrying it through this
// struct is load-bearing: the persistent in-flight claim (#258) is keyed
// on (pr_id, head_sha), and an empty SHA silently bypasses the claim —
// which is exactly how theburrowhub/heimdallm#264 reproduced the #243
// double-review pattern. An empty HeadSHA here means the resolve failed;
// the downstream claim will log and fall back to the other layered
// defenses (fail-closed SHA in pipeline.Run, circuit breaker, PublishedAt
// grace) rather than block a review.
type Tier2PR struct {
	ID        int64
	Number    int
	Repo      string
	Title     string
	HTMLURL   string
	Author    string
	State     string
	Draft     bool
	UpdatedAt time.Time
	HeadSHA   string
}

// Tier2PRProcessor runs the PR review pipeline on a single PR.
type Tier2PRProcessor interface {
	ProcessPR(ctx context.Context, pr Tier2PR) error
	PublishPending()
}

// Tier2IssueProcessor processes issues for a single repo.
type Tier2IssueProcessor interface {
	ProcessRepo(ctx context.Context, repo string) (int, error)
}

// Tier2Promoter runs the issue promotion pass.
type Tier2Promoter interface {
	PromoteReady(ctx context.Context, repos []string) (int, error)
}

// Tier2PRPublisher publishes PR review requests to NATS.
type Tier2PRPublisher interface {
	PublishPRReview(ctx context.Context, repo string, number int, githubID int64, headSHA string) error
}

// Tier2Store checks if a PR has already been reviewed recently.
type Tier2Store interface {
	PRAlreadyReviewed(githubID int64, updatedAt time.Time) bool
}

// Tier2Deps holds all dependencies for the per-repo tier.
type Tier2Deps struct {
	Limiter        *RateLimiter
	WatchQueue     *WatchQueue
	PRFetcher      Tier2PRFetcher
	PRProcessor    Tier2PRProcessor
	PRPublisher    Tier2PRPublisher
	IssueProcessor Tier2IssueProcessor
	Promoter       Tier2Promoter
	Store          Tier2Store
	ConfigFn       func() []string // returns monitored repos for PR filtering
	Interval       time.Duration
}

// RunTier2 runs the review-requested polling tier.
//
// coldStart controls the behaviour of the very first tick:
//   - true (initial daemon startup): fire processTick() immediately so the
//     UI sees activity without waiting PollInterval.
//   - false (pipeline reload): skip the immediate tick. A reload is often
//     triggered by a UI config PATCH and firing the tick immediately would
//     re-poll every repo before backoff state has settled, amplifying any
//     in-flight review loop. See theburrowhub/heimdallm#243.
func RunTier2(ctx context.Context, deps Tier2Deps, reposChan <-chan []string, coldStart bool) {
	var (
		mu    sync.Mutex
		repos []string
	)

	// Goroutine to receive repo updates from Tier 1
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-reposChan:
				mu.Lock()
				repos = r
				mu.Unlock()
				slog.Info("tier2: received repo list", "count", len(r))
			}
		}
	}()

	// Brief delay for Tier 1 to send first batch
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return
	}

	ticker := time.NewTicker(deps.Interval)
	defer ticker.Stop()

	processTick := func() {
		mu.Lock()
		currentRepos := append([]string(nil), repos...)
		mu.Unlock()

		if len(currentRepos) == 0 {
			return
		}

		// PR processing
		if err := deps.Limiter.Acquire(ctx, TierRepo); err != nil {
			return
		}
		prs, err := deps.PRFetcher.FetchPRsToReview()
		if err != nil {
			slog.Error("tier2: fetch PRs", "err", err)
		} else {
			monitoredSet := make(map[string]struct{}, len(currentRepos))
			for _, r := range currentRepos {
				monitoredSet[r] = struct{}{}
			}
			for _, pr := range prs {
				if _, ok := monitoredSet[pr.Repo]; !ok {
					continue
				}
				if deps.Store.PRAlreadyReviewed(pr.ID, pr.UpdatedAt) {
					continue
				}
				// On publish failure the PR is not marked reviewed in the store,
				// so the next poll cycle will re-detect and retry it.
				if err := deps.PRPublisher.PublishPRReview(ctx, pr.Repo, pr.Number, pr.ID, pr.HeadSHA); err != nil {
					slog.Error("tier2: publish PR review", "repo", pr.Repo, "pr", pr.Number, "err", err)
				}
			}
		}

		// Issue promotion
		if deps.Promoter != nil {
			if err := deps.Limiter.Acquire(ctx, TierRepo); err != nil {
				return
			}
			if n, err := deps.Promoter.PromoteReady(ctx, currentRepos); err != nil {
				slog.Error("tier2: promotion", "err", err)
			} else if n > 0 {
				slog.Info("tier2: promoted issues", "count", n)
			}
		}

		// Issue processing per repo
		for _, repo := range currentRepos {
			if err := deps.Limiter.Acquire(ctx, TierRepo); err != nil {
				return
			}
			n, err := deps.IssueProcessor.ProcessRepo(ctx, repo)
			if err != nil {
				slog.Error("tier2: issue processing", "repo", repo, "err", err)
				continue
			}
			if n > 0 {
				slog.Info("tier2: processed issues", "repo", repo, "count", n)
			}
		}

		// Retry pending publishes
		deps.PRProcessor.PublishPending()
	}

	// Run immediately only on a cold start. On pipeline reload (coldStart
	// == false) wait one full PollInterval before the first tick so a UI
	// config PATCH cannot fan out to every repo the instant it lands.
	if coldStart {
		processTick()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			processTick()
		}
	}
}
