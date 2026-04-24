// daemon/internal/worker/triage.go
package worker

import (
	"context"
	"log/slog"
	"runtime/debug"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go"
)

// TriageWorker consumes issue triage requests from NATS and delegates
// to a handler that runs the issue review pipeline in ReviewOnly mode.
type TriageWorker struct {
	conn      *nats.Conn
	handler   func(ctx context.Context, msg bus.IssueMsg)
	semaphore chan struct{}
}

// NewTriageWorker creates a worker that subscribes to the issue triage subject.
func NewTriageWorker(conn *nats.Conn, maxConcurrent int, handler func(context.Context, bus.IssueMsg)) *TriageWorker {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &TriageWorker{
		conn:      conn,
		handler:   handler,
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

// Start subscribes and blocks until ctx is cancelled.
func (w *TriageWorker) Start(ctx context.Context) error {
	sub, err := w.conn.Subscribe(bus.SubjIssueTriage, func(msg *nats.Msg) {
		var issueMsg bus.IssueMsg
		if err := bus.Decode(msg.Data, &issueMsg); err != nil {
			slog.Error("triage-worker: decode message", "err", err)
			return
		}

		select {
		case w.semaphore <- struct{}{}:
		case <-ctx.Done():
			return
		}

		go func() {
			defer func() { <-w.semaphore }()

			slog.Info("triage-worker: processing",
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

func (w *TriageWorker) safeHandle(ctx context.Context, msg bus.IssueMsg) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("triage-worker: handler panic",
				"repo", msg.Repo, "number", msg.Number, "panic", r,
				"stack", string(debug.Stack()))
		}
	}()
	w.handler(ctx, msg)
}
