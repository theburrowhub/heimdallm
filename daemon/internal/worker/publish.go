// daemon/internal/worker/publish.go
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

// PublishWorker consumes PR publish requests from NATS and delegates
// to a handler that submits the review to GitHub.
//
// Unlike ReviewWorker, the handler returns an error to control ack/nak:
//   - nil → Ack (success or permanent failure, no retry)
//   - non-nil → NakWithDelay(30s) for NATS retry on transient failures
type PublishWorker struct {
	js      jetstream.JetStream
	handler func(ctx context.Context, msg bus.PRPublishMsg) error
}

// NewPublishWorker creates a worker that consumes from the publish-worker
// durable consumer.
func NewPublishWorker(js jetstream.JetStream, handler func(context.Context, bus.PRPublishMsg) error) *PublishWorker {
	return &PublishWorker{js: js, handler: handler}
}

// Start begins consuming. Blocks until ctx is cancelled.
func (w *PublishWorker) Start(ctx context.Context) error {
	cons, err := w.js.Consumer(ctx, bus.StreamWork, bus.ConsumerPublish)
	if err != nil {
		return err
	}

	iter, err := cons.Messages(jetstream.PullMaxMessages(1))
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	for {
		msg, err := iter.Next()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				return nil
			}
			return fmt.Errorf("publish-worker: iter.Next: %w", err)
		}

		var pubMsg bus.PRPublishMsg
		if err := bus.Decode(msg.Data(), &pubMsg); err != nil {
			slog.Error("publish-worker: decode message", "err", err)
			msg.Ack()
			continue
		}

		slog.Info("publish-worker: processing", "review_id", pubMsg.ReviewID)

		if err := w.safeHandle(ctx, pubMsg); err != nil {
			slog.Warn("publish-worker: transient error, will retry",
				"review_id", pubMsg.ReviewID, "err", err)
			msg.NakWithDelay(30 * time.Second)
		} else {
			msg.Ack()
		}
	}
}

// safeHandle calls the handler with panic recovery.
func (w *PublishWorker) safeHandle(ctx context.Context, msg bus.PRPublishMsg) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("publish-worker: handler panic",
				"review_id", msg.ReviewID, "panic", r,
				"stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return w.handler(ctx, msg)
}
