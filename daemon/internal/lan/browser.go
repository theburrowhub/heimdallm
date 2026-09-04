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

	// maxAddrsPerHost bounds one hostname's address set. Capping the number of
	// host keys is not enough on its own: a sender emitting many distinct A
	// records for the *same* name grows that one entry without limit, which is
	// the flood this file claims to defend against arriving through the door
	// left open. A real host has a handful of interfaces.
	maxAddrsPerHost = 16
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

// accumulator collects records across packets and joins them into peers.
//
// Everything an advertiser can influence is keyed by the address it was sent
// from. That is the design rather than a check: a sender can only ever add to,
// or withdraw from, its own entries, so there is no shared table for one host
// on the link to poison on another's behalf. Two rounds of trying to police a
// global address table with source checks produced a bypass each time — once
// through an attacker-chosen SRV target naming somebody else's hostname, once
// through claiming ownership of a name by advertising it first.
type accumulator struct {
	// instances are the record names seen in a PTR answer, i.e. the set of
	// daemons that claim to exist.
	instances map[string]bool
	srv       map[string]*dns.SRV
	txt       map[string][]string
	source    map[string]netip.Addr // record name -> who advertised it

	// addrs is keyed by (sender, host FQDN), which is what makes address
	// ownership unforgeable.
	addrs    map[hostKey][]netip.Addr
	seenAddr map[addrKey]bool
}

// hostKey scopes a hostname's addresses to the sender that advertised them.
type hostKey struct {
	source netip.Addr
	host   string
}

// addrKey identifies one address under one sender's hostname.
type addrKey struct {
	host hostKey
	addr netip.Addr
}

func newAccumulator() *accumulator {
	return &accumulator{
		instances: map[string]bool{},
		srv:       map[string]*dns.SRV{},
		txt:       map[string][]string{},
		source:    map[string]netip.Addr{},
		addrs:     map[hostKey][]netip.Addr{},
		seenAddr:  map[addrKey]bool{},
	}
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
		// collected for it is dropped rather than reported as present. Only
		// this sender's own records, though — a goodbye is as unauthenticated
		// as everything else here, and without that a single packet would
		// evict somebody else's daemon from an in-flight browse.
		if rr.Header().Ttl == 0 {
			a.forget(rr, source)
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
				// on the same link as whoever advertised it, and so a goodbye
				// can be matched against the advertiser.
				if source.IsValid() {
					a.source[name] = source
				}
			}
		case *dns.TXT:
			if !a.full() {
				a.txt[strings.ToLower(rec.Hdr.Name)] = rec.Txt
			}
		case *dns.A:
			a.addAddr(rec.Hdr.Name, rec.A, source)
		case *dns.AAAA:
			a.addAddr(rec.Hdr.Name, rec.AAAA, source)
		}
	}
}

// forget drops what this sender previously advertised, and only that.
func (a *accumulator) forget(rr dns.RR, source netip.Addr) {
	name := strings.ToLower(rr.Header().Name)
	switch rec := rr.(type) {
	case *dns.PTR:
		a.retract(strings.ToLower(rec.Ptr), source)
	case *dns.A, *dns.AAAA:
		// Only ever this sender's copy: the key includes the sender, so there
		// is nothing else here to withdraw.
		a.dropAddrs(hostKey{source, name})
	default:
		a.retract(name, source)
	}
}

// retract drops a record name, but only for the sender that advertised it.
//
// A name nobody has advertised yet is left alone rather than pre-emptively
// blocked: there is nothing to protect and no claim to compare against.
func (a *accumulator) retract(name string, source netip.Addr) {
	advertiser, known := a.source[name]
	if known && source.IsValid() && advertiser.IsValid() && advertiser != source {
		return // somebody else's daemon; not this sender's to retire
	}
	if srv, ok := a.srv[name]; ok {
		// This sender's copy of the target's addresses. An attacker naming
		// somebody else's hostname as their SRV target reaches only their own
		// entry, because the key carries their address.
		a.dropAddrs(hostKey{source, strings.ToLower(srv.Target)})
	}
	delete(a.instances, name)
	delete(a.srv, name)
	delete(a.txt, name)
	delete(a.source, name)
}

func (a *accumulator) dropAddrs(key hostKey) {
	for _, addr := range a.addrs[key] {
		delete(a.seenAddr, addrKey{key, addr})
	}
	delete(a.addrs, key)
}

// full reports whether the accumulator has taken all it will hold. Records past
// this are dropped rather than growing the maps, so a flooded window costs a
// bounded amount of memory and the peers heard first still get reported.
func (a *accumulator) full() bool {
	return len(a.instances) >= maxRecordNames ||
		len(a.srv) >= maxRecordNames ||
		len(a.txt) >= maxRecordNames
}

func (a *accumulator) addAddr(host string, ip net.IP, source netip.Addr) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return
	}
	addr = addr.Unmap()
	key := hostKey{source, strings.ToLower(host)}
	if _, known := a.addrs[key]; !known && len(a.addrs) >= maxRecordNames {
		return
	}
	if len(a.addrs[key]) >= maxAddrsPerHost {
		return
	}
	// Deduplicated through a set rather than a scan of the slice, which was
	// quadratic in the number of records a stranger chooses to send.
	if a.seenAddr[addrKey{key, addr}] {
		return
	}
	a.seenAddr[addrKey{key, addr}] = true
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
		peer.Source = a.source[name]
		// The addresses this same sender published for its own SRV target.
		// Reading a shared host table here is what let one advertiser supply
		// another's addresses.
		peer.Addrs = a.addrs[hostKey{peer.Source, strings.ToLower(srv.Target)}]
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
