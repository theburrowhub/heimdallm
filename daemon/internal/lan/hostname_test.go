package lan

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
)

// An SRV target is supplied by whoever answered a multicast query — anyone on
// the link, unauthenticated — and the consumer turns it into a URL and fetches
// it. Anything but a single-label .local name hands the subnet an SSRF
// primitive, so the check is on the allow side, not the deny side.
func TestValidateMDNSHostname(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a normal host", "mac-sergio.local", "mac-sergio.local"},
		{"trailing dot stripped", "mac-sergio.local.", "mac-sergio.local"},
		{"case folded", "MAC-Sergio.LOCAL", "mac-sergio.local"},
		{"padded", "  srv-a.local  ", "srv-a.local"},
		{"digits", "node7.local", "node7.local"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateMDNSHostname(tt.in)
			if err != nil {
				t.Fatalf("ValidateMDNSHostname(%q) = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateMDNSHostnameRefusesAnythingOffLink(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		// The attack the check exists for: an advertiser naming a host the hub
		// can reach but the attacker cannot.
		{"cloud metadata", "metadata.google.internal."},
		{"aws metadata", "169.254.169.254"},
		{"an internal service", "vault.corp.example.com."},
		{"localhost", "localhost"},
		{"a bare ip", "10.0.0.11"},
		{"loopback", "127.0.0.1"},
		// A delegated suffix smuggled past a naive HasSuffix check.
		{"multi-label ending in .local", "metadata.google.internal.local"},
		{"two labels", "a.b.local"},
		{"empty", ""},
		{"only whitespace", "   "},
		{"only the domain", ".local"},
		{"no domain", "srv-a"},
		{"wrong domain", "srv-a.lan"},
		{"underscore", "srv_a.local"},
		{"leading hyphen", "-srv-a.local"},
		{"trailing hyphen", "srv-a-.local"},
		{"label too long", strings.Repeat("a", 64) + ".local"},
		{"a url", "http://srv-a.local"},
		{"embedded port", "srv-a.local:7842"},
		{"embedded path", "srv-a.local/health"},
		{"embedded credentials", "user@srv-a.local"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := ValidateMDNSHostname(tt.in); err == nil {
				t.Fatalf("ValidateMDNSHostname(%q) accepted it as %q", tt.in, got)
			}
		})
	}
}

// BaseURL is the function that actually builds the request target, so the
// refusal has to hold there too rather than only in the validator.
func TestBaseURLRefusesAHostnameOffLink(t *testing.T) {
	for _, host := range []string{
		"metadata.google.internal.",
		"169.254.169.254",
		"vault.corp.example.com",
		"metadata.google.internal.local",
		"127.0.0.1",
	} {
		peer := Peer{InstanceID: "x", Hostname: host, Port: 80}
		if got := peer.BaseURL(); got != "" {
			t.Fatalf("BaseURL for %q = %q, want empty", host, got)
		}
	}
}

func TestBaseURLRefusesAnImpossiblePort(t *testing.T) {
	for _, port := range []int{0, -1, 65536, 99999} {
		peer := Peer{InstanceID: "x", Hostname: "srv-a.local", Port: port}
		if got := peer.BaseURL(); got != "" {
			t.Fatalf("BaseURL with port %d = %q, want empty", port, got)
		}
	}
}

// A .local name says what a peer is called; it says nothing about where the
// name resolves, because mDNS resolution is itself unauthenticated. DialAddrs
// is what takes the choice of destination away from the advertiser.
func TestDialAddrsRefusesWhatTheHubMustNotBeSentTo(t *testing.T) {
	peer := Peer{Addrs: []netip.Addr{
		netip.MustParseAddr("127.0.0.1"),       // the hub's own services
		netip.MustParseAddr("::1"),             //
		netip.MustParseAddr("169.254.169.254"), // cloud metadata: the prize
		netip.MustParseAddr("169.254.1.1"),     // the rest of link-local
		netip.MustParseAddr("fe80::1"),         // v6 link-local
		netip.MustParseAddr("224.0.0.251"),     // multicast
		netip.MustParseAddr("ff02::fb"),        //
		netip.MustParseAddr("0.0.0.0"),         // not a host
		netip.MustParseAddr("::"),              //
	}}
	if got := peer.DialAddrs(); len(got) != 0 {
		t.Fatalf("DialAddrs kept %v; none of those are a peer", got)
	}
}

func TestDialAddrsKeepsRoutableAddresses(t *testing.T) {
	peer := Peer{Addrs: []netip.Addr{
		netip.MustParseAddr("127.0.0.1"),
		netip.MustParseAddr("10.0.0.11"),
		netip.MustParseAddr("169.254.169.254"),
		netip.MustParseAddr("192.168.1.20"),
		netip.MustParseAddr("2001:db8::1"),
	}}
	got := peer.DialAddrs()
	// 2001:db8::1 is a routable unicast address and is still dropped: the
	// transport is IPv4-only, so it could never be reached from a browse.
	want := []string{"10.0.0.11", "192.168.1.20"}
	if len(got) != len(want) {
		t.Fatalf("DialAddrs = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i].String() != w {
			t.Fatalf("DialAddrs[%d] = %s, want %s", i, got[i], w)
		}
	}
}

// A hub with two interfaces: the LAN it shares with an attacker, and a VPN
// the attacker cannot reach. Interface indices, not just prefixes, because the
// index is the half of the same-link rule that comes from the kernel.
const (
	lanIface = 2
	vpnIface = 3
)

// multiHomedHub stubs the prefix lookup per interface, the way the real one
// answers: each interface knows only its own networks.
func multiHomedHub(t *testing.T) {
	t.Helper()
	real := localPrefixes
	localPrefixes = func(idx int) []netip.Prefix {
		switch idx {
		case lanIface:
			return []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}
		case vpnIface:
			return []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16")}
		}
		return nil
	}
	t.Cleanup(func() { localPrefixes = real })
}

// The IPv4-only rule is the whole reason a v6 advertisement is refused, so it
// has to hold even when every other check would have passed. Without it the
// address survives DialAddrs whenever Source is absent, and is then dropped
// silently by sameLink whenever Source is present — two different answers for
// the same peer depending on which transport carried it.
func TestDialAddrsDropsIPv6EvenWithNothingElseAgainstIt(t *testing.T) {
	// An interface attached to the v6 network the candidate lives on, and a
	// sender on that same network: sameLink would say yes.
	realPrefixes := localPrefixes
	localPrefixes = func(int) []netip.Prefix {
		return []netip.Prefix{netip.MustParsePrefix("2001:db8::/64")}
	}
	t.Cleanup(func() { localPrefixes = realPrefixes })

	for _, source := range []netip.Addr{
		{}, // in-memory transport: no source at all
		netip.MustParseAddr("2001:db8::99"),
	} {
		peer := Peer{
			Source:      source,
			SourceIface: lanIface,
			Addrs:       []netip.Addr{netip.MustParseAddr("2001:db8::1")},
		}
		if got := peer.DialAddrs(); len(got) != 0 {
			t.Fatalf("source %v: DialAddrs = %v, want none", source, got)
		}
	}
}

// Filtering by address class is not the same as binding to a link, and the
// difference matters on a multi-homed hub: it can reach a VPN that an attacker
// on the LAN cannot, so an advertisement naming a VPN address is still asking
// the hub to make a request the sender could not make itself.
func TestDialAddrsRequiresTheSameLinkAsTheSender(t *testing.T) {
	multiHomedHub(t)

	lanSender := netip.MustParseAddr("192.168.1.99")

	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"its own link", "192.168.1.20", true},
		{"the VPN it is not on", "10.42.0.10", false},
		{"somewhere else entirely", "172.16.0.5", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peer := Peer{
				Source:      lanSender,
				SourceIface: lanIface,
				Addrs:       []netip.Addr{netip.MustParseAddr(tt.addr)},
			}
			got := len(peer.DialAddrs()) == 1
			if got != tt.want {
				t.Fatalf("DialAddrs kept %s = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

// The attack the interface scoping exists for, and the one a source-only rule
// could not stop.
//
// A UDP source address is chosen by whoever sends the packet. An attacker on
// the LAN who wants the hub to reach into the VPN simply forges a source
// inside the VPN range and advertises a VPN address: both halves then sit in
// the same prefix, and a rule that compares them against "every network this
// machine is attached to" agrees they are on one link. Scoping the lookup to
// the interface the packet physically arrived on is what takes that choice
// away — the LAN interface knows nothing about 10.42.0.0/16.
func TestDialAddrsRefusesAForgedSourceFromAnotherLink(t *testing.T) {
	multiHomedHub(t)

	attacker := Peer{
		// Arrived on the LAN, claims to be on the VPN.
		SourceIface: lanIface,
		Source:      netip.MustParseAddr("10.42.0.50"),
		Addrs:       []netip.Addr{netip.MustParseAddr("10.42.0.99")},
	}
	if got := attacker.DialAddrs(); len(got) != 0 {
		t.Fatalf("DialAddrs = %v; a source forged from another link must allow nothing", got)
	}

	// The same records arriving on the VPN interface are a legitimate peer,
	// so the rule is about provenance and not about the VPN range.
	genuine := attacker
	genuine.SourceIface = vpnIface
	if got := genuine.DialAddrs(); len(got) != 1 {
		t.Fatalf("DialAddrs = %v; a real VPN peer must still be reachable", got)
	}
}

// A sender on the VPN may name VPN addresses — the rule is "the link it came
// from", not "the LAN".
func TestDialAddrsAllowsTheSendersOwnLinkWhicheverItIs(t *testing.T) {
	multiHomedHub(t)

	peer := Peer{
		Source:      netip.MustParseAddr("10.42.0.99"),
		SourceIface: vpnIface,
		Addrs:       []netip.Addr{netip.MustParseAddr("10.42.0.10")},
	}
	if got := peer.DialAddrs(); len(got) != 1 {
		t.Fatalf("DialAddrs = %v, want the sender's own link to be allowed", got)
	}
}

// Without a source the class filter is all there is, which is the in-memory
// transport's case and any transport that does not carry one.
func TestDialAddrsFallsBackToTheClassFilterWithoutASource(t *testing.T) {
	peer := Peer{Addrs: []netip.Addr{
		netip.MustParseAddr("10.42.0.10"),
		netip.MustParseAddr("127.0.0.1"),
	}}
	got := peer.DialAddrs()
	if len(got) != 1 || got[0].String() != "10.42.0.10" {
		t.Fatalf("DialAddrs = %v, want just the routable address", got)
	}
}

// A source on no network attached to the interface it arrived on allows
// nothing.
//
// This deliberately fails closed. A UDP source address is trivially spoofed by
// anyone on the same L2, so treating "off-link source" as a benign routed-relay
// case turned the same-link rule into something an attacker switches off by
// setting a field — and then names a VPN address the hub can reach and they
// cannot.
func TestDialAddrsFailsClosedOnAnUnrecognisedSource(t *testing.T) {
	multiHomedHub(t)

	// The attack: spoof an off-link source, then name the VPN.
	spoofed := Peer{
		Source:      netip.MustParseAddr("203.0.113.7"),
		SourceIface: lanIface,
		Addrs: []netip.Addr{
			netip.MustParseAddr("10.42.0.10"),
			netip.MustParseAddr("192.168.1.20"),
		},
	}
	if got := spoofed.DialAddrs(); len(got) != 0 {
		t.Fatalf("DialAddrs = %v; a spoofed off-link source must allow nothing", got)
	}
}

// A source with no interface — a transport that carries one but not the other,
// which no real socket is — allows nothing rather than everything.
func TestDialAddrsFailsClosedWithoutAnInterface(t *testing.T) {
	multiHomedHub(t)

	peer := Peer{
		Source:      netip.MustParseAddr("192.168.1.99"),
		SourceIface: 0,
		Addrs:       []netip.Addr{netip.MustParseAddr("192.168.1.20")},
	}
	if got := peer.DialAddrs(); len(got) != 0 {
		t.Fatalf("DialAddrs = %v; with no known interface nothing should be dialable", got)
	}
}

// And with no prefixes on that interface at all — one whose addresses could
// not be enumerated — nothing is dialable either, rather than everything.
func TestDialAddrsFailsClosedWithoutLocalPrefixes(t *testing.T) {
	realPrefixes := localPrefixes
	localPrefixes = func(int) []netip.Prefix { return nil }
	t.Cleanup(func() { localPrefixes = realPrefixes })

	peer := Peer{
		Source:      netip.MustParseAddr("192.168.1.99"),
		SourceIface: lanIface,
		Addrs:       []netip.Addr{netip.MustParseAddr("192.168.1.20")},
	}
	if got := peer.DialAddrs(); len(got) != 0 {
		t.Fatalf("DialAddrs = %v; with no known links nothing should be dialable", got)
	}
}

// An interface index the machine does not have resolves to no prefixes, so a
// packet claiming one allows nothing.
func TestInterfacePrefixesRefusesAnUnknownInterface(t *testing.T) {
	realLookup := netInterfaceByIndex
	t.Cleanup(func() { netInterfaceByIndex = realLookup })

	netInterfaceByIndex = func(int) (*net.Interface, error) {
		return nil, errors.New("no such interface")
	}
	if got := interfacePrefixes(9999); got != nil {
		t.Fatalf("interfacePrefixes(9999) = %v, want nil", got)
	}
}

// "No interface" is refused before the lookup, not because the lookup happens
// to fail on it.
//
// net.InterfaceByIndex(0) does error today, so the guard looks redundant —
// which is exactly why it needs pinning rather than trusting: that is
// unspecified behaviour, and 0 is the value the in-memory transport reports,
// so a platform that answered it with a real interface would turn the
// same-link rule off for every peer. The lookup is stubbed to succeed here so
// the guard is the only thing that can produce the right answer.
func TestInterfacePrefixesRefusesANonPositiveIndexWithoutAsking(t *testing.T) {
	realLookup := netInterfaceByIndex
	t.Cleanup(func() { netInterfaceByIndex = realLookup })

	asked := false
	netInterfaceByIndex = func(int) (*net.Interface, error) {
		asked = true
		return nil, nil // a lookup that does not fail, unlike the real one
	}
	for _, idx := range []int{0, -1} {
		if got := interfacePrefixes(idx); got != nil {
			t.Fatalf("interfacePrefixes(%d) = %v, want nil", idx, got)
		}
	}
	if asked {
		t.Fatal("interfacePrefixes looked up a non-positive index instead of refusing it")
	}
}

// The real reader, against a real interface. Deliberately shallow — the
// conversion is pinned in TestPrefixesFromSkipsWhatIsNotANetwork, and all
// this adds is that the interface lookup is wired to it correctly.
func TestInterfacePrefixesDescribesARealInterface(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil || len(ifaces) == 0 {
		t.Skipf("cannot enumerate interfaces here: %v", err)
	}
	var all []netip.Prefix
	for _, iface := range ifaces {
		all = append(all, interfacePrefixes(iface.Index)...)
	}
	// Shape, not values: what this returns depends on the host.
	for _, p := range all {
		if !p.IsValid() {
			t.Errorf("systemPrefixes returned an invalid prefix %v", p)
		}
		if p.Addr() != p.Masked().Addr() {
			t.Errorf("prefix %v is not masked to its network", p)
		}
	}
}

// Every branch of the conversion, against a fixed list rather than whatever
// interfaces the machine has. The old test called systemPrefixes() directly
// and asserted only shape, so which skip branches ran depended on the host —
// two runs of identical code reported different covered-statement counts.
func TestPrefixesFromSkipsWhatIsNotANetwork(t *testing.T) {
	_, v4Net, _ := net.ParseCIDR("192.168.1.20/24")
	_, v6Net, _ := net.ParseCIDR("2001:db8::1/64")

	got := prefixesFrom([]net.Addr{
		&net.IPNet{IP: net.ParseIP("192.168.1.20"), Mask: v4Net.Mask},
		&net.IPNet{IP: net.ParseIP("2001:db8::1"), Mask: v6Net.Mask},
		// Not an *net.IPNet: a point-to-point link reports one of these, and
		// it carries no mask to derive a prefix from.
		&net.IPAddr{IP: net.ParseIP("10.0.0.1")},
		// An IP of a length netip cannot interpret.
		&net.IPNet{IP: net.IP{1, 2, 3}, Mask: v4Net.Mask},
		// A v4 address with a mask wider than 32 bits: Prefix() refuses it.
		&net.IPNet{IP: net.ParseIP("192.168.1.20").To4(), Mask: v6Net.Mask},
	})

	want := []string{"192.168.1.0/24", "2001:db8::/64"}
	if len(got) != len(want) {
		t.Fatalf("prefixesFrom = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i].String() != w {
			t.Fatalf("prefixesFrom[%d] = %s, want %s", i, got[i], w)
		}
	}
}

// Nothing at all is not an error, just no prefixes.
func TestPrefixesFromToleratesAnEmptyList(t *testing.T) {
	if got := prefixesFrom(nil); len(got) != 0 {
		t.Fatalf("prefixesFrom(nil) = %v, want none", got)
	}
}
