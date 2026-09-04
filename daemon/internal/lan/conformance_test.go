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
		acc.addAddr("srv-a.local.", net.ParseIP(fmt.Sprintf("10.0.%d.%d", i/256, i%256)))
	}
	if got := len(acc.addrs["srv-a.local."]); got > maxAddrsPerHost {
		t.Fatalf("one hostname accumulated %d addresses, above the %d cap",
			got, maxAddrsPerHost)
	}
}

// The same address twice is still recorded once, now that dedup goes through a
// set rather than a linear scan.
func TestAccumulatorStillDeduplicatesAddresses(t *testing.T) {
	acc := newAccumulator()
	for range 5 {
		acc.addAddr("srv-a.local.", net.ParseIP("10.0.0.11"))
	}
	if got := len(acc.addrs["srv-a.local."]); got != 1 {
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
	if len(acc.addrs["srv-a.local."]) != 1 {
		t.Fatal("the address was not recorded")
	}

	goodbye := srvRR()
	goodbye.Hdr.Ttl = 0
	acc.absorb(pack(t, goodbye), owner)

	if got := acc.addrs["srv-a.local."]; len(got) != 0 {
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
