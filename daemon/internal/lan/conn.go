package lan

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// mDNS group addresses and port (RFC 6762 §3).
var (
	ipv4Group = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
)

// PacketConn is the slice of net.PacketConn this package needs.
//
// It exists so Advertiser and Browser can be wired to something other than a
// real multicast socket. That is not a stylistic preference: the daemon's Go
// tests run inside a container on Docker's default bridge, where no interface
// carries the MULTICAST flag, so a test that reached for a real socket could
// only ever skip. With this seam the advertise/browse round trip runs on an
// in-memory pair and is actually exercised in CI.
type PacketConn interface {
	ReadFrom(b []byte) (int, net.Addr, error)
	WriteTo(b []byte, addr net.Addr) (int, error)
	SetReadDeadline(t time.Time) error
	Close() error
}

// GroupAddr is where a multicast message is sent. Callers pass it to WriteTo;
// the in-memory transport ignores the address entirely.
func GroupAddr() net.Addr { return ipv4Group }

// MulticastConn joins the mDNS IPv4 group and returns the real transport.
//
// IPv4 only. Dual-stack would mean two sockets and de-duplicating the same peer
// arriving over both, for no gain: every Heimdallm deployment reaches its peers
// over IPv4 already, since that is what a base_url resolves to.
func MulticastConn(iface *net.Interface) (PacketConn, error) {
	conn, err := net.ListenMulticastUDP("udp4", iface, ipv4Group)
	if err != nil {
		return nil, fmt.Errorf("lan: joining the mDNS multicast group: %w", err)
	}
	return conn, nil
}

// memConn is one end of an in-memory transport pair.
type memConn struct {
	name string
	in   chan memPacket
	peer *memConn

	mu       sync.Mutex
	closed   bool
	deadline time.Time
}

type memPacket struct {
	data []byte
	from net.Addr
}

// memAddr identifies an end of the pair. It satisfies net.Addr so callers can
// treat an in-memory read exactly like a socket read.
type memAddr string

func (a memAddr) Network() string { return "mem" }
func (a memAddr) String() string  { return string(a) }

// NewMemConn returns two connected in-memory PacketConns. Anything written to
// one is readable from the other, so an Advertiser on one end answers a Browser
// on the other with no network involved.
//
// Writes never block: a full buffer drops the packet, which is exactly what a
// real datagram transport does under pressure and keeps a test from deadlocking
// on an unread response.
func NewMemConn() (PacketConn, PacketConn) {
	const buffer = 64
	a := &memConn{name: "a", in: make(chan memPacket, buffer)}
	b := &memConn{name: "b", in: make(chan memPacket, buffer)}
	a.peer, b.peer = b, a
	return a, b
}

func (c *memConn) ReadFrom(b []byte) (int, net.Addr, error) {
	c.mu.Lock()
	deadline := c.deadline
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, nil, net.ErrClosed
	}

	var timeout <-chan time.Time
	if !deadline.IsZero() {
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		timeout = timer.C
	}

	select {
	case pkt, ok := <-c.in:
		if !ok {
			return 0, nil, net.ErrClosed
		}
		n := copy(b, pkt.data)
		return n, pkt.from, nil
	case <-timeout:
		return 0, nil, timeoutError{}
	}
}

func (c *memConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, net.ErrClosed
	}
	// Copy: the caller owns b and is free to reuse it for the next packet.
	data := make([]byte, len(b))
	copy(data, b)
	select {
	case c.peer.in <- memPacket{data: data, from: memAddr(c.name)}:
	default:
	}
	return len(b), nil
}

func (c *memConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadline = t
	return nil
}

func (c *memConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	close(c.in)
	return nil
}

// timeoutError reports a read deadline the same way the net package does, so
// callers can use net.Error's Timeout() without special-casing the transport.
type timeoutError struct{}

func (timeoutError) Error() string   { return "lan: read deadline exceeded" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
