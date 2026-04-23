package bus

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"
)

// RepoPublisher publishes discovered repo lists to NATS JetStream.
// Implements scheduler.Tier1Publisher.
type RepoPublisher struct {
	js jetstream.JetStream
}

// NewRepoPublisher creates a publisher that writes to SubjDiscoveryRepos.
func NewRepoPublisher(js jetstream.JetStream) *RepoPublisher {
	return &RepoPublisher{js: js}
}

// PublishRepos serializes the repo list and publishes it to the discovery subject.
func (p *RepoPublisher) PublishRepos(ctx context.Context, repos []string) error {
	data, err := Encode(DiscoveryMsg{Repos: repos})
	if err != nil {
		return fmt.Errorf("bus: encode discovery: %w", err)
	}
	_, err = p.js.Publish(ctx, SubjDiscoveryRepos, data)
	if err != nil {
		return fmt.Errorf("bus: publish discovery: %w", err)
	}
	return nil
}

// PRReviewPublisher publishes PR review requests to NATS JetStream.
// Implements scheduler.Tier2PRPublisher.
type PRReviewPublisher struct {
	js jetstream.JetStream
}

// NewPRReviewPublisher creates a publisher that writes to SubjPRReview.
func NewPRReviewPublisher(js jetstream.JetStream) *PRReviewPublisher {
	return &PRReviewPublisher{js: js}
}

// PublishPRReview publishes a single PR review request with dedup via Nats-Msg-Id.
// Returns an error if headSHA is empty to prevent dedup key collisions.
func (p *PRReviewPublisher) PublishPRReview(ctx context.Context, repo string, number int, githubID int64, headSHA string) error {
	if headSHA == "" {
		return fmt.Errorf("bus: publish pr review: empty headSHA for %s #%d", repo, number)
	}
	data, err := Encode(PRReviewMsg{
		Repo:     repo,
		Number:   number,
		GithubID: githubID,
		HeadSHA:  headSHA,
	})
	if err != nil {
		return fmt.Errorf("bus: encode pr review: %w", err)
	}
	msgID := fmt.Sprintf("%d:%s", githubID, headSHA)
	_, err = p.js.Publish(ctx, SubjPRReview, data, jetstream.WithMsgID(msgID))
	if err != nil {
		return fmt.Errorf("bus: publish pr review: %w", err)
	}
	return nil
}

// PRPublishPublisher publishes review publish requests to NATS JetStream.
type PRPublishPublisher struct {
	js jetstream.JetStream
}

// NewPRPublishPublisher creates a publisher that writes to SubjPRPublish.
func NewPRPublishPublisher(js jetstream.JetStream) *PRPublishPublisher {
	return &PRPublishPublisher{js: js}
}

// PublishPRPublish enqueues a review for GitHub publication.
// Dedup via Nats-Msg-Id prevents the scanner and review worker from
// double-publishing the same review.
func (p *PRPublishPublisher) PublishPRPublish(ctx context.Context, reviewID int64) error {
	data, err := Encode(PRPublishMsg{ReviewID: reviewID})
	if err != nil {
		return fmt.Errorf("bus: encode pr publish: %w", err)
	}
	msgID := fmt.Sprintf("rev:%d", reviewID)
	_, err = p.js.Publish(ctx, SubjPRPublish, data, jetstream.WithMsgID(msgID))
	if err != nil {
		return fmt.Errorf("bus: publish pr publish: %w", err)
	}
	return nil
}

// NATSIssuePublisher publishes classified issues to NATS JetStream.
type NATSIssuePublisher struct {
	js jetstream.JetStream
}

// NewIssuePublisher creates a publisher for issue triage and implement subjects.
func NewIssuePublisher(js jetstream.JetStream) *NATSIssuePublisher {
	return &NATSIssuePublisher{js: js}
}

// PublishIssueTriage publishes a review_only issue to the triage subject.
func (p *NATSIssuePublisher) PublishIssueTriage(ctx context.Context, repo string, number int, githubID int64) error {
	data, err := Encode(IssueMsg{Repo: repo, Number: number, GithubID: githubID})
	if err != nil {
		return fmt.Errorf("bus: encode issue triage: %w", err)
	}
	msgID := fmt.Sprintf("issue-triage:%d", githubID)
	_, err = p.js.Publish(ctx, SubjIssueTriage, data, jetstream.WithMsgID(msgID))
	if err != nil {
		return fmt.Errorf("bus: publish issue triage: %w", err)
	}
	return nil
}

// PublishIssueImplement publishes a develop issue to the implement subject.
func (p *NATSIssuePublisher) PublishIssueImplement(ctx context.Context, repo string, number int, githubID int64) error {
	data, err := Encode(IssueMsg{Repo: repo, Number: number, GithubID: githubID})
	if err != nil {
		return fmt.Errorf("bus: encode issue implement: %w", err)
	}
	msgID := fmt.Sprintf("issue-impl:%d", githubID)
	_, err = p.js.Publish(ctx, SubjIssueImplement, data, jetstream.WithMsgID(msgID))
	if err != nil {
		return fmt.Errorf("bus: publish issue implement: %w", err)
	}
	return nil
}
