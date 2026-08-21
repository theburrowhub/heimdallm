// daemon/internal/worker/refinement.go
package worker

import (
	"context"
	"log/slog"
	"runtime/debug"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go"
)

// RefinementWorker consumes issue refinement requests from NATS and delegates
// to a handler that runs the issue pipeline in Refinement mode.
type RefinementWorker struct {
	conn      *nats.Conn
	handler   func(ctx context.Context, msg bus.IssueMsg)
	semaphore chan struct{}
}

// NewRefinementWorker creates a worker that subscribes to the issue refinement subject.
func NewRefinementWorker(conn *nats.Conn, maxConcurrent int, handler func(context.Context, bus.IssueMsg)) *RefinementWorker {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &RefinementWorker{
		conn:      conn,
		handler:   handler,
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

// Start subscribes and blocks until ctx is cancelled.
func (w *RefinementWorker) Start(ctx context.Context, ready ...chan<- error) error {
	sub, err := w.conn.Subscribe(bus.SubjIssueRefinement, func(msg *nats.Msg) {
		var issueMsg bus.IssueMsg
		if err := bus.Decode(msg.Data, &issueMsg); err != nil {
			slog.Error("refinement-worker: decode message", "err", err)
			return
		}

		select {
		case w.semaphore <- struct{}{}:
		case <-ctx.Done():
			return
		}

		go func() {
			defer func() { <-w.semaphore }()

			slog.Info("refinement-worker: processing",
				"repo", issueMsg.Repo, "number", issueMsg.Number, "github_id", issueMsg.GithubID)

			w.safeHandle(ctx, issueMsg)
		}()
	})
	if err != nil {
		_ = reportSubscriptionReady(w.conn, ready, err)
		return err
	}
	defer sub.Unsubscribe()
	if err := reportSubscriptionReady(w.conn, ready, nil); err != nil {
		return err
	}

	<-ctx.Done()
	return nil
}

func (w *RefinementWorker) safeHandle(ctx context.Context, msg bus.IssueMsg) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("refinement-worker: handler panic",
				"repo", msg.Repo, "number", msg.Number, "panic", r,
				"stack", string(debug.Stack()))
		}
	}()
	w.handler(ctx, msg)
}
