package lan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// maxPeers bounds what one browse will report, and maxRecordNames bounds what
// the accumulator will hold while assembling it.
//
// Both exist because the input is a multicast group that anyone on the link can
// write to. Without a cap, a single sender emitting distinct PTR/SRV/TXT names
// for the whole browse window grows the accumulator without limit and then
// hands the caller thousands of peers to go and probe. A real network has a
// handful of daemons; a number this far above that is not a bigger cluster, it
// is someone filling the window.
const (
	maxPeers       = 64
	maxRecordNames = 512
)

// Browser asks the network which Heimdallm daemons are on it.
//
// One Browse is one question and a listening window: mDNS has no notion of a
// complete answer, only of who happened to reply before we stopped listening.
// Callers are expected to browse on a loop and treat the result as the current
// view rather than the truth.
type Browser struct {
	conn PacketConn
	log  *slog.Logger
}

// NewBrowser wires a browser to a transport.
func NewBrowser(conn PacketConn, log *slog.Logger) (*Browser, error) {
	if conn == nil {
		return nil, errors.New("lan: browser needs a connection")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Browser{conn: conn, log: log}, nil
}

// Close releases the underlying transport.
func (b *Browser) Close() error { return b.conn.Close() }

// Browse sends one PTR query and collects answers for the given window.
//
// Peers are returned sorted by instance id so a caller diffing two consecutive
// browses sees a stable order rather than the order packets happened to arrive.
func (b *Browser) Browse(ctx context.Context, window time.Duration) ([]Peer, error) {
	if window <= 0 {
		window = 2 * time.Second
	}
	if err := b.query(); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(window)
	// Records for one peer can arrive in separate packets, so responses are
	// accumulated per record name and only assembled into Peers at the end.
	acc := newAccumulator()
	buf := make([]byte, 9000)
	failures := 0

	for {
		if ctx.Err() != nil {
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if remaining > 250*time.Millisecond {
			remaining = 250 * time.Millisecond
		}
		_ = b.conn.SetReadDeadline(time.Now().Add(remaining))

		n, from, err := b.conn.ReadFrom(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				failures = 0
				continue
			}
			if ctx.Err() != nil {
				break
			}
			if errors.Is(err, net.ErrClosed) {
				return nil, fmt.Errorf("lan: the browser's connection was closed: %w", err)
			}
			failures++
			if failures >= maxConsecutiveReadErrors {
				return nil, fmt.Errorf("lan: giving up on this connection after %d "+
					"consecutive read failures: %w", failures, err)
			}
			b.log.Debug("lan: read failed while browsing", "err", err, "consecutive", failures)
			continue
		}
		failures = 0
		acc.absorb(buf[:n], sourceAddr(from))
	}

	return acc.peers(), nil
}

// query asks the group for the service's PTR records.
func (b *Browser) query() error {
	msg := new(dns.Msg)
	msg.SetQuestion(serviceFQDN(), dns.TypePTR)
	// Recursion is meaningless on a link-local multicast query, and RFC 6762
	// §18.6 requires the bit to be clear.
	msg.RecursionDesired = false

	packed, err := msg.Pack()
	if err != nil {
		return err
	}
	if _, err := b.conn.WriteTo(packed, GroupAddr()); err != nil {
		return err
	}
	return nil
}

// accumulator collects records across packets and joins them into peers.
type accumulator struct {
	// instances are the record names seen in a PTR answer, i.e. the set of
	// daemons that claim to exist.
	instances map[string]bool
	srv       map[string]*dns.SRV
	txt       map[string][]string
	addrs     map[string][]netip.Addr // keyed by host FQDN
	source    map[string]netip.Addr   // keyed by record name
}

func newAccumulator() *accumulator {
	return &accumulator{
		instances: map[string]bool{},
		srv:       map[string]*dns.SRV{},
		txt:       map[string][]string{},
		addrs:     map[string][]netip.Addr{},
		source:    map[string]netip.Addr{},
	}
}

// sourceAddr extracts the sender's IP, or the zero value when the transport
// does not carry one (the in-memory pair does not).
func sourceAddr(from net.Addr) netip.Addr {
	udp, ok := from.(*net.UDPAddr)
	if !ok {
		return netip.Addr{}
	}
	addr, ok := netip.AddrFromSlice(udp.IP)
	if !ok {
		return netip.Addr{}
	}
	return addr.Unmap()
}

func (a *accumulator) absorb(packet []byte, source netip.Addr) {
	var msg dns.Msg
	if err := msg.Unpack(packet); err != nil {
		return
	}
	if !msg.Response {
		return // another browser's question, not an answer
	}
	// Extra carries the SRV/TXT/A that a well-behaved responder bundles with
	// its PTR, so both sections have to be read.
	for _, rr := range append(append([]dns.RR{}, msg.Answer...), msg.Extra...) {
		// TTL 0 is a goodbye: the peer is leaving, so anything already
		// collected for it is dropped rather than reported as present.
		if rr.Header().Ttl == 0 {
			a.forget(rr)
			continue
		}
		switch rec := rr.(type) {
		case *dns.PTR:
			if strings.EqualFold(rec.Hdr.Name, serviceFQDN()) && !a.full() {
				a.instances[strings.ToLower(rec.Ptr)] = true
			}
		case *dns.SRV:
			if !a.full() {
				name := strings.ToLower(rec.Hdr.Name)
				a.srv[name] = rec
				// Remembered per record so a candidate can be required to sit
				// on the same link as whoever advertised it.
				if source.IsValid() {
					a.source[name] = source
				}
			}
		case *dns.TXT:
			if !a.full() {
				a.txt[strings.ToLower(rec.Hdr.Name)] = rec.Txt
			}
		case *dns.A:
			a.addAddr(rec.Hdr.Name, rec.A)
		case *dns.AAAA:
			a.addAddr(rec.Hdr.Name, rec.AAAA)
		}
	}
}

func (a *accumulator) forget(rr dns.RR) {
	name := strings.ToLower(rr.Header().Name)
	if ptr, ok := rr.(*dns.PTR); ok {
		delete(a.instances, strings.ToLower(ptr.Ptr))
		return
	}
	delete(a.instances, name)
	delete(a.srv, name)
	delete(a.txt, name)
	delete(a.source, name)
}

// full reports whether the accumulator has taken all it will hold. Records past
// this are dropped rather than growing the maps, so a flooded window costs a
// bounded amount of memory and the peers heard first still get reported.
func (a *accumulator) full() bool {
	return len(a.instances) >= maxRecordNames ||
		len(a.srv) >= maxRecordNames ||
		len(a.txt) >= maxRecordNames
}

func (a *accumulator) addAddr(host string, ip net.IP) {
	if len(a.addrs) >= maxRecordNames {
		return
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return
	}
	addr = addr.Unmap()
	key := strings.ToLower(host)
	for _, existing := range a.addrs[key] {
		if existing == addr {
			return
		}
	}
	a.addrs[key] = append(a.addrs[key], addr)
}

// peers joins the collected records. An instance is only reported when it has
// both an SRV (somewhere to connect) and a TXT (something to identify it);
// either alone is an incomplete response we would only have to discard later.
func (a *accumulator) peers() []Peer {
	out := make([]Peer, 0, len(a.instances))
	for name := range a.instances {
		srv, hasSRV := a.srv[name]
		txt, hasTXT := a.txt[name]
		if !hasSRV || !hasTXT {
			continue
		}
		peer := decodeTXT(txt)
		if peer.InstanceID == "" {
			continue // nothing to identify it by; not worth proposing
		}
		peer.Hostname = strings.TrimSuffix(srv.Target, ".")
		peer.Port = int(srv.Port)
		peer.Addrs = a.addrs[strings.ToLower(srv.Target)]
		peer.Source = a.source[name]
		if peer.Scheme == "" {
			peer.Scheme = "http"
		}
		if peer.BaseURL() == "" {
			// Includes an SRV target that is not a single .local label, which
			// is refused rather than probed — see ValidateMDNSHostname.
			continue
		}
		out = append(out, peer)
	}
	sortPeers(out)
	// Sorted first, so a capped result is the same subset every time rather
	// than whichever names Go's map iteration happened to yield.
	if len(out) > maxPeers {
		out = out[:maxPeers]
	}
	return out
}
