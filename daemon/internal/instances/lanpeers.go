package instances

import (
	"context"
	"log/slog"
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

// NewDiscoverer builds a Discoverer. A nil browser makes every method a no-op,
// which is what a daemon with discovery switched off gets.
func NewDiscoverer(reg *Registry, browser PeerBrowser, interval time.Duration, factory ClientFactory) *Discoverer {
	if factory == nil {
		factory = func(inst Instance) *Client { return NewClient(inst, nil) }
	}
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
	// A typed nil pointer in a non-nil interface would defeat every nil check
	// in this file, so an unusable browser is normalised to a true nil.
	if b == nil {
		d.browser = nil
		return
	}
	d.browser = b
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

// Scan runs one browse-verify-classify cycle and replaces the cache.
func (d *Discoverer) Scan(ctx context.Context) []Candidate {
	d.mu.RLock()
	browser, registry := d.browser, d.registry
	d.mu.RUnlock()
	if browser == nil {
		return nil
	}

	peers, err := browser.Browse(ctx, discoveryBrowseWindow)
	if err != nil {
		slog.Debug("instances: browsing the local network failed", "err", err)
		return d.Candidates()
	}

	// Verified in parallel, like Prober.ProbeAll. Sequentially, one advertised
	// address that accepts a connection and then says nothing would hold up
	// every peer behind it, and a scan is something a stranger on the LAN can
	// trigger the cost of.
	verified := make([]Candidate, len(peers))
	ok := make([]bool, len(peers))
	var wg sync.WaitGroup
	for i, peer := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			verified[i], ok[i] = d.verify(ctx, peer)
		}()
	}
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
	return found
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

	probe := Instance{ID: "candidate", BaseURL: baseURL, Enabled: true}
	ctx, cancel := context.WithTimeout(ctx, discoveryVerifyTimeout)
	defer cancel()
	health, err := d.newClient(probe).Health(ctx)
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

// Run scans on a ticker until ctx is cancelled.
func (d *Discoverer) Run(ctx context.Context) {
	if d == nil {
		return
	}
	d.mu.RLock()
	browser := d.browser
	interval := d.interval
	d.mu.RUnlock()
	if browser == nil {
		return // discovery is off; nothing to do
	}

	// Scan immediately so the Instances tab has something to show before the
	// first tick, rather than an empty section for a minute after startup.
	d.Scan(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.Scan(ctx)
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
