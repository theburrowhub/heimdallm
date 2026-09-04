package lan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// osHostname is a variable so tests can pin the advertised name instead of
// depending on whatever the machine running them happens to be called.
var osHostname = os.Hostname

// Advertisement is what this daemon publishes about itself.
type Advertisement struct {
	InstanceID   string
	InstanceName string
	Role         string
	Version      string
	Scheme       string // http unless set
	Hostname     string // defaults to "<os hostname>.local."
	Port         int

	// Addrs supplies the A/AAAA records, and is called per response rather
	// than sampled once. A daemon's addresses are exactly the thing that
	// changes underneath it — that is the bug this whole feature exists for —
	// so an advertiser that captured them at startup would answer with a stale
	// address after the very lease change it is meant to survive. nil means
	// advertise no addresses, which is valid: the SRV target is a name, and
	// resolving it is the resolver's job.
	Addrs func() []netip.Addr
}

// Advertiser answers mDNS queries for this daemon's service record.
//
// It is a responder, not a broadcaster: it does not announce itself on a timer,
// it replies when asked. That keeps a daemon that nobody is looking for
// completely silent on the network, which matters on a corporate LAN where
// advertising a service at all is a deliberate choice.
//
// The one unsolicited message it sends is the goodbye on shutdown.
type Advertiser struct {
	conn PacketConn
	ad   Advertisement
	log  *slog.Logger

	instanceName string // the escaped FQDN this daemon answers to
	txt          []string
}

// NewAdvertiser validates the advertisement and prepares the records.
func NewAdvertiser(conn PacketConn, ad Advertisement, log *slog.Logger) (*Advertiser, error) {
	if conn == nil {
		return nil, errors.New("lan: advertiser needs a connection")
	}
	if strings.TrimSpace(ad.InstanceID) == "" {
		return nil, errors.New("lan: advertiser needs an instance id")
	}
	if err := validatePort(ad.Port); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	if ad.Scheme == "" {
		ad.Scheme = "http"
	}
	if strings.TrimSpace(ad.Hostname) == "" {
		ad.Hostname = defaultHostname()
	}
	ad.Hostname = dns.Fqdn(strings.TrimSpace(ad.Hostname))

	// The DNS-SD instance label is the display name when there is one, and the
	// id otherwise. The id is the tiebreaker rather than the first choice
	// because two machines can legitimately share a name and never share an id.
	label := ad.InstanceName
	if strings.TrimSpace(label) == "" {
		label = ad.InstanceID
	}

	return &Advertiser{
		conn:         conn,
		ad:           ad,
		log:          log,
		instanceName: instanceFQDN(label),
		txt: encodeTXT(Peer{
			InstanceID:   ad.InstanceID,
			InstanceName: ad.InstanceName,
			Role:         ad.Role,
			Version:      ad.Version,
			Scheme:       ad.Scheme,
		}),
	}, nil
}

// Close releases the underlying transport. Run must have returned first, so
// the goodbye it sends on the way out still reaches the network.
func (a *Advertiser) Close() error { return a.conn.Close() }

// maxConsecutiveReadErrors is how many non-timeout read failures in a row mean
// the socket is not coming back.
//
// Swallowing them indefinitely was a bug: after a suspend and resume the
// socket can keep returning a permanent interface error, and a loop that only
// logs and continues spins on it forever — never returning, so never
// reconnecting and never backing off. A handful of failures is a blip; a
// steady stream is a socket that has to be replaced, which only the caller can
// do.
const maxConsecutiveReadErrors = 10

// recordTTL is the lifetime published with every record.
const recordTTL = 120

// serviceEnumerationName is the DNS-SD meta-query asking which service types
// exist on this link (RFC 6763 §9).
const serviceEnumerationName = "_services._dns-sd._udp." + Domain + "."

// unicastResponseBit is the top bit of an mDNS question's class field, used to
// request a unicast reply (RFC 6762 §18.12). It is a flag, not part of the
// class, and has to be masked off before comparing.
const unicastResponseBit = 1 << 15

// Run answers queries until ctx is cancelled or the socket stops working, then
// sends a goodbye.
//
// Returns nil on cancellation and an error when the connection failed, so the
// caller can tell "we are shutting down" from "this socket needs replacing".
func (a *Advertiser) Run(ctx context.Context) error {
	a.log.Info("lan: advertising on the local network",
		"service", Service, "instance", a.instanceName,
		"hostname", strings.TrimSuffix(a.ad.Hostname, "."), "port", a.ad.Port)

	// The goodbye rides on a deferred call with its own deadline: by the time
	// we get here ctx is already cancelled, so reusing it would send nothing.
	defer a.goodbye()

	buf := make([]byte, 9000) // jumbo frame; mDNS responses are far smaller
	failures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		// A short deadline rather than a blocking read, so cancellation is
		// noticed promptly without a second goroutine to close the socket.
		_ = a.conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, from, err := a.conn.ReadFrom(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				failures = 0
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return fmt.Errorf("lan: the advertiser's connection was closed: %w", err)
			}
			failures++
			if failures >= maxConsecutiveReadErrors {
				return fmt.Errorf("lan: giving up on this connection after %d "+
					"consecutive read failures: %w", failures, err)
			}
			a.log.Debug("lan: read failed", "err", err, "consecutive", failures)
			continue
		}
		failures = 0
		a.respond(buf[:n], from)
	}
}

// respond answers a query if it is asking about us.
func (a *Advertiser) respond(packet []byte, from net.Addr) {
	var msg dns.Msg
	if err := msg.Unpack(packet); err != nil {
		return // not our business; the mDNS group carries everyone's traffic
	}
	if msg.Response || len(msg.Question) == 0 {
		return
	}

	// De-duplicated across questions: a query carrying both a PTR and an SRV
	// question for this instance would otherwise repeat the whole record set,
	// which is wasteful and can push the response past the MTU.
	var answers []dns.RR
	seen := map[string]bool{}
	for _, q := range msg.Question {
		for _, rr := range a.recordsFor(q) {
			key := rr.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			answers = append(answers, rr)
		}
	}
	if len(answers) == 0 {
		return
	}

	reply := new(dns.Msg)
	reply.SetReply(&msg)
	// An mDNS response is not a unicast DNS answer: it must stand on its own,
	// because a listener may not have seen the question (RFC 6762 §6).
	reply.Question = nil
	reply.Answer = answers
	reply.Authoritative = true

	a.send(reply)
}

// recordsFor returns everything we hold that answers q.
//
// The full record set goes out for any matching question, not just the type
// asked for. That is what RFC 6763 §12 calls for and what makes one round trip
// enough: a browser asking only for PTR still gets the SRV, TXT and A it will
// need next.
func (a *Advertiser) recordsFor(q dns.Question) []dns.RR {
	// The top bit of the class field is mDNS's unicast-response flag
	// (RFC 6762 §18.12), not part of the class. Masking it is what stops a
	// question with QU set being read as an unknown class and ignored; a class
	// that is genuinely not INET is not ours to answer.
	if q.Qclass&^unicastResponseBit != dns.ClassINET {
		return nil
	}

	name := strings.ToLower(dns.Fqdn(q.Name))
	service := strings.ToLower(serviceFQDN())
	instance := strings.ToLower(a.instanceName)

	switch name {
	case serviceEnumerationName:
		// The meta-query asks which service *types* exist here, so the answer
		// is a PTR named after the meta-query itself pointing at the type
		// (RFC 6763 §9). Replying with our own service's records instead
		// answers a question nobody asked and leaves generic browsers unable
		// to enumerate us at all.
		if q.Qtype != dns.TypePTR && q.Qtype != dns.TypeANY {
			return nil
		}
		return []dns.RR{&dns.PTR{
			Hdr: dns.RR_Header{Name: serviceEnumerationName, Rrtype: dns.TypePTR,
				Class: dns.ClassINET, Ttl: recordTTL},
			Ptr: serviceFQDN(),
		}}
	case service, instance:
		if q.Qtype != dns.TypePTR && q.Qtype != dns.TypeSRV &&
			q.Qtype != dns.TypeTXT && q.Qtype != dns.TypeANY {
			return nil
		}
		return a.allRecords()
	case strings.ToLower(a.ad.Hostname):
		// A host question: addresses and nothing else. Gated on the type, or
		// a PTR query for the hostname would be answered with A records.
		if q.Qtype != dns.TypeA && q.Qtype != dns.TypeAAAA && q.Qtype != dns.TypeANY {
			return nil
		}
		return a.addressRecords()
	}
	return nil
}

func (a *Advertiser) allRecords() []dns.RR {
	records := []dns.RR{
		&dns.PTR{
			Hdr: dns.RR_Header{Name: serviceFQDN(), Rrtype: dns.TypePTR,
				Class: dns.ClassINET, Ttl: recordTTL},
			Ptr: a.instanceName,
		},
		&dns.SRV{
			Hdr: dns.RR_Header{Name: a.instanceName, Rrtype: dns.TypeSRV,
				Class: dns.ClassINET, Ttl: recordTTL},
			Priority: 0, Weight: 0,
			Port:   uint16(a.ad.Port),
			Target: a.ad.Hostname,
		},
		&dns.TXT{
			Hdr: dns.RR_Header{Name: a.instanceName, Rrtype: dns.TypeTXT,
				Class: dns.ClassINET, Ttl: recordTTL},
			Txt: a.txt,
		},
	}
	return append(records, a.addressRecords()...)
}

func (a *Advertiser) addressRecords() []dns.RR {
	if a.ad.Addrs == nil {
		return nil
	}
	var out []dns.RR
	for _, addr := range a.ad.Addrs() {
		if addr.Is4() {
			out = append(out, &dns.A{
				Hdr: dns.RR_Header{Name: a.ad.Hostname, Rrtype: dns.TypeA,
					Class: dns.ClassINET, Ttl: recordTTL},
				A: net.IP(addr.AsSlice()),
			})
			continue
		}
		out = append(out, &dns.AAAA{
			Hdr: dns.RR_Header{Name: a.ad.Hostname, Rrtype: dns.TypeAAAA,
				Class: dns.ClassINET, Ttl: recordTTL},
			AAAA: net.IP(addr.AsSlice()),
		})
	}
	return out
}

// goodbye retracts the record by re-announcing it with TTL 0, so a hub browsing
// right now drops us immediately instead of holding a dead entry for the TTL.
func (a *Advertiser) goodbye() {
	msg := new(dns.Msg)
	msg.Response = true
	msg.Authoritative = true
	msg.Answer = a.allRecords()
	for _, rr := range msg.Answer {
		rr.Header().Ttl = 0
	}
	a.send(msg)
}

func (a *Advertiser) send(msg *dns.Msg) {
	packed, err := msg.Pack()
	if err != nil {
		a.log.Debug("lan: packing a response failed", "err", err)
		return
	}
	// Always to the group. A unicast reply would reach only the asker, and mDNS
	// peers legitimately learn from responses to questions they did not ask.
	if _, err := a.conn.WriteTo(packed, GroupAddr()); err != nil {
		a.log.Debug("lan: sending a response failed", "err", err)
	}
}

// defaultHostname is this machine's mDNS name. Falls back to a generic label
// rather than failing: an advertisement with a wrong-but-present hostname is
// still useful for the addresses it carries.
func defaultHostname() string {
	const fallback = "heimdallm." + Domain + "."

	host, err := osHostname()
	if err != nil {
		return fallback
	}
	// A machine configured with an FQDN ("mac.corp.example.com") still answers
	// mDNS as its short name under .local.
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, "."+Domain)
	if i := strings.Index(host, "."); i > 0 {
		host = host[:i]
	}
	// Sanitised into a legal label, and the fallback used when nothing legal
	// survives. An over-long or exotic hostname otherwise produces a name that
	// msg.Pack refuses, and packing failures are only logged at Debug — so the
	// advertiser would answer nothing at all, for the life of the process,
	// with no visible reason.
	host = sanitizeLabel(host)
	if host == "" {
		return fallback
	}
	return host + "." + Domain + "."
}

// sanitizeLabel reduces s to a legal DNS label, or "" when nothing usable is
// left. Legal here is the hostname rule: letters, digits and inner hyphens, at
// most 63 characters.
func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 63 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	label := strings.Trim(b.String(), "-")
	if !isDNSLabel(label) {
		return ""
	}
	return label
}
