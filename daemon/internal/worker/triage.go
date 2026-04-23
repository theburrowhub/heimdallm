// daemon/internal/worker/triage.go
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

// TriageWorker consumes issue triage requests from NATS and delegates
// to a handler that runs the issue review pipeline in ReviewOnly mode.
type TriageWorker struct {
	js      jetstream.JetStream
	handler func(ctx context.Context, msg bus.IssueMsg)
}

// NewTriageWorker creates a worker that consumes from the triage-worker
// durable consumer.
func NewTriageWorker(js jetstream.JetStream, handler func(context.Context, bus.IssueMsg)) *TriageWorker {
	return &TriageWorker{js: js, handler: handler}
}

// Start begins consuming. Blocks until ctx is cancelled.
// Always acks messages — errors are logged inside the handler.
func (w *TriageWorker) Start(ctx context.Context) error {
	cons, err := w.js.Consumer(ctx, bus.StreamWork, bus.ConsumerTriage)
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
			return fmt.Errorf("triage-worker: iter.Next: %w", err)
		}

		var issueMsg bus.IssueMsg
		if err := bus.Decode(msg.Data(), &issueMsg); err != nil {
			slog.Error("triage-worker: decode message", "err", err)
			msg.Ack()
			continue
		}

		slog.Info("triage-worker: processing",
			"repo", issueMsg.Repo, "number", issueMsg.Number, "github_id", issueMsg.GithubID)

		w.safeHandle(ctx, issueMsg)
		msg.Ack()
	}
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
