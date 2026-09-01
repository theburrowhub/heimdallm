// Package instances implements Heimdallm's multi-daemon control plane: the
// registry of known instances, the org/repo -> instance router, the HTTP client
// the hub uses to reach the others, health probing, and config propagation.
//
// The whole package is inert unless [cluster] is configured. With no registry
// and no routing rules a Router reports that this daemon owns every repo, which
// is precisely how a single-daemon install has always behaved.
package instances

import (
	"fmt"
	"sort"
	"strings"

	"github.com/heimdallm/daemon/internal/config"
)

// Instance is one resolved registry entry: a config record whose token source
// has already been read, so callers never touch the environment or the disk.
type Instance struct {
	ID      string
	Name    string
	BaseURL string
	Token   string
	Enabled bool
	Labels  []string

	// Self marks the entry describing the daemon holding this Registry. The
	// hub short-circuits its own entry instead of proxying to itself.
	Self bool

	// TokenErr records why the token could not be resolved. Such an instance
	// is excluded from Enabled() but stays in List() so the GUI can show the
	// operator what is wrong instead of the instance vanishing.
	TokenErr error
}

// Usable reports whether the instance can actually be talked to.
func (i Instance) Usable() bool { return i.Enabled && i.TokenErr == nil && i.Token != "" }

// Registry is an immutable snapshot of [cluster.instances] with tokens
// resolved. Build a new one on config reload rather than mutating this.
type Registry struct {
	selfID string
	byID   map[string]Instance
	order  []string // sorted ids, so every listing and round robin is stable
}

// NewRegistry resolves cfg's registry.
//
// A token that fails to resolve is deliberately NOT fatal: one unreadable token
// file must not stop the hub from booting and managing the instances that do
// work. The failure is attached to the entry and surfaced through the API.
func NewRegistry(cfg *config.Config) *Registry {
	r := &Registry{byID: make(map[string]Instance), order: nil}
	if cfg == nil {
		return r
	}
	r.selfID = cfg.Cluster.InstanceID
	for id, ic := range cfg.Cluster.Instances {
		inst := Instance{
			ID:      id,
			Name:    cfg.ResolvedInstanceName(id),
			BaseURL: strings.TrimRight(ic.BaseURL, "/"),
			Enabled: ic.IsEnabled(),
			Labels:  append([]string(nil), ic.Labels...),
			Self:    id != "" && id == cfg.Cluster.InstanceID,
		}
		token, err := cfg.ResolveInstanceToken(id)
		if err != nil {
			inst.TokenErr = err
		} else {
			inst.Token = token
		}
		r.byID[id] = inst
		r.order = append(r.order, id)
	}
	sort.Strings(r.order)
	return r
}

// SelfID returns this daemon's instance id, or "" when it has none.
func (r *Registry) SelfID() string { return r.selfID }

// Self returns the entry describing this daemon.
func (r *Registry) Self() (Instance, bool) { return r.Get(r.selfID) }

// Get returns one instance by id.
func (r *Registry) Get(id string) (Instance, bool) {
	inst, ok := r.byID[id]
	return inst, ok
}

// Len reports how many instances are registered, enabled or not.
func (r *Registry) Len() int { return len(r.order) }

// Empty reports whether no instance is registered at all.
func (r *Registry) Empty() bool { return len(r.order) == 0 }

// List returns every instance in id order, including disabled ones and ones
// whose token failed to resolve.
func (r *Registry) List() []Instance {
	out := make([]Instance, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byID[id])
	}
	return out
}

// Enabled returns the instances that can actually be used, in id order.
func (r *Registry) Enabled() []Instance {
	out := make([]Instance, 0, len(r.order))
	for _, id := range r.order {
		if inst := r.byID[id]; inst.Usable() {
			out = append(out, inst)
		}
	}
	return out
}

// EnabledIDs returns the ids of Enabled(), in id order.
func (r *Registry) EnabledIDs() []string {
	enabled := r.Enabled()
	out := make([]string, 0, len(enabled))
	for _, inst := range enabled {
		out = append(out, inst.ID)
	}
	return out
}

// Require returns an instance or an error naming the missing id. Handlers use
// it so an unknown id becomes a clean 404 instead of a zero-value Instance
// whose empty BaseURL would produce a confusing request failure later.
func (r *Registry) Require(id string) (Instance, error) {
	inst, ok := r.byID[id]
	if !ok {
		return Instance{}, fmt.Errorf("instances: unknown instance %q", id)
	}
	return inst, nil
}
