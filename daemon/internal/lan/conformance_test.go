package lan

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

// Capping the number of host *keys* was not enough: a sender emitting many
// distinct A records for the same name grew that one entry without limit,
// which is the flood this package claims to defend against arriving through
// the door left open.
func TestAccumulatorCapsAddressesPerHost(t *testing.T) {
	acc := newAccumulator()
	for i := range maxAddrsPerHost * 10 {
		acc.addAddr("srv-a.local.", net.ParseIP(fmt.Sprintf("10.0.%d.%d", i/256, i%256)), netip.Addr{})
	}
	if got := len(acc.addrs[hostKey{netip.Addr{}, "srv-a.local."}]); got > maxAddrsPerHost {
		t.Fatalf("one hostname accumulated %d addresses, above the %d cap",
			got, maxAddrsPerHost)
	}
}

// The same address twice is still recorded once, now that dedup goes through a
// set rather than a linear scan.
func TestAccumulatorStillDeduplicatesAddresses(t *testing.T) {
	acc := newAccumulator()
	for range 5 {
		acc.addAddr("srv-a.local.", net.ParseIP("10.0.0.11"), netip.Addr{})
	}
	if got := len(acc.addrs[hostKey{netip.Addr{}, "srv-a.local."}]); got != 1 {
		t.Fatalf("recorded the same address %d times, want 1", got)
	}
}

// A goodbye is as unauthenticated as everything else on this group. Without a
// source check, any host on the link evicts a legitimate daemon from an
// in-flight browse by naming it — denial of discovery for one packet.
func TestGoodbyeFromAStrangerIsIgnored(t *testing.T) {
	owner := netip.MustParseAddr("192.168.1.20")
	attacker := netip.MustParseAddr("192.168.1.99")

	acc := newAccumulator()
	acc.absorb(pack(t, ptrRR(), srvRR(), txtRR()), owner)
	if len(acc.instances) != 1 {
		t.Fatalf("the advertisement was not recorded: %+v", acc.instances)
	}

	goodbye := ptrRR()
	goodbye.Hdr.Ttl = 0
	acc.absorb(pack(t, goodbye), attacker)

	if len(acc.instances) != 1 {
		t.Fatal("a stranger's goodbye evicted somebody else's daemon")
	}
}

// The daemon's own goodbye is still honoured, or a peer that really left would
// linger for the rest of the window.
func TestGoodbyeFromTheAdvertiserIsHonoured(t *testing.T) {
	owner := netip.MustParseAddr("192.168.1.20")

	acc := newAccumulator()
	acc.absorb(pack(t, ptrRR(), srvRR(), txtRR()), owner)

	goodbye := ptrRR()
	goodbye.Hdr.Ttl = 0
	acc.absorb(pack(t, goodbye), owner)

	if len(acc.instances) != 0 {
		t.Fatalf("the advertiser's own goodbye was ignored: %+v", acc.instances)
	}
}

// A retraction takes the host's addresses with it. Leaving them meant an
// instance re-advertised later in the same window was reported carrying the
// addresses it had just retracted.
func TestGoodbyeAlsoDropsTheAddresses(t *testing.T) {
	owner := netip.MustParseAddr("192.168.1.20")

	acc := newAccumulator()
	addr := &dns.A{
		Hdr: dns.RR_Header{Name: "srv-a.local.", Rrtype: dns.TypeA,
			Class: dns.ClassINET, Ttl: recordTTL},
		A: net.ParseIP("192.168.1.20"),
	}
	acc.absorb(pack(t, ptrRR(), srvRR(), txtRR(), addr), owner)
	if len(acc.addrs[hostKey{owner, "srv-a.local."}]) != 1 {
		t.Fatal("the address was not recorded")
	}

	goodbye := srvRR()
	goodbye.Hdr.Ttl = 0
	acc.absorb(pack(t, goodbye), owner)

	if got := acc.addrs[hostKey{owner, "srv-a.local."}]; len(got) != 0 {
		t.Fatalf("addresses survived the retraction: %v", got)
	}
}

// RFC 6763 §9: the reply to the meta-query is a PTR named after the meta-query
// itself, pointing at the service type. Answering with our own service's
// records instead leaves generic browsers unable to enumerate us at all.
func TestServiceEnumerationAnswersWithTheServiceType(t *testing.T) {
	adv := newTestAdvertiser(t)

	got := adv.recordsFor(dns.Question{
		Name: serviceEnumerationName, Qtype: dns.TypePTR, Qclass: dns.ClassINET,
	})
	if len(got) != 1 {
		t.Fatalf("got %d records, want exactly one PTR: %v", len(got), got)
	}
	ptr, ok := got[0].(*dns.PTR)
	if !ok {
		t.Fatalf("got %T, want *dns.PTR", got[0])
	}
	if ptr.Hdr.Name != serviceEnumerationName {
		t.Errorf("PTR name = %q, want the meta-query's own name", ptr.Hdr.Name)
	}
	if ptr.Ptr != serviceFQDN() {
		t.Errorf("PTR target = %q, want the service type %q", ptr.Ptr, serviceFQDN())
	}
}

// A host question is answered with addresses, and only when addresses are what
// was asked for — a PTR query for the hostname used to draw A records.
func TestHostQuestionIsGatedOnTheType(t *testing.T) {
	adv := newTestAdvertiser(t)
	host := adv.ad.Hostname

	for _, tt := range []struct {
		qtype uint16
		name  string
		want  bool
	}{
		{dns.TypeA, "A", true},
		{dns.TypeAAAA, "AAAA", true},
		{dns.TypeANY, "ANY", true},
		{dns.TypePTR, "PTR", false},
		{dns.TypeSRV, "SRV", false},
		{dns.TypeTXT, "TXT", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := adv.recordsFor(dns.Question{Name: host, Qtype: tt.qtype, Qclass: dns.ClassINET})
			if (len(got) > 0) != tt.want {
				t.Fatalf("a %s question for the hostname returned %d records, want any=%v",
					tt.name, len(got), tt.want)
			}
		})
	}
}

// The top bit of the class is mDNS's unicast-response flag, not part of the
// class. Reading it as one would make every QU question go unanswered.
func TestQuestionClassHandling(t *testing.T) {
	adv := newTestAdvertiser(t)

	t.Run("unicast-response bit is masked, not rejected", func(t *testing.T) {
		got := adv.recordsFor(dns.Question{
			Name: serviceFQDN(), Qtype: dns.TypePTR,
			Qclass: dns.ClassINET | unicastResponseBit,
		})
		if len(got) == 0 {
			t.Fatal("a question with the unicast-response bit set went unanswered")
		}
	})

	t.Run("a class that is genuinely not INET is not ours", func(t *testing.T) {
		got := adv.recordsFor(dns.Question{
			Name: serviceFQDN(), Qtype: dns.TypePTR, Qclass: dns.ClassCHAOS,
		})
		if len(got) != 0 {
			t.Fatalf("answered a CHAOS question with %d records", len(got))
		}
	})
}

// A query carrying two questions for this instance used to repeat the whole
// record set, which is wasteful and can push the response past the MTU.
func TestResponseDoesNotRepeatRecordsAcrossQuestions(t *testing.T) {
	adConn, peerConn := NewMemConn()
	t.Cleanup(func() { _ = adConn.Close(); _ = peerConn.Close() })

	adv, err := NewAdvertiser(adConn, testAdvertisement(), quietLogger())
	if err != nil {
		t.Fatalf("NewAdvertiser: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = adv.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	msg := new(dns.Msg)
	msg.Question = []dns.Question{
		{Name: serviceFQDN(), Qtype: dns.TypePTR, Qclass: dns.ClassINET},
		{Name: serviceFQDN(), Qtype: dns.TypeSRV, Qclass: dns.ClassINET},
		{Name: serviceFQDN(), Qtype: dns.TypeTXT, Qclass: dns.ClassINET},
	}
	packed, _ := msg.Pack()
	if _, err := peerConn.WriteTo(packed, GroupAddr()); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	reply := readReply(t, peerConn)
	seen := map[string]int{}
	for _, rr := range reply.Answer {
		seen[rr.String()]++
	}
	for record, n := range seen {
		if n > 1 {
			t.Errorf("record repeated %d times in one reply: %s", n, record)
		}
	}
}

// An over-long or exotic hostname must not produce a name msg.Pack refuses:
// packing failures are only logged at Debug, so the advertiser would answer
// nothing at all for the life of the process with no visible reason.
func TestDefaultHostnameIsAlwaysPackable(t *testing.T) {
	real := osHostname
	t.Cleanup(func() { osHostname = real })

	for _, host := range []string{
		strings.Repeat("a", 200),
		"MAC-Sergio",
		"mac sergio with spaces",
		"emoji-🎉-host",
		"...",
		"-leading-hyphen",
		"trailing-hyphen-",
		"under_score",
		"",
		"   ",
	} {
		t.Run(host, func(t *testing.T) {
			osHostname = func() (string, error) { return host, nil }
			name := defaultHostname()

			if _, err := ValidateMDNSHostname(name); err != nil {
				t.Fatalf("defaultHostname() = %q, which is not a usable mDNS name: %v", name, err)
			}
			// And it survives the wire, which is the failure being prevented.
			msg := new(dns.Msg)
			msg.SetQuestion(name, dns.TypeA)
			if _, err := msg.Pack(); err != nil {
				t.Fatalf("defaultHostname() = %q, which msg.Pack refuses: %v", name, err)
			}
		})
	}
}

func TestSanitizeLabel(t *testing.T) {
	tests := []struct{ in, want string }{
		{"mac-sergio", "mac-sergio"},
		{"under_score", "under-score"},
		{"-leading", "leading"},
		{"trailing-", "trailing"},
		{"emoji-🎉-host", "emoji--host"},
		{strings.Repeat("a", 100), strings.Repeat("a", 63)},
		{"...", ""},
		{"", ""},
		{"---", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := sanitizeLabel(tt.in); got != tt.want {
				t.Fatalf("sanitizeLabel(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// newTestAdvertiser builds an advertiser over a throwaway transport.
func newTestAdvertiser(t *testing.T) *Advertiser {
	t.Helper()
	conn, other := NewMemConn()
	t.Cleanup(func() { _ = conn.Close(); _ = other.Close() })
	adv, err := NewAdvertiser(conn, testAdvertisement(), quietLogger())
	if err != nil {
		t.Fatalf("NewAdvertiser: %v", err)
	}
	return adv
}

// pack renders records as a response packet.
func pack(t *testing.T, answers ...dns.RR) []byte {
	t.Helper()
	msg := new(dns.Msg)
	msg.Response = true
	msg.Answer = answers
	packed, err := msg.Pack()
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	return packed
}

// The hole the source check was supposed to close, reopened by my own fix: a
// refused retraction fell through and cleared the addresses anyway, so a forged
// goodbye still emptied a legitimate peer even though the record survived.
func TestAForgedGoodbyeCannotStripAPeersAddresses(t *testing.T) {
	owner := netip.MustParseAddr("192.168.1.20")
	attacker := netip.MustParseAddr("192.168.1.99")

	addr := &dns.A{
		Hdr: dns.RR_Header{Name: "srv-a.local.", Rrtype: dns.TypeA,
			Class: dns.ClassINET, Ttl: recordTTL},
		A: net.ParseIP("192.168.1.20"),
	}

	for _, tt := range []struct {
		name    string
		goodbye func() dns.RR
	}{
		{"an SRV goodbye", func() dns.RR { r := srvRR(); r.Hdr.Ttl = 0; return r }},
		{"a TXT goodbye", func() dns.RR { r := txtRR(); r.Hdr.Ttl = 0; return r }},
		{"an A goodbye", func() dns.RR {
			r := &dns.A{Hdr: addr.Hdr, A: addr.A}
			r.Hdr.Ttl = 0
			return r
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			acc := newAccumulator()
			acc.absorb(pack(t, ptrRR(), srvRR(), txtRR(), addr), owner)
			if len(acc.addrs[hostKey{owner, "srv-a.local."}]) != 1 {
				t.Fatal("the address was not recorded")
			}

			acc.absorb(pack(t, tt.goodbye()), attacker)

			if got := acc.addrs[hostKey{owner, "srv-a.local."}]; len(got) != 1 {
				t.Fatalf("a stranger's goodbye stripped the addresses: %v", got)
			}
			if len(acc.instances) != 1 {
				t.Fatal("a stranger's goodbye evicted the instance")
			}
		})
	}
}

// And the advertiser's own address goodbye still works, or a peer that really
// moved would be reported at an address it no longer answers on.
func TestAnAdvertiserCanWithdrawItsOwnAddresses(t *testing.T) {
	owner := netip.MustParseAddr("192.168.1.20")
	addr := &dns.A{
		Hdr: dns.RR_Header{Name: "srv-a.local.", Rrtype: dns.TypeA,
			Class: dns.ClassINET, Ttl: recordTTL},
		A: net.ParseIP("192.168.1.20"),
	}

	acc := newAccumulator()
	acc.absorb(pack(t, ptrRR(), srvRR(), txtRR(), addr), owner)

	goodbye := &dns.A{Hdr: addr.Hdr, A: addr.A}
	goodbye.Hdr.Ttl = 0
	acc.absorb(pack(t, goodbye), owner)

	if got := acc.addrs[hostKey{owner, "srv-a.local."}]; len(got) != 0 {
		t.Fatalf("the advertiser could not withdraw its own addresses: %v", got)
	}
}

// Address ownership is scoped to the sender, so neither of the two ways an
// attacker previously reached somebody else's addresses works — and neither
// works because there is nothing shared to reach, rather than because a check
// caught it.
func TestOneSenderCannotTouchAnothersAddresses(t *testing.T) {
	owner := netip.MustParseAddr("192.168.1.20")
	attacker := netip.MustParseAddr("192.168.1.99")

	ownerAddr := &dns.A{
		Hdr: dns.RR_Header{Name: "srv-a.local.", Rrtype: dns.TypeA,
			Class: dns.ClassINET, Ttl: recordTTL},
		A: net.ParseIP("192.168.1.20"),
	}

	t.Run("by naming their hostname as its own SRV target", func(t *testing.T) {
		acc := newAccumulator()
		acc.absorb(pack(t, ptrRR(), srvRR(), txtRR(), ownerAddr), owner)

		// The attacker registers its own instance pointing at the victim's
		// hostname, then retires it — which used to take the victim's
		// addresses with it.
		evilName := "evil._heimdallm._tcp.local."
		evilSRV := &dns.SRV{
			Hdr: dns.RR_Header{Name: evilName, Rrtype: dns.TypeSRV,
				Class: dns.ClassINET, Ttl: recordTTL},
			Port: 7842, Target: "srv-a.local.",
		}
		acc.absorb(pack(t,
			&dns.PTR{Hdr: dns.RR_Header{Name: serviceFQDN(), Rrtype: dns.TypePTR,
				Class: dns.ClassINET, Ttl: recordTTL}, Ptr: evilName},
			evilSRV,
			&dns.TXT{Hdr: dns.RR_Header{Name: evilName, Rrtype: dns.TypeTXT,
				Class: dns.ClassINET, Ttl: recordTTL}, Txt: []string{"id=evil"}},
		), attacker)

		goodbye := &dns.SRV{Hdr: evilSRV.Hdr, Port: evilSRV.Port, Target: evilSRV.Target}
		goodbye.Hdr.Ttl = 0
		acc.absorb(pack(t, goodbye), attacker)

		if got := acc.addrs[hostKey{owner, "srv-a.local."}]; len(got) != 1 {
			t.Fatalf("the victim's addresses were stripped through an "+
				"attacker-chosen SRV target: %v", got)
		}
	})

	t.Run("by claiming their hostname first", func(t *testing.T) {
		acc := newAccumulator()

		// The attacker publishes an address for the victim's hostname before
		// the victim does, which used to make it the owner.
		acc.absorb(pack(t, &dns.A{Hdr: ownerAddr.Hdr, A: net.ParseIP("10.0.0.66")}), attacker)
		acc.absorb(pack(t, ptrRR(), srvRR(), txtRR(), ownerAddr), owner)

		goodbye := &dns.A{Hdr: ownerAddr.Hdr, A: net.ParseIP("10.0.0.66")}
		goodbye.Hdr.Ttl = 0
		acc.absorb(pack(t, goodbye), attacker)

		if got := acc.addrs[hostKey{owner, "srv-a.local."}]; len(got) != 1 {
			t.Fatalf("the victim's addresses were stripped by a first-claimer: %v", got)
		}
	})
}

// And a peer is reported with the addresses its own sender published, not with
// whatever another sender attached to the same hostname.
func TestAPeerCarriesOnlyItsOwnSendersAddresses(t *testing.T) {
	owner := netip.MustParseAddr("192.168.1.20")
	attacker := netip.MustParseAddr("192.168.1.99")

	acc := newAccumulator()
	// The attacker gets its address in first, under the victim's hostname.
	acc.absorb(pack(t, &dns.A{
		Hdr: dns.RR_Header{Name: "srv-a.local.", Rrtype: dns.TypeA,
			Class: dns.ClassINET, Ttl: recordTTL},
		A: net.ParseIP("10.0.0.66"),
	}), attacker)
	acc.absorb(pack(t, ptrRR(), srvRR(), txtRR(), &dns.A{
		Hdr: dns.RR_Header{Name: "srv-a.local.", Rrtype: dns.TypeA,
			Class: dns.ClassINET, Ttl: recordTTL},
		A: net.ParseIP("192.168.1.20"),
	}), owner)

	peers := acc.peers()
	if len(peers) != 1 {
		t.Fatalf("got %d peers, want 1", len(peers))
	}
	for _, addr := range peers[0].Addrs {
		if addr.String() == "10.0.0.66" {
			t.Fatalf("the peer carries an address another sender attached to "+
				"its hostname: %v", peers[0].Addrs)
		}
	}
	if len(peers[0].Addrs) != 1 || peers[0].Addrs[0].String() != "192.168.1.20" {
		t.Fatalf("Addrs = %v, want just the owner's own", peers[0].Addrs)
	}
}
