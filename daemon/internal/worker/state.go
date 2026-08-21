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
	"github.com/heimdallm/daemon/internal/workgate"
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
	workGate  *workgate.Gate
}

// SetWorkGate admits a complete state transaction, including handler side
// effects and the final watch backoff write, before an updater may stop the
// daemon.
func (w *StateWorker) SetWorkGate(gate *workgate.Gate) { w.workGate = gate }

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
func (w *StateWorker) Start(ctx context.Context, ready ...chan<- error) error {
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
			handleCtx, permit, owned, err := w.workGate.AcquireContext(ctx, workgate.KindState)
			if err != nil {
				slog.Debug("state-worker: deferred while application update drains",
					"type", checkMsg.Type, "repo", checkMsg.Repo, "number", checkMsg.Number)
				return
			}
			if owned {
				defer permit.Release()
			}

			slog.Debug("state-worker: checking",
				"type", checkMsg.Type, "repo", checkMsg.Repo,
				"number", checkMsg.Number, "github_id", checkMsg.GithubID)

			changed, handlerErr := w.safeHandle(handleCtx, checkMsg)

			key := fmt.Sprintf("%s.%d", checkMsg.Type, checkMsg.GithubID)
			if handlerErr != nil {
				slog.Warn("state-worker: check failed",
					"type", checkMsg.Type, "repo", checkMsg.Repo,
					"number", checkMsg.Number, "err", handlerErr)
				if kvErr := w.watchKV.IncreaseBackoff(handleCtx, key); kvErr != nil && !errors.Is(kvErr, bus.ErrWatchNotFound) {
					slog.Warn("state-worker: increase backoff failed", "key", key, "err", kvErr)
				}
			} else if changed {
				slog.Info("state-worker: change detected",
					"type", checkMsg.Type, "repo", checkMsg.Repo,
					"number", checkMsg.Number)
				if kvErr := w.watchKV.ResetBackoff(handleCtx, key, time.Now()); kvErr != nil && !errors.Is(kvErr, bus.ErrWatchNotFound) {
					slog.Warn("state-worker: reset backoff failed", "key", key, "err", kvErr)
				}
			} else {
				if kvErr := w.watchKV.IncreaseBackoff(handleCtx, key); kvErr != nil && !errors.Is(kvErr, bus.ErrWatchNotFound) {
					slog.Warn("state-worker: increase backoff failed", "key", key, "err", kvErr)
				}
			}
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
