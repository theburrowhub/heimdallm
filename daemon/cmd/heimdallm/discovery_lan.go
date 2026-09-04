package main

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/instances"
	"github.com/heimdallm/daemon/internal/lan"
)

// discoverySignature is everything this daemon advertises about itself, plus
// whether it browses. Update rebuilds the sockets only when this changes, so an
// unrelated config reload does not tear down and re-join the multicast group.
type discoverySignature struct {
	enabled bool
	hub     bool
	id      string
	name    string
	role    string
	port    int
}

func newDiscoverySignature(cfg *config.Config, port int) discoverySignature {
	return discoverySignature{
		enabled: cfg.DiscoveryEnabled(),
		hub:     cfg.IsHub(),
		id:      cfg.Cluster.InstanceID,
		name:    resolvedSelfName(cfg),
		role:    effectiveRole(cfg),
		port:    port,
	}
}

// buildLANDiscovery prepares this daemon's mDNS advertiser and, on a hub, its
// browser. Both are nil when discovery is off, which is the default.
//
// A failure to join the multicast group is logged and swallowed rather than
// fatal. Discovery is a convenience layered on a registry that works without
// it: a daemon that cannot advertise itself still reviews pull requests, and
// refusing to start over it would turn a nice-to-have into a single point of
// failure.
func buildLANDiscovery(sig discoverySignature) (*lan.Advertiser, *lan.Browser) {
	if !sig.enabled {
		return nil, nil
	}
	warnIfDiscoveryIsContainerised()

	advertiser := buildAdvertiser(sig)

	// Only a hub browses. A worker knowing about its peers would be duplicated
	// effort and a second source of truth for the same question — the same
	// reason only a hub probes.
	var browser *lan.Browser
	if sig.hub {
		conn, err := lan.MulticastConn(nil)
		if err != nil {
			slog.Warn("cluster: mDNS discovery is on but the network could not be browsed",
				"err", err)
		} else if b, err := lan.NewBrowser(conn, slog.Default()); err != nil {
			slog.Warn("cluster: could not start the mDNS browser", "err", err)
			_ = conn.Close()
		} else {
			browser = b
		}
	}
	return advertiser, browser
}

func buildAdvertiser(sig discoverySignature) *lan.Advertiser {
	conn, err := lan.MulticastConn(nil)
	if err != nil {
		slog.Warn("cluster: mDNS discovery is on but this daemon could not be advertised",
			"err", err)
		return nil
	}
	advertiser, err := lan.NewAdvertiser(conn, lan.Advertisement{
		InstanceID:   sig.id,
		InstanceName: sig.name,
		Role:         sig.role,
		Version:      version,
		Port:         sig.port,
		Addrs:        localAddresses(),
	}, slog.Default())
	if err != nil {
		slog.Warn("cluster: could not start advertising on the local network", "err", err)
		_ = conn.Close()
		return nil
	}
	return advertiser
}

// effectiveRole is what this daemon calls itself on the wire. An empty role is
// standalone, and reporting "" would make a browsing hub show a blank column.
func effectiveRole(cfg *config.Config) string {
	role := strings.ToLower(strings.TrimSpace(cfg.Cluster.Role))
	if role == "" {
		return config.RoleStandalone
	}
	return role
}

// localAddresses lists this machine's usable unicast addresses for the A/AAAA
// records. Loopback and link-local are skipped: a peer cannot reach us on
// either, so advertising them would only add noise to the UI.
//
// These are for display and diagnostics. What a hub actually dials is the
// hostname, which is the whole point — an address goes stale, a name does not.
func localAddresses() []netip.Addr {
	ifaceAddrs, err := net.InterfaceAddrs()
	if err != nil {
		slog.Debug("cluster: could not enumerate local addresses", "err", err)
		return nil
	}
	var out []netip.Addr
	for _, a := range ifaceAddrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(ipNet.IP)
		if !ok {
			continue
		}
		addr = addr.Unmap()
		if addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() {
			continue
		}
		out = append(out, addr)
	}
	return out
}

// warnIfDiscoveryIsContainerised says so once at startup when discovery is on
// inside a container.
//
// mDNS does not cross Docker's default bridge, which is how the daemon is
// usually deployed — so the most likely outcome of switching discovery on in a
// container is that nothing whatsoever happens, with no error to explain it.
// One line at startup is cheaper than the support conversation.
//
// The check is deliberately coarse: it cannot tell host networking from bridged
// networking, so it hedges rather than claiming discovery is broken.
func warnIfDiscoveryIsContainerised() {
	if !runningInContainer() {
		return
	}
	slog.Warn("cluster: mDNS discovery is on inside a container; " +
		"it does not cross Docker's default bridge, so unless this container uses " +
		"host networking this daemon will neither see peers nor be seen. " +
		"Set HEIMDALLM_CLUSTER_DISCOVERY=off to silence this, or address instances " +
		"by hostname or a DHCP reservation instead.")
}

// runningInContainer is a best-effort guess, used only to decide whether to
// print a warning. A false positive costs one log line.
func runningInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	cgroup, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	body := string(cgroup)
	return strings.Contains(body, "docker") ||
		strings.Contains(body, "containerd") ||
		strings.Contains(body, "kubepods")
}

// RunAdvertiser answers mDNS queries until ctx is cancelled. No-op when
// discovery is off.
func (cs *clusterState) RunAdvertiser(ctx context.Context) {
	cs.mu.RLock()
	advertiser := cs.advertiser
	cs.mu.RUnlock()
	if advertiser == nil {
		return
	}
	advertiser.Run(ctx)
}

// RunDiscoverer drives the browse loop until ctx is cancelled. No-op on a
// worker, or when discovery is off.
func (cs *clusterState) RunDiscoverer(ctx context.Context) {
	cs.mu.RLock()
	discoverer := cs.discoverer
	cs.mu.RUnlock()
	if discoverer == nil {
		return
	}
	discoverer.Run(ctx)
}

// Discoverer returns the LAN discoverer, or nil when this daemon is not a hub
// or discovery is off.
func (cs *clusterState) Discoverer() *instances.Discoverer {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.discoverer
}

// closeLANDiscovery releases the multicast sockets. Called on shutdown and
// whenever a reload replaces them.
func closeLANDiscovery(advertiser *lan.Advertiser, browser *lan.Browser) {
	if advertiser != nil {
		_ = advertiser.Close()
	}
	if browser != nil {
		_ = browser.Close()
	}
}
