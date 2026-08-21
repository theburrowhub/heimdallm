package worker

import (
	"time"

	"github.com/nats-io/nats.go"
)

// reportSubscriptionReady flushes a newly-created Core NATS subscription and
// reports exactly one startup result when the caller requested readiness.
// Start's variadic channel keeps existing one-argument callers source-compatible.
func reportSubscriptionReady(conn *nats.Conn, ready []chan<- error, startErr error) error {
	if len(ready) == 0 || ready[0] == nil {
		return startErr
	}
	if startErr == nil {
		startErr = conn.FlushTimeout(2 * time.Second)
	}
	ready[0] <- startErr
	return startErr
}
