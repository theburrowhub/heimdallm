package lan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
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
// # Name conflicts are not handled
//
// RFC 6762 §8 asks a responder to probe a name before claiming it and to
// defend or rename on conflict. This does neither: it claims its instance
// label and <hostname>.local. outright, with the cache-flush bit set. Two
// daemons on one link with the same hostname — cloned images, default
// hostnames, or two distinct FQDNs that defaultHostname collapses to the same
// short label — will both answer authoritatively and resolvers will flip
// between them, which undermines exactly the stable-addressing this package
// exists for.
//
// Accepted rather than overlooked. Probing is a state machine with its own
// timing rules, and getting it half right is worse than not having it; the
// collision needs two hosts sharing a name on one subnet, which an operator
// can see and fix by setting cluster.instance_name. Deriving the advertised
// name from the instance id on conflict would be the fix if this ever bites.
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

	// mu guards the pacing state below.
	mu                 sync.Mutex
	lastResponse       time.Time
	lastLegacyResponse time.Time
	// pending is the answer waiting to go out, accumulated from every query
	// that arrived while a send was already scheduled, and pendingKinds says
	// which shapes it already covers so a repeated question costs nothing.
	pending      []dns.RR
	pendingSeen  map[string]bool
	pendingKinds answerKind
	timer        *time.Timer
}

// answerKind is the shape of answer a question calls for. A bitmask, because
// one packet may carry several questions and the reply is their union.
type answerKind uint8

const (
	answerEnumeration answerKind = 1 << iota // the DNS-SD service-type meta-query
	answerService                            // our PTR + SRV + TXT + addresses
	answerAddresses                          // addresses only, for a host question
)

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
	// The hostname we publish is held to the same rule we hold a peer's to.
	//
	// Not defence — an advertiser is not a threat to itself — but consistency.
	// A caller passing "hub.corp.example.com" gets an SRV target every
	// Heimdallm hub rejects in ValidateMDNSHostname, so the daemon advertises
	// perfectly well and is discovered by nobody, with the refusal logged on
	// the other machine. Failing here names the problem where it can be fixed.
	// defaultHostname always produces a legal name, so this can only fire on
	// a hostname the caller chose.
	host, err := ValidateMDNSHostname(ad.Hostname)
	if err != nil {
		return nil, err
	}
	ad.Hostname = dns.Fqdn(host)

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

// cacheFlush is the same top bit in a *response*, where it means "this is the
// authoritative set for this name — replace what you have rather than adding
// to it" (RFC 6762 §10.2).
//
// It matters more here than anywhere else in the protocol. Without it a
// resolver merges our new address in beside the old one and keeps answering
// with both until the old record expires, which is exactly the stale-address
// behaviour this whole feature exists to end: a daemon that moves would be
// resolvable at the address it just left for another two minutes.
//
// Set on the records that are uniquely ours — SRV, TXT and the addresses — and
// deliberately not on PTR, which is a shared record type where several hosts
// legitimately contribute entries under one name.
const cacheFlush = 1 << 15

// Response pacing (RFC 6762 §6).
//
// Without it the responder is the asymmetric half of this package: the browser
// takes care about a hostile group, while an attacker sending queries at high
// rate makes this daemon multicast a full PTR+SRV+TXT+A set onto the LAN at
// the same rate — turning it into an amplifier for traffic it did not
// originate. The rate limit is the part that matters; the jitter is what the
// RFC asks for on shared records so that several responders do not answer the
// same query in the same instant.
const (
	responseJitterMin = 20 * time.Millisecond
	responseJitterMax = 120 * time.Millisecond
)

// minResponseInterval is a variable so tests can shorten it; nothing
// reassigns it at runtime.
var minResponseInterval = time.Second

// responseDelay is a variable so tests do not pay the jitter. Nothing
// reassigns it at runtime.
var responseDelay = randomResponseDelay

func randomResponseDelay() time.Duration {
	span := responseJitterMax - responseJitterMin
	return responseJitterMin + time.Duration(rand.Int64N(int64(span)+1))
}

// Run answers queries until ctx is cancelled or the socket stops working, then
// sends a goodbye.
//
// Returns nil on cancellation and an error when the connection failed, so the
// caller can tell "we are shutting down" from "this socket needs replacing".
func (a *Advertiser) Run(ctx context.Context) error {
	a.log.Info("lan: advertising on the local network",
		"service", Service, "instance", a.instanceName,
		"hostname", strings.TrimSuffix(a.ad.Hostname, "."), "port", a.ad.Port)

	// Deferred so it runs on every exit path, including the socket failures
	// below. It takes no context: ctx is already cancelled by the time this
	// runs, and the write is a single unacknowledged datagram with nothing to
	// wait for.
	defer a.goodbye()
	// Runs before the goodbye (defers are LIFO), so a scheduled answer cannot
	// fire afterwards and re-announce records we just retracted.
	defer a.cancelPending()

	buf := make([]byte, 9000) // jumbo frame; mDNS responses are far smaller
	failures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		// A short deadline rather than a blocking read, so cancellation is
		// noticed promptly without a second goroutine to close the socket.
		_ = a.conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, from, _, err := a.conn.ReadFrom(buf)
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
//
// Nothing is sent from here. A matching query schedules the answer and the
// read loop carries straight on, so neither the pacing interval nor the
// jitter stops the socket being drained — see scheduleResponse.
func (a *Advertiser) respond(packet []byte, from net.Addr) {
	var msg dns.Msg
	if err := msg.Unpack(packet); err != nil {
		return // not our business; the mDNS group carries everyone's traffic
	}
	if msg.Response || len(msg.Question) == 0 {
		return
	}

	// Matched before anything is built. Building means invoking the Addrs()
	// callback and formatting every record to key the dedup map, and the
	// whole point of a rate limit is that somebody else chooses how often
	// queries arrive — so a question that is not about us must cost a name
	// comparison and nothing more.
	kinds := a.kindsFor(msg.Question)
	if kinds == 0 {
		return
	}

	// A legacy one-shot resolver is not part of the group and will never see
	// a multicast answer, so it gets its own reply (RFC 6762 §6.7).
	if isLegacyQuerier(from) {
		a.respondLegacy(&msg, kinds, from)
		return
	}

	// Already covered by an answer that has not gone out yet: a flood of the
	// same question costs one build per scheduled send rather than one each.
	if a.alreadyPending(kinds) {
		return
	}
	a.scheduleResponse(kinds, a.recordsForKinds(kinds))
}

// alreadyPending reports whether a scheduled answer already carries every
// shape these questions ask for.
func (a *Advertiser) alreadyPending(kinds answerKind) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.timer != nil && a.pendingKinds&kinds == kinds
}

// scheduleResponse arranges for answers to go out later, and returns at once.
//
// Deferred rather than dropped when the rate limit bites. Dropping was an
// amplification defence that made concurrent discovery lossy: Browse sends
// exactly one query per window, so two hubs browsing the same link within the
// interval meant the second was answered by silence and saw this daemon as
// absent for its whole window. Holding the answer until the interval elapses
// is what RFC 6762 §6 actually describes, and keeps the ceiling on how often
// we transmit.
//
// The jitter is the §6 requirement that several responders not reply in the
// same instant. It used to be a time.Sleep on the read loop's own goroutine,
// which stopped the socket being drained for up to its duration and let the
// kernel receive buffer discard other queries; a timer decouples the two.
func (a *Advertiser) scheduleResponse(kinds answerKind, answers []dns.RR) {
	if len(answers) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.pendingSeen == nil {
		a.pendingSeen = map[string]bool{}
	}
	// De-duplicated across questions and across coalesced queries: a query
	// carrying both a PTR and an SRV question for this instance would
	// otherwise repeat the whole record set, which is wasteful and can push
	// the response past the MTU.
	for _, rr := range answers {
		key := rr.String()
		if a.pendingSeen[key] {
			continue
		}
		a.pendingSeen[key] = true
		a.pending = append(a.pending, rr)
	}
	a.pendingKinds |= kinds

	if a.timer != nil {
		return // riding along with the send already scheduled
	}
	delay := responseDelay()
	if !a.lastResponse.IsZero() {
		if wait := time.Until(a.lastResponse.Add(minResponseInterval)); wait > delay {
			delay = wait
		}
	}
	a.timer = time.AfterFunc(delay, a.flush)
}

// flush sends whatever has accumulated and clears the slot.
func (a *Advertiser) flush() {
	a.mu.Lock()
	answers := a.pending
	a.pending, a.pendingSeen, a.pendingKinds, a.timer = nil, nil, 0, nil
	a.lastResponse = time.Now()
	a.mu.Unlock()

	if len(answers) == 0 {
		return
	}
	// Built fresh rather than from the query. An mDNS response is not a
	// unicast DNS answer: it must stand on its own, because a listener may
	// not have seen the question (RFC 6762 §6), and it carries no query id,
	// which §18.1 requires of a multicast response. A non-zero id would
	// invite a receiver that did ask something to match this against its own
	// outstanding query by transaction id — the model mDNS deliberately does
	// not use, since the packet is addressed to everyone and most of them
	// asked nothing.
	reply := new(dns.Msg)
	reply.Response = true
	reply.Authoritative = true
	reply.Answer = answers
	a.send(reply)
}

// cancelPending drops a scheduled answer. Called on the way out of Run so a
// timer cannot fire after the goodbye and re-announce records we just
// retracted.
func (a *Advertiser) cancelPending() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	a.pending, a.pendingSeen, a.pendingKinds = nil, nil, 0
}

// legacyResponseTTL is the ceiling RFC 6762 §6.7 puts on a legacy unicast
// answer: the querier is not in the group, so it will never hear the
// cache-flush or goodbye that would otherwise correct it.
const legacyResponseTTL = 10

// isLegacyQuerier reports whether a query came from a one-shot resolver
// rather than a full mDNS participant.
//
// The test is the source port (RFC 6762 §6.7): a participant queries from
// 5353 and is listening to the group, so it hears the multicast answer along
// with everyone else. Anything else — dig, a stub resolver — is not in the
// group and would wait out its timeout while the answer went past it.
func isLegacyQuerier(from net.Addr) bool {
	udp, ok := from.(*net.UDPAddr)
	if !ok {
		return false // no port to judge by; treat it as a participant
	}
	return udp.Port != mdnsPort
}

// respondLegacy answers one non-participant directly (RFC 6762 §6.7): unicast
// to the asker, echoing its query id and question, with a short TTL.
//
// Paced on its own clock rather than the multicast one. Sharing a limiter
// would let a legacy flood starve the group's answers, which is the opposite
// of what the limit is for; separating them keeps one ceiling per path.
func (a *Advertiser) respondLegacy(query *dns.Msg, kinds answerKind, to net.Addr) {
	if !a.allowLegacyResponse() {
		return
	}
	answers := a.recordsForKinds(kinds)
	if len(answers) == 0 {
		return
	}
	reply := new(dns.Msg)
	reply.SetReply(query)
	reply.Authoritative = true
	reply.Answer = make([]dns.RR, 0, len(answers))
	for _, rr := range answers {
		// Copied before the TTL is clamped: these records are shared with the
		// multicast path, and mutating them there would quietly shorten
		// everyone's.
		c := dns.Copy(rr)
		// The cache-flush bit is a multicast notion and means nothing to a
		// one-shot resolver, which would read it as an unknown class.
		c.Header().Class &^= cacheFlush
		if c.Header().Ttl > legacyResponseTTL {
			c.Header().Ttl = legacyResponseTTL
		}
		reply.Answer = append(reply.Answer, c)
	}
	a.sendTo(reply, to)
}

// allowLegacyResponse is allowResponse for the unicast path, on its own clock.
func (a *Advertiser) allowLegacyResponse() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if !a.lastLegacyResponse.IsZero() && now.Sub(a.lastLegacyResponse) < minResponseInterval {
		return false
	}
	a.lastLegacyResponse = now
	return true
}

// recordsFor returns everything we hold that answers q.
//
// The full record set goes out for any matching question, not just the type
// asked for. That is what RFC 6763 §12 calls for and what makes one round trip
// enough: a browser asking only for PTR still gets the SRV, TXT and A it will
// need next.
func (a *Advertiser) recordsFor(q dns.Question) []dns.RR {
	return a.recordsForKinds(a.classify(q))
}

// classify says what shape of answer a question calls for, without building
// anything. Matching and building are separate so a query that is not about
// us costs a name comparison — see respond.
//
// The single place the question-to-answer mapping lives: respond's cheap
// pre-check and recordsFor both go through it, so the two cannot drift.
func (a *Advertiser) classify(q dns.Question) answerKind {
	// The top bit of the class field is mDNS's unicast-response flag
	// (RFC 6762 §18.12), not part of the class. Masking it is what stops a
	// question with QU set being read as an unknown class and ignored; a class
	// that is genuinely not INET is not ours to answer.
	if q.Qclass&^unicastResponseBit != dns.ClassINET {
		return 0
	}

	name := strings.ToLower(dns.Fqdn(q.Name))
	service := strings.ToLower(serviceFQDN())
	instance := strings.ToLower(a.instanceName)

	switch name {
	case serviceEnumerationName:
		if q.Qtype != dns.TypePTR && q.Qtype != dns.TypeANY {
			return 0
		}
		return answerEnumeration
	case service, instance:
		if q.Qtype != dns.TypePTR && q.Qtype != dns.TypeSRV &&
			q.Qtype != dns.TypeTXT && q.Qtype != dns.TypeANY {
			return 0
		}
		return answerService
	case strings.ToLower(a.ad.Hostname):
		// A host question: addresses and nothing else. Gated on the type, or
		// a PTR query for the hostname would be answered with A records.
		if q.Qtype != dns.TypeA && q.Qtype != dns.TypeAAAA && q.Qtype != dns.TypeANY {
			return 0
		}
		return answerAddresses
	}
	return 0
}

// kindsFor is classify over every question in one packet.
func (a *Advertiser) kindsFor(questions []dns.Question) answerKind {
	var kinds answerKind
	for _, q := range questions {
		kinds |= a.classify(q)
	}
	return kinds
}

// recordsForKinds builds the records for a set of answer shapes.
func (a *Advertiser) recordsForKinds(kinds answerKind) []dns.RR {
	var out []dns.RR
	if kinds&answerEnumeration != 0 {
		// The meta-query asks which service *types* exist here, so the answer
		// is a PTR named after the meta-query itself pointing at the type
		// (RFC 6763 §9). Replying with our own service's records instead
		// answers a question nobody asked and leaves generic browsers unable
		// to enumerate us at all.
		out = append(out, &dns.PTR{
			Hdr: dns.RR_Header{Name: serviceEnumerationName, Rrtype: dns.TypePTR,
				Class: dns.ClassINET, Ttl: recordTTL},
			Ptr: serviceFQDN(),
		})
	}
	if kinds&answerService != 0 {
		out = append(out, a.allRecords()...)
	}
	if kinds&answerAddresses != 0 && kinds&answerService == 0 {
		// allRecords already carries the addresses, so this only adds them
		// when the service records were not asked for.
		out = append(out, a.addressRecords()...)
	}
	return out
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
				Class: dns.ClassINET | cacheFlush, Ttl: recordTTL},
			Priority: 0, Weight: 0,
			Port:   uint16(a.ad.Port),
			Target: a.ad.Hostname,
		},
		&dns.TXT{
			Hdr: dns.RR_Header{Name: a.instanceName, Rrtype: dns.TypeTXT,
				Class: dns.ClassINET | cacheFlush, Ttl: recordTTL},
			Txt: a.txt,
		},
	}
	return append(records, a.addressRecords()...)
}

// addressRecords renders the host's addresses as A/AAAA.
//
// AAAA is published even though Peer.DialAddrs refuses v6 — see the comment
// there. Advertising is for whoever is browsing; dialing is limited by the
// socket we hold.
func (a *Advertiser) addressRecords() []dns.RR {
	if a.ad.Addrs == nil {
		return nil
	}
	var out []dns.RR
	for _, addr := range a.ad.Addrs() {
		if addr.Is4() {
			out = append(out, &dns.A{
				Hdr: dns.RR_Header{Name: a.ad.Hostname, Rrtype: dns.TypeA,
					Class: dns.ClassINET | cacheFlush, Ttl: recordTTL},
				A: net.IP(addr.AsSlice()),
			})
			continue
		}
		out = append(out, &dns.AAAA{
			Hdr: dns.RR_Header{Name: a.ad.Hostname, Rrtype: dns.TypeAAAA,
				Class: dns.ClassINET | cacheFlush, Ttl: recordTTL},
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

// send puts a message on the group. This is the normal path: mDNS peers
// legitimately learn from responses to questions they did not ask, so a
// reply that reached only the asker would waste the round trip for everyone
// else on the link.
func (a *Advertiser) send(msg *dns.Msg) { a.sendTo(msg, GroupAddr()) }

// sendTo puts a message on one address. Only the legacy unicast path uses
// anything but the group.
func (a *Advertiser) sendTo(msg *dns.Msg, to net.Addr) {
	packed, err := msg.Pack()
	if err != nil {
		a.log.Debug("lan: packing a response failed", "err", err)
		return
	}
	if _, err := a.conn.WriteTo(packed, to); err != nil {
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
