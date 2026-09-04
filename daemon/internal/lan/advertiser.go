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

// Run answers queries until ctx is cancelled, then sends a goodbye.
func (a *Advertiser) Run(ctx context.Context) {
	a.log.Info("lan: advertising on the local network",
		"service", Service, "instance", a.instanceName,
		"hostname", strings.TrimSuffix(a.ad.Hostname, "."), "port", a.ad.Port)

	// The goodbye rides on a deferred call with its own deadline: by the time
	// we get here ctx is already cancelled, so reusing it would send nothing.
	defer a.goodbye()

	buf := make([]byte, 9000) // jumbo frame; mDNS responses are far smaller
	for {
		if ctx.Err() != nil {
			return
		}
		// A short deadline rather than a blocking read, so cancellation is
		// noticed promptly without a second goroutine to close the socket.
		_ = a.conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, from, err := a.conn.ReadFrom(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			a.log.Debug("lan: read failed", "err", err)
			continue
		}
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

	var answers []dns.RR
	for _, q := range msg.Question {
		answers = append(answers, a.recordsFor(q)...)
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

	a.send(reply, from)
}

// recordsFor returns everything we hold that answers q.
//
// The full record set goes out for any matching question, not just the type
// asked for. That is what RFC 6763 §12 calls for and what makes one round trip
// enough: a browser asking only for PTR still gets the SRV, TXT and A it will
// need next.
func (a *Advertiser) recordsFor(q dns.Question) []dns.RR {
	name := strings.ToLower(dns.Fqdn(q.Name))
	service := strings.ToLower(serviceFQDN())
	instance := strings.ToLower(a.instanceName)

	switch name {
	case service, "_services._dns-sd._udp." + Domain + ".":
	case instance:
	case strings.ToLower(a.ad.Hostname):
		// A host-only question: answer with addresses alone.
		return a.addressRecords()
	default:
		return nil
	}
	if q.Qtype != dns.TypePTR && q.Qtype != dns.TypeSRV &&
		q.Qtype != dns.TypeTXT && q.Qtype != dns.TypeANY {
		return nil
	}
	return a.allRecords()
}

func (a *Advertiser) allRecords() []dns.RR {
	const ttl = 120

	records := []dns.RR{
		&dns.PTR{
			Hdr: dns.RR_Header{Name: serviceFQDN(), Rrtype: dns.TypePTR,
				Class: dns.ClassINET, Ttl: ttl},
			Ptr: a.instanceName,
		},
		&dns.SRV{
			Hdr: dns.RR_Header{Name: a.instanceName, Rrtype: dns.TypeSRV,
				Class: dns.ClassINET, Ttl: ttl},
			Priority: 0, Weight: 0,
			Port:   uint16(a.ad.Port),
			Target: a.ad.Hostname,
		},
		&dns.TXT{
			Hdr: dns.RR_Header{Name: a.instanceName, Rrtype: dns.TypeTXT,
				Class: dns.ClassINET, Ttl: ttl},
			Txt: a.txt,
		},
	}
	return append(records, a.addressRecords()...)
}

func (a *Advertiser) addressRecords() []dns.RR {
	const ttl = 120
	if a.ad.Addrs == nil {
		return nil
	}
	var out []dns.RR
	for _, addr := range a.ad.Addrs() {
		if addr.Is4() {
			out = append(out, &dns.A{
				Hdr: dns.RR_Header{Name: a.ad.Hostname, Rrtype: dns.TypeA,
					Class: dns.ClassINET, Ttl: ttl},
				A: net.IP(addr.AsSlice()),
			})
			continue
		}
		out = append(out, &dns.AAAA{
			Hdr: dns.RR_Header{Name: a.ad.Hostname, Rrtype: dns.TypeAAAA,
				Class: dns.ClassINET, Ttl: ttl},
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
	a.send(msg, GroupAddr())
}

func (a *Advertiser) send(msg *dns.Msg, to net.Addr) {
	packed, err := msg.Pack()
	if err != nil {
		a.log.Debug("lan: packing a response failed", "err", err)
		return
	}
	// Always to the group. A unicast reply would reach only the asker, and mDNS
	// peers legitimately learn from responses to questions they did not ask.
	if _, err := a.conn.WriteTo(packed, responseTarget(to)); err != nil {
		a.log.Debug("lan: sending a response failed", "err", err)
	}
}

// responseTarget keeps in-memory transports working: they route by pair, not by
// address, so handing them the real multicast group would be meaningless.
func responseTarget(from net.Addr) net.Addr {
	if _, ok := from.(memAddr); ok {
		return from
	}
	return GroupAddr()
}

// defaultHostname is this machine's mDNS name. Falls back to a generic label
// rather than failing: an advertisement with a wrong-but-present hostname is
// still useful for the addresses it carries.
func defaultHostname() string {
	host, err := osHostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "heimdallm." + Domain + "."
	}
	// A machine configured with an FQDN ("mac.corp.example.com") still answers
	// mDNS as its short name under .local.
	host = strings.TrimSuffix(host, "."+Domain)
	if i := strings.Index(host, "."); i > 0 {
		host = host[:i]
	}
	return fmt.Sprintf("%s.%s.", host, Domain)
}
