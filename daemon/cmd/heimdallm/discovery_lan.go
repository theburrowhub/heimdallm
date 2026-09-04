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
	"sync/atomic"
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

	// discoveryConnectionEstablished is how long a connection has to last
	// before it counts as having worked. Past this, the previous failure is
	// stale and the backoff starts over — so a laptop that discovered fine for
	// hours and then slept reconnects in seconds rather than at the ceiling.
	// Short of it, the socket never really worked and the backoff keeps
	// climbing.
	discoveryConnectionEstablished = 30 * time.Second
)

// discoverySignature is the identity this daemon would advertise, and whether
// it browses. The loops read it to decide what to build; a reload that changes
// it restarts them (see applyDiscovery).
//
// Deliberately no port. server.port is what the operator asked for, not
// necessarily what is being served: the HTTP listener is bound once at startup
// and no reload rebinds it, so a live edit from 7842 to 9000 would otherwise
// have this daemon advertising a port nothing is listening on. The address
// actually served is tracked separately, in clusterState.served.
type discoverySignature struct {
	enabled bool
	hub     bool
	id      string
	name    string
	role    string
}

func newDiscoverySignature(cfg *config.Config) discoverySignature {
	return discoverySignature{
		enabled: cfg.DiscoveryEnabled(),
		hub:     cfg.IsHub(),
		id:      cfg.Cluster.InstanceID,
		name:    resolvedSelfName(cfg),
		role:    effectiveRole(cfg),
	}
}

// SetServedAddr records where the HTTP server is actually listening. Called
// once at startup with the bound listener's own address, so discovery
// advertises what a peer can really connect to rather than what config.toml
// asked for.
func (cs *clusterState) SetServedAddr(addr net.Addr) {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.served = tcp
}

func (cs *clusterState) servedAddr() *net.TCPAddr {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.served
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

	// Nothing is advertised until we know where the server actually answers.
	// Publishing a guess would be worse than publishing nothing: a hub only
	// ever offers a peer it has already reached over HTTP, so an address the
	// server refuses does not produce an error anyone sees — the machine just
	// never appears, with nothing to explain why.
	served := cs.servedAddr()
	if served == nil {
		slog.Warn("cluster: not advertising on the local network: " +
			"the HTTP listener's address is not known yet")
		return
	}
	if !servedReachableFromLAN(served) {
		slog.Warn("cluster: not advertising on the local network: the daemon "+
			"only listens on a loopback address, so no peer could reach it. "+
			"Set server.bind_addr to a LAN address (or 0.0.0.0) to be discoverable.",
			"bind_addr", served.IP.String(), "port", served.Port)
		return
	}
	warnIfDiscoveryIsContainerised()

	runWithMulticast(ctx, "advertise on", func(conn lan.PacketConn) error {
		advertiser, err := lan.NewAdvertiser(conn, lan.Advertisement{
			InstanceID:   sig.id,
			InstanceName: sig.name,
			Role:         sig.role,
			Version:      version,
			Port:         served.Port,
			Addrs:        localAddresses,
		}, slog.Default())
		if err != nil {
			// Waiting will not make an invalid instance id valid.
			slog.Warn("cluster: could not start advertising on the local network", "err", err)
			return errNotRetryable
		}
		// Run sends its TTL-0 goodbye before returning, and runWithMulticast
		// closes the connection only after that — so the goodbye is on the
		// wire rather than racing the close.
		advertiser.Run(ctx)
		return nil
	})
}

// servedReachableFromLAN reports whether a peer could connect to this address.
// A loopback bind cannot be reached from another machine, and 0.0.0.0 / :: mean
// every interface, which is exactly what discovery wants.
func servedReachableFromLAN(addr *net.TCPAddr) bool {
	ip, ok := netip.AddrFromSlice(addr.IP)
	if !ok {
		// No IP at all is how a wildcard listener reports itself on some
		// platforms; treat it as "all interfaces" rather than refusing.
		return len(addr.IP) == 0
	}
	ip = ip.Unmap()
	if ip.IsUnspecified() {
		return true
	}
	return !ip.IsLoopback()
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
		// Cleared before runWithMulticast closes the socket, so a scan
		// triggered by the API cannot reach a browser whose connection is
		// going away.
		defer discoverer.SetBrowser(nil)
		discoverer.Run(ctx)
		return nil
	})
}

// runWithMulticast keeps a socket under use for as long as ctx lives: it dials,
// hands the connection to use, closes it when use returns, and dials again.
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
// runWithMulticast owns the connection throughout, including on the
// non-retryable path. use is never responsible for closing it, so no branch can
// return early and strand a joined multicast socket for the life of the daemon.
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

		start := time.Now()
		useErr := use(conn)
		lasted := time.Since(start)
		_ = conn.Close()

		if errors.Is(useErr, errNotRetryable) || ctx.Err() != nil {
			return
		}

		// The reset is gated on the connection having actually lasted, not on
		// the dial having succeeded. Joining the group can succeed on an
		// interface with no multicast route, in a bridged container, or on a
		// firewalled group — where the socket is unusable the moment it exists.
		// Resetting on dial alone would retry that forever at the floor and the
		// ceiling would never engage, which is precisely the case the redial
		// was added for.
		if lasted >= discoveryConnectionEstablished {
			wait = discoveryRetryMin
		}
		slog.Info("cluster: mDNS "+purpose+" the local network stopped; reconnecting",
			"lasted", lasted, "retry_in", wait)
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
var (
	containerWarnOnce      sync.Once
	containerWarnEvaluated atomic.Bool
)

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
		containerWarnEvaluated.Store(true)
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
