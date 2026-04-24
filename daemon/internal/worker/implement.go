// daemon/internal/worker/implement.go
package worker

import (
	"context"
	"log/slog"
	"runtime/debug"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go"
)

// ImplementWorker consumes issue implement requests from NATS and delegates
// to a handler that runs the issue pipeline in Develop mode.
type ImplementWorker struct {
	conn      *nats.Conn
	handler   func(ctx context.Context, msg bus.IssueMsg)
	semaphore chan struct{}
}

// NewImplementWorker creates a worker that subscribes to the issue implement subject.
func NewImplementWorker(conn *nats.Conn, maxConcurrent int, handler func(context.Context, bus.IssueMsg)) *ImplementWorker {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &ImplementWorker{
		conn:      conn,
		handler:   handler,
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

// Start subscribes and blocks until ctx is cancelled.
func (w *ImplementWorker) Start(ctx context.Context) error {
	sub, err := w.conn.Subscribe(bus.SubjIssueImplement, func(msg *nats.Msg) {
		var issueMsg bus.IssueMsg
		if err := bus.Decode(msg.Data, &issueMsg); err != nil {
			slog.Error("implement-worker: decode message", "err", err)
			return
		}

		select {
		case w.semaphore <- struct{}{}:
		case <-ctx.Done():
			return
		}

		go func() {
			defer func() { <-w.semaphore }()

			slog.Info("implement-worker: processing",
				"repo", issueMsg.Repo, "number", issueMsg.Number, "github_id", issueMsg.GithubID)

			w.safeHandle(ctx, issueMsg)
		}()
	})
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()

	<-ctx.Done()
	return nil
}

func (w *ImplementWorker) safeHandle(ctx context.Context, msg bus.IssueMsg) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("implement-worker: handler panic",
				"repo", msg.Repo, "number", msg.Number, "panic", r,
				"stack", string(debug.Stack()))
		}
	}()
	w.handler(ctx, msg)
}
