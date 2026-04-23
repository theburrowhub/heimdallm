// daemon/internal/bus/bus_test.go
package bus_test

import (
	"context"
	"testing"

	"github.com/heimdallm/daemon/internal/bus"
)

func TestBus_StartStop(t *testing.T) {
	b := newTestBus(t)

	if b.Conn() == nil {
		t.Fatal("Conn() returned nil after Start")
	}
	if b.JetStream() == nil {
		t.Fatal("JetStream() returned nil after Start")
	}
}

func TestBus_DoubleStop(t *testing.T) {
	dir := t.TempDir()
	b := bus.New(bus.Config{DataDir: dir, MaxConcurrentWorkers: 2})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b.Stop()
	b.Stop() // must not panic
}

func TestBus_DefaultWorkers(t *testing.T) {
	dir := t.TempDir()
	b := bus.New(bus.Config{DataDir: dir, MaxConcurrentWorkers: 0})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(b.Stop)
	// Verify it started without error (default of 5 applied internally)
}
