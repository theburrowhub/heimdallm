// daemon/internal/bus/testhelper_test.go
package bus_test

import (
	"context"
	"testing"

	"github.com/heimdallm/daemon/internal/bus"
)

// newTestBus creates a Bus configured for testing with a temporary data
// directory. It calls t.Fatal if Start fails and registers a cleanup
// function to Stop the bus when the test ends.
func newTestBus(t *testing.T) *bus.Bus {
	t.Helper()
	dir := t.TempDir()
	b := bus.New(bus.Config{
		DataDir:              dir,
		MaxConcurrentWorkers: 3,
	})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("newTestBus: Start failed: %v", err)
	}
	t.Cleanup(b.Stop)
	return b
}
