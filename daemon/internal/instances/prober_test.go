package instances

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
)

// fakeStateStore records what the prober persisted.
type fakeStateStore struct {
	mu      sync.Mutex
	saved   []State
	seed    []State
	loadErr error
	saveErr error
}

func (f *fakeStateStore) SaveInstanceState(s State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, s)
	return nil
}

func (f *fakeStateStore) LoadInstanceStates() ([]State, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.seed, nil
}

func (f *fakeStateStore) savedFor(id string) []State {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []State
	for _, s := range f.saved {
		if s.InstanceID == id {
			out = append(out, s)
		}
	}
	return out
}

// fakeEvents records reachability transitions.
type fakeEvents struct {
	mu     sync.Mutex
	events []State
}

func (f *fakeEvents) InstanceStateChanged(s State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, s)
}

func (f *fakeEvents) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

// toggleDaemon flips between healthy and failing on demand.
type toggleDaemon struct {
	*fakeDaemon
	mu   sync.Mutex
	down bool
}

func newToggleDaemon(t *testing.T, id string) *toggleDaemon {
	td := &toggleDaemon{}
	td.fakeDaemon = newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		td.mu.Lock()
		down := td.down
		td.mu.Unlock()
		if down {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "version": "0.9.0", "instance_id": id, "role": "worker",
			"uptime_seconds": 10.0,
		})
	})
	return td
}

func (td *toggleDaemon) setDown(down bool) {
	td.mu.Lock()
	td.down = down
	td.mu.Unlock()
}

// degradedDaemon answers /health with 503 and a valid body, the way a real
// daemon does when its last_poll check lags — as opposed to toggleDaemon's
// "down", which is a hard 500 with no body.
type degradedDaemon struct {
	*fakeDaemon
}

func newDegradedDaemon(t *testing.T, id string) *degradedDaemon {
	dd := &degradedDaemon{}
	dd.fakeDaemon = newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "degraded", "version": "0.9.0", "instance_id": id, "role": "hub",
		})
	})
	return dd
}

func proberFixture(t *testing.T, selfID string, daemons map[string]*toggleDaemon, store StateStore, events EventPublisher) *Prober {
	t.Helper()
	insts := map[string]config.InstanceConfig{
		selfID: {Name: selfID, BaseURL: "http://127.0.0.1:7842", Token: "t"},
	}
	for id, d := range daemons {
		insts[id] = config.InstanceConfig{Name: id, BaseURL: d.URL, Token: "secret"}
	}
	cfg := cfgWith(config.RoleHub, selfID, selfID, insts, config.RoutingConfig{})
	reg := NewRegistry(cfg)
	return NewProber(reg, time.Minute, func(inst Instance) *Client {
		if d, ok := daemons[inst.ID]; ok {
			return NewClient(inst, d.Client())
		}
		return NewClient(inst, nil)
	}, store, events)
}

func TestProberRecordsHealthyInstance(t *testing.T) {
	d := newToggleDaemon(t, "srv-a")
	store := &fakeStateStore{}
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"srv-a": d}, store, nil)

	p.ProbeAll(context.Background())

	s := p.State("srv-a")
	if !s.Reachable {
		t.Errorf("srv-a = %+v, want reachable", s)
	}
	if s.Version != "0.9.0" || s.Role != "worker" || s.RemoteInstanceID != "srv-a" {
		t.Errorf("srv-a = %+v, want the remote's reported identity", s)
	}
	if s.LastSeenAt.IsZero() {
		t.Error("LastSeenAt not set on a successful probe")
	}
	if len(store.savedFor("srv-a")) == 0 {
		t.Error("state was not persisted")
	}
}

// The hub does not probe itself over the network: a loopback hiccup would
// report the hub as down while it is demonstrably serving the request that asks.
func TestProberDoesNotProbeSelfOverHTTP(t *testing.T) {
	d := newToggleDaemon(t, "srv-a")
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"srv-a": d}, nil, nil)

	p.ProbeAll(context.Background())

	self := p.State("hub")
	if !self.Reachable || self.Status != "ok" {
		t.Errorf("hub self state = %+v, want reachable/ok without an HTTP call", self)
	}
}

func TestProberCountsConsecutiveFailuresAndKeepsLastKnown(t *testing.T) {
	d := newToggleDaemon(t, "srv-a")
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"srv-a": d}, nil, nil)
	ctx := context.Background()

	p.ProbeAll(ctx)
	firstSeen := p.State("srv-a").LastSeenAt
	if firstSeen.IsZero() {
		t.Fatal("precondition: expected a successful first probe")
	}

	d.setDown(true)
	p.ProbeAll(ctx)
	p.ProbeAll(ctx)

	s := p.State("srv-a")
	if s.Reachable {
		t.Error("srv-a still reachable after two failed probes")
	}
	if s.ConsecutiveFailures != 2 {
		t.Errorf("ConsecutiveFailures = %d, want 2", s.ConsecutiveFailures)
	}
	if s.LastError == "" {
		t.Error("LastError is empty on a failed probe")
	}
	// Showing the last good version and last-seen time next to "unreachable"
	// is far more useful than blanking the row.
	if s.Version != "0.9.0" {
		t.Errorf("Version = %q, want the last known value retained", s.Version)
	}
	if !s.LastSeenAt.Equal(firstSeen) {
		t.Errorf("LastSeenAt = %v, want the last successful probe time %v", s.LastSeenAt, firstSeen)
	}

	// Recovery resets the counter.
	d.setDown(false)
	p.ProbeAll(ctx)
	if got := p.State("srv-a"); !got.Reachable || got.ConsecutiveFailures != 0 {
		t.Errorf("after recovery = %+v, want reachable with the counter reset", got)
	}
}

// Only transitions are published. A 30s ticker across N instances would
// otherwise flood the SSE stream with events saying nothing changed.
// A degraded-but-responding instance (503 with a valid body, e.g. its own
// last_poll check lagging) must not be flagged unreachable, must not fire
// instance_down, and must not log "became unreachable" — that flapping is
// exactly what made the hub's own row in the Instances tab look like it kept
// disconnecting while it was demonstrably serving the very health check.
func TestProberDoesNotFlagDegradedInstanceUnreachable(t *testing.T) {
	d := newDegradedDaemon(t, "srv-a")
	insts := map[string]config.InstanceConfig{
		"hub":   {Name: "hub", BaseURL: "http://127.0.0.1:7842", Token: "t"},
		"srv-a": {Name: "srv-a", BaseURL: d.URL, Token: "secret"},
	}
	cfg := cfgWith(config.RoleHub, "hub", "hub", insts, config.RoutingConfig{})
	reg := NewRegistry(cfg)
	events := &fakeEvents{}
	p := NewProber(reg, time.Minute, func(inst Instance) *Client {
		if inst.ID == "srv-a" {
			return NewClient(inst, d.Client())
		}
		return NewClient(inst, nil)
	}, nil, events)

	p.ProbeAll(context.Background())
	p.ProbeAll(context.Background())

	s := p.State("srv-a")
	if !s.Reachable {
		t.Errorf("srv-a = %+v, want reachable despite the 503", s)
	}
	if s.Status != "degraded" {
		t.Errorf("Status = %q, want %q", s.Status, "degraded")
	}
	// First ProbeAll publishes one event per instance's first sighting (self +
	// srv-a = 2, matching TestProberPublishesOnlyTransitions). The second
	// ProbeAll, with srv-a steadily degraded, must publish nothing more —
	// never an unreachable/recovered flap in between.
	if got := events.count(); got != 2 {
		t.Errorf("published %d events across two degraded probes, want exactly 2 (first observation of each instance, no flapping after)", got)
	}
}

func TestProberPublishesOnlyTransitions(t *testing.T) {
	d := newToggleDaemon(t, "srv-a")
	events := &fakeEvents{}
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"srv-a": d}, nil, events)
	ctx := context.Background()

	p.ProbeAll(ctx) // first observation of hub + srv-a => 2 events
	initial := events.count()
	if initial != 2 {
		t.Fatalf("first cycle published %d events, want 2 (one per instance)", initial)
	}

	p.ProbeAll(ctx)
	p.ProbeAll(ctx)
	if got := events.count(); got != initial {
		t.Errorf("steady state published %d more events, want none", got-initial)
	}

	d.setDown(true)
	p.ProbeAll(ctx)
	if got := events.count(); got != initial+1 {
		t.Errorf("going down published %d events, want exactly 1", got-initial)
	}

	d.setDown(false)
	p.ProbeAll(ctx)
	if got := events.count(); got != initial+2 {
		t.Errorf("recovering published %d events, want exactly 1", got-initial-1)
	}
}

func TestProberRestoresPersistedState(t *testing.T) {
	d := newToggleDaemon(t, "srv-a")
	store := &fakeStateStore{seed: []State{
		{InstanceID: "srv-a", Name: "srv-a", Reachable: true, Version: "0.8.0"},
	}}
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"srv-a": d}, store, nil)

	// Before any probe the API must already have something to show.
	if got := p.State("srv-a"); got.Version != "0.8.0" {
		t.Errorf("restored state = %+v, want the persisted version", got)
	}
}

func TestProberSurvivesStoreFailures(t *testing.T) {
	d := newToggleDaemon(t, "srv-a")
	store := &fakeStateStore{loadErr: errors.New("db gone"), saveErr: errors.New("disk full")}
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"srv-a": d}, store, nil)

	// A broken store must degrade the UI, not take down the control plane.
	p.ProbeAll(context.Background())
	if !p.State("srv-a").Reachable {
		t.Error("probing failed because the store did; the in-memory view must still work")
	}
}

func TestProberStatesCoverEveryRegisteredInstance(t *testing.T) {
	d := newToggleDaemon(t, "srv-a")
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"srv-a": d}, nil, nil)

	// Never-probed instances must still appear, with a renderable name.
	states := p.States()
	if len(states) != 2 {
		t.Fatalf("States() returned %d entries, want one per instance", len(states))
	}
	for _, s := range states {
		if s.InstanceID == "" || s.Name == "" {
			t.Errorf("state %+v has no id or name", s)
		}
	}
}

func TestProberHealthyIDs(t *testing.T) {
	a := newToggleDaemon(t, "a")
	b := newToggleDaemon(t, "b")
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"a": a, "b": b}, nil, nil)
	ctx := context.Background()

	// Before the first probe everything counts as healthy: refusing to
	// dispatch until the first tick would stall work right after a restart.
	if got := len(p.HealthyIDs()); got != 3 {
		t.Errorf("HealthyIDs() before probing = %d entries, want all 3", got)
	}

	b.setDown(true)
	p.ProbeAll(ctx)
	healthy := p.HealthyIDs()
	if containsID(healthy, "b") {
		t.Errorf("HealthyIDs() = %v, want b excluded", healthy)
	}
	if !containsID(healthy, "a") || !containsID(healthy, "hub") {
		t.Errorf("HealthyIDs() = %v, want a and hub included", healthy)
	}
}

func TestProberUpdateForgetsRemovedInstances(t *testing.T) {
	a := newToggleDaemon(t, "a")
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"a": a}, nil, nil)
	p.ProbeAll(context.Background())
	if !p.State("a").Reachable {
		t.Fatal("precondition: a should be reachable")
	}

	cfg := cfgWith(config.RoleHub, "hub", "hub", map[string]config.InstanceConfig{
		"hub": {BaseURL: "http://127.0.0.1:7842", Token: "t"},
	}, config.RoutingConfig{})
	p.Update(NewRegistry(cfg), 15*time.Second)

	if got := p.States(); len(got) != 1 || got[0].InstanceID != "hub" {
		t.Errorf("States() after removal = %+v, want only hub", got)
	}
	// A stale row for a deregistered instance would keep showing up in the UI.
	if s := p.State("a"); s.Reachable {
		t.Errorf("State(a) = %+v, want the removed instance forgotten", s)
	}
}

func TestProberRunProbesImmediatelyAndStops(t *testing.T) {
	d := newToggleDaemon(t, "srv-a")
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"srv-a": d}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	// Run must probe once up front so the GUI is not blank for a whole
	// interval after startup.
	deadline := time.After(2 * time.Second)
	for {
		if p.State("srv-a").Reachable {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Run() did not perform an immediate probe")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after ctx cancellation")
	}
}

func TestNewProberDefaultsInterval(t *testing.T) {
	cfg := cfgWith(config.RoleHub, "hub", "hub", map[string]config.InstanceConfig{
		"hub": {BaseURL: "http://127.0.0.1:7842", Token: "t"},
	}, config.RoutingConfig{})
	p := NewProber(NewRegistry(cfg), 0, nil, nil, nil)
	if p.interval != 30*time.Second {
		t.Errorf("interval = %v, want the 30s default for a non-positive input", p.interval)
	}
}

func TestProberRunPicksUpAChangedInterval(t *testing.T) {
	d := newToggleDaemon(t, "srv-a")
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"srv-a": d}, nil, nil)
	p.interval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	// Wait for at least one ticker cycle, then change the cadence: the loop
	// must adopt it without being restarted.
	time.Sleep(60 * time.Millisecond)
	p.Update(p.registry, 30*time.Millisecond)
	time.Sleep(80 * time.Millisecond)

	p.mu.RLock()
	got := p.interval
	p.mu.RUnlock()
	if got != 30*time.Millisecond {
		t.Errorf("interval = %v, want the reloaded value", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestProberUpdateKeepsIntervalWhenNotSupplied(t *testing.T) {
	d := newToggleDaemon(t, "srv-a")
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"srv-a": d}, nil, nil)
	p.Update(p.registry, 0)
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.interval != time.Minute {
		t.Errorf("interval = %v, want the previous value kept", p.interval)
	}
}

func TestProberHealthyIDsSkipsUnusableInstances(t *testing.T) {
	cfg := cfgWith(config.RoleHub, "hub", "hub", map[string]config.InstanceConfig{
		"hub": {BaseURL: "http://127.0.0.1:7842", Token: "t"},
		"off": {BaseURL: "http://127.0.0.1:7843", Token: "t", Enabled: boolPtr(false)},
	}, config.RoutingConfig{})
	p := NewProber(NewRegistry(cfg), time.Minute, nil, nil, nil)

	// A disabled instance is never a dispatch target, probed or not.
	if containsID(p.HealthyIDs(), "off") {
		t.Errorf("HealthyIDs() = %v, want the disabled instance excluded", p.HealthyIDs())
	}
}

// The hub does not probe itself over the network, so without SetSelfInfo its
// own row would be the only one with no version — which reads as a bug.
func TestProberSelfRowCarriesVersionAndRole(t *testing.T) {
	d := newToggleDaemon(t, "srv-a")
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"srv-a": d}, nil, nil)
	p.SetSelfInfo("1.2.3", "hub")

	p.ProbeAll(context.Background())

	self := p.State("hub")
	if !self.Reachable || self.Version != "1.2.3" || self.Role != "hub" {
		t.Errorf("hub self state = %+v, want its own version and role", self)
	}
	if self.RemoteInstanceID != "hub" {
		t.Errorf("RemoteInstanceID = %q, want the hub's own id", self.RemoteInstanceID)
	}
}

// ConfirmedDown is the #765 gate: it must be strictly harder to satisfy than
// !Reachable, because "I cannot reach it" and "it stopped working" are only
// the same statement outside a network partition.
func TestProberConfirmedDownRequiresARunOfFailures(t *testing.T) {
	a := newToggleDaemon(t, "a")
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"a": a}, nil, nil)
	ctx := context.Background()

	// Never probed: not confirmed down, matching HealthyIDs. Refusing to trust
	// an unprobed instance would stall every decision after a hub restart.
	if p.ConfirmedDown("a", 3) {
		t.Error("ConfirmedDown() = true before any probe, want false")
	}

	a.setDown(true)
	for i := 1; i <= 2; i++ {
		p.ProbeAll(ctx)
		if p.ConfirmedDown("a", 3) {
			t.Fatalf("ConfirmedDown() = true after %d of 3 failures, want false", i)
		}
		if containsID(p.HealthyIDs(), "a") {
			t.Fatalf("HealthyIDs() still lists a after %d failures — the two must differ", i)
		}
	}
	p.ProbeAll(ctx)
	if !p.ConfirmedDown("a", 3) {
		t.Error("ConfirmedDown() = false after 3 consecutive failures, want true")
	}
}

func TestProberConfirmedDownResetsOnRecovery(t *testing.T) {
	a := newToggleDaemon(t, "a")
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"a": a}, nil, nil)
	ctx := context.Background()

	a.setDown(true)
	for i := 0; i < 3; i++ {
		p.ProbeAll(ctx)
	}
	if !p.ConfirmedDown("a", 3) {
		t.Fatal("precondition: a should be confirmed down")
	}

	a.setDown(false)
	p.ProbeAll(ctx)
	if p.ConfirmedDown("a", 3) {
		t.Error("ConfirmedDown() = true after a successful probe, want false")
	}
}

func TestProberConfirmedDownClampsAThresholdBelowOne(t *testing.T) {
	// A zero from a test double or a future caller must not silently mean
	// "give up on the first missed probe" — that is exactly #765.
	a := newToggleDaemon(t, "a")
	p := proberFixture(t, "hub", map[string]*toggleDaemon{"a": a}, nil, nil)
	if p.ConfirmedDown("a", 0) {
		t.Error("ConfirmedDown(0) = true before any probe, want false")
	}
	a.setDown(true)
	p.ProbeAll(context.Background())
	if !p.ConfirmedDown("a", 0) {
		t.Error("ConfirmedDown(0) = false after one failure, want true (clamped to 1)")
	}
	if !p.ConfirmedDown("a", -5) {
		t.Error("ConfirmedDown(-5) = false after one failure, want true (clamped to 1)")
	}
}

// The hub never probes itself over the network, so it must never be able to
// conclude it is down and start deferring its own repos to nobody.
func TestProberConfirmedDownNeverFiresForSelf(t *testing.T) {
	p := proberFixture(t, "hub", nil, nil, nil)
	p.ProbeAll(context.Background())
	if p.ConfirmedDown("hub", 1) {
		t.Error("ConfirmedDown(self) = true, want false")
	}
}
