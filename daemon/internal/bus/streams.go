// daemon/internal/bus/streams.go
package bus

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

func (b *Bus) ensureStreams(ctx context.Context) error {
	streams := []jetstream.StreamConfig{
		{
			Name:       StreamWork,
			Subjects:   []string{"heimdallm.pr.*", "heimdallm.issue.*", "heimdallm.state.*"},
			Retention:  jetstream.WorkQueuePolicy,
			MaxAge:     24 * time.Hour,
			Duplicates: 2 * time.Minute,
			Storage:    jetstream.FileStorage,
		},
		{
			Name:       StreamDiscovery,
			Subjects:   []string{"heimdallm.discovery.*"},
			Retention:  jetstream.InterestPolicy,
			MaxAge:     1 * time.Hour,
			Duplicates: 5 * time.Minute,
			Storage:    jetstream.FileStorage,
		},
		{
			Name:      StreamEvents,
			Subjects:  []string{"heimdallm.events.>"},
			Retention: jetstream.InterestPolicy,
			MaxAge:    1 * time.Hour,
			Storage:   jetstream.FileStorage,
		},
	}
	for _, sc := range streams {
		if _, err := b.js.CreateOrUpdateStream(ctx, sc); err != nil {
			return fmt.Errorf("bus: create stream %s: %w", sc.Name, err)
		}
	}
	return nil
}

func (b *Bus) ensureConsumers(ctx context.Context) error {
	workers := b.cfg.MaxConcurrentWorkers

	type def struct {
		stream string
		cfg    jetstream.ConsumerConfig
	}
	consumers := []def{
		{StreamWork, jetstream.ConsumerConfig{
			Durable:       ConsumerReview,
			FilterSubject: SubjPRReview,
			MaxAckPending: workers,
			AckWait:       30 * time.Minute,
			MaxDeliver:    3,
			AckPolicy:     jetstream.AckExplicitPolicy,
		}},
		{StreamWork, jetstream.ConsumerConfig{
			Durable:       ConsumerPublish,
			FilterSubject: SubjPRPublish,
			MaxAckPending: workers,
			AckWait:       5 * time.Minute,
			MaxDeliver:    5,
			AckPolicy:     jetstream.AckExplicitPolicy,
		}},
		{StreamWork, jetstream.ConsumerConfig{
			Durable:       ConsumerTriage,
			FilterSubject: SubjIssueTriage,
			MaxAckPending: workers,
			AckWait:       30 * time.Minute,
			MaxDeliver:    3,
			AckPolicy:     jetstream.AckExplicitPolicy,
		}},
		{StreamWork, jetstream.ConsumerConfig{
			Durable:       ConsumerImplement,
			FilterSubject: SubjIssueImplement,
			MaxAckPending: workers,
			AckWait:       45 * time.Minute,
			MaxDeliver:    3,
			AckPolicy:     jetstream.AckExplicitPolicy,
		}},
		{StreamWork, jetstream.ConsumerConfig{
			Durable:       ConsumerState,
			FilterSubject: SubjStateCheck,
			MaxAckPending: workers * 2,
			AckWait:       2 * time.Minute,
			MaxDeliver:    3,
			AckPolicy:     jetstream.AckExplicitPolicy,
		}},
		{StreamDiscovery, jetstream.ConsumerConfig{
			Durable:       ConsumerDiscovery,
			FilterSubject: SubjDiscoveryRepos,
			MaxAckPending: 1,
			AckWait:       1 * time.Minute,
			MaxDeliver:    3,
			AckPolicy:     jetstream.AckExplicitPolicy,
		}},
	}

	for _, d := range consumers {
		if _, err := b.js.CreateOrUpdateConsumer(ctx, d.stream, d.cfg); err != nil {
			return fmt.Errorf("bus: create consumer %s on %s: %w", d.cfg.Durable, d.stream, err)
		}
	}
	return nil
}
