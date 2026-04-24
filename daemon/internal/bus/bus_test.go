// daemon/internal/bus/bus_test.go
package bus_test

import (
	"context"
	"testing"

	"github.com/heimdallm/daemon/internal/bus"
)

func TestBus_StartStop(t *testing.T) {
	env := newTestEnv(t)

	if env.bus.Conn() == nil {
		t.Fatal("Conn() returned nil after Start")
	}
}

func TestBus_DoubleStop(t *testing.T) {
	b := bus.New(bus.Config{MaxConcurrentWorkers: 2})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b.Stop()
	b.Stop() // must not panic
}

func TestBus_MaxConcurrentWorkers(t *testing.T) {
	b := bus.New(bus.Config{MaxConcurrentWorkers: 7})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(b.Stop)

	if b.MaxConcurrentWorkers() != 7 {
		t.Errorf("MaxConcurrentWorkers = %d, want 7", b.MaxConcurrentWorkers())
	}
}

func TestBus_MaxConcurrentWorkers_Clamp(t *testing.T) {
	b := bus.New(bus.Config{MaxConcurrentWorkers: 0})
	if b.MaxConcurrentWorkers() != 1 {
		t.Errorf("MaxConcurrentWorkers = %d, want 1 (clamped)", b.MaxConcurrentWorkers())
	}
}
