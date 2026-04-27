package scheduler

import (
	"context"
	"time"
)

// ── Tier 1 types (formerly in tier1.go) ─────────────────────────────────

// Tier1Discovery is the interface the discovery tier needs.
type Tier1Discovery interface {
	Discovered() []string
}

// Tier1Publisher publishes the discovered repo list.
type Tier1Publisher interface {
	PublishRepos(ctx context.Context, repos []string) error
}

// Tier1Config provides the repo lists for merging.
type Tier1Config struct {
	StaticRepos    []string
	NonMonitored   []string
	DiscoveryTopic string
	DiscoveryOrgs  []string
}

// ── Tier 2 types (formerly in tier2.go) ─────────────────────────────────

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
	PRAlreadyReviewed(githubID int64, repo string, number int, updatedAt time.Time) bool
}

// ── Tier 3 / Watch types (formerly in tier3.go + queue.go) ──────────────

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

// WatchItem represents a PR or issue being actively watched for changes.
// Kept for external consumers (e.g. the state-check worker in main.go)
// that build a WatchItem from a NATS KV entry. The in-memory WatchQueue
// has been replaced by NATS KV but the struct shape is still needed.
type WatchItem struct {
	Type     string // "pr" | "issue"
	Repo     string
	Number   int
	GithubID int64
	LastSeen time.Time // last detected activity
}
