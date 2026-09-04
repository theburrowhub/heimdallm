package instances

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
)

// localOnlyKeys are the TOML paths that must never travel between instances.
//
// The rule is: anything that describes THIS machine stays here. Pushing a port,
// a bind address, a credential or a filesystem path to another host does not
// merely fail — it silently breaks that instance in a way that looks like a
// heisenbug days later (a daemon that binds the wrong interface, an agent that
// cannot find its repos, a token that belongs to someone else).
//
// Repository lists are local for a subtler reason: [github].repositories and
// non_monitored double as runtime discovery state, merged *below* the store
// layer. Overwriting them would fight the discovery loop on every propagation.
// Partitioning is not done by trimming these lists — it is done by the Router's
// ownership filter, which leaves discovery global and only narrows what a
// daemon acts on.
var localOnlyKeys = []string{
	"cluster", // identity and the registry itself; only the hub owns this

	"server.port",
	"server.bind_addr",
	"server.max_concurrent_workers",

	"github.token",
	"github.repositories",
	"github.non_monitored",
	"github.first_seen_at",

	"ai.local_dir_base",
	"ai.local_dirs_detected",
}

// localOnlyLeafKeys are flat keys from GET /config's map that are local or
// read-only regardless of where they appear.
var localOnlyLeafKeys = map[string]bool{
	"server_port":         true,
	"bind_addr":           true,
	"github_token":        true,
	"repositories":        true,
	"non_monitored":       true,
	"local_dir_base":      true,
	"local_dirs_detected": true,
	"first_seen_at":       true,
	"cluster":             true,
	"instance_id":         true,
	"instance_name":       true,
}

// IsLocalOnly reports whether a dotted TOML path is machine-specific and must
// not be propagated. A parent match covers its children, so "cluster" also
// blocks "cluster.routing.repos".
func IsLocalOnly(path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	if localOnlyLeafKeys[path] {
		return true
	}
	for _, blocked := range localOnlyKeys {
		if path == blocked || strings.HasPrefix(path, blocked+".") {
			return true
		}
	}
	return false
}

// FilterPropagatable returns a copy of patch with every machine-specific key
// removed, along with the sorted list of paths it dropped.
//
// It recurses into nested tables so a patch of {"server": {"port": 1, "x": 2}}
// keeps "x" and drops only the port, rather than dropping the whole table.
func FilterPropagatable(patch map[string]any) (map[string]any, []string) {
	dropped := map[string]bool{}
	out := filterTable(patch, "", dropped)
	paths := make([]string, 0, len(dropped))
	for p := range dropped {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return out, paths
}

func filterTable(table map[string]any, prefix string, dropped map[string]bool) map[string]any {
	out := make(map[string]any, len(table))
	for key, value := range table {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if IsLocalOnly(path) {
			dropped[path] = true
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			child := filterTable(nested, path, dropped)
			// An emptied table would still be written as an empty TOML table,
			// which is noise; drop it entirely when everything inside was local.
			if len(child) == 0 && len(nested) > 0 {
				continue
			}
			out[key] = child
			continue
		}
		out[key] = value
	}
	return out
}

// Result is the outcome of propagating to one instance.
type Result struct {
	InstanceID string   `json:"instance_id"`
	Name       string   `json:"name"`
	OK         bool     `json:"ok"`
	Skipped    bool     `json:"skipped,omitempty"`
	Error      string   `json:"error,omitempty"`
	AppliedKey []string `json:"applied_keys,omitempty"`
}

// ClientFactory builds a client for an instance. Injected so tests can point
// the propagator at httptest servers without a real registry.
type ClientFactory func(Instance) *Client

// Propagator pushes shared configuration from the hub to the other instances.
type Propagator struct {
	registry  *Registry
	newClient ClientFactory
}

// NewPropagator builds a Propagator. A nil factory uses the real HTTP client.
func NewPropagator(reg *Registry, factory ClientFactory) *Propagator {
	if factory == nil {
		factory = func(inst Instance) *Client { return NewClient(inst, nil) }
	}
	return &Propagator{registry: reg, newClient: factory}
}

// Propagate applies patch to every target instance.
//
// targets restricts the push; empty means every usable instance. The hub's own
// entry is skipped: its config is the source of truth and it has already
// written it locally, so pushing it back to itself would be a no-op at best and
// a write-loop at worst.
//
// A failure on one instance never aborts the batch. Partial success is the
// normal outcome when a machine is rebooting, and the per-instance results let
// the operator retry exactly the ones that failed.
func (p *Propagator) Propagate(ctx context.Context, patch map[string]any, targets []string) []Result {
	filtered, _ := FilterPropagatable(patch)
	results := make([]Result, 0, p.registry.Len())

	applied := flattenKeys(filtered)
	for _, inst := range p.registry.List() {
		if len(targets) > 0 && !containsID(targets, inst.ID) {
			continue
		}
		res := Result{InstanceID: inst.ID, Name: inst.Name}
		switch {
		case inst.Self:
			res.Skipped = true
			res.OK = true
			res.Error = "hub is the source of this config"
		case !inst.Enabled:
			res.Skipped = true
			res.Error = "instance is disabled"
		case inst.TokenErr != nil:
			res.Error = inst.TokenErr.Error()
		case len(filtered) == 0:
			res.Skipped = true
			res.OK = true
			res.Error = "nothing to propagate after removing machine-specific keys"
		default:
			if _, err := p.newClient(inst).PatchConfig(ctx, filtered); err != nil {
				res.Error = err.Error()
			} else {
				res.OK = true
				res.AppliedKey = applied
			}
		}
		results = append(results, res)
	}
	return results
}

// flattenKeys returns the dotted leaf paths of a nested table, sorted. Used to
// report exactly what a propagation applied.
func flattenKeys(table map[string]any) []string {
	var out []string
	var walk func(map[string]any, string)
	walk = func(t map[string]any, prefix string) {
		for key, value := range t {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if nested, ok := value.(map[string]any); ok && len(nested) > 0 {
				walk(nested, path)
				continue
			}
			out = append(out, path)
		}
	}
	walk(table, "")
	sort.Strings(out)
	return out
}

// Drift is one propagatable key whose value differs between the hub and an
// instance.
type Drift struct {
	InstanceID  string `json:"instance_id"`
	Name        string `json:"name"`
	Key         string `json:"key"`
	HubValue    any    `json:"hub_value"`
	RemoteValue any    `json:"remote_value"`
	Missing     bool   `json:"missing,omitempty"` // key absent on the instance
}

// InstanceDrift groups the differences found on one instance, or the reason it
// could not be inspected.
type InstanceDrift struct {
	InstanceID string  `json:"instance_id"`
	Name       string  `json:"name"`
	OK         bool    `json:"ok"`
	Skipped    bool    `json:"skipped,omitempty"`
	Error      string  `json:"error,omitempty"`
	Drifts     []Drift `json:"drifts"`
}

// DetectDrift compares the hub's propagatable config against each instance.
//
// It answers the question the GUI needs before offering "apply to all": which
// instances have diverged, and in what. Machine-specific keys are excluded, so
// a differing port is correctly not reported as drift.
func (p *Propagator) DetectDrift(ctx context.Context, hubConfig map[string]any, targets []string) []InstanceDrift {
	reference, _ := FilterPropagatable(hubConfig)
	out := make([]InstanceDrift, 0, p.registry.Len())

	for _, inst := range p.registry.List() {
		if len(targets) > 0 && !containsID(targets, inst.ID) {
			continue
		}
		entry := InstanceDrift{InstanceID: inst.ID, Name: inst.Name, Drifts: []Drift{}}
		switch {
		case inst.Self:
			entry.Skipped, entry.OK = true, true
		case !inst.Enabled:
			entry.Skipped = true
			entry.Error = "instance is disabled"
		case inst.TokenErr != nil:
			entry.Error = inst.TokenErr.Error()
		default:
			remote, err := p.newClient(inst).GetConfig(ctx)
			if err != nil {
				entry.Error = err.Error()
				break
			}
			entry.OK = true
			entry.Drifts = diffConfigs(inst, reference, remote)
		}
		out = append(out, entry)
	}
	return out
}

// diffConfigs compares the reference (hub) values against a remote flat config
// map. Only keys present in the reference are compared: an instance carrying
// extra local keys is not drift, it is just a different machine.
func diffConfigs(inst Instance, reference, remote map[string]any) []Drift {
	var drifts []Drift
	for _, key := range sortedKeys(reference) {
		want := reference[key]
		got, present := remote[key]
		if !present {
			drifts = append(drifts, Drift{
				InstanceID: inst.ID, Name: inst.Name, Key: key,
				HubValue: want, Missing: true,
			})
			continue
		}
		if !equalConfigValues(want, got) {
			drifts = append(drifts, Drift{
				InstanceID: inst.ID, Name: inst.Name, Key: key,
				HubValue: want, RemoteValue: got,
			})
		}
	}
	if drifts == nil {
		return []Drift{}
	}
	return drifts
}

// equalConfigValues compares two config values that reached us by different
// routes: the hub's own copy is built from typed Go values, while an instance's
// arrives decoded from JSON.
//
// Both differences would otherwise produce constant false positives:
//
//   - Numbers: an int on the hub against the float64 the same value becomes
//     after a JSON round trip.
//   - Composites: a typed map or struct on the hub against the generic
//     map[string]any from the wire. reflect.DeepEqual compares types first, so
//     it reported EVERY nested section as drift — enough noise to make the
//     drift view useless.
//
// Canonicalising through JSON settles both. encoding/json sorts map keys, so
// the comparison is order-independent.
func equalConfigValues(a, b any) bool {
	if an, aok := numericValue(a); aok {
		if bn, bok := numericValue(b); bok {
			return an == bn
		}
		return false
	}
	if reflect.DeepEqual(a, b) {
		return true
	}
	ab, aok := canonicalJSON(a)
	bb, bok := canonicalJSON(b)
	if !aok || !bok {
		return false
	}
	return bytes.Equal(ab, bb)
}

// canonicalJSON renders a value in a form that does not depend on how it
// reached us.
//
// The round trip through `any` is the point: marshalling a struct emits its
// fields in declaration order, while marshalling a map sorts the keys. The hub
// holds structs and the wire delivers maps, so a single Marshal on each side
// produces different bytes for identical content. Decoding to maps first and
// re-marshalling makes both sides sorted.
func canonicalJSON(v any) ([]byte, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, false
	}
	out, err := json.Marshal(generic)
	if err != nil {
		return nil, false
	}
	return out, true
}

func numericValue(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

func sortedKeys(table map[string]any) []string {
	out := make([]string, 0, len(table))
	for key := range table {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// ErrNoTargets is returned when a caller asks to propagate to a specific
// instance that is not registered.
func ErrNoTargets(targets []string) error {
	return fmt.Errorf("instances: none of the requested targets are registered: %s", strings.Join(targets, ", "))
}

// ------------------------------------------------------- Partition propagation
//
// The ownership partition is deliberately not just another propagated config
// key: cluster.* is (correctly) on the local-only denylist above, because
// most of it — role, the registry, tokens — describes one machine. But the
// partition itself (an instance's assigned identity, default_instance,
// routing.orgs/repos) is the one piece of cluster.* that MUST reach every
// instance, or a worker's Router has nothing to enforce and Owns() fails open
// — the root cause of theburrowhub/heimdallm#769. This section is a narrow,
// separate channel for exactly that subset, alongside (not instead of) the
// denylist Propagate/FilterPropagatable still apply to everything else.

// PartitionRules is the ownership partition the hub computed for the whole
// fleet: every routing.orgs/routing.repos rule (not filtered to one instance
// — a worker needs to see rules that point elsewhere too, to know a repo is
// NOT its own) and the effective default_instance. DefaultInstance should be
// Router.Fallback() (the resolved value), never the raw configured one, which
// may itself be empty or point at an unusable instance.
type PartitionRules struct {
	DefaultInstance string
	Orgs            map[string]string
	Repos           map[string]string
}

// PartitionPatch renders rules as the [cluster] overlay a legacy instance —
// one that predates PUT /cluster/partition — accepts through PATCH /config.
// Sent directly via Client.PatchConfig, bypassing Propagate's
// FilterPropagatable: that filter always strips cluster.* for the general
// propagation path, which is correct there and exactly wrong here, since this
// patch targets cluster.* on purpose and nothing else.
//
// targetID becomes cluster.instance_id in the patch, but PatchConfig merges
// rather than replaces (config.DeepMerge on the receiving end), and a legacy
// instance has no resolveReloadInstanceID to adopt an id that does not match
// what it already resolved. PropagatePartition only takes this path once the
// instance's own reported identity already matches targetID — see
// pushPartitionTo.
func PartitionPatch(targetID string, rules PartitionRules) map[string]any {
	cluster := map[string]any{"instance_id": targetID}
	if rules.DefaultInstance != "" {
		cluster["default_instance"] = rules.DefaultInstance
	}
	routing := map[string]any{}
	if len(rules.Orgs) > 0 {
		routing["orgs"] = toAnyMap(rules.Orgs)
	}
	if len(rules.Repos) > 0 {
		routing["repos"] = toAnyMap(rules.Repos)
	}
	if len(routing) > 0 {
		cluster["routing"] = routing
	}
	return map[string]any{"cluster": cluster}
}

// toAnyMap widens a string map to map[string]any, the shape a TOML/JSON patch
// body needs.
func toAnyMap(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// PartitionResult is the outcome of pushing the partition to one instance.
type PartitionResult struct {
	InstanceID string `json:"instance_id"`
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	Skipped    bool   `json:"skipped,omitempty"`
	Error      string `json:"error,omitempty"`
	// Legacy reports that PUT /cluster/partition 404'd and the rules (if
	// applied) went through the PATCH /config fallback instead.
	Legacy bool `json:"legacy,omitempty"`
	// IdentityMismatch reports that this instance's own /health disagrees
	// with the id it is registered under. On the modern path PutPartition
	// resolves this (the hub assigns the id and the instance adopts it); on
	// the legacy path it cannot, so the rules are withheld instead of applied
	// under an identity the instance does not recognise as itself.
	IdentityMismatch bool `json:"identity_mismatch,omitempty"`
}

// PropagatePartition pushes rules to every usable, enabled, non-self
// instance, each assigned its own registry id.
//
// targets restricts the push the same way Propagate's does; empty means every
// usable instance. The hub enforces the partition through its own Router
// directly and is always skipped, the same reasoning as Propagate's self
// case.
func (p *Propagator) PropagatePartition(ctx context.Context, rules PartitionRules, targets []string) []PartitionResult {
	results := make([]PartitionResult, 0, p.registry.Len())
	for _, inst := range p.registry.List() {
		if len(targets) > 0 && !containsID(targets, inst.ID) {
			continue
		}
		res := PartitionResult{InstanceID: inst.ID, Name: inst.Name}
		switch {
		case inst.Self:
			res.Skipped, res.OK = true, true
			res.Error = "hub enforces the partition through its own Router, not a push to itself"
		case !inst.Enabled:
			res.Skipped = true
			res.Error = "instance is disabled"
		case inst.TokenErr != nil:
			res.Error = inst.TokenErr.Error()
		default:
			p.pushPartitionTo(ctx, inst, rules, &res)
		}
		results = append(results, res)
	}
	return results
}

// pushPartitionTo delivers rules to one instance, filling res in place.
//
// Health() runs first: it is the cheap, unauthenticated call, and it gives
// reachability, the instance's own reported identity and its version in one
// round trip — exactly what deciding between the modern and legacy paths (and
// detecting an identity mismatch) needs, without a second call to an instance
// that turns out to be unreachable.
func (p *Propagator) pushPartitionTo(ctx context.Context, inst Instance, rules PartitionRules, res *PartitionResult) {
	client := p.newClient(inst)
	health, err := client.Health(ctx)
	if err != nil {
		res.Error = err.Error()
		return
	}
	if health.InstanceID != "" && health.InstanceID != inst.ID {
		res.IdentityMismatch = true
	}

	push := PartitionPush{
		InstanceID:      inst.ID,
		DefaultInstance: rules.DefaultInstance,
		Orgs:            rules.Orgs,
		Repos:           rules.Repos,
		HubInstanceID:   p.registry.SelfID(),
	}
	if _, err := client.PutPartition(ctx, push); err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Status == http.StatusNotFound {
			res.Legacy = true
			if res.IdentityMismatch {
				// The legacy PATCH /config path cannot adopt a new identity —
				// that requires resolveReloadInstanceID, which does not exist
				// on an instance old enough to 404 here. Applying the rules
				// under an id this instance does not recognise as itself
				// would leave it filtering with the wrong selfID: exactly
				// the live mismatch behind theburrowhub/heimdallm#769
				// (registered as "192-168-1-100-3000", reporting itself as
				// "friday"). Withhold instead, and say so plainly enough for
				// an operator to act: update and restart this instance.
				res.Error = fmt.Sprintf(
					"registered as %q but reports itself as %q; update this instance to receive its partition (rules withheld to avoid filtering under the wrong identity)",
					inst.ID, health.InstanceID)
				return
			}
			if _, patchErr := client.PatchConfig(ctx, PartitionPatch(inst.ID, rules)); patchErr != nil {
				res.Error = patchErr.Error()
				return
			}
			res.OK = true
			return
		}
		// A 503 (still starting) is a plain failure to retry on the next
		// propagation cycle, not "this instance predates the endpoint" — it
		// will accept the same request once it finishes booting.
		res.Error = err.Error()
		return
	}
	res.OK = true
}
