// daemon/internal/bus/testhelper_test.go
package bus_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/heimdallm/daemon/internal/bus"
	_ "modernc.org/sqlite"
)

// testEnv groups the bus and watch store used by tests.
type testEnv struct {
	bus   *bus.Bus
	watch *bus.WatchStore
	db    *sql.DB
}

// newTestEnv creates a Bus + SQLite WatchStore for testing. Registers cleanup.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	b := bus.New(bus.Config{MaxConcurrentWorkers: 3})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("newTestEnv: bus Start failed: %v", err)
	}
	t.Cleanup(b.Stop)

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("newTestEnv: open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	w, err := bus.NewWatchStore(db)
	if err != nil {
		t.Fatalf("newTestEnv: watch store: %v", err)
	}

	return &testEnv{bus: b, watch: w, db: db}
}
