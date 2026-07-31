package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	// Assert the failure shape rather than a specific errno: EADDRINUSE is
	// Unix-only (Windows reports WSAEADDRINUSE) and the daemon only relies on
	// "bind failed", not on which errno said so.
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected a *net.OpError from the failed bind, got %T: %v", err, err)
	}
	if opErr.Op != "listen" {
		t.Errorf("OpError.Op = %q, want \"listen\"", opErr.Op)
	}
}

// A second Listen must not silently orphan the first listener/server pair by
// overwriting srv.httpServer.
func TestListen_RejectsSecondCall(t *testing.T) {
	srv := newListenTestServer(t)
	first, err := srv.Listen(freePort(t), "127.0.0.1")
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	defer first.Close()

	second, err := srv.Listen(freePort(t), "127.0.0.1")
	if err == nil {
		second.Close()
		t.Fatal("second Listen succeeded; the first listener would be orphaned")
	}
	if second != nil {
		second.Close()
		t.Fatal("second Listen returned a listener alongside an error")
	}
}

// Serve without a prior Listen must report the misuse instead of panicking on
// a nil httpServer.
func TestServe_WithoutListen(t *testing.T) {
	srv := newListenTestServer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	if err := srv.Serve(ln); err == nil {
		t.Fatal("Serve without Listen returned nil error")
	}
}

// The signal main relies on to exit non-zero: if the listener dies mid-flight,
// Serve must return an error that is NOT ErrServerClosed. Without this, a
// future refactor could silently restore the headless-daemon behaviour of #646.
func TestServe_ListenerDiesMidFlight_ReturnsNonClosedError(t *testing.T) {
	srv := newListenTestServer(t)
	ln, err := srv.Listen(freePort(t), "127.0.0.1")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	// Let Serve reach its Accept loop, then pull the listener out from under it.
	if _, err := httpGetWithRetry(fmt.Sprintf("http://%s/health", ln.Addr().String())); err != nil {
		t.Fatalf("server never came up: %v", err)
	}
	ln.Close()

	select {
	case err := <-serveErr:
		if err == nil {
			t.Fatal("Serve returned nil after the listener closed; main would keep polling headless")
		}
		if errors.Is(err, http.ErrServerClosed) {
			t.Fatal("Serve reported ErrServerClosed for a listener failure; main would treat it as a clean shutdown")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after the listener closed")
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
// API budget. Uses a syntactically invalid host so the result does not depend
// on host routing: binding a non-local IP succeeds wherever
// net.ipv4.ip_nonlocal_bind=1 is set (common in load-balancer and container
// images), which made an earlier TEST-NET-3 version of this test flaky.
func TestListen_FailsOnUnusableBindAddr(t *testing.T) {
	srv := newListenTestServer(t)
	ln, err := srv.Listen(freePort(t), "not a host")
	if err == nil {
		ln.Close()
		t.Fatal("Listen on an invalid bind address returned no error")
	}
}

// While main is still wiring dependencies the socket is already served, so
// /health must say so rather than run deep checks against half-built
// dependencies — and must stay a real HTTP answer so the app's reachability
// probe knows the port is taken and does not spawn a rival daemon (#646).
func TestHealth_ReportsStartingUntilMarkReady(t *testing.T) {
	srv := newListenTestServer(t)
	srv.MarkStarting()

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("starting: status = %d, want 503", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("starting: decode: %v", err)
	}
	if body["status"] != "starting" {
		t.Errorf("starting: status field = %v, want \"starting\"", body["status"])
	}

	srv.MarkReady()
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	// Deliberately not asserting 200: a Server with no wired dependencies may
	// legitimately report degraded. What matters is that it stopped saying
	// "starting" once MarkReady ran.
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("ready: decode: %v", err)
	}
	if body["status"] == "starting" {
		t.Error("ready: still reporting \"starting\" after MarkReady")
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
