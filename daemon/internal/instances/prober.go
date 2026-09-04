package instances

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// State is the last observed condition of one instance.
type State struct {
	InstanceID          string    `json:"instance_id"`
	Name                string    `json:"name"`
	Reachable           bool      `json:"reachable"`
	Status              string    `json:"status,omitempty"`
	Version             string    `json:"version,omitempty"`
	Role                string    `json:"role,omitempty"`
	RemoteInstanceID    string    `json:"remote_instance_id,omitempty"`
	UptimeSeconds       float64   `json:"uptime_seconds,omitempty"`
	LastSeenAt          time.Time `json:"last_seen_at,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
}

// StateStore persists observed state so the GUI still shows something useful
// immediately after a hub restart, before the first probe cycle completes.
// Narrow on purpose: the store package implements it, tests use a fake.
type StateStore interface {
	SaveInstanceState(State) error
	LoadInstanceStates() ([]State, error)
}

// EventPublisher receives up/down transitions. Only transitions are published,
// never every probe: a 30-second ticker across N instances would otherwise
// flood the SSE stream with events that say nothing changed.
type EventPublisher interface {
	InstanceStateChanged(State)
}

// Prober polls each instance's unauthenticated GET /health and tracks the
// results. It runs only on the hub.
type Prober struct {
	mu        sync.RWMutex
	registry  *Registry
	states    map[string]State
	newClient ClientFactory
	store     StateStore
	events    EventPublisher
	interval  time.Duration
	now       func() time.Time

	// selfVersion and selfRole describe this daemon. The hub does not probe
	// itself over the network, so without these its own row would be the only
	// one in the listing with no version — which reads as a bug, not a design
	// choice.
	selfVersion string
	selfRole    string
}

// NewProber builds a Prober. store and events may be nil.
func NewProber(reg *Registry, interval time.Duration, factory ClientFactory, store StateStore, events EventPublisher) *Prober {
	if factory == nil {
		factory = func(inst Instance) *Client { return NewClient(inst, nil) }
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	p := &Prober{
		registry:  reg,
		states:    make(map[string]State),
		newClient: factory,
		store:     store,
		events:    events,
		interval:  interval,
		now:       time.Now,
	}
	p.restore()
	return p
}

// restore seeds the in-memory view from the store so the API has data before
// the first tick. A failure here is not worth refusing to start over.
func (p *Prober) restore() {
	if p.store == nil {
		return
	}
	saved, err := p.store.LoadInstanceStates()
	if err != nil {
		slog.Warn("instances: could not restore instance states", "err", err)
		return
	}
	for _, s := range saved {
		p.states[s.InstanceID] = s
	}
}

// SetSelfInfo records what this daemon reports about itself, so the hub's own
// row carries the same version and role as every other.
func (p *Prober) SetSelfInfo(version, role string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.selfVersion, p.selfRole = version, role
}

// Update swaps in a new registry after a config reload and forgets the state of
// instances that were removed.
func (p *Prober) Update(reg *Registry, interval time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registry = reg
	if interval > 0 {
		p.interval = interval
	}
	for id := range p.states {
		if _, ok := reg.Get(id); !ok {
			delete(p.states, id)
		}
	}
}

// States returns the current view, in instance id order.
func (p *Prober) States() []State {
	p.mu.RLock()
	reg := p.registry
	p.mu.RUnlock()

	out := make([]State, 0, reg.Len())
	for _, inst := range reg.List() {
		out = append(out, p.State(inst.ID))
	}
	return out
}

// State returns the last observed state of one instance. An instance that has
// never been probed comes back with its id and name so callers always have
// something to render.
func (p *Prober) State(id string) State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if s, ok := p.states[id]; ok {
		return s
	}
	name := id
	if inst, ok := p.registry.Get(id); ok {
		name = inst.Name
	}
	return State{InstanceID: id, Name: name}
}

// HealthyIDs returns the instances currently believed reachable. Used to keep
// round-robin dispatch away from machines that are down.
func (p *Prober) HealthyIDs() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []string
	for _, inst := range p.registry.List() {
		if !inst.Usable() {
			continue
		}
		// An instance that has never been probed counts as healthy: refusing
		// to use it until the first tick would stall every dispatch made in
		// the first few seconds after a hub restart.
		s, probed := p.states[inst.ID]
		if !probed || s.Reachable {
			out = append(out, inst.ID)
		}
	}
	return out
}

// ConfirmedDown reports whether id has failed at least minFailures consecutive
// health probes.
//
// Deliberately narrower than !Reachable, and the distinction is the whole
// point. One failed probe means "I could not reach it", which a network
// partition makes a bad proxy for "it is not working": the instance may still
// be reaching GitHub and reviewing the repos it owns, in which case doing its
// work for it publishes a second review on the same PR
// (theburrowhub/heimdallm#765). Requiring a run of failures does not make the
// inference sound — nothing observable from one side of a partition can — but
// it keeps a single dropped packet from triggering a takeover.
//
// An instance that has never been probed is not confirmed down, matching
// HealthyIDs: refusing to trust an unprobed instance would stall every
// ownership decision for the first probe interval after a hub restart.
//
// minFailures below 1 is clamped to 1 rather than treated as "always down":
// the caller's config is validated, but a zero from a test double or a future
// caller must not silently mean "take over on the first missed probe".
func (p *Prober) ConfirmedDown(id string, minFailures int) bool {
	if minFailures < 1 {
		minFailures = 1
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, probed := p.states[id]
	if !probed || s.Reachable {
		return false
	}
	return s.ConsecutiveFailures >= minFailures
}

// ProbeAll probes every registered instance once, concurrently.
func (p *Prober) ProbeAll(ctx context.Context) []State {
	p.mu.RLock()
	reg := p.registry
	p.mu.RUnlock()

	instances := reg.List()
	var wg sync.WaitGroup
	for _, inst := range instances {
		if inst.Self {
			// The hub does not probe itself over the network: it would work,
			// but a loopback failure would report the hub as down while it is
			// demonstrably serving the very request asking the question.
			p.recordSelf(inst)
			continue
		}
		wg.Add(1)
		go func(inst Instance) {
			defer wg.Done()
			p.Probe(ctx, inst)
		}(inst)
	}
	wg.Wait()
	return p.States()
}

func (p *Prober) recordSelf(inst Instance) {
	p.mu.RLock()
	version, role := p.selfVersion, p.selfRole
	p.mu.RUnlock()
	p.record(State{
		InstanceID:       inst.ID,
		Name:             inst.Name,
		Reachable:        true,
		Status:           "ok",
		Version:          version,
		Role:             role,
		RemoteInstanceID: inst.ID,
		LastSeenAt:       p.now(),
	})
}

// Probe polls one instance and records the outcome.
func (p *Prober) Probe(ctx context.Context, inst Instance) State {
	prev := p.State(inst.ID)
	next := State{InstanceID: inst.ID, Name: inst.Name}

	health, err := p.newClient(inst).Health(ctx)
	if err != nil {
		next.Reachable = false
		next.LastError = Sanitize(err.Error())
		next.ConsecutiveFailures = prev.ConsecutiveFailures + 1
		// Keep what we knew: showing the last good version and last-seen time
		// next to "unreachable" is far more useful than blanking the row.
		next.Version = prev.Version
		next.Role = prev.Role
		next.RemoteInstanceID = prev.RemoteInstanceID
		next.LastSeenAt = prev.LastSeenAt
		p.record(next)
		return next
	}

	next.Reachable = true
	next.Status = health.Status
	next.Version = health.Version
	next.Role = health.Role
	next.RemoteInstanceID = health.InstanceID
	next.UptimeSeconds = health.UptimeSeconds
	next.LastSeenAt = p.now()
	p.record(next)
	return next
}

// record stores a state and publishes an event only when reachability flipped.
func (p *Prober) record(next State) {
	p.mu.Lock()
	prev, existed := p.states[next.InstanceID]
	p.states[next.InstanceID] = next
	p.mu.Unlock()

	if p.store != nil {
		if err := p.store.SaveInstanceState(next); err != nil {
			slog.Warn("instances: could not persist instance state",
				"instance", next.InstanceID, "err", err)
		}
	}
	if p.events != nil && (!existed || prev.Reachable != next.Reachable) {
		p.events.InstanceStateChanged(next)
	}
	if existed && prev.Reachable && !next.Reachable {
		slog.Warn("instances: instance became unreachable",
			"instance", next.InstanceID, "err", next.LastError)
	}
	if existed && !prev.Reachable && next.Reachable {
		slog.Info("instances: instance recovered",
			"instance", next.InstanceID, "version", next.Version)
	}
}

// Run probes on a ticker until ctx is cancelled. It probes once immediately so
// the GUI is not blank for a whole interval after startup.
func (p *Prober) Run(ctx context.Context) {
	p.ProbeAll(ctx)

	p.mu.RLock()
	interval := p.interval
	p.mu.RUnlock()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.ProbeAll(ctx)
			// Pick up an interval changed by a config reload without needing
			// to restart the loop.
			p.mu.RLock()
			current := p.interval
			p.mu.RUnlock()
			if current != interval {
				interval = current
				ticker.Reset(interval)
			}
		}
	}
}
