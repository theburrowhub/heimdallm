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

	// maxPeersPerSender bounds how much of the result one sender can occupy.
	// The global cap alone let a flooder crowd real daemons out of it: peers
	// are ordered by an instance id the sender chooses, so a few hundred
	// entries named "aaa…" take every slot. A real host advertises one daemon.
	maxPeersPerSender = 4
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

// capPerSender keeps at most maxPeersPerSender entries from any one address,
// preserving order.
func capPerSender(peers []Peer) []Peer {
	seen := map[netip.Addr]int{}
	out := peers[:0]
	for _, p := range peers {
		if p.Source.IsValid() {
			if seen[p.Source] >= maxPeersPerSender {
				continue
			}
			seen[p.Source]++
		}
		out = append(out, p)
	}
	return out
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
// Every map is keyed by the address the record was sent from. That is the whole
// security model of this type, and it is structural rather than a check: a
// sender can only add to, or withdraw from, its own entries, so there is no
// shared state for one host on the link to poison on another's behalf.
//
// It is structural because the alternative was tried. Policing shared tables
// with source comparisons produced a bypass on every attempt — an
// attacker-chosen SRV target naming somebody else's host, a hostname claimed by
// advertising it first, and a TTL-0 PTR arriving before the victim's SRV did,
// when there was no recorded owner to compare against yet. A check has to
// anticipate the shape of the attack; a key does not.
//
// A peer is therefore one coherent advertisement from one sender: PTR, SRV and
// TXT all from the same address, or no peer.
type accumulator struct {
	// instances are the record names seen in a PTR answer, i.e. the set of
	// daemons that claim to exist, scoped to whoever claimed it.
	instances map[recordKey]bool
	srv       map[recordKey]*dns.SRV
	txt       map[recordKey][]string
	addrs     map[recordKey][]netip.Addr
	seenAddr  map[addrKey]bool
}

// recordKey scopes a record name to the sender that advertised it.
type recordKey struct {
	source netip.Addr
	name   string
}

// addrKey identifies one address under one sender's hostname.
type addrKey struct {
	host recordKey
	addr netip.Addr
}

func newAccumulator() *accumulator {
	return &accumulator{
		instances: map[recordKey]bool{},
		srv:       map[recordKey]*dns.SRV{},
		txt:       map[recordKey][]string{},
		addrs:     map[recordKey][]netip.Addr{},
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
		// this sender's own records — the key sees to that.
		if rr.Header().Ttl == 0 {
			a.forget(rr, source)
			continue
		}
		switch rec := rr.(type) {
		case *dns.PTR:
			if strings.EqualFold(rec.Hdr.Name, serviceFQDN()) && !a.full() {
				a.instances[recordKey{source, strings.ToLower(rec.Ptr)}] = true
			}
		case *dns.SRV:
			if !a.full() {
				a.srv[recordKey{source, strings.ToLower(rec.Hdr.Name)}] = rec
			}
		case *dns.TXT:
			if !a.full() {
				a.txt[recordKey{source, strings.ToLower(rec.Hdr.Name)}] = rec.Txt
			}
		case *dns.A:
			a.addAddr(rec.Hdr.Name, rec.A, source)
		case *dns.AAAA:
			a.addAddr(rec.Hdr.Name, rec.AAAA, source)
		}
	}
}

// forget drops what this sender previously advertised, and only that. There is
// no ownership check because there is nothing to check: the key already
// restricts the reach to the sender's own records.
func (a *accumulator) forget(rr dns.RR, source netip.Addr) {
	name := strings.ToLower(rr.Header().Name)
	switch rec := rr.(type) {
	case *dns.PTR:
		a.retract(recordKey{source, strings.ToLower(rec.Ptr)})
	case *dns.A, *dns.AAAA:
		a.dropAddrs(recordKey{source, name})
	default:
		a.retract(recordKey{source, name})
	}
}

func (a *accumulator) retract(key recordKey) {
	if srv, ok := a.srv[key]; ok {
		a.dropAddrs(recordKey{key.source, strings.ToLower(srv.Target)})
	}
	delete(a.instances, key)
	delete(a.srv, key)
	delete(a.txt, key)
}

func (a *accumulator) dropAddrs(key recordKey) {
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
	key := recordKey{source, strings.ToLower(host)}
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

// peers joins the collected records.
//
// An instance is only reported when one sender supplied both an SRV (somewhere
// to connect) and a TXT (something to identify it). Either alone is an
// incomplete response, and accepting them from different senders would be the
// shared-state problem again in another form.
func (a *accumulator) peers() []Peer {
	out := make([]Peer, 0, len(a.instances))
	for key := range a.instances {
		srv, hasSRV := a.srv[key]
		txt, hasTXT := a.txt[key]
		if !hasSRV || !hasTXT {
			continue
		}
		peer := decodeTXT(txt)
		if peer.InstanceID == "" {
			continue // nothing to identify it by; not worth proposing
		}
		peer.Hostname = strings.TrimSuffix(srv.Target, ".")
		peer.Port = int(srv.Port)
		peer.Source = key.source
		peer.Addrs = a.addrs[recordKey{key.source, strings.ToLower(srv.Target)}]
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
	// Trimmed per sender before the global cap, so filling the window with
	// low-sorting names costs the flooder its own slots rather than everyone
	// else's. Without this the sort — which is over an attacker-chosen id —
	// decides who survives the truncation.
	out = capPerSender(out)
	if len(out) > maxPeers {
		out = out[:maxPeers]
	}
	return out
}
