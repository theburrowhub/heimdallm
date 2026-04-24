package bus

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"
)

// RepoPublisher publishes discovered repo lists to NATS.
// Implements scheduler.Tier1Publisher.
type RepoPublisher struct {
	conn *nats.Conn
}

// NewRepoPublisher creates a publisher that writes to SubjDiscoveryRepos.
func NewRepoPublisher(conn *nats.Conn) *RepoPublisher {
	return &RepoPublisher{conn: conn}
}

// PublishRepos serializes the repo list and publishes it to the discovery subject.
func (p *RepoPublisher) PublishRepos(_ context.Context, repos []string) error {
	data, err := Encode(DiscoveryMsg{Repos: repos})
	if err != nil {
		return fmt.Errorf("bus: encode discovery: %w", err)
	}
	if err := p.conn.Publish(SubjDiscoveryRepos, data); err != nil {
		return fmt.Errorf("bus: publish discovery: %w", err)
	}
	return nil
}

// PRReviewPublisher publishes PR review requests to NATS.
// Implements scheduler.Tier2PRPublisher.
type PRReviewPublisher struct {
	conn *nats.Conn
}

// NewPRReviewPublisher creates a publisher that writes to SubjPRReview.
func NewPRReviewPublisher(conn *nats.Conn) *PRReviewPublisher {
	return &PRReviewPublisher{conn: conn}
}

// PublishPRReview publishes a single PR review request.
// Returns an error if headSHA is empty to prevent dedup key collisions.
// Note: dedup is no longer NATS-side — pollers handle it via store checks.
func (p *PRReviewPublisher) PublishPRReview(_ context.Context, repo string, number int, githubID int64, headSHA string) error {
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
	if err := p.conn.Publish(SubjPRReview, data); err != nil {
		return fmt.Errorf("bus: publish pr review: %w", err)
	}
	return nil
}

// PRPublishPublisher publishes review publish requests to NATS.
type PRPublishPublisher struct {
	conn *nats.Conn
}

// NewPRPublishPublisher creates a publisher that writes to SubjPRPublish.
func NewPRPublishPublisher(conn *nats.Conn) *PRPublishPublisher {
	return &PRPublishPublisher{conn: conn}
}

// PublishPRPublish enqueues a review for GitHub publication.
func (p *PRPublishPublisher) PublishPRPublish(_ context.Context, reviewID int64) error {
	data, err := Encode(PRPublishMsg{ReviewID: reviewID})
	if err != nil {
		return fmt.Errorf("bus: encode pr publish: %w", err)
	}
	if err := p.conn.Publish(SubjPRPublish, data); err != nil {
		return fmt.Errorf("bus: publish pr publish: %w", err)
	}
	return nil
}

// NATSIssuePublisher publishes classified issues to NATS.
type NATSIssuePublisher struct {
	conn *nats.Conn
}

// NewIssuePublisher creates a publisher for issue triage and implement subjects.
func NewIssuePublisher(conn *nats.Conn) *NATSIssuePublisher {
	return &NATSIssuePublisher{conn: conn}
}

// PublishIssueTriage publishes a review_only issue to the triage subject.
func (p *NATSIssuePublisher) PublishIssueTriage(_ context.Context, repo string, number int, githubID int64) error {
	data, err := Encode(IssueMsg{Repo: repo, Number: number, GithubID: githubID})
	if err != nil {
		return fmt.Errorf("bus: encode issue triage: %w", err)
	}
	if err := p.conn.Publish(SubjIssueTriage, data); err != nil {
		return fmt.Errorf("bus: publish issue triage: %w", err)
	}
	return nil
}

// PublishIssueImplement publishes a develop issue to the implement subject.
func (p *NATSIssuePublisher) PublishIssueImplement(_ context.Context, repo string, number int, githubID int64) error {
	data, err := Encode(IssueMsg{Repo: repo, Number: number, GithubID: githubID})
	if err != nil {
		return fmt.Errorf("bus: encode issue implement: %w", err)
	}
	if err := p.conn.Publish(SubjIssueImplement, data); err != nil {
		return fmt.Errorf("bus: publish issue implement: %w", err)
	}
	return nil
}

// StateCheckPublisher publishes state check requests to NATS.
type StateCheckPublisher struct {
	conn *nats.Conn
}

// NewStateCheckPublisher creates a publisher for state check requests.
func NewStateCheckPublisher(conn *nats.Conn) *StateCheckPublisher {
	return &StateCheckPublisher{conn: conn}
}

// PublishStateCheck publishes a state check request for a watched item.
func (p *StateCheckPublisher) PublishStateCheck(_ context.Context, typ, repo string, number int, githubID int64) error {
	data, err := Encode(StateCheckMsg{
		Type:     typ,
		Repo:     repo,
		Number:   number,
		GithubID: githubID,
	})
	if err != nil {
		return fmt.Errorf("bus: encode state check: %w", err)
	}
	if err := p.conn.Publish(SubjStateCheck, data); err != nil {
		return fmt.Errorf("bus: publish state check: %w", err)
	}
	return nil
}
