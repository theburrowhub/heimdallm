// daemon/internal/worker/review.go
package worker

import (
	"context"
	"log/slog"
	"runtime/debug"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go"
)

// ReviewWorker consumes PR review requests from NATS and delegates
// to a handler function that runs the actual review pipeline.
type ReviewWorker struct {
	conn      *nats.Conn
	handler   func(ctx context.Context, msg bus.PRReviewMsg)
	semaphore chan struct{}
}

// NewReviewWorker creates a worker that subscribes to the PR review subject.
// maxConcurrent controls how many messages are processed simultaneously.
func NewReviewWorker(conn *nats.Conn, maxConcurrent int, handler func(context.Context, bus.PRReviewMsg)) *ReviewWorker {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &ReviewWorker{
		conn:      conn,
		handler:   handler,
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

// Start subscribes to the NATS PR review subject and blocks until ctx
// is cancelled. Core NATS is fire-and-forget — no ack/nak.
func (w *ReviewWorker) Start(ctx context.Context) error {
	sub, err := w.conn.Subscribe(bus.SubjPRReview, func(msg *nats.Msg) {
		var prMsg bus.PRReviewMsg
		if err := bus.Decode(msg.Data, &prMsg); err != nil {
			slog.Error("review-worker: decode message", "err", err)
			return
		}

		// Acquire semaphore for backpressure.
		select {
		case w.semaphore <- struct{}{}:
		case <-ctx.Done():
			return
		}

		go func() {
			defer func() { <-w.semaphore }()

			slog.Info("review-worker: processing",
				"repo", prMsg.Repo, "pr", prMsg.Number, "github_id", prMsg.GithubID)

			w.safeHandle(ctx, prMsg)
		}()
	})
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()

	<-ctx.Done()
	return nil
}

// safeHandle calls the handler with panic recovery so a single bad PR
// cannot kill the worker goroutine.
func (w *ReviewWorker) safeHandle(ctx context.Context, msg bus.PRReviewMsg) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("review-worker: handler panic",
				"repo", msg.Repo, "pr", msg.Number, "panic", r,
				"stack", string(debug.Stack()))
		}
	}()
	w.handler(ctx, msg)
}
