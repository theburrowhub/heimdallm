package instances

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/heimdallm/daemon/internal/lan"
)

// Candidate statuses. These describe what the operator is being offered, not
// what the daemon has done: discovery never registers anything and never
// rewrites an address.
const (
	// StatusNew is a verified daemon nobody has registered yet.
	StatusNew = "new"
	// StatusRegistered is one already in the registry at the same address.
	// Reported so the UI can say "we can see it" rather than implying that
	// everything it found is something to act on.
	StatusRegistered = "registered"
	// StatusAddressChanged is a registered instance answering somewhere else.
	// This is the #765 case: its base_url has gone stale, the hub has it down,
	// and its peers are taking over repositories it is still reviewing.
	StatusAddressChanged = "address_changed"
)

// DefaultDiscoveryInterval is how often the hub re-browses the network.
//
// Slower than the health probe on purpose. A probe answers "is a known
// instance up", which the UI shows continuously; a browse answers "has a new
// machine appeared", which nobody is watching second by second. The cache
// exists so opening the Instances tab does not wait on a browse window.
const DefaultDiscoveryInterval = 60 * time.Second

// discoveryBrowseWindow is how long one browse listens for answers. mDNS has no
// concept of a complete response — only of who replied before we stopped
// listening — so this trades latency for the chance of hearing a slow peer.
const discoveryBrowseWindow = 2 * time.Second

// discoveryVerifyTimeout bounds one peer's /health check.
//
// Much shorter than the control-plane default, because the caller and the
// subject are on the same link and because the input is untrusted: anything on
// the LAN can advertise an address that swallows connections, and a scan must
// not be something a stranger can stall. A daemon one hop away that cannot
// answer in this long is not one to propose adopting.
const discoveryVerifyTimeout = 3 * time.Second

// discoveryVerifyWorkers bounds how many peers are probed at once.
//
// Fanning out one goroutine per peer looks harmless at cluster scale and is not
// safe at LAN scale: the peer list comes from a multicast group anyone on the
// link can write to, so "one request per advertised name" is a knob a stranger
// turns. A fixed pool means a flooded browse costs a bounded number of sockets
// and takes longer, instead of costing every file descriptor the daemon has.
const discoveryVerifyWorkers = 8

// discoveryHTTPTimeout bounds the whole verification client. Slightly above
// discoveryVerifyTimeout so the per-peer context is what normally expires.
const discoveryHTTPTimeout = 5 * time.Second

// newDiscoveryHTTPClient builds the client used to verify one discovered peer.
//
// Two things are locked down here, and both matter because every input came
// from an unauthenticated advertisement.
//
// Redirects are refused outright. Following one would let whoever sent the
// advertisement choose a second URL that never had to pass any of these checks.
//
// More importantly the destination is pinned: DialContext ignores the hostname
// entirely and connects to one of the addresses the peer published, filtered by
// Peer.DialAddrs. Resolving the name instead would hand the choice of
// destination straight back to the attacker — mDNS resolution is unauthenticated,
// so anyone on the link can answer a query for "peer.local" with any address,
// and a link-local metadata endpoint is reachable from the hub and from nowhere
// else. Restricting the hostname to <label>.local constrains what a peer is
// called; only pinning the dial constrains where the request goes.
func newDiscoveryHTTPClient(addrs []netip.Addr, port int) *http.Client {
	var dialer net.Dialer
	return &http.Client{
		Timeout: discoveryHTTPTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("instances: refusing to follow a redirect from a "+
				"discovered peer (to %s)", req.URL.Redacted())
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				if len(addrs) == 0 {
					return nil, errors.New("instances: the peer advertised no usable address")
				}
				// Each address gets its own slice of the budget. Without that,
				// an address that swallows connections rather than refusing
				// them — trivial for an advertiser to publish — would consume
				// the whole verification timeout and the peer's working
				// address would never be tried.
				share := perAddressDialTimeout(ctx, len(addrs))
				var lastErr error
				for _, addr := range addrs {
					attemptCtx, cancel := context.WithTimeout(ctx, share)
					target := net.JoinHostPort(addr.String(), strconv.Itoa(port))
					conn, err := dialer.DialContext(attemptCtx, network, target)
					cancel()
					if err == nil {
						return conn, nil
					}
					lastErr = err
					if ctx.Err() != nil {
						break
					}
				}
				return nil, lastErr
			},
		},
	}
}

// Candidate is one daemon seen on the network, after verification, joined with
// what the registry already knows about it.
type Candidate struct {
	InstanceID   string    `json:"instance_id"`
	InstanceName string    `json:"name"`
	Role         string    `json:"role"`
	Version      string    `json:"version"`
	BaseURL      string    `json:"base_url"`
	Hostname     string    `json:"hostname"`
	Addresses    []string  `json:"addresses"`
	Status       string    `json:"status"`
	SeenAt       time.Time `json:"seen_at"`

	// RegisteredID and RegisteredBaseURL are set when this peer matches an
	// entry in the registry, so the UI can point at the row it affects.
	RegisteredID      string `json:"registered_id,omitempty"`
	RegisteredBaseURL string `json:"registered_base_url,omitempty"`
}

// PeerBrowser is the discovery transport. Narrow so a test can answer with a
// fixed set of peers and never touch a socket.
type PeerBrowser interface {
	Browse(ctx context.Context, window time.Duration) ([]lan.Peer, error)
}

// Discoverer browses the local network for daemons, verifies each one over
// HTTP, and classifies the survivors against the registry. It runs only on the
// hub, and it only ever proposes.
//
// Deliberately shaped like Prober: same lock discipline, same Update-in-place
// on reload, same Run loop. Two things in this package that do the same kind of
// job should not need to be learned twice.
type Discoverer struct {
	mu         sync.RWMutex
	registry   *Registry
	browser    PeerBrowser
	newClient  ClientFactory
	interval   time.Duration
	now        func() time.Time
	candidates []Candidate
	lastScan   time.Time
}

// perAddressDialTimeout splits whatever budget is left between the addresses
// still to try, with a floor so a tight deadline does not make every attempt
// fail instantly.
func perAddressDialTimeout(ctx context.Context, n int) time.Duration {
	const floor = 500 * time.Millisecond
	deadline, ok := ctx.Deadline()
	if !ok || n <= 0 {
		return discoveryVerifyTimeout
	}
	share := time.Until(deadline) / time.Duration(n)
	if share < floor {
		return floor
	}
	return share
}

// NewDiscoverer builds a Discoverer. A nil browser makes every method a no-op,
// which is what a daemon with discovery switched off gets.
//
// A nil factory is the production path: verification then goes through a
// per-peer client built by newDiscoveryHTTPClient, which pins the destination to
// the peer's own advertised addresses. Tests pass a factory to reach an
// httptest server instead.
func NewDiscoverer(reg *Registry, browser PeerBrowser, interval time.Duration, factory ClientFactory) *Discoverer {
	if interval <= 0 {
		interval = DefaultDiscoveryInterval
	}
	return &Discoverer{
		registry:  reg,
		browser:   browser,
		newClient: factory,
		interval:  interval,
		now:       time.Now,
	}
}

// Update swaps in a new registry after a config reload. The cached candidates
// are reclassified rather than discarded: an instance registered a moment ago
// should stop being offered immediately, without waiting for the next browse.
func (d *Discoverer) Update(reg *Registry, interval time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.registry = reg
	if interval > 0 {
		d.interval = interval
	}
	for i := range d.candidates {
		d.classify(reg, &d.candidates[i])
	}
}

// SetBrowser swaps the transport, for when a config reload replaces the
// multicast socket. A nil browser switches discovery off without discarding
// the object the HTTP handler already holds.
func (d *Discoverer) SetBrowser(b PeerBrowser) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Normalised to a true nil, and reflect is needed to do it: an interface
	// holding a nil pointer is itself non-nil, so `b == nil` would let one
	// through and every later nil check in this file would pass on something
	// that panics when called.
	if b == nil || isNilPointer(b) {
		d.browser = nil
		return
	}
	d.browser = b
}

// isNilPointer reports whether v is an interface wrapping a nil pointer.
func isNilPointer(v any) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}

// Candidates returns the current view.
func (d *Discoverer) Candidates() []Candidate {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Candidate, len(d.candidates))
	copy(out, d.candidates)
	return out
}

// LastScan reports when the view was last refreshed. Zero means never.
func (d *Discoverer) LastScan() time.Time {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastScan
}

// Scan runs one browse-verify-classify cycle and replaces the cache. A failed
// browse keeps the previous view rather than blanking the list the operator is
// looking at.
func (d *Discoverer) Scan(ctx context.Context) []Candidate {
	_ = d.scan(ctx)
	return d.Candidates()
}

// scan is Scan with the browse error kept, so Run can tell a socket that needs
// replacing from a network with nothing on it.
func (d *Discoverer) scan(ctx context.Context) error {
	d.mu.RLock()
	browser, registry := d.browser, d.registry
	d.mu.RUnlock()
	if browser == nil {
		return nil
	}

	peers, err := browser.Browse(ctx, discoveryBrowseWindow)
	if err != nil {
		slog.Debug("instances: browsing the local network failed", "err", err)
		return err
	}

	// Verified in parallel like Prober.ProbeAll, but through a fixed pool
	// rather than a goroutine per peer. Sequentially, one advertised address
	// that accepts a connection and then says nothing would hold up every peer
	// behind it; unbounded, the number of concurrent outbound requests is a
	// number anyone on the link chooses by advertising more names.
	verified := make([]Candidate, len(peers))
	ok := make([]bool, len(peers))
	work := make(chan int)
	var wg sync.WaitGroup
	for range min(discoveryVerifyWorkers, len(peers)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				verified[i], ok[i] = d.verify(ctx, peers[i])
			}
		}()
	}
	for i := range peers {
		select {
		case work <- i:
		case <-ctx.Done():
		}
	}
	close(work)
	wg.Wait()

	seen := make(map[string]bool, len(peers))
	found := make([]Candidate, 0, len(peers))
	for i := range verified {
		if !ok[i] {
			continue
		}
		candidate := verified[i]
		// The hub hears its own advertisement: the advertiser and the browser
		// share the multicast group, so every response reaches both. Dropping
		// it is not just tidiness. The hub's self entry is deliberately
		// loopback (nothing ever dials it — the proxy short-circuits its own
		// id and serves locally), so classifying it against the registry would
		// call it address_changed and invite the operator to "correct" a
		// working entry into one that only resolves from the hub itself.
		if registry != nil && candidate.InstanceID == registry.SelfID() {
			continue
		}
		// Two records claiming the same identity: keep the first. Which one
		// wins matters less than not offering the operator the same instance
		// twice with different addresses.
		if seen[candidate.InstanceID] {
			continue
		}
		seen[candidate.InstanceID] = true
		d.classify(registry, &candidate)
		found = append(found, candidate)
	}

	d.mu.Lock()
	d.candidates = found
	d.lastScan = d.now()
	d.mu.Unlock()
	return nil
}

// verify asks the peer to identify itself over HTTP.
//
// This is what keeps the feature safe. mDNS is unauthenticated, so a TXT record
// is only a claim: anything on the LAN can announce _heimdallm._tcp with any id
// it likes. The identity that ends up in a Candidate therefore comes from the
// daemon's own /health response, never from the advertisement — the TXT values
// are discarded the moment the peer answers. Something that does not answer, or
// answers without naming itself, is not offered at all.
//
// /health is unauthenticated (Client.Health sends no token), which is what
// makes this possible before the operator holds a token for the peer. It is
// the same probe handleRegisterInstance already runs before writing an entry.
func (d *Discoverer) verify(ctx context.Context, peer lan.Peer) (Candidate, bool) {
	baseURL := peer.BaseURL()
	if baseURL == "" {
		return Candidate{}, false
	}

	// Pinned to what the peer published, never to what its name resolves to.
	// A peer with no routable advertised address is not probed at all: the
	// alternative is resolving the hostname, which is exactly the choice this
	// is taking away from whoever sent the advertisement.
	dialAddrs := peer.DialAddrs()
	if len(dialAddrs) == 0 {
		slog.Debug("instances: a peer on the network advertised no routable address",
			"base_url", baseURL)
		return Candidate{}, false
	}

	probe := Instance{ID: "candidate", BaseURL: baseURL, Enabled: true}
	ctx, cancel := context.WithTimeout(ctx, discoveryVerifyTimeout)
	defer cancel()
	health, err := d.verifyClient(probe, dialAddrs, peer.Port).Health(ctx)
	if err != nil {
		slog.Debug("instances: a peer on the network did not answer /health",
			"base_url", baseURL, "err", err)
		return Candidate{}, false
	}
	// An empty id means whatever is listening is not a daemon that can be
	// registered — a different service, or one too old to identify itself.
	// Either way there is nothing to offer.
	if strings.TrimSpace(health.InstanceID) == "" {
		return Candidate{}, false
	}

	name := health.InstanceName
	if strings.TrimSpace(name) == "" {
		name = health.InstanceID
	}

	addrs := make([]string, 0, len(peer.Addrs))
	for _, addr := range peer.Addrs {
		addrs = append(addrs, addr.String())
	}

	return Candidate{
		InstanceID:   health.InstanceID,
		InstanceName: name,
		Role:         health.Role,
		Version:      health.Version,
		BaseURL:      baseURL,
		Hostname:     strings.TrimSuffix(peer.Hostname, "."),
		Addresses:    addrs,
		SeenAt:       d.now(),
	}, true
}

// verifyClient builds the client used to probe one candidate, pinned to that
// peer's advertised addresses unless a factory was injected.
func (d *Discoverer) verifyClient(probe Instance, addrs []netip.Addr, port int) *Client {
	d.mu.RLock()
	factory := d.newClient
	d.mu.RUnlock()
	if factory != nil {
		return factory(probe)
	}
	return NewClient(probe, newDiscoveryHTTPClient(addrs, port))
}

// classify decides what the operator is being offered. Pure: registry in,
// status out.
func (d *Discoverer) classify(reg *Registry, c *Candidate) {
	c.RegisteredID = ""
	c.RegisteredBaseURL = ""

	if reg == nil {
		c.Status = StatusNew
		return
	}
	registered, ok := reg.Get(c.InstanceID)
	if !ok {
		c.Status = StatusNew
		return
	}

	c.RegisteredID = registered.ID
	c.RegisteredBaseURL = registered.BaseURL
	if sameBaseURL(registered.BaseURL, c.BaseURL) {
		c.Status = StatusRegistered
		return
	}
	c.Status = StatusAddressChanged
}

// sameBaseURL compares two base URLs the way the registry stores them: the
// registry already strips a trailing slash, so only case and stray whitespace
// are left to normalise. A host is case-insensitive in DNS, and an operator who
// typed "SRV-A.local" should not be told the address changed.
func sameBaseURL(a, b string) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/"))
	}
	return norm(a) == norm(b)
}

// Run scans on a ticker until ctx is cancelled, or until the browser's socket
// stops working.
//
// Returns an error in the latter case so the caller replaces the connection.
// A browse that fails because nothing answered is not an error — that is an
// empty network, which is a normal state.
func (d *Discoverer) Run(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	browser := d.browser
	interval := d.interval
	d.mu.RUnlock()
	if browser == nil {
		return nil // discovery is off; nothing to do
	}

	// Scan immediately so the Instances tab has something to show before the
	// first tick, rather than an empty section for a minute after startup.
	if err := d.scan(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := d.scan(ctx); err != nil {
				return err
			}
			d.mu.RLock()
			current := d.interval
			d.mu.RUnlock()
			if current != interval {
				interval = current
				ticker.Reset(interval)
			}
		}
	}
}
