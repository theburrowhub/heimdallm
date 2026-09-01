package instances

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/heimdallm/daemon/internal/config"
)

// opAssign is the internal round-robin counter used when handing an
// as-yet-unrouted repo to a pool member. It is separate from the per-operation
// counters so assigning repos does not skew the review/merge/issue rotation.
const opAssign = "assign"

// Router answers the two questions the cluster needs: "does this daemon own
// this repo?" and "which instance should take the next operation?".
//
// Ownership precedence mirrors github.TokenRouter (ForRepo, then ForOrg, then
// the default): an explicit repo rule wins over an org rule, which wins over
// the fallback owner. Round robin only ever produces NEW assignments; it never
// overrides a rule that already exists.
//
// Safe for concurrent use. Reload swaps the rules in place via Update while the
// round-robin counters deliberately survive, so a config reload does not reset
// the rotation and send a burst of work to the first instance again.
type Router struct {
	mu       sync.RWMutex
	selfID   string
	rules    config.RoutingConfig
	pool     []string // resolved, ordered, usable instance ids
	fallback string   // owner of everything no rule claims
	enabled  bool     // false => this daemon owns every repo (single-daemon behaviour)

	counters sync.Map // op -> *atomic.Uint64
}

// NewRouter builds a Router from a registry and config snapshot.
func NewRouter(reg *Registry, cfg *config.Config) *Router {
	r := &Router{}
	r.Update(reg, cfg)
	return r
}

// Update swaps in a new registry/config snapshot after a reload.
func (r *Router) Update(reg *Registry, cfg *config.Config) {
	if reg == nil {
		reg = &Registry{byID: map[string]Instance{}}
	}
	var rules config.RoutingConfig
	if cfg != nil {
		rules = cfg.Cluster.Routing
	}

	usable := reg.EnabledIDs()
	pool := resolvePool(rules.RoundRobinPool, usable)

	fallback := ""
	if cfg != nil {
		fallback = cfg.Cluster.DefaultInstance
	}
	// An explicit default_instance that is disabled or unknown would orphan
	// every unrouted repo, so fall back to the first pool member. The pool is
	// sorted, and every daemon shares the same config, so all of them reach
	// the same answer without coordinating.
	if fallback == "" || !containsID(usable, fallback) {
		if len(pool) > 0 {
			fallback = pool[0]
		} else {
			fallback = ""
		}
	}

	// The feature only engages once there is something to route between. A
	// registry with fewer than two usable instances and no rules behaves
	// exactly like a single-daemon install.
	enabled := rules.Configured() || len(usable) > 1

	r.mu.Lock()
	defer r.mu.Unlock()
	r.selfID = reg.SelfID()
	r.rules = rules
	r.pool = pool
	r.fallback = fallback
	r.enabled = enabled
}

// resolvePool intersects the configured pool with the usable instances,
// preserving the configured order and dropping duplicates. An empty configured
// pool means "every usable instance".
func resolvePool(configured, usable []string) []string {
	if len(configured) == 0 {
		return append([]string(nil), usable...)
	}
	seen := make(map[string]bool, len(configured))
	out := make([]string, 0, len(configured))
	for _, id := range configured {
		if seen[id] || !containsID(usable, id) {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// Enabled reports whether routing is actually in play. When false, Owns always
// returns true and callers behave exactly as they did before instances existed.
func (r *Router) Enabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled
}

// Pool returns the round-robin pool in rotation order.
func (r *Router) Pool() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.pool...)
}

// Fallback returns the instance that owns everything no rule claims.
func (r *Router) Fallback() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.fallback
}

// RulesSnapshot returns a deep copy of the current routing rules. Copied rather
// than shared because callers render and serialise it while a reload may be
// swapping the live rules underneath.
func (r *Router) RulesSnapshot() config.RoutingConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := config.RoutingConfig{
		Mode:           r.rules.Mode,
		RoundRobinPool: append([]string(nil), r.rules.RoundRobinPool...),
		RoundRobinOps:  append([]string(nil), r.rules.RoundRobinOps...),
	}
	if r.rules.Orgs != nil {
		out.Orgs = make(map[string]string, len(r.rules.Orgs))
		for k, v := range r.rules.Orgs {
			out.Orgs[k] = v
		}
	}
	if r.rules.Repos != nil {
		out.Repos = make(map[string]string, len(r.rules.Repos))
		for k, v := range r.rules.Repos {
			out.Repos[k] = v
		}
	}
	return out
}

// RuleFor returns the explicitly configured owner of repo, or "" when no repo
// or org rule matches. It never consults the fallback, so callers can tell an
// explicit assignment apart from an inherited one.
func (r *Router) RuleFor(repo string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ruleForLocked(repo)
}

func (r *Router) ruleForLocked(repo string) string {
	if repo == "" {
		return ""
	}
	if id, ok := r.rules.Repos[repo]; ok && id != "" {
		return id
	}
	if org := repoOrg(repo); org != "" {
		if id, ok := r.rules.Orgs[org]; ok && id != "" {
			return id
		}
		// Org keys are operator-typed; match case-insensitively the way
		// GitHub treats owner slugs.
		for candidate, id := range r.rules.Orgs {
			if id != "" && strings.EqualFold(candidate, org) {
				return id
			}
		}
	}
	return ""
}

// OwnerFor returns the instance that should act on repo, resolving repo rule >
// org rule > fallback. Returns "" only when routing is not engaged at all.
func (r *Router) OwnerFor(repo string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.enabled {
		return ""
	}
	if id := r.ruleForLocked(repo); id != "" {
		return id
	}
	return r.fallback
}

// Owns reports whether this daemon should poll, review and merge repo.
//
// This is the single guard that partitions autonomous work. It is deliberately
// permissive in every ambiguous case: with routing disabled, with no identity
// of our own, or with no owner resolvable, it returns true. Acting twice is
// recoverable (the in-flight claims and dedup catch it); acting never is not —
// a repo would silently go unreviewed with nothing to indicate why.
func (r *Router) Owns(repo string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.enabled || r.selfID == "" {
		return true
	}
	owner := r.ruleForLocked(repo)
	if owner == "" {
		owner = r.fallback
	}
	if owner == "" {
		return true
	}
	return owner == r.selfID
}

// FilterOwned returns the subset of repos this daemon owns, preserving order.
func (r *Router) FilterOwned(repos []string) []string {
	if len(repos) == 0 {
		return repos
	}
	r.mu.RLock()
	enabled, selfID := r.enabled, r.selfID
	r.mu.RUnlock()
	if !enabled || selfID == "" {
		return repos
	}
	out := make([]string, 0, len(repos))
	for _, repo := range repos {
		if r.Owns(repo) {
			out = append(out, repo)
		}
	}
	return out
}

// Next returns the next instance in the rotation for op, or "" when the pool is
// empty. Each op keeps its own counter so reviews and merges rotate
// independently instead of interleaving into an uneven split.
func (r *Router) Next(op string) string {
	return r.NextAmong(op, nil)
}

// NextAmong is Next restricted to candidates (typically the instances the
// prober currently considers reachable). A nil or empty candidate set means the
// full pool. When no candidate is also in the pool it falls back to the full
// pool rather than returning nothing: sending work to an instance that might be
// down beats dropping the operation on the floor.
func (r *Router) NextAmong(op string, candidates []string) string {
	r.mu.RLock()
	pool := append([]string(nil), r.pool...)
	r.mu.RUnlock()
	if len(pool) == 0 {
		return ""
	}
	if len(candidates) > 0 {
		filtered := make([]string, 0, len(pool))
		for _, id := range pool {
			if containsID(candidates, id) {
				filtered = append(filtered, id)
			}
		}
		if len(filtered) > 0 {
			pool = filtered
		}
	}
	counter, _ := r.counters.LoadOrStore(normalizeOp(op), new(atomic.Uint64))
	n := counter.(*atomic.Uint64).Add(1) - 1
	return pool[n%uint64(len(pool))]
}

// AssignRepo resolves the sticky owner of repo for the hub.
//
// An existing explicit rule is returned untouched (changed=false). Otherwise
// the next pool member is picked round robin and returned with changed=true;
// the caller persists it under [cluster.routing.repos] so the assignment
// survives restarts and shows up in the UI as an ordinary, editable rule.
func (r *Router) AssignRepo(repo string) (instanceID string, changed bool) {
	if repo == "" {
		return "", false
	}
	if existing := r.RuleFor(repo); existing != "" {
		return existing, false
	}
	if picked := r.Next(opAssign); picked != "" {
		return picked, true
	}
	return "", false
}

// RoundRobinsOp reports whether op should be spread per-operation. True only in
// dispatch mode, and only for ops the operator listed.
func (r *Router) RoundRobinsOp(op string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.enabled {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(r.rules.Mode), config.ModeDispatch) {
		return false
	}
	return r.rules.RoundRobinsOp(op)
}

func normalizeOp(op string) string {
	op = strings.ToLower(strings.TrimSpace(op))
	if op == "" {
		return "default"
	}
	return op
}

// repoOrg extracts the owner from an "owner/name" slug. Duplicated from
// package config rather than exported from it: it is three lines, and widening
// config's API surface for it would invite callers to depend on config for
// string parsing.
func repoOrg(repo string) string {
	if i := strings.Index(repo, "/"); i > 0 {
		return repo[:i]
	}
	return ""
}
