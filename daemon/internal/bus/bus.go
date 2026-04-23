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
	"github.com/nats-io/nats.go/jetstream"
)

// Config holds the parameters for the embedded NATS bus.
type Config struct {
	DataDir              string // JetStream file storage directory
	MaxConcurrentWorkers int    // maps to MaxAckPending on consumers
}

// Bus wraps an embedded NATS server with a JetStream-enabled client.
type Bus struct {
	server  *natsserver.Server
	conn    *nats.Conn
	js      jetstream.JetStream
	cfg     Config
	watchKV *WatchKV

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

// Start launches the embedded NATS server, connects an in-process client,
// and creates all JetStream streams and consumers.
func (b *Bus) Start(ctx context.Context) error {
	opts := &natsserver.Options{
		ServerName: "heimdallm-bus",
		DontListen: true,
		JetStream:  true,
		StoreDir:   b.cfg.DataDir,
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

	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		srv.Shutdown()
		return fmt.Errorf("bus: jetstream: %w", err)
	}
	b.js = js

	if err := b.ensureStreams(ctx); err != nil {
		conn.Close()
		srv.Shutdown()
		return err
	}
	if err := b.ensureConsumers(ctx); err != nil {
		conn.Close()
		srv.Shutdown()
		return err
	}

	kv, err := b.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: kvBucketWatch,
	})
	if err != nil {
		conn.Close()
		srv.Shutdown()
		return fmt.Errorf("bus: create KV bucket %s: %w", kvBucketWatch, err)
	}
	b.watchKV = NewWatchKV(kv)

	slog.Info("bus: NATS started", "store_dir", b.cfg.DataDir, "workers", b.cfg.MaxConcurrentWorkers)
	return nil
}

// Stop drains the client connection and shuts down the embedded server.
// Safe to call multiple times.
func (b *Bus) Stop() {
	b.stopOnce.Do(func() {
		if b.conn != nil {
			// Drain() initiates a graceful drain asynchronously: it
			// unsubscribes, processes pending messages, flushes, and then
			// closes the connection. Wait for the connection to reach
			// CLOSED state before shutting down the embedded server so
			// in-flight JetStream acks are not truncated.
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

// JetStream returns the JetStream context. Use for stream/consumer operations.
func (b *Bus) JetStream() jetstream.JetStream {
	return b.js
}

// WatchKV returns the durable watch-state KV store.
func (b *Bus) WatchKV() *WatchKV {
	return b.watchKV
}
