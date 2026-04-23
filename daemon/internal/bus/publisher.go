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
