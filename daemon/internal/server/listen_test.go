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
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
	"github.com/heimdallm/daemon/internal/workgate"
)

func newListenTestServer(t *testing.T) *server.Server {
	return newListenTestServerWithToken(t, "")
}

func newListenTestServerWithToken(t *testing.T, token string) *server.Server {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	return server.NewWithOptions(s, broker, nil, token, server.Options{UpdateBootID: "test-boot-id"})
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

	ln, err := server.Listen(port, "127.0.0.1")
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

// A second Serve must not silently replace the first http.Server.
func TestServe_RejectsSecondCall(t *testing.T) {
	srv := newListenTestServer(t)
	first, err := server.Listen(0, "127.0.0.1")
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	defer first.Close()
	firstErr := make(chan error, 1)
	go func() { firstErr <- srv.Serve(first) }()
	if _, err := httpGetWithRetry(fmt.Sprintf("http://%s/health", first.Addr())); err != nil {
		t.Fatalf("first Serve never came up: %v", err)
	}

	second, err := server.Listen(0, "127.0.0.1")
	if err != nil {
		t.Fatalf("second Listen: %v", err)
	}
	defer second.Close()
	if err := srv.Serve(second); err == nil {
		t.Fatal("second Serve succeeded; the first server would be orphaned")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	<-firstErr
}

// Shutdown may be requested before the goroutine which calls Serve is
// scheduled. Serve must preserve that decision, close the subsequently handed
// listener, and report the normal graceful-shutdown sentinel rather than
// briefly accepting traffic or leaving the daemon port bound.
func TestShutdownBeforeServe_ClosesSubsequentListener(t *testing.T) {
	srv := newListenTestServer(t)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown before Serve: %v", err)
	}

	ln, err := server.Listen(0, "127.0.0.1")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve after Shutdown = %v, want http.ErrServerClosed", err)
	}

	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatalf("listener %s still accepts connections after Shutdown won", addr)
	}
}

// Main creates a gated Server before the store and API token exist, then
// injects them before MarkReady. Verify that the router observes both values
// only after publication: authentication uses the late token and the handler
// can use the late store without a nil dereference.
func TestConfigure_PublishesDependenciesBeforeMarkReady(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)

	srv := server.New(nil, nil, nil, "")
	srv.MarkStarting()
	srv.Configure(s, broker, nil, "late-secret")
	srv.MarkReady()

	unauthenticated := httptest.NewRecorder()
	srv.Router().ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/prs", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /prs status = %d, want 401", unauthenticated.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/prs", nil)
	req.Header.Set("X-Heimdallm-Token", "late-secret")
	authenticated := httptest.NewRecorder()
	srv.Router().ServeHTTP(authenticated, req)
	if authenticated.Code != http.StatusOK {
		t.Fatalf("authenticated /prs status = %d, want 200; body=%s", authenticated.Code, authenticated.Body.String())
	}
}

// The signal main relies on to exit non-zero: if the listener dies mid-flight,
// Serve must return an error that is NOT ErrServerClosed. Without this, a
// future refactor could silently restore the headless-daemon behaviour of #646.
func TestServe_ListenerDiesMidFlight_ReturnsNonClosedError(t *testing.T) {
	srv := newListenTestServer(t)
	ln, err := server.Listen(freePort(t), "127.0.0.1")
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

	ln, err := server.Listen(port, "127.0.0.1")
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
	ln, err := server.Listen(freePort(t), "not a host")
	if err == nil {
		ln.Close()
		t.Fatal("Listen on an invalid bind address returned no error")
	}
}

func TestStartingGate_BlocksEveryRouteExceptHealth(t *testing.T) {
	srv := newListenTestServerWithToken(t, "secret")
	srv.MarkStarting()
	shutdownCalled := false
	meCalled := false
	srv.SetShutdownFn(func() { shutdownCalled = true })
	srv.SetMeFn(func() (string, error) {
		meCalled = true
		return "bot", nil
	})

	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/me"},
		{http.MethodGet, "/prs"},
		{http.MethodGet, "/events"},
		{http.MethodGet, "/logs/stream"},
		{http.MethodPatch, "/config"},
		{http.MethodPost, "/shutdown"},
		{http.MethodGet, "/not-a-route"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", w.Code)
			}
			if got := w.Header().Get(server.HeaderDaemon); got != "1" {
				t.Errorf("%s header = %q, want 1", server.HeaderDaemon, got)
			}
			if got := w.Header().Get("Retry-After"); got != "1" {
				t.Errorf("Retry-After = %q, want 1", got)
			}
		})
	}
	if shutdownCalled {
		t.Fatal("shutdown callback ran while server was starting")
	}
	if meCalled {
		t.Fatal("me callback ran while server was starting")
	}

	// MarkReady publishes the callbacks and restores the normal router.
	srv.MarkReady()
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("X-Heimdallm-Token", "secret")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ready /me status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !meCalled {
		t.Fatal("ready /me did not invoke the published callback")
	}
}

func TestStartingGateAllowsAuthenticatedUpdateLeaseRenewal(t *testing.T) {
	srv := newListenTestServerWithToken(t, "secret")
	srv.SetUpdatePreparationFns(
		func(leaseID string) (server.UpdatePreparationStatus, error) {
			return server.UpdatePreparationStatus{
				State:   "ready",
				LeaseID: leaseID,
				PID:     42,
			}, nil
		},
		func(leaseID string) (server.UpdatePreparationStatus, error) {
			return server.UpdatePreparationStatus{State: "running", LeaseID: leaseID, PID: 42}, nil
		},
	)
	srv.SetUpdateConfirmFn(func(leaseID string) (server.UpdatePreparationStatus, error) {
		return server.UpdatePreparationStatus{
			State:               "ready",
			LeaseID:             leaseID,
			PID:                 42,
			Sealed:              true,
			BootstrapAuthorized: true,
		}, nil
	})
	srv.SetUpdateSealFn(func(leaseID string) (server.UpdatePreparationStatus, error) {
		return server.UpdatePreparationStatus{
			State:   "ready",
			LeaseID: leaseID,
			PID:     42,
			Sealed:  true,
		}, nil
	})
	srv.MarkStarting()

	unauthorized := httptest.NewRequest(http.MethodPost, "/update/prepare", nil)
	unauthorized.Header.Set(server.HeaderUpdateLease, "updater-owner")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, unauthorized)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated startup renewal status = %d, want 401", w.Code)
	}

	renew := httptest.NewRequest(http.MethodPost, "/update/prepare", nil)
	renew.Header.Set("X-Heimdallm-Token", "secret")
	renew.Header.Set(server.HeaderUpdateLease, "updater-owner")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, renew)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated startup renewal status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	seal := httptest.NewRequest(http.MethodPost, "/update/seal", nil)
	seal.Header.Set("X-Heimdallm-Token", "secret")
	seal.Header.Set(server.HeaderUpdateLease, "updater-owner")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, seal)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated startup seal status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	confirm := httptest.NewRequest(http.MethodPost, "/update/confirm", nil)
	confirm.Header.Set("X-Heimdallm-Token", "secret")
	confirm.Header.Set(server.HeaderUpdateLease, "updater-owner")
	confirm.Header.Set(server.HeaderExpectedUpdateBootID, "test-boot-id")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, confirm)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated startup confirmation status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	// MarkStopping reuses the startup gate, but must not reopen lease mutations
	// while the listener is being torn down.
	srv.MarkStopping()
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, renew)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("renewal while stopping status = %d, want 503", w.Code)
	}
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, confirm)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("confirmation while stopping status = %d, want 503", w.Code)
	}
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, seal)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("seal while stopping status = %d, want 503", w.Code)
	}
}

func TestReplacementBootstrapHandshakeConvergesRestoredLeaseAndKeepsWorkSealedUntilHealthy(t *testing.T) {
	gate := workgate.New(time.Minute)
	if _, err := gate.Prepare("updater-owner"); err != nil {
		t.Fatal(err)
	}

	srv := newListenTestServerWithToken(t, "secret")
	toStatus := func(snapshot workgate.Snapshot) server.UpdatePreparationStatus {
		return server.UpdatePreparationStatus{
			State:               "ready",
			PID:                 42,
			Version:             "v1.2.3",
			LeaseID:             snapshot.LeaseID,
			Sealed:              snapshot.Sealed,
			BootstrapAuthorized: snapshot.BootstrapAuthorized,
			BootID:              "test-boot-id",
			ActiveTotal:         snapshot.Total(),
			Active:              map[string]int{},
		}
	}
	srv.SetUpdatePreparationFns(
		func(leaseID string) (server.UpdatePreparationStatus, error) {
			snapshot, err := gate.Prepare(leaseID)
			return toStatus(snapshot), err
		},
		func(leaseID string) (server.UpdatePreparationStatus, error) {
			snapshot, err := gate.Cancel(leaseID)
			return toStatus(snapshot), err
		},
	)
	srv.SetUpdateConfirmFn(func(leaseID string) (server.UpdatePreparationStatus, error) {
		snapshot, err := gate.ConfirmBootstrap(leaseID)
		return toStatus(snapshot), err
	})
	srv.SetUpdateSealFn(func(leaseID string) (server.UpdatePreparationStatus, error) {
		snapshot, err := gate.Seal(leaseID)
		return toStatus(snapshot), err
	})
	srv.MarkStarting()

	bootstrap := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { bootstrap <- gate.WaitUntilBootstrapAuthorized(ctx) }()

	health := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, health)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("health before confirmation = %d, want 503", w.Code)
	}

	// A crash can restore an ordinary lease rather than a sealed marker. The
	// authenticated owner can converge that state to the durable barrier while
	// the minimal startup router is still active.
	seal := httptest.NewRequest(http.MethodPost, "/update/seal", nil)
	seal.Header.Set("X-Heimdallm-Token", "secret")
	seal.Header.Set(server.HeaderUpdateLease, "updater-owner")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, seal)
	if w.Code != http.StatusOK {
		t.Fatalf("seal restored ordinary lease = %d, body=%s", w.Code, w.Body.String())
	}
	if snapshot := gate.Status(); !snapshot.Sealed || snapshot.BootstrapAuthorized {
		t.Fatalf("state after startup seal = %+v", snapshot)
	}

	// A delayed confirmation for a daemon process that has already died must
	// not authorize this replacement process to touch stateful dependencies.
	staleConfirm := httptest.NewRequest(http.MethodPost, "/update/confirm", nil)
	staleConfirm.Header.Set("X-Heimdallm-Token", "secret")
	staleConfirm.Header.Set(server.HeaderUpdateLease, "updater-owner")
	staleConfirm.Header.Set(server.HeaderExpectedUpdateBootID, "previous-process-boot-id")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, staleConfirm)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale-process confirm status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	if snapshot := gate.Status(); snapshot.BootstrapAuthorized {
		t.Fatalf("stale-process confirmation authorized bootstrap: %+v", snapshot)
	}
	select {
	case err := <-bootstrap:
		t.Fatalf("stale-process confirmation unblocked bootstrap: %v", err)
	default:
	}

	confirm := httptest.NewRequest(http.MethodPost, "/update/confirm", nil)
	confirm.Header.Set("X-Heimdallm-Token", "secret")
	confirm.Header.Set(server.HeaderUpdateLease, "updater-owner")
	confirm.Header.Set(server.HeaderExpectedUpdateBootID, "test-boot-id")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, confirm)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body=%s", w.Code, w.Body.String())
	}
	select {
	case err := <-bootstrap:
		if err != nil {
			t.Fatalf("bootstrap authorization: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bootstrap did not unblock after confirmation")
	}
	if _, err := gate.Acquire(workgate.KindReview); !errors.Is(err, workgate.ErrDraining) {
		t.Fatalf("confirmation opened work admission: %v", err)
	}

	// This models successful dependency wiring. Readiness becomes visible while
	// admission is still sealed; only the subsequent owner cancellation opens it.
	srv.MarkReady()
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, health)
	if w.Code != http.StatusOK {
		t.Fatalf("health after bootstrap = %d, body=%s", w.Code, w.Body.String())
	}
	cancelRequest := httptest.NewRequest(http.MethodDelete, "/update/prepare", nil)
	cancelRequest.Header.Set("X-Heimdallm-Token", "secret")
	cancelRequest.Header.Set(server.HeaderUpdateLease, "updater-owner")
	cancelRequest.Header.Set(server.HeaderExpectedUpdateBootID, "test-boot-id")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, cancelRequest)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel after health = %d, body=%s", w.Code, w.Body.String())
	}
	permit, err := gate.Acquire(workgate.KindReview)
	if err != nil {
		t.Fatalf("work admission stayed closed after verified cancellation: %v", err)
	}
	permit.Release()
}

func TestReplacementBootstrapFailureLeavesAdmissionSealed(t *testing.T) {
	gate := workgate.New(time.Minute)
	if _, err := gate.Prepare("updater-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Seal("updater-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.ConfirmBootstrap("updater-owner"); err != nil {
		t.Fatal(err)
	}

	// A failed initialization exits without owner cancellation. The durable
	// sealed marker remains closed and its process-local authorization is not
	// sufficient to admit work.
	if snapshot := gate.Status(); !snapshot.Draining || !snapshot.Sealed || !snapshot.BootstrapAuthorized {
		t.Fatalf("failed-bootstrap snapshot = %+v", snapshot)
	}
	if _, err := gate.Acquire(workgate.KindReview); !errors.Is(err, workgate.ErrDraining) {
		t.Fatalf("failed bootstrap opened work admission: %v", err)
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

func TestHealth_StartingIncludesVersion(t *testing.T) {
	srv := server.NewWithOptions(nil, nil, nil, "", server.Options{Version: "v-test"})
	srv.MarkStarting()

	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if body["version"] != "v-test" {
		t.Fatalf("starting health version = %v, want v-test", body["version"])
	}
}

var errSecondListenerClose = errors.New("second listener close failed")

type secondCloseErrorListener struct {
	addr          net.Addr
	acceptStarted chan struct{}
	closed        chan struct{}
	acceptOnce    sync.Once
	closeOnce     sync.Once
	mu            sync.Mutex
	closeCount    int
}

func newSecondCloseErrorListener() *secondCloseErrorListener {
	return &secondCloseErrorListener{
		addr:          testListenerAddr("127.0.0.1:0"),
		acceptStarted: make(chan struct{}),
		closed:        make(chan struct{}),
	}
}

func (l *secondCloseErrorListener) Accept() (net.Conn, error) {
	l.acceptOnce.Do(func() { close(l.acceptStarted) })
	<-l.closed
	return nil, net.ErrClosed
}

func (l *secondCloseErrorListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeCount++
	if l.closeCount == 1 {
		l.closeOnce.Do(func() { close(l.closed) })
		return nil
	}
	return errSecondListenerClose
}

func (l *secondCloseErrorListener) Addr() net.Addr { return l.addr }

type testListenerAddr string

func (a testListenerAddr) Network() string { return "tcp" }
func (a testListenerAddr) String() string  { return string(a) }

// Before registerStream/cancelActiveStreams existed, http.Server.Shutdown had
// to wait for handleSSE's handler to return on its own — which it never did
// on a select blocked on the broker channel — so Shutdown hung until the
// caller's ctx deadline expired ("server shutdown failed: context deadline
// exceeded" on every restart that had /events open). This proves Shutdown
// now returns promptly with a real open SSE connection.
func TestShutdownEndsOpenSSEStreamPromptly(t *testing.T) {
	srv := newListenTestServer(t)
	ln, err := server.Listen(0, "127.0.0.1")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	// Open a real, long-lived GET /events connection and wait for the
	// preamble so we know the handler is actually blocked in its event loop.
	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/events")
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	var resp *http.Response
	select {
	case resp = <-respCh:
	case err := <-errCh:
		t.Fatalf("GET /events: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("GET /events never got a response")
	}
	defer resp.Body.Close()
	preamble := make([]byte, len(": connected\n\n"))
	if _, err := io.ReadFull(resp.Body, preamble); err != nil {
		t.Fatalf("reading SSE preamble: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown with an open SSE stream: %v", err)
	}
	// The old bug hung for the full shutdown deadline (this test uses 3s to
	// give it room to fail loudly); the fix returns in well under one.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Shutdown took %v with an open SSE stream, want well under 1s", elapsed)
	}

	select {
	case <-serveErr:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not finish after Shutdown")
	}
}

func TestShutdownReturnsTrackedListenerCloseError(t *testing.T) {
	srv := newListenTestServer(t)
	ln := newSecondCloseErrorListener()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	select {
	case <-ln.acceptStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve never entered Accept")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); !errors.Is(err, errSecondListenerClose) {
		t.Fatalf("Shutdown error = %v, want %v", err, errSecondListenerClose)
	}
	select {
	case <-serveErr:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not finish after Shutdown")
	}
}

func TestMarkStopping_KeepsOwnershipVisibleAndGatesRoutes(t *testing.T) {
	srv := newListenTestServerWithToken(t, "secret")
	srv.MarkStopping()

	health := httptest.NewRecorder()
	srv.Router().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status = %d, want 503", health.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(health.Body).Decode(&body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if body["status"] != "stopping" {
		t.Fatalf("health status field = %v, want stopping", body["status"])
	}
	if got := health.Header().Get(server.HeaderDaemon); got != "1" {
		t.Fatalf("%s header = %q, want 1", server.HeaderDaemon, got)
	}

	mutation := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	req.Header.Set("X-Heimdallm-Token", "secret")
	srv.Router().ServeHTTP(mutation, req)
	if mutation.Code != http.StatusServiceUnavailable {
		t.Fatalf("gated mutation status = %d, want 503", mutation.Code)
	}
}

// The app identifies our daemon by this header, so every /health response must
// carry it — including the 503s. Without it the app falls back to sniffing the
// body, and a bare {"status": ...} is the shape of most health endpoints in the
// wild, so a foreign service would be mistaken for the daemon and no daemon
// would ever be spawned.
func TestHealth_AlwaysCarriesDaemonHeader(t *testing.T) {
	srv := newListenTestServer(t)

	for _, state := range []string{"starting", "ready"} {
		if state == "starting" {
			srv.MarkStarting()
		} else {
			srv.MarkReady()
		}
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
		if got := w.Header().Get(server.HeaderDaemon); got != "1" {
			t.Errorf("%s: %s header = %q, want \"1\"", state, server.HeaderDaemon, got)
		}
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
