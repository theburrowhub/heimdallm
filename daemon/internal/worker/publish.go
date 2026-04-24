// daemon/internal/worker/publish.go
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go"
)

// PublishWorker consumes PR publish requests from NATS and delegates
// to a handler that submits the review to GitHub.
//
// Unlike ReviewWorker, the handler returns an error. With core NATS
// there is no nak/retry — errors are logged. The PublishPending scanner
// re-enqueues unpublished reviews on the next poll cycle.
type PublishWorker struct {
	conn      *nats.Conn
	handler   func(ctx context.Context, msg bus.PRPublishMsg) error
	semaphore chan struct{}
}

// NewPublishWorker creates a worker that subscribes to the PR publish subject.
func NewPublishWorker(conn *nats.Conn, maxConcurrent int, handler func(context.Context, bus.PRPublishMsg) error) *PublishWorker {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &PublishWorker{
		conn:      conn,
		handler:   handler,
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

// Start subscribes and blocks until ctx is cancelled.
func (w *PublishWorker) Start(ctx context.Context) error {
	sub, err := w.conn.Subscribe(bus.SubjPRPublish, func(msg *nats.Msg) {
		var pubMsg bus.PRPublishMsg
		if err := bus.Decode(msg.Data, &pubMsg); err != nil {
			slog.Error("publish-worker: decode message", "err", err)
			return
		}

		select {
		case w.semaphore <- struct{}{}:
		case <-ctx.Done():
			return
		}

		go func() {
			defer func() { <-w.semaphore }()

			slog.Info("publish-worker: processing", "review_id", pubMsg.ReviewID)

			if err := w.safeHandle(ctx, pubMsg); err != nil {
				slog.Warn("publish-worker: transient error (no retry in core NATS, PublishPending will re-enqueue)",
					"review_id", pubMsg.ReviewID, "err", err)
			}
		}()
	})
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()

	<-ctx.Done()
	return nil
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
