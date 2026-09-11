package lan

import (
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

// mDNS group addresses and port (RFC 6762 §3).
const mdnsPort = 5353

var (
	ipv4Group = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: mdnsPort}
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
	// ReadFrom returns the packet, its source address, and the index of the
	// interface it arrived on.
	//
	// The interface index is the part a plain net.PacketConn cannot give, and
	// it is a security requirement rather than a nicety: the socket joins the
	// group on every interface, so without it a packet from the LAN and a
	// packet from a VPN are indistinguishable, and the only thing left to
	// judge provenance by is the source address — which anyone on the link
	// can forge. See sameLink.
	//
	// An index of 0 means the transport cannot say. Only the in-memory pair
	// answers that; a real socket always reports one, which
	// TestRealSocketAlwaysCarriesAnInterface pins.
	ReadFrom(b []byte) (n int, src net.Addr, ifIndex int, err error)
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
	// Ask the kernel to report the receiving interface with every packet
	// (IP_PKTINFO on Linux, IP_RECVIF on the BSDs; x/net picks the right one).
	// Without this the same-link rule has nothing but the source address to
	// go on, and a source address is not evidence of anything.
	pc := ipv4.NewPacketConn(conn)
	if err := pc.SetControlMessage(ipv4.FlagInterface, true); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("lan: asking for the receiving interface: %w", err)
	}
	return &multicastConn{udp: conn, pc: pc}, nil
}

// multicastConn is the real transport: a UDP socket that also reports which
// interface each packet arrived on.
type multicastConn struct {
	udp *net.UDPConn
	pc  *ipv4.PacketConn
}

func (c *multicastConn) ReadFrom(b []byte) (int, net.Addr, int, error) {
	n, cm, src, err := c.pc.ReadFrom(b)
	if err != nil {
		return n, src, 0, err
	}
	// cm is nil if the control message did not come back. Reporting 0 rather
	// than guessing is what makes the caller fail closed.
	if cm == nil {
		return n, src, 0, nil
	}
	return n, src, cm.IfIndex, nil
}

func (c *multicastConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	return c.udp.WriteTo(b, addr)
}

func (c *multicastConn) SetReadDeadline(t time.Time) error { return c.udp.SetReadDeadline(t) }
func (c *multicastConn) Close() error                      { return c.udp.Close() }

// memConn is one end of an in-memory transport pair.
type memConn struct {
	name string
	in   chan memPacket
	// done is closed by Close. Closing `in` instead would make a write from
	// the other end panic, where a real socket returns an error — and the
	// loops' whole error handling is built on getting an error.
	done chan struct{}
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
	a := &memConn{name: "a", in: make(chan memPacket, buffer), done: make(chan struct{})}
	b := &memConn{name: "b", in: make(chan memPacket, buffer), done: make(chan struct{})}
	a.peer, b.peer = b, a
	return a, b
}

func (c *memConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *memConn) ReadFrom(b []byte) (int, net.Addr, int, error) {
	c.mu.Lock()
	deadline := c.deadline
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, nil, 0, net.ErrClosed
	}

	var timeout <-chan time.Time
	if !deadline.IsZero() {
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		timeout = timer.C
	}

	select {
	case pkt := <-c.in:
		n := copy(b, pkt.data)
		// No interface: there is no link here. Callers must treat this the
		// same way they treat an absent source, which is what keeps the
		// in-memory transport from being a way around the same-link rule.
		return n, pkt.from, 0, nil
	case <-c.done:
		return 0, nil, 0, net.ErrClosed
	case <-timeout:
		return 0, nil, 0, timeoutError{}
	}
}

func (c *memConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	if c.isClosed() {
		return 0, net.ErrClosed
	}
	// A closed peer is reported the way a real socket reports an unreachable
	// destination: an error, not a panic.
	if c.peer.isClosed() {
		return 0, net.ErrClosed
	}
	// Copy: the caller owns b and is free to reuse it for the next packet.
	data := make([]byte, len(b))
	copy(data, b)
	select {
	case c.peer.in <- memPacket{data: data, from: memAddr(c.name)}:
	case <-c.peer.done:
		return 0, net.ErrClosed
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
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	close(c.done)
	return nil
}

// timeoutError reports a read deadline the same way the net package does, so
// callers can use net.Error's Timeout() without special-casing the transport.
type timeoutError struct{}

func (timeoutError) Error() string   { return "lan: read deadline exceeded" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
