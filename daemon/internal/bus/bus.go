// daemon/internal/bus/bus.go
package bus

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// Config holds the parameters for the embedded NATS bus.
type Config struct {
	MaxConcurrentWorkers int // semaphore size for worker backpressure
}

// Bus wraps an embedded NATS server with a core pub/sub client.
// JetStream is NOT enabled — the server uses ~20-30 MB instead of 500-750 MB.
type Bus struct {
	server *natsserver.Server
	conn   *nats.Conn
	cfg    Config

	stopOnce sync.Once
}

// New creates a Bus with the given config. Call Start to launch the server.
// MaxConcurrentWorkers must be > 0; if not, it is clamped to 1.
func New(cfg Config) *Bus {
	if cfg.MaxConcurrentWorkers <= 0 {
		cfg.MaxConcurrentWorkers = 1
	}
	return &Bus{cfg: cfg}
}

// Start launches the embedded NATS server and connects an in-process client.
// No JetStream, no streams, no consumers — just core pub/sub.
func (b *Bus) Start(_ context.Context) error {
	opts := &natsserver.Options{
		ServerName: "heimdallm-bus",
		DontListen: true,
		NoLog:      true,
		NoSigs:     true,
	}

	srv, err := natsserver.NewServer(opts)
	if err != nil {
		return fmt.Errorf("bus: create server: %w", err)
	}
	srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		srv.Shutdown()
		return fmt.Errorf("bus: server not ready after 5s")
	}
	b.server = srv

	conn, err := nats.Connect("", nats.InProcessServer(srv), nats.Name("heimdallm-daemon"))
	if err != nil {
		srv.Shutdown()
		return fmt.Errorf("bus: connect: %w", err)
	}
	b.conn = conn

	slog.Info("bus: NATS started (core only, no JetStream)", "workers", b.cfg.MaxConcurrentWorkers)
	return nil
}

// Stop drains the client connection and shuts down the embedded server.
// Safe to call multiple times.
func (b *Bus) Stop() {
	b.stopOnce.Do(func() {
		if b.conn != nil {
			if err := b.conn.Drain(); err != nil {
				slog.Warn("bus: drain failed", "err", err)
			}
			for !b.conn.IsClosed() {
				time.Sleep(50 * time.Millisecond)
			}
		}
		if b.server != nil {
			b.server.Shutdown()
			b.server.WaitForShutdown()
		}
		slog.Info("bus: NATS stopped")
	})
}

// Conn returns the NATS client connection. Use for publishing messages.
func (b *Bus) Conn() *nats.Conn {
	return b.conn
}

// MaxConcurrentWorkers returns the configured concurrency limit for workers.
func (b *Bus) MaxConcurrentWorkers() int {
	return b.cfg.MaxConcurrentWorkers
}
