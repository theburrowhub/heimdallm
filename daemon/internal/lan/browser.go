package lan

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"
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

		n, _, err := b.conn.ReadFrom(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			b.log.Debug("lan: read failed while browsing", "err", err)
			continue
		}
		acc.absorb(buf[:n])
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
}

func newAccumulator() *accumulator {
	return &accumulator{
		instances: map[string]bool{},
		srv:       map[string]*dns.SRV{},
		txt:       map[string][]string{},
		addrs:     map[string][]netip.Addr{},
	}
}

func (a *accumulator) absorb(packet []byte) {
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
			if strings.EqualFold(rec.Hdr.Name, serviceFQDN()) {
				a.instances[strings.ToLower(rec.Ptr)] = true
			}
		case *dns.SRV:
			a.srv[strings.ToLower(rec.Hdr.Name)] = rec
		case *dns.TXT:
			a.txt[strings.ToLower(rec.Hdr.Name)] = rec.Txt
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
}

func (a *accumulator) addAddr(host string, ip net.IP) {
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
		if peer.Scheme == "" {
			peer.Scheme = "http"
		}
		if peer.BaseURL() == "" {
			continue // no addressable host or port
		}
		out = append(out, peer)
	}
	sortPeers(out)
	return out
}
