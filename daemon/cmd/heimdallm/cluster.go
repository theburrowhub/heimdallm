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
	}
	cs.factory = func(inst instances.Instance) *instances.Client {
		return instances.NewClient(inst, nil)
	}
	// Only a hub probes. A worker knowing about its peers would be duplicated
	// effort and a second source of truth for the same question.
	if cfg.IsHub() {
		var stateStore instances.StateStore
		if st != nil {
			stateStore = st
		}
		cs.prober = instances.NewProber(
			registry, clusterProbeInterval(cfg), cs.factory, stateStore, newClusterEvents(broker),
		)
		cs.prober.SetSelfInfo(version, cfg.Cluster.Role)
	}
	return cs
}

// Update swaps in a reloaded config. Called from the same reload path that
// restarts the pollers, so ownership and routing take effect on the next tick.
func (cs *clusterState) Update(cfg *config.Config) {
	registry := instances.NewRegistry(cfg)

	cs.mu.Lock()
	cs.registry = registry
	cs.selfID = cfg.Cluster.InstanceID
	cs.selfName = resolvedSelfName(cfg)
	cs.role = cfg.Cluster.Role
	prober := cs.prober
	router := cs.router
	cs.mu.Unlock()

	router.Update(registry, cfg)
	if prober != nil {
		prober.Update(registry, clusterProbeInterval(cfg))
	}
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
