package lan

import (
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
	want := []string{"10.0.0.11", "192.168.1.20", "2001:db8::1"}
	if len(got) != len(want) {
		t.Fatalf("DialAddrs = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i].String() != w {
			t.Fatalf("DialAddrs[%d] = %s, want %s", i, got[i], w)
		}
	}
}
