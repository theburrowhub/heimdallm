// Package lan discovers Heimdallm daemons on the local network over
// mDNS / DNS-SD (Bonjour, Avahi, zeroconf).
//
// It is deliberately ignorant of the cluster: it knows how to announce "a
// daemon lives here" and how to ask "which daemons live here", and nothing
// about the registry, routing or whether any of the answers can be trusted.
// Everything a Peer carries is a claim made by whoever answered the query —
// anything on the LAN can advertise this service type, so a Peer is a lead to
// follow up, never a fact. instances.Discoverer is what verifies it.
//
// The package is named lan rather than discovery because internal/discovery is
// already GitHub repository discovery, and because "peers on this local
// network" is what this is.
//
// # Scope
//
// mDNS is link-local. This finds daemons on the same subnet and nothing
// further: no routed networks, no other sites, and not across Docker's default
// bridge. Clusters that span more than one link still need DNS, a VPN or
// Tailscale.
package lan

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Service is the DNS-SD service type. The instance's own record is published at
// "<instance>.<Service>.<Domain>." — for example
// "mac-sergio._heimdallm._tcp.local.".
const (
	Service = "_heimdallm._tcp"
	Domain  = "local"
)

// TXT keys. Short on purpose: a DNS-SD TXT record is a handful of key=value
// strings sharing one UDP packet with the SRV and A records, and RFC 6763 §6.4
// asks implementations to keep the whole response inside a single MTU.
const (
	txtKeyID      = "id"
	txtKeyName    = "name"
	txtKeyRole    = "role"
	txtKeyVersion = "ver"
	txtKeyScheme  = "scheme"
)

// txtValueMax bounds one TXT value. A single DNS character-string cannot exceed
// 255 bytes including the "key=" prefix; capping the value well below that
// keeps any one field from crowding out the others.
const txtValueMax = 128

// Peer is one daemon seen on the network.
//
// Every field is unverified. A rogue advertiser can put any id, name or role it
// likes in a TXT record — that is the nature of an unauthenticated protocol —
// so a Peer is only ever a proposal. The consumer is expected to reach the
// daemon over HTTP and let it identify itself before believing any of this.
type Peer struct {
	InstanceID   string
	InstanceName string
	Role         string
	Version      string

	// Scheme is http unless the advertiser says otherwise.
	Scheme string

	// Hostname is the SRV target with the trailing dot stripped, e.g.
	// "mac-sergio.local". This — not Addrs — is what a base URL should be
	// built from: a name is re-resolved on every dial, which is the entire
	// point of discovering an instance rather than pinning its address.
	Hostname string
	Port     int

	// Addrs are the A/AAAA records that came with the response.
	Addrs []netip.Addr

	// Source is the address the response actually arrived from, when the
	// transport carries one. It is the only field an advertiser cannot forge,
	// which is what makes it the basis for the same-link check in DialAddrs.
	Source netip.Addr
}

// BaseURL renders the peer as a daemon base URL built from its hostname.
// Returns "" when the peer is not addressable, which includes any hostname that
// fails ValidateMDNSHostname.
func (p Peer) BaseURL() string {
	host, err := ValidateMDNSHostname(p.Hostname)
	if err != nil || p.Port <= 0 || p.Port > 65535 {
		return ""
	}
	scheme := p.Scheme
	if scheme != "https" {
		scheme = "http"
	}
	return scheme + "://" + host + ":" + strconv.Itoa(p.Port)
}

// ValidateMDNSHostname accepts only a single-label name in .local, and returns
// it without the trailing dot.
//
// This is a security boundary, not tidiness. An SRV target is supplied by
// whoever answered a multicast query — anyone on the link, with no
// authentication — and the consumer turns it into a URL and fetches it. Left
// unchecked, an attacker advertises `metadata.google.internal.` or any internal
// hostname and the hub makes the request for them: a server-side request
// forgery primitive handed out to the whole subnet.
//
// Restricting it to <label>.local is what makes the name harmless. That is the
// only shape mDNS actually assigns (RFC 6762 §3 gives a host one single-label
// name in the .local domain), it cannot name anything off-link, and it is
// resolved by the mDNS resolver rather than by unicast DNS — so it cannot be
// pointed at a public record or an internal one. A multi-label name is rejected
// too: "metadata.google.internal.local" is not a host mDNS can assign, and
// allowing it would let an attacker smuggle a delegated suffix past the check.
func ValidateMDNSHostname(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if host == "" {
		return "", errors.New("lan: peer advertised no hostname")
	}
	label, ok := strings.CutSuffix(host, "."+Domain)
	if !ok {
		return "", fmt.Errorf("lan: peer hostname %q is not in .%s; refusing to "+
			"probe a name mDNS cannot have assigned", raw, Domain)
	}
	if !isDNSLabel(label) {
		return "", fmt.Errorf("lan: peer hostname %q is not a single .%s label", raw, Domain)
	}
	return host, nil
}

// isDNSLabel reports whether s is one legal DNS label: 1-63 characters of
// letters, digits and hyphens, not starting or ending with a hyphen. A dot
// anywhere fails, which is the point.
func isDNSLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

// DialAddrs returns the advertised addresses that are safe to connect to.
//
// A peer must be reached by address rather than by resolving its name, and that
// is a security requirement rather than an optimisation. Restricting a hostname
// to `<label>.local` constrains what it is *called*, not what it *resolves to*:
// mDNS resolution is itself unauthenticated, so anyone on the link can answer
// the resolver's query for `peer.local` with any address they like — including
// one only the hub can reach. `169.254.169.254` is the obvious prize, since a
// cloud metadata endpoint is reachable from the hub and from nowhere else, so
// having the hub fetch it is a real escalation rather than something the
// attacker could have done directly.
//
// Filtering the advertised addresses instead removes the attacker's choice: the
// hub only ever connects to a routable unicast address that was published in
// the packet, which is an address the sender could have reached itself.
//
// Rejected, and why:
//   - loopback: names the hub's own services, not the peer's
//   - link-local (169.254/16, fe80::/10): the metadata range lives here, and a
//     link-local address is not dialable from a record anyway
//   - multicast, unspecified: not a host
func (p Peer) DialAddrs() []netip.Addr {
	out := make([]netip.Addr, 0, len(p.Addrs))
	for _, addr := range p.Addrs {
		if !addr.IsValid() {
			continue
		}
		addr = addr.Unmap()
		switch {
		case addr.IsLoopback(),
			addr.IsLinkLocalUnicast(),
			addr.IsLinkLocalMulticast(),
			addr.IsMulticast(),
			addr.IsUnspecified(),
			addr.IsInterfaceLocalMulticast():
			continue
		}
		// Same link as whoever sent the advertisement.
		//
		// The class filter above is not enough on a multi-homed host. A hub on
		// both a LAN and a VPN can reach the VPN; an attacker on the LAN
		// cannot — so an advertisement naming a VPN address would still be
		// asking the hub to make a request the sender could not make itself,
		// which is the definition of the problem. Requiring the address to
		// share a local prefix with the packet's own source keeps the reach to
		// the link the advertisement came from.
		if p.Source.IsValid() && !sameLink(p.Source, addr) {
			continue
		}
		out = append(out, addr)
	}
	return out
}

// localPrefixes is a variable so tests can describe a multi-homed host without
// needing one.
var localPrefixes = systemPrefixes

// systemPrefixes returns the networks this machine is directly attached to.
func systemPrefixes() []netip.Prefix {
	ifaceAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	out := make([]netip.Prefix, 0, len(ifaceAddrs))
	for _, a := range ifaceAddrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(ipNet.IP)
		if !ok {
			continue
		}
		ones, _ := ipNet.Mask.Size()
		prefix, err := addr.Unmap().Prefix(ones)
		if err != nil {
			continue
		}
		out = append(out, prefix)
	}
	return out
}

// sameLink reports whether source and candidate fall inside the same locally
// attached network.
//
// A host with no matching prefix at all is allowed through: that is a routed
// mDNS relay or an unusual topology, not an attack shape, and refusing it
// would break a setup that works today for no security gain — the check exists
// to stop an advertiser naming a network it is not on, and if we cannot tell
// which network anything is on there is nothing to enforce.
func sameLink(source, candidate netip.Addr) bool {
	matched := false
	for _, prefix := range localPrefixes() {
		if !prefix.Contains(source) {
			continue
		}
		matched = true
		if prefix.Contains(candidate) {
			return true
		}
	}
	return !matched
}

// encodeTXT renders a peer's identity as DNS-SD TXT strings, in a stable order
// so the same peer always produces byte-identical records.
func encodeTXT(p Peer) []string {
	pairs := [][2]string{
		{txtKeyID, p.InstanceID},
		{txtKeyName, p.InstanceName},
		{txtKeyRole, p.Role},
		{txtKeyVersion, p.Version},
		{txtKeyScheme, p.Scheme},
	}
	out := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		v := sanitizeTXTValue(kv[1])
		if v == "" {
			continue
		}
		out = append(out, kv[0]+"="+v)
	}
	return out
}

// decodeTXT reads the strings back. Unknown keys are ignored rather than
// rejected: a future version adding a field must not make its peers invisible
// to an older one.
func decodeTXT(txt []string) Peer {
	var p Peer
	for _, entry := range txt {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		value = sanitizeTXTValue(value)
		switch strings.ToLower(key) {
		case txtKeyID:
			p.InstanceID = value
		case txtKeyName:
			p.InstanceName = value
		case txtKeyRole:
			p.Role = value
		case txtKeyVersion:
			p.Version = value
		case txtKeyScheme:
			p.Scheme = strings.ToLower(value)
		}
	}
	return p
}

// sanitizeTXTValue drops anything that has no business in a TXT record and
// that a UI would otherwise have to defend against: control characters,
// invalid UTF-8, and runaway lengths. A value that survives is safe to render.
func sanitizeTXTValue(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !utf8.ValidString(raw) {
		return ""
	}
	var b strings.Builder
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > txtValueMax {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// serviceFQDN is the browse target, "_heimdallm._tcp.local.".
func serviceFQDN() string {
	return Service + "." + Domain + "."
}

// instanceFQDN is one daemon's record name within the service.
func instanceFQDN(instance string) string {
	return escapeLabel(instance) + "." + serviceFQDN()
}

// escapeLabel makes an arbitrary instance name safe as a single DNS label.
// DNS-SD instance names are allowed to be free-form UTF-8, but a literal dot
// would split the label and silently reparent the record under a different
// service, so dots and backslashes are escaped the way the DNS presentation
// format expects.
func escapeLabel(name string) string {
	name = sanitizeTXTValue(name)
	if name == "" {
		return "heimdallm"
	}
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '.', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// validatePort rejects a port that cannot be advertised, so a misconfiguration
// fails at startup rather than producing an SRV record nobody can dial.
func validatePort(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("lan: port %d is out of range", port)
	}
	return nil
}

// sortPeers orders peers by instance id, so consecutive browses of an unchanged
// network produce an identical slice and a caller can diff them meaningfully.
func sortPeers(peers []Peer) {
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].InstanceID < peers[j].InstanceID
	})
}
