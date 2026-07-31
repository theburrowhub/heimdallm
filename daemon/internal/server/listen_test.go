package server_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// newListenTestServer builds the smallest Server that can bind: Listen only
// needs the router, which New always wires.
func newListenTestServer(t *testing.T) *server.Server {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	return server.New(s, broker, nil, "")
}

// freePort returns a port number nothing is listening on. Racy by nature —
// callers that need the port to stay free must bind it themselves.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe for free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// Listen must report a port collision to the caller instead of leaving the
// daemon headless. A daemon that keeps running without an HTTP listener is
// invisible to the app but still burns the shared GitHub API budget — that is
// how five zombie daemons exhausted the hourly quota in #646.
func TestListen_FailsWhenPortIsOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	srv := newListenTestServer(t)
	ln, err := srv.Listen(port, "127.0.0.1")
	if err == nil {
		ln.Close()
		t.Fatalf("Listen on occupied port %d returned no error; the daemon would run headless", port)
	}
	if ln != nil {
		ln.Close()
		t.Fatal("Listen returned a non-nil listener alongside an error")
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("expected EADDRINUSE, got %v", err)
	}
}

// The happy path must still bind and serve, and the returned listener must be
// the one Serve uses — otherwise the split would silently bind twice.
func TestListen_BindsAndServeUsesTheListener(t *testing.T) {
	srv := newListenTestServer(t)
	port := freePort(t)

	ln, err := srv.Listen(port, "127.0.0.1")
	if err != nil {
		t.Fatalf("Listen on free port %d: %v", port, err)
	}
	if got := ln.Addr().(*net.TCPAddr).Port; got != port {
		t.Fatalf("listener bound to port %d, want %d", got, port)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("Serve returned %v, want nil or ErrServerClosed", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Serve did not return after Shutdown")
		}
	})

	resp, err := httpGetWithRetry(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health status = %d, want 200", resp.StatusCode)
	}
}

// Listen must not bind when the address is unusable, so a typo in bind_addr
// fails fast at startup rather than after the pollers are already spending
// API budget.
func TestListen_FailsOnUnusableBindAddr(t *testing.T) {
	srv := newListenTestServer(t)
	ln, err := srv.Listen(freePort(t), "203.0.113.1") // TEST-NET-3, not local
	if err == nil {
		ln.Close()
		t.Fatal("Listen on a non-local address returned no error")
	}
}

func httpGetWithRetry(url string) (*http.Response, error) {
	var lastErr error
	for i := 0; i < 50; i++ {
		resp, err := http.Get(url)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	return nil, lastErr
}
