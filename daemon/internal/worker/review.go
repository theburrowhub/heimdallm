// daemon/internal/worker/review.go
package worker

import (
	"context"
	"log/slog"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

// ReviewWorker consumes PR review requests from NATS and delegates
// to a handler function that runs the actual review pipeline.
type ReviewWorker struct {
	js      jetstream.JetStream
	handler func(ctx context.Context, msg bus.PRReviewMsg)
}

// NewReviewWorker creates a worker that consumes from the review-worker
// durable consumer. The handler is called for each message and should
// contain the full review logic (fetch PR, run pipeline, push to watch queue).
func NewReviewWorker(js jetstream.JetStream, handler func(context.Context, bus.PRReviewMsg)) *ReviewWorker {
	return &ReviewWorker{js: js, handler: handler}
}

// Start begins consuming from the NATS review-worker consumer.
// Blocks until ctx is cancelled. Always acks messages — errors are
// logged inside the handler, not retried via NATS redelivery.
func (w *ReviewWorker) Start(ctx context.Context) error {
	cons, err := w.js.Consumer(ctx, bus.StreamWork, bus.ConsumerReview)
	if err != nil {
		return err
	}

	iter, err := cons.Messages(jetstream.PullMaxMessages(1))
	if err != nil {
		return err
	}

	// Stop the iterator when context is cancelled so iter.Next() unblocks.
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	for {
		msg, err := iter.Next()
		if err != nil {
			// Context cancelled or iterator stopped — clean exit.
			return nil
		}

		var prMsg bus.PRReviewMsg
		if err := bus.Decode(msg.Data(), &prMsg); err != nil {
			slog.Error("review-worker: decode message", "err", err)
			msg.Ack()
			continue
		}

		slog.Info("review-worker: processing",
			"repo", prMsg.Repo, "pr", prMsg.Number, "github_id", prMsg.GithubID)

		w.handler(ctx, prMsg)
		msg.Ack()
	}
}
