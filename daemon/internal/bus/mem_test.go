package bus_test

import (
	"runtime"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func sysMemMB() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Sys / 1024 / 1024
}

func TestMemoryFootprint_CoreOnly(t *testing.T) {
	runtime.GC()
	baseline := sysMemMB()
	t.Logf("Baseline: %d MB", baseline)

	opts := &natsserver.Options{
		ServerName: "mem-test-core",
		DontListen: true,
		// No JetStream — core NATS only.
		NoLog:  true,
		NoSigs: true,
	}

	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("not ready")
	}

	runtime.GC()
	afterStart := sysMemMB()
	t.Logf("After server start (core only): %d MB (+%d)", afterStart, afterStart-baseline)

	conn, _ := nats.Connect("", nats.InProcessServer(srv), nats.Name("test"))

	// Subscribe and publish — basic sanity.
	ch := make(chan *nats.Msg, 1)
	sub, _ := conn.ChanSubscribe("test.>", ch)
	conn.Publish("test.foo", []byte("hello"))
	conn.Flush()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout on core NATS pub/sub")
	}
	sub.Unsubscribe()

	runtime.GC()
	afterAll := sysMemMB()
	t.Logf("After pub/sub: %d MB (+%d from baseline)", afterAll, afterAll-baseline)

	conn.Drain()
	for !conn.IsClosed() {
		time.Sleep(50 * time.Millisecond)
	}
	srv.Shutdown()
	srv.WaitForShutdown()

	runtime.GC()
	afterShut := sysMemMB()
	t.Logf("After shutdown: %d MB (+%d from baseline)", afterShut, afterShut-baseline)
}
