// daemon/internal/worker/state.go
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go"
)

// StateHandler is invoked for each state check message. Returns whether
// a change was detected. The handler is responsible for calling
// HandleChange when changed==true.
type StateHandler func(ctx context.Context, msg bus.StateCheckMsg) (changed bool, err error)

// StateWorker consumes state check requests from NATS.
type StateWorker struct {
	conn      *nats.Conn
	watchKV   *bus.WatchStore
	handler   StateHandler
	semaphore chan struct{}
}

// NewStateWorker creates a worker that subscribes to the state check subject.
// After each handler call, it updates the SQLite backoff state: reset on
// change, increase on no change or error.
func NewStateWorker(conn *nats.Conn, maxConcurrent int, watchKV *bus.WatchStore, handler StateHandler) *StateWorker {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &StateWorker{
		conn:      conn,
		watchKV:   watchKV,
		handler:   handler,
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

// Start subscribes and blocks until ctx is cancelled.
func (w *StateWorker) Start(ctx context.Context) error {
	sub, err := w.conn.Subscribe(bus.SubjStateCheck, func(msg *nats.Msg) {
		var checkMsg bus.StateCheckMsg
		if err := bus.Decode(msg.Data, &checkMsg); err != nil {
			slog.Error("state-worker: decode message", "err", err)
			return
		}

		select {
		case w.semaphore <- struct{}{}:
		case <-ctx.Done():
			return
		}

		go func() {
			defer func() { <-w.semaphore }()

			slog.Debug("state-worker: checking",
				"type", checkMsg.Type, "repo", checkMsg.Repo,
				"number", checkMsg.Number, "github_id", checkMsg.GithubID)

			changed, handlerErr := w.safeHandle(ctx, checkMsg)

			key := fmt.Sprintf("%s.%d", checkMsg.Type, checkMsg.GithubID)
			if handlerErr != nil {
				slog.Warn("state-worker: check failed",
					"type", checkMsg.Type, "repo", checkMsg.Repo,
					"number", checkMsg.Number, "err", handlerErr)
				if kvErr := w.watchKV.IncreaseBackoff(ctx, key); kvErr != nil {
					slog.Warn("state-worker: increase backoff failed", "key", key, "err", kvErr)
				}
			} else if changed {
				slog.Info("state-worker: change detected",
					"type", checkMsg.Type, "repo", checkMsg.Repo,
					"number", checkMsg.Number)
				if kvErr := w.watchKV.ResetBackoff(ctx, key, time.Now()); kvErr != nil {
					slog.Warn("state-worker: reset backoff failed", "key", key, "err", kvErr)
				}
			} else {
				if kvErr := w.watchKV.IncreaseBackoff(ctx, key); kvErr != nil {
					slog.Warn("state-worker: increase backoff failed", "key", key, "err", kvErr)
				}
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
