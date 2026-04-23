// daemon/internal/worker/state.go
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

// StateHandler is invoked for each state check message. Returns whether
// a change was detected. The handler is responsible for calling
// HandleChange when changed==true.
type StateHandler func(ctx context.Context, msg bus.StateCheckMsg) (changed bool, err error)

// StateWorker consumes state check requests from NATS.
type StateWorker struct {
	js      jetstream.JetStream
	watchKV *bus.WatchKV
	handler StateHandler
}

// NewStateWorker creates a worker that consumes from the state-worker
// durable consumer. After each handler call, it updates the KV backoff
// state: reset on change, increase on no change.
func NewStateWorker(js jetstream.JetStream, watchKV *bus.WatchKV, handler StateHandler) *StateWorker {
	return &StateWorker{js: js, watchKV: watchKV, handler: handler}
}

// Start begins consuming. Blocks until ctx is cancelled.
func (w *StateWorker) Start(ctx context.Context) error {
	cons, err := w.js.Consumer(ctx, bus.StreamWork, bus.ConsumerState)
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
			return fmt.Errorf("state-worker: iter.Next: %w", err)
		}

		var checkMsg bus.StateCheckMsg
		if err := bus.Decode(msg.Data(), &checkMsg); err != nil {
			slog.Error("state-worker: decode message", "err", err)
			msg.Ack()
			continue
		}

		slog.Debug("state-worker: checking",
			"type", checkMsg.Type, "repo", checkMsg.Repo,
			"number", checkMsg.Number, "github_id", checkMsg.GithubID)

		changed, handlerErr := w.safeHandle(ctx, checkMsg)

		// Key format uses "." separator (NATS KV doesn't allow ":")
		key := fmt.Sprintf("%s.%d", checkMsg.Type, checkMsg.GithubID)
		if handlerErr != nil {
			slog.Warn("state-worker: check failed",
				"type", checkMsg.Type, "repo", checkMsg.Repo,
				"number", checkMsg.Number, "err", handlerErr)
			w.watchKV.IncreaseBackoff(ctx, key)
		} else if changed {
			slog.Info("state-worker: change detected",
				"type", checkMsg.Type, "repo", checkMsg.Repo,
				"number", checkMsg.Number)
			w.watchKV.ResetBackoff(ctx, key, time.Now())
		} else {
			w.watchKV.IncreaseBackoff(ctx, key)
		}

		msg.Ack()
	}
}

func (w *StateWorker) safeHandle(ctx context.Context, msg bus.StateCheckMsg) (changed bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("state-worker: handler panic",
				"type", msg.Type, "repo", msg.Repo, "number", msg.Number,
				"panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return w.handler(ctx, msg)
}
