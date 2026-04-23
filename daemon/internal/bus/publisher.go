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
