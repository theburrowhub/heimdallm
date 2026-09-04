package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/instances"
	"github.com/heimdallm/daemon/internal/lan"
)

// discoveryRetryMin and discoveryRetryMax bound the wait between attempts to
// join the multicast group.
//
// Retrying matters more than it looks. The commonest reason to fail is that the
// daemon started before the network did — a laptop resuming, a container
// racing its bridge, a machine booting with a slow DHCP lease. Giving up there
// would leave discovery dead for the life of the process even though the
// interface came up two seconds later, and nothing in the config changed to
// prompt another attempt.
// Variables rather than constants so tests can shorten them; nothing at
// runtime reassigns these.
var (
	discoveryRetryMin = 5 * time.Second
	discoveryRetryMax = 2 * time.Minute
)

// discoverySignature is what this daemon would advertise, and whether it
// browses. The loops read it to decide what to build; a reload that changes it
// restarts them (see applyDiscovery).
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

// discoverySignatureNow reads the current signature.
func (cs *clusterState) discoverySignatureNow() discoverySignature {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.discoverySig
}

// dialMulticastGroup is how the loops obtain a socket. A variable so tests can
// exercise the retry and backoff, which is the part of this file that carries
// the risk and cannot be reached with a real socket.
var dialMulticastGroup = func() (lan.PacketConn, error) { return lan.MulticastConn(nil) }

// errNotRetryable marks a failure that waiting cannot fix, so the loops stop
// instead of redialling forever.
var errNotRetryable = errors.New("not retryable")

// RunAdvertiser answers mDNS queries for this daemon until ctx is cancelled.
// No-op when cluster.discovery is off.
//
// The socket is owned by this loop, not by clusterState. That is deliberate:
// a socket held elsewhere has to be closed by whoever notices the config
// changed, and on this daemon that happens on the reload goroutine while this
// loop is still reading — closing a connection out from under it and losing the
// goodbye. Owning it here means the only thing that ever closes it is the loop
// that was using it, after Run has returned and its goodbye is on the wire.
func (cs *clusterState) RunAdvertiser(ctx context.Context) {
	sig := cs.discoverySignatureNow()
	if !sig.enabled {
		return
	}
	warnIfDiscoveryIsContainerised()

	runWithMulticast(ctx, "advertise on", func(conn lan.PacketConn) error {
		advertiser, err := lan.NewAdvertiser(conn, lan.Advertisement{
			InstanceID:   sig.id,
			InstanceName: sig.name,
			Role:         sig.role,
			Version:      version,
			Port:         sig.port,
			Addrs:        localAddresses,
		}, slog.Default())
		if err != nil {
			// Waiting will not make an invalid instance id valid.
			slog.Warn("cluster: could not start advertising on the local network", "err", err)
			return errNotRetryable
		}
		advertiser.Run(ctx)
		// After Run, so the TTL-0 goodbye it sends on the way out has already
		// been written rather than racing this close.
		return advertiser.Close()
	})
}

// RunDiscoverer browses for peers until ctx is cancelled. No-op on a worker, or
// when discovery is off.
//
// The Discoverer itself outlives this loop — the HTTP handler holds it, and its
// cached view should survive a poller restart — so only the socket is scoped
// here, handed over with SetBrowser and taken back on the way out.
func (cs *clusterState) RunDiscoverer(ctx context.Context) {
	discoverer := cs.Discoverer()
	if discoverer == nil {
		return
	}

	runWithMulticast(ctx, "browse", func(conn lan.PacketConn) error {
		browser, err := lan.NewBrowser(conn, slog.Default())
		if err != nil {
			slog.Warn("cluster: could not start the mDNS browser", "err", err)
			return errNotRetryable
		}
		discoverer.SetBrowser(browser)
		defer func() {
			// Cleared before the socket closes, so a scan triggered by the API
			// cannot reach a browser whose connection is going away.
			discoverer.SetBrowser(nil)
			_ = browser.Close()
		}()
		discoverer.Run(ctx)
		return nil
	})
}

// runWithMulticast keeps a socket under use for as long as ctx lives: it dials,
// hands the connection to use, and dials again when use returns.
//
// Redialling matters as much as the first attempt. A socket can fail before it
// is ever obtained — a daemon that started before its network did, which is a
// laptop resuming or a container racing its bridge — and it can equally fail
// after: suspend and resume drops multicast group membership with the
// interface, a VPN or interface flap tears it down, a container network
// restarts. Dialling once would leave discovery dead for the life of the
// process in every one of those cases, with nothing in the config changing to
// prompt another attempt — and a laptop is exactly the machine this feature
// exists for.
//
// The backoff means a permanently broken network costs one attempt every two
// minutes rather than a spin.
func runWithMulticast(ctx context.Context, purpose string, use func(lan.PacketConn) error) {
	wait := discoveryRetryMin
	for ctx.Err() == nil {
		conn, err := dialMulticastGroup()
		if err != nil {
			slog.Warn("cluster: mDNS discovery is on but this daemon could not "+
				purpose+" the local network; retrying",
				"err", err, "retry_in", wait)
			if !sleepCtx(ctx, wait) {
				return
			}
			wait = nextBackoff(wait)
			continue
		}

		// A connection that lasted is not evidence the next one will, but it
		// does mean the last failure is stale: start over from the short wait
		// so a resume after hours of sleep is picked up in seconds.
		wait = discoveryRetryMin

		if err := use(conn); errors.Is(err, errNotRetryable) {
			return
		}
		if ctx.Err() != nil {
			return
		}
		slog.Info("cluster: mDNS "+purpose+" the local network stopped; reconnecting",
			"retry_in", wait)
		if !sleepCtx(ctx, wait) {
			return
		}
		wait = nextBackoff(wait)
	}
}

// sleepCtx waits for d. Reports false when ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(wait time.Duration) time.Duration {
	if wait *= 2; wait > discoveryRetryMax {
		return discoveryRetryMax
	}
	return wait
}

// Discoverer returns the LAN discoverer, or nil when this daemon is not a hub
// or discovery is off.
func (cs *clusterState) Discoverer() *instances.Discoverer {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.discoverer
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

// localAddresses lists this machine's advertisable unicast addresses for the
// A/AAAA records. Called per response, never cached — see Advertisement.Addrs.
//
// Loopback is skipped because no peer can reach us there. IPv4 link-local
// (169.254/16) is skipped for the same reason: it means DHCP failed.
//
// IPv6 link-local (fe80::/10) is skipped for a different reason, and it is not
// that it is unreachable — on an IPv6 link it is often the only stable address
// a host has. It is skipped because it is not dialable from a DNS record: an
// fe80:: address needs a zone identifier to say which interface it belongs to,
// and there is nowhere to put one in an AAAA record. Advertising it would hand
// peers an address they cannot use. A peer reaches us by the SRV hostname
// anyway, which the resolver scopes correctly.
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

// containerWarnOnce keeps the container notice to one line per process. It is
// reachable from every advertiser start, and a daemon whose config is edited a
// few times should not repeat a paragraph it already printed.
var containerWarnOnce sync.Once

// warnIfDiscoveryIsContainerised says so once when discovery is on inside a
// container.
//
// mDNS does not cross Docker's default bridge, which is how the daemon is
// usually deployed — so the most likely outcome of switching discovery on in a
// container is that nothing whatsoever happens, with no error to explain it.
// One line at startup is cheaper than the support conversation.
//
// The check is deliberately coarse: it cannot tell host networking from bridged
// networking, so it hedges rather than claiming discovery is broken.
func warnIfDiscoveryIsContainerised() {
	containerWarnOnce.Do(func() {
		if !runningInContainer() {
			return
		}
		slog.Warn("cluster: mDNS discovery is on inside a container; " +
			"it does not cross Docker's default bridge, so unless this container uses " +
			"host networking this daemon will neither see peers nor be seen. " +
			"Set HEIMDALLM_CLUSTER_DISCOVERY=off to silence this, or address instances " +
			"by hostname or a DHCP reservation instead.")
	})
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

// containerWarnOnceFired reports whether the container notice has been
// considered. Test seam: sync.Once exposes no state of its own.
func containerWarnOnceFired() bool {
	fired := true
	containerWarnOnce.Do(func() { fired = false })
	return fired
}
