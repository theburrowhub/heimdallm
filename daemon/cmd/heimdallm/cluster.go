package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/instances"
	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// instanceIDFileMode matches the API token's: this file is not a secret, but it
// lives in the same directory and there is no reason for it to be broader.
const instanceIDFileMode = 0o600

// clusterState owns everything the multi-instance control plane needs at
// runtime. One is built at startup and updated in place on every config reload.
//
// The Router is deliberately long-lived and updated rather than rebuilt: its
// round-robin counters are the rotation itself, so replacing it would send every
// operation back to the first instance in the pool.
type clusterState struct {
	mu       sync.RWMutex
	registry *instances.Registry
	router   *instances.Router
	prober   *instances.Prober
	factory  instances.ClientFactory
	selfID   string
	selfName string
	role     string

	// store and broker are retained (rather than only used inline at
	// construction) so Update can lazily build a prober if this daemon is
	// promoted from worker/standalone to hub by a config reload instead of a
	// restart. May be nil in tests.
	store  instances.StateStore
	broker *sse.Broker
}

// ensureSelfInstance gives a hub an entry for itself when the operator has not
// written one.
//
// A hub is an instance like any other: the UI lists it, repos route to it, and
// its data is read through the same path. Requiring the operator to describe
// their own machine in config.toml would also be a chicken-and-egg trap —
// registering the first remote instance would fail validation because the hub
// had not registered itself yet.
//
// The entry is in-memory only. It is deliberately not written to config.toml:
// the file stays minimal, and an operator who does write an explicit entry
// keeps full control (theirs wins).
//
// base_url is loopback because nothing ever dials it: the proxy short-circuits
// the hub's own id and serves locally.
func ensureSelfInstance(cfg *config.Config, dataDir string) {
	id := cfg.Cluster.InstanceID
	if !cfg.IsHub() || id == "" {
		return
	}
	if _, exists := cfg.Cluster.Instances[id]; exists {
		return
	}
	if cfg.Cluster.Instances == nil {
		cfg.Cluster.Instances = map[string]config.InstanceConfig{}
	}
	port := cfg.Server.Port
	if port == 0 {
		port = 7842
	}
	cfg.Cluster.Instances[id] = config.InstanceConfig{
		Name:      resolvedSelfName(cfg),
		BaseURL:   fmt.Sprintf("http://127.0.0.1:%d", port),
		TokenFile: filepath.Join(dataDir, "api_token"),
	}
}

// newClusterState builds the control plane from the first loaded config.
func newClusterState(cfg *config.Config, st *store.Store, broker *sse.Broker) *clusterState {
	registry := instances.NewRegistry(cfg)
	cs := &clusterState{
		registry: registry,
		router:   instances.NewRouter(registry, cfg),
		selfID:   cfg.Cluster.InstanceID,
		selfName: resolvedSelfName(cfg),
		role:     cfg.Cluster.Role,
		broker:   broker,
	}
	if st != nil {
		cs.store = st
	}
	cs.factory = func(inst instances.Instance) *instances.Client {
		return instances.NewClient(inst, nil)
	}
	// Only a hub probes. A worker knowing about its peers would be duplicated
	// effort and a second source of truth for the same question.
	if cfg.IsHub() {
		cs.prober = instances.NewProber(
			registry, clusterProbeInterval(cfg), cs.factory, cs.store, newClusterEvents(broker),
		)
		cs.prober.SetSelfInfo(version, cfg.Cluster.Role)
	}
	return cs
}

// Update swaps in a reloaded config. Called from the same reload path that
// restarts the pollers, so ownership and routing take effect on the next tick.
//
// Reports proberBuilt=true when this call just promoted the daemon from
// worker/standalone to hub and therefore built a prober that did not exist at
// boot. The caller must then start a fresh RunProber goroutine itself — this
// method only builds the prober, it never runs its loop, so a promotion that
// happens purely via reload (no process restart) still gets health probing
// and the /instances route wired without the operator having to restart.
func (cs *clusterState) Update(cfg *config.Config) (proberBuilt bool) {
	registry := instances.NewRegistry(cfg)

	cs.mu.Lock()
	cs.registry = registry
	cs.selfID = cfg.Cluster.InstanceID
	cs.selfName = resolvedSelfName(cfg)
	cs.role = cfg.Cluster.Role
	router := cs.router
	if cs.prober == nil && cfg.IsHub() {
		cs.prober = instances.NewProber(
			registry, clusterProbeInterval(cfg), cs.factory, cs.store, newClusterEvents(cs.broker),
		)
		cs.prober.SetSelfInfo(version, cfg.Cluster.Role)
		proberBuilt = true
	}
	prober := cs.prober
	cs.mu.Unlock()

	router.Update(registry, cfg)
	// A prober built above this tick (proberBuilt) already has this exact
	// registry and interval from its NewProber call; updating it again would
	// be a harmless but pointless no-op.
	if prober != nil && !proberBuilt {
		prober.Update(registry, clusterProbeInterval(cfg))
	}
	return proberBuilt
}

// resolveHealthyOwner returns the instance repo is routed to, when that
// instance is registered, is not this daemon itself, and the prober currently
// reports it reachable. ok=false covers every case where handing off to it
// would be unsafe: no configured owner, an owner that resolves to self
// (nothing to hand off — Owns(repo) would already be true), an owner that was
// never registered, or one currently reported unreachable.
//
// Fail-open, by design, in two cases: a nil prober (this daemon is not a
// hub — only a hub ever builds one, see newClusterState/Update) skips health
// verification entirely, and a non-nil prober that has not probed owner yet
// treats it as healthy too (Prober.HealthyIDs' own contract — refusing an
// unprobed instance would stall every dispatch for the first probe interval
// after a promotion or restart). Both converge on the same safety net:
// dispatch()'s caller falls back to handling the work locally the moment the
// RPC itself fails, so an owner that turns out to be genuinely unreachable
// never loses the work — it costs one extra round trip, not a dropped review.
func (cs *clusterState) resolveHealthyOwner(repo string) (inst instances.Instance, ok bool) {
	if cs == nil {
		return instances.Instance{}, false
	}
	cs.mu.RLock()
	router, registry, prober := cs.router, cs.registry, cs.prober
	cs.mu.RUnlock()
	if router == nil || registry == nil {
		return instances.Instance{}, false
	}

	owner := router.OwnerFor(repo)
	if owner == "" {
		return instances.Instance{}, false
	}
	inst, found := registry.Get(owner)
	if !found || inst.Self {
		return instances.Instance{}, false
	}
	if prober != nil {
		healthy := false
		for _, id := range prober.HealthyIDs() {
			if id == owner {
				healthy = true
				break
			}
		}
		if !healthy {
			return instances.Instance{}, false
		}
	}
	return inst, true
}

// dispatch sends a repo's work to the instance the router says owns it,
// calling do against a client for that instance. It returns handled=true only
// when the remote accepted the call.
//
// Every other outcome — no routed owner, the owner is this daemon itself, the
// owner is not currently healthy, or the remote call errors — returns
// handled=false so the caller falls back to acting locally. Before dispatch
// existed, Owns()==false just meant the poller skipped the repo outright
// (main.go's tier2Adapter did a bare `continue`/`return 0, nil`), so a repo
// routed to an instance that was down, unreachable or simply never got the
// dispatch went completely unreviewed with nothing to show for it. Falling
// back to local handling is what makes routing safe to turn on.
func (cs *clusterState) dispatch(ctx context.Context, repo string, do func(*instances.Client) error) (handled bool) {
	inst, ok := cs.resolveHealthyOwner(repo)
	if !ok {
		return false
	}
	cs.mu.RLock()
	factory := cs.factory
	cs.mu.RUnlock()

	if err := do(factory(inst)); err != nil {
		slog.Warn("cluster: dispatch to owning instance failed; falling back to local review",
			"repo", repo, "instance", inst.ID, "err", err)
		return false
	}
	return true
}

// OwnerCanHandle reports whether repo is routed to another instance healthy
// enough to be trusted with work this daemon cannot dispatch as a single RPC
// call — issue triage, whose individual items are not known until deep inside
// per-repo processing, unlike a PR review which has one PR id to hand off.
//
// false means either there is no routing away from this daemon, or the routed
// owner is unregistered/unreachable — in both of those cases the caller must
// still act locally instead of leaving the repo's issues unattended, exactly
// as DispatchPRReview falls back for reviews.
func (cs *clusterState) OwnerCanHandle(repo string) bool {
	_, ok := cs.resolveHealthyOwner(repo)
	return ok
}

// DispatchPRReview sends a PR review to the instance repo is routed to, if
// that instance is registered, not self, and currently healthy. handled=false
// means the caller must review the PR locally instead of dropping it.
func (cs *clusterState) DispatchPRReview(ctx context.Context, repo string, prID int64, prURL string) bool {
	return cs.dispatch(ctx, repo, func(client *instances.Client) error {
		// An instance that does not own the repo has never seen the PR, so it
		// has to adopt it before it can review it. Ignoring an add failure is
		// deliberate: the PR may already be known there, in which case the
		// review call below is the one whose result matters.
		if prURL != "" {
			if _, err := client.AddPR(ctx, prURL); err != nil {
				slog.Debug("cluster: add-PR before dispatched review failed", "repo", repo, "err", err)
			}
		}
		return client.TriggerPRReview(ctx, prID)
	})
}

// DispatchIssueReview sends issue-triage work to the instance repo is routed
// to, if healthy. handled=false means the caller must process it locally.
func (cs *clusterState) DispatchIssueReview(ctx context.Context, repo string, issueID int64) bool {
	return cs.dispatch(ctx, repo, func(client *instances.Client) error {
		return client.TriggerIssueReview(ctx, issueID)
	})
}

// propagateClusterConfig pushes the hub's on-disk config to every healthy
// non-self instance and publishes EventConfigPropagated so the GUI reflects
// the result without an operator having to open the manual propagate dialog.
//
// Before this, config sync only ever happened when someone remembered to
// trigger it by hand — which is how 14 keys (ai_primary, agent_configs,
// org_overrides, repo_overrides, poll_interval...) silently drifted between
// a hub and a worker with nobody noticing. Reads straight from cfgPath rather
// than a decoded *config.Config so operator-only keys and TOML structure
// survive the round trip exactly like the manual /cluster/propagate endpoint.
func propagateClusterConfig(ctx context.Context, cfgPath string, cs *clusterState, broker *sse.Broker) {
	source, err := config.ReadTOMLMap(cfgPath)
	if err != nil {
		slog.Warn("cluster: could not read config for automatic propagation", "path", cfgPath, "err", err)
		return
	}
	filtered, _ := instances.FilterPropagatable(source)

	snap := cs.Snapshot()
	results := snap.Propagator.Propagate(ctx, filtered, nil)

	for _, res := range results {
		if !res.OK && !res.Skipped {
			slog.Warn("cluster: automatic config propagation failed",
				"instance", res.InstanceID, "err", res.Error)
		}
	}
	if broker != nil {
		broker.Publish(sse.Event{
			Type: sse.EventConfigPropagated,
			Data: sseData(map[string]any{"results": results}),
		})
	}
}

// resolveReloadInstanceID keeps this daemon's identity stable in memory across
// reloads while recovering from booting before [cluster] existed: if identity
// was never resolved (current is empty, because clustering was off when
// ensureInstanceID ran at boot) and the newly reloaded config now wants
// clustering, resolve — and, if needed, generate and persist — an identity now
// instead of leaving it stuck at "" until the next full process restart.
//
// A "" selfID makes Router.Owns fail open (it treats an unidentifiable daemon
// as owning everything), which is what let a hub silently keep reviewing repos
// that were routed away from it after an operator added [cluster] without
// restarting.
func resolveReloadInstanceID(current string, newCfg *config.Config, dataDir string) (string, error) {
	if strings.TrimSpace(current) != "" || !newCfg.ClusterEnabled() {
		return current, nil
	}
	return ensureInstanceID(newCfg, dataDir)
}

// Router returns the live router. Never nil.
func (cs *clusterState) Router() *instances.Router {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.router
}

// Owns reports whether this daemon should act on repo. This is the single guard
// that partitions autonomous work between instances.
func (cs *clusterState) Owns(repo string) bool {
	if cs == nil {
		return true
	}
	return cs.Router().Owns(repo)
}

// FilterOwned narrows a repo list to what this daemon owns.
func (cs *clusterState) FilterOwned(repos []string) []string {
	if cs == nil {
		return repos
	}
	return cs.Router().FilterOwned(repos)
}

// Snapshot supplies the HTTP handlers with the current control-plane view.
func (cs *clusterState) Snapshot() server.ClusterSnapshot {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return server.ClusterSnapshot{
		Registry:   cs.registry,
		Router:     cs.router,
		Propagator: instances.NewPropagator(cs.registry, cs.factory),
		Role:       cs.role,
		SelfID:     cs.selfID,
		SelfName:   cs.selfName,
	}
}

// Identity returns this daemon's id, name and role for GET /health.
func (cs *clusterState) Identity() (id, name, role string) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.selfID, cs.selfName, cs.role
}

// Prober returns the health prober, or nil when this daemon is not a hub.
func (cs *clusterState) Prober() *instances.Prober {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.prober
}

// RunProber drives the health loop until ctx is cancelled. No-op on a worker.
func (cs *clusterState) RunProber(ctx context.Context) {
	if p := cs.Prober(); p != nil {
		p.Run(ctx)
	}
}

// clusterEvents publishes instance up/down transitions onto the SSE stream so
// the GUI can react without polling.
type clusterEvents struct{ broker *sse.Broker }

func newClusterEvents(broker *sse.Broker) instances.EventPublisher {
	if broker == nil {
		return nil
	}
	return &clusterEvents{broker: broker}
}

func (e *clusterEvents) InstanceStateChanged(s instances.State) {
	eventType := sse.EventInstanceDown
	if s.Reachable {
		eventType = sse.EventInstanceUp
	}
	e.broker.Publish(sse.Event{
		Type: eventType,
		Data: sseData(map[string]any{
			"instance_id":   s.InstanceID,
			"instance_name": s.Name,
			"reachable":     s.Reachable,
			"version":       s.Version,
			"error":         s.LastError,
		}),
	})
}

func clusterProbeInterval(cfg *config.Config) time.Duration {
	d, err := time.ParseDuration(cfg.Cluster.ProbeInterval)
	if err != nil || d <= 0 {
		return 30 * time.Second
	}
	return d
}

// resolvedSelfName returns the display name for this daemon, defaulting to the
// hostname so an operator adding a second machine sees something meaningful
// without having to name it first.
func resolvedSelfName(cfg *config.Config) string {
	if name := strings.TrimSpace(cfg.Cluster.InstanceName); name != "" {
		return name
	}
	// No identity to name: a daemon outside a cluster has no instance to label.
	if !cfg.ClusterEnabled() {
		return ""
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return cfg.Cluster.InstanceID
}

// ensureInstanceID resolves this daemon's stable identity.
//
// Precedence: an explicit [cluster].instance_id, then a previously generated id
// persisted next to the API token, then a freshly generated one. Persisting it
// in the data directory rather than config.toml means the id survives an
// operator rewriting or deleting their config, which is exactly when a hub must
// not suddenly consider this a different machine.
//
// Uses the same O_CREATE|O_EXCL race handling as loadOrCreateAPIToken: if two
// processes start together, the loser adopts the winner's id instead of the two
// silently diverging.
func ensureInstanceID(cfg *config.Config, dir string) (string, error) {
	if id := strings.TrimSpace(cfg.Cluster.InstanceID); id != "" {
		if err := config.ValidateInstanceID(id); err != nil {
			return "", err
		}
		return id, nil
	}
	// A daemon that is not part of a cluster does not need an identity, and
	// generating one would write a file into the data dir of every single
	// install for a feature they are not using.
	if !cfg.ClusterEnabled() {
		return "", nil
	}

	path := filepath.Join(dir, "instance_id")
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); config.ValidateInstanceID(id) == nil {
			return id, nil
		}
		// A truncated or hand-edited file must not wedge the daemon at boot.
		// Remove it first: the exclusive create below would otherwise fail with
		// "file exists" and the race-recovery path would just re-read the same
		// unusable value.
		slog.Warn("instance_id: stored value is unusable, regenerating", "path", path)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("instance_id: could not replace the unusable file %s: %w", path, err)
		}
	}

	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("instance_id: generate random: %w", err)
	}
	id := instanceIDPrefix() + hex.EncodeToString(buf)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, instanceIDFileMode)
	if err != nil {
		if os.IsExist(err) {
			// Another process created the file between our read and here: adopt
			// its id rather than letting the two silently diverge.
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return "", fmt.Errorf("instance_id: read after race: %w", readErr)
			}
			if existing := strings.TrimSpace(string(data)); config.ValidateInstanceID(existing) == nil {
				return existing, nil
			}
			return "", fmt.Errorf("instance_id: %s exists but holds an unusable id; remove it and restart", path)
		}
		return "", fmt.Errorf("instance_id: create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s\n", id); err != nil {
		return "", fmt.Errorf("instance_id: write %s: %w", path, err)
	}
	slog.Info("instance_id: generated a new instance identity", "id", id, "path", path)
	return id, nil
}

// instanceIDPrefix derives a short, readable prefix from the hostname so ids
// look like "mbp-3f2a91..." rather than opaque hex in logs and the UI.
func instanceIDPrefix() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "node-"
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(host) {
		if len(b.String()) >= 12 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	prefix := strings.Trim(b.String(), "-")
	if prefix == "" || !(prefix[0] >= 'a' && prefix[0] <= 'z') {
		return "node-"
	}
	return prefix + "-"
}

// wireCluster installs the control plane on the HTTP server.
//
// The identity is always published (a worker must be recognisable on /health),
// but ClusterDeps — and therefore the /instances and /cluster routes — only on
// a hub.
func wireCluster(srv *server.Server, cs *clusterState, st *store.Store) {
	id, name, role := cs.Identity()
	// A daemon that is not part of a cluster publishes nothing: /health must
	// look exactly as it did before instances existed, with no identity fields
	// for a feature the operator is not using.
	if id == "" && role == "" {
		return
	}
	srv.SetClusterIdentity(id, name, role)

	if !strings.EqualFold(role, config.RoleHub) {
		return
	}
	deps := &server.ClusterDeps{
		Snapshot:  cs.Snapshot,
		Prober:    cs.Prober(),
		NewClient: cs.factory,
	}
	if st != nil {
		deps.Store = st
	}
	srv.SetCluster(deps)
}
