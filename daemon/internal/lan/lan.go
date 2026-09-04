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
	"fmt"
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

	// Addrs are the A/AAAA records that came with the response, kept for
	// display and diagnostics only.
	Addrs []netip.Addr
}

// BaseURL renders the peer as a daemon base URL built from its hostname.
// Returns "" when the peer is too incomplete to address.
func (p Peer) BaseURL() string {
	host := strings.TrimSuffix(strings.TrimSpace(p.Hostname), ".")
	if host == "" || p.Port <= 0 {
		return ""
	}
	scheme := p.Scheme
	if scheme != "https" {
		scheme = "http"
	}
	return scheme + "://" + host + ":" + strconv.Itoa(p.Port)
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
