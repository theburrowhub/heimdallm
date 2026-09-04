package lan

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// faultConn is a PacketConn that fails on demand, so the loops' error handling
// can be exercised. A real socket cannot be made to fail on cue, and these are
// exactly the paths that decide whether a daemon recovers from a suspended
// laptop or sits there dead.
type faultConn struct {
	mu        sync.Mutex
	readErr   error
	writeErr  error
	reads     atomic.Int32
	writes    atomic.Int32
	closed    atomic.Bool
	blockRead bool

	// alternate makes every other read fail, deterministically. Timing the
	// failures from another goroutine raced the loop and made the test flaky.
	alternate bool
}

func (c *faultConn) setReadErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readErr = err
}

func (c *faultConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n := c.reads.Add(1)
	c.mu.Lock()
	err, block, alternate := c.readErr, c.blockRead, c.alternate
	c.mu.Unlock()
	if alternate {
		if n%2 == 0 {
			return 0, memAddr("peer"), nil
		}
		return 0, nil, errors.New("transient")
	}
	if block {
		time.Sleep(5 * time.Millisecond)
		return 0, nil, timeoutError{}
	}
	if err != nil {
		return 0, nil, err
	}
	return 0, memAddr("peer"), nil
}

func (c *faultConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	c.writes.Add(1)
	c.mu.Lock()
	err := c.writeErr
	c.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *faultConn) SetReadDeadline(time.Time) error { return nil }

func (c *faultConn) Close() error {
	c.closed.Store(true)
	return nil
}

// A steady stream of read failures means the socket is not coming back, and the
// loop has to say so — a caller that is never told cannot replace it, which is
// what left discovery dead after a suspend.
func TestAdvertiserGivesUpOnAPersistentlyFailingSocket(t *testing.T) {
	conn := &faultConn{}
	conn.setReadErr(errors.New("network is down"))

	adv, err := NewAdvertiser(conn, testAdvertisement(), quietLogger())
	if err != nil {
		t.Fatalf("NewAdvertiser: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- adv.Run(context.Background()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil on a socket that never worked")
		}
		if !strings.Contains(err.Error(), "consecutive read failures") {
			t.Fatalf("error = %v, want the give-up message", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run spun forever on a failing socket instead of giving up")
	}
	if got := conn.reads.Load(); int(got) < maxConsecutiveReadErrors {
		t.Errorf("gave up after %d reads, want at least %d", got, maxConsecutiveReadErrors)
	}
}

// A closed connection is terminal immediately: there is nothing to wait for.
func TestAdvertiserReportsAClosedConnection(t *testing.T) {
	conn := &faultConn{}
	conn.setReadErr(net.ErrClosed)

	adv, _ := NewAdvertiser(conn, testAdvertisement(), quietLogger())
	err := adv.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "was closed") {
		t.Fatalf("Run = %v, want a closed-connection error", err)
	}
}

// Occasional failures are a blip, not a broken socket: the counter resets on
// the next good read, so a flaky link does not force a needless reconnect.
func TestAdvertiserToleratesIntermittentFailures(t *testing.T) {
	conn := &faultConn{alternate: true}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	if err := adverRun(t, conn, ctx); err != nil {
		t.Fatalf("Run gave up on an intermittently failing socket: %v", err)
	}
	// Far more failures than the give-up threshold, none of them consecutive.
	if got := conn.reads.Load(); int(got) < 2*maxConsecutiveReadErrors {
		t.Fatalf("only %d reads; the test did not exercise the reset", got)
	}
}

// The browser resets the same way.
func TestBrowserToleratesIntermittentFailures(t *testing.T) {
	conn := &faultConn{alternate: true}

	b, _ := NewBrowser(conn, quietLogger())
	if _, err := b.Browse(context.Background(), 120*time.Millisecond); err != nil {
		t.Fatalf("Browse gave up on an intermittently failing socket: %v", err)
	}
	if got := conn.reads.Load(); int(got) < 2*maxConsecutiveReadErrors {
		t.Fatalf("only %d reads; the test did not exercise the reset", got)
	}
}

func adverRun(t *testing.T, conn PacketConn, ctx context.Context) error {
	t.Helper()
	adv, err := NewAdvertiser(conn, testAdvertisement(), quietLogger())
	if err != nil {
		t.Fatalf("NewAdvertiser: %v", err)
	}
	return adv.Run(ctx)
}

// Cancellation is not a failure, so it must not be reported as one — the
// caller uses the difference to decide whether to redial.
func TestAdvertiserReturnsNilOnCancellation(t *testing.T) {
	conn := &faultConn{blockRead: true}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := adverRun(t, conn, ctx); err != nil {
		t.Fatalf("Run = %v on cancellation, want nil", err)
	}
}

// A write that fails is logged and swallowed: one unsent response is not a
// reason to tear down a socket that may still be fine.
func TestAdvertiserSurvivesAFailedWrite(t *testing.T) {
	conn := &faultConn{blockRead: true, writeErr: errors.New("no route to host")}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := adverRun(t, conn, ctx); err != nil {
		t.Fatalf("Run = %v, want nil despite the failed goodbye", err)
	}
	// The goodbye was still attempted.
	if conn.writes.Load() == 0 {
		t.Error("no write was attempted on shutdown")
	}
}

func TestBrowserGivesUpOnAPersistentlyFailingSocket(t *testing.T) {
	conn := &faultConn{}
	conn.setReadErr(errors.New("network is down"))

	b, err := NewBrowser(conn, quietLogger())
	if err != nil {
		t.Fatalf("NewBrowser: %v", err)
	}
	peers, err := b.Browse(context.Background(), 5*time.Second)
	if err == nil {
		t.Fatal("Browse returned nil error on a socket that never worked")
	}
	if !strings.Contains(err.Error(), "consecutive read failures") {
		t.Fatalf("error = %v, want the give-up message", err)
	}
	if peers != nil {
		t.Errorf("peers = %v, want nil alongside an error", peers)
	}
}

func TestBrowserReportsAClosedConnection(t *testing.T) {
	conn := &faultConn{}
	conn.setReadErr(net.ErrClosed)

	b, _ := NewBrowser(conn, quietLogger())
	if _, err := b.Browse(context.Background(), time.Second); err == nil ||
		!strings.Contains(err.Error(), "was closed") {
		t.Fatalf("Browse = %v, want a closed-connection error", err)
	}
}

// A browser that cannot even send its query has nothing to wait for.
func TestBrowserReportsAFailedQuery(t *testing.T) {
	conn := &faultConn{writeErr: errors.New("no route to host")}

	b, _ := NewBrowser(conn, quietLogger())
	if _, err := b.Browse(context.Background(), time.Second); err == nil {
		t.Fatal("Browse succeeded despite being unable to send its query")
	}
}

// An empty network is not an error: a browse that hears nothing is the normal
// state of a LAN with one daemon on it.
func TestBrowseFindsNothingWithoutFailing(t *testing.T) {
	conn := &faultConn{blockRead: true}

	b, _ := NewBrowser(conn, quietLogger())
	peers, err := b.Browse(context.Background(), 30*time.Millisecond)
	if err != nil {
		t.Fatalf("Browse = %v, want nil on an empty network", err)
	}
	if len(peers) != 0 {
		t.Fatalf("found %d peers on an empty network", len(peers))
	}
}

func TestDefaultHostname(t *testing.T) {
	real := osHostname
	t.Cleanup(func() { osHostname = real })

	tests := []struct {
		name string
		host string
		err  error
		want string
	}{
		{"short name", "mac-sergio", nil, "mac-sergio.local."},
		// A machine configured with an FQDN still answers mDNS as its short
		// name under .local.
		{"fqdn", "mac.corp.example.com", nil, "mac.local."},
		{"already .local", "mac-sergio.local", nil, "mac-sergio.local."},
		{"unavailable", "", errors.New("nope"), "heimdallm.local."},
		{"blank", "   ", nil, "heimdallm.local."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			osHostname = func() (string, error) { return tt.host, tt.err }
			if got := defaultHostname(); got != tt.want {
				t.Fatalf("defaultHostname() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMemAddr(t *testing.T) {
	a := memAddr("a")
	if a.Network() != "mem" || a.String() != "a" {
		t.Fatalf("memAddr = %s/%s, want mem/a", a.Network(), a.String())
	}
}

// A real multicast join is not reachable from the test container, but the
// failure path is: an interface index that does not exist.
func TestMulticastConnReportsAJoinFailure(t *testing.T) {
	conn, err := MulticastConn(&net.Interface{Index: 9999, Name: "nope"})
	if err == nil {
		_ = conn.Close()
		t.Skip("this machine joined the group on a bogus interface")
	}
	if !strings.Contains(err.Error(), "multicast group") {
		t.Fatalf("error = %v, want it to name the multicast group", err)
	}
}

func TestGroupAddrIsTheMDNSGroup(t *testing.T) {
	if got := GroupAddr().String(); got != "224.0.0.251:5353" {
		t.Fatalf("GroupAddr = %s, want the mDNS group", got)
	}
}

// DialAddrs skips the same-link check when a response carries no source
// address, which is safe only because a real socket always carries one. That
// is an assumption about the net package, so it gets an assertion rather than
// a comment: if it ever stopped holding, the same-link rule would silently
// become optional from the wire.
func TestRealSocketAlwaysCarriesASource(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind a local UDP socket here: %v", err)
	}
	defer listener.Close()

	sender, err := net.Dial("udp4", listener.LocalAddr().String())
	if err != nil {
		t.Skipf("cannot dial the local UDP socket here: %v", err)
	}
	defer sender.Close()
	if _, err := sender.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	_ = listener.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 32)
	_, from, err := listener.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}

	if _, ok := from.(*net.UDPAddr); !ok {
		t.Fatalf("a UDP read reported %T, not *net.UDPAddr; sourceAddr would "+
			"return nothing and the same-link check would be skipped", from)
	}
	if got := sourceAddr(from); !got.IsValid() {
		t.Fatal("sourceAddr could not extract an address from a real UDP read")
	}
}
