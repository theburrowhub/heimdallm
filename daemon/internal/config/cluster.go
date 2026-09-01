package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Cluster roles. StandAlone is the zero value on purpose: a config that never
// mentions [cluster] behaves exactly like every single-daemon install did
// before instances existed, and none of the control-plane routes are mounted.
const (
	RoleStandalone = "standalone"
	RoleHub        = "hub"
	RoleWorker     = "worker"
)

// Routing modes.
//
//   - ModeAssignment (default): repos are partitioned across instances and each
//     daemon polls, reviews and merges only what it owns. Round robin is used to
//     hand an as-yet-unassigned repo to a member of the pool, and the assignment
//     is then sticky (persisted under [cluster.routing.repos]).
//   - ModeDispatch: additionally lets the hub spread explicitly triggered
//     operations across the pool per operation, regardless of who owns the repo.
const (
	ModeAssignment = "assignment"
	ModeDispatch   = "dispatch"
)

// Operations that can be round-robined in ModeDispatch.
const (
	OpReview = "review"
	OpMerge  = "merge"
	OpIssue  = "issue"
)

// DefaultClusterProbeInterval is how often the hub polls each instance's
// unauthenticated GET /health.
const DefaultClusterProbeInterval = "30s"

// instanceIDPattern constrains instance ids to what is safe to interpolate into
// a URL path segment (the hub proxies at /instances/{id}/proxy/*) and into a
// TOML bare key. Deliberately stricter than necessary: no dots, no slashes, no
// percent-encoding, so an id can never traverse or alias another route.
var instanceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

// ClusterConfig is the [cluster] section: this daemon's own identity plus, when
// it is the hub, the registry of instances and the routing rules.
//
// Instances is a map keyed by instance id rather than an array of tables
// ([[cluster.instances]]) for two reasons: the config schema validator in
// canonical_map.go rejects slices of composite structs outright, and a map makes
// id uniqueness a property of the format instead of something Validate has to
// enforce. It also means the generic scoped-PATCH handlers already used for
// [merge_tracking.repos] work verbatim for editing one instance from the GUI.
type ClusterConfig struct {
	Role string `toml:"role"` // standalone|hub|worker

	// InstanceID is this daemon's stable identity. Empty means "derive it":
	// the process generates one on first boot and persists it next to the API
	// token (see instanceIDPath in cmd/heimdallm), so it survives restarts
	// without the operator having to invent one.
	InstanceID   string `toml:"instance_id"`
	InstanceName string `toml:"instance_name"` // defaults to the hostname

	// DefaultInstance owns every repo that no rule and no round-robin
	// assignment has claimed yet. Without it a routing typo would silently
	// orphan a repo — nobody would poll it and nothing would say so.
	DefaultInstance string `toml:"default_instance"`

	// ProbeInterval is the hub's health-poll cadence for the registry.
	ProbeInterval string `toml:"probe_interval"`

	Instances map[string]InstanceConfig `toml:"instances"` // [cluster.instances.<id>]
	Routing   RoutingConfig             `toml:"routing"`
}

// InstanceConfig is one registered daemon. The map key is its id.
//
// The token has three mutually exclusive sources so an operator never has to
// paste a secret into a file the GUI rewrites: Token inline (matching how
// github.token already works), TokenEnv, or TokenFile. Resolution order is
// Token > TokenEnv > TokenFile.
type InstanceConfig struct {
	Name      string   `toml:"name"`
	BaseURL   string   `toml:"base_url"`
	Token     string   `toml:"token,omitempty"`
	TokenEnv  string   `toml:"token_env,omitempty"`
	TokenFile string   `toml:"token_file,omitempty"`
	Enabled   *bool    `toml:"enabled,omitempty"` // nil = enabled; explicit false disables
	Labels    []string `toml:"labels,omitempty"`
}

// IsEnabled reports whether the instance participates. Absent means enabled:
// registering an instance and having it silently ignored would be surprising.
func (i InstanceConfig) IsEnabled() bool {
	return i.Enabled == nil || *i.Enabled
}

// RoutingConfig holds the org/repo -> instance rules and the round-robin pool.
//
// Precedence when resolving an owner mirrors github.TokenRouter (ForRepo, then
// ForOrg, then the default): Repos > Orgs > round-robin pool > DefaultInstance.
type RoutingConfig struct {
	Mode string `toml:"mode"` // assignment|dispatch

	// RoundRobinPool restricts which instances take part. Empty means every
	// enabled instance.
	RoundRobinPool []string `toml:"round_robin_pool,omitempty"`

	// RoundRobinOps lists which operations are spread per-operation in
	// ModeDispatch. Empty means all of them.
	RoundRobinOps []string `toml:"round_robin_ops,omitempty"`

	Orgs  map[string]string `toml:"orgs,omitempty"`  // org -> instance id
	Repos map[string]string `toml:"repos,omitempty"` // "org/repo" -> instance id
}

// Configured reports whether any routing rule or pool restriction exists. When
// false every daemon owns every repo, which is exactly today's behaviour.
func (r RoutingConfig) Configured() bool {
	return len(r.Orgs) > 0 || len(r.Repos) > 0 || len(r.RoundRobinPool) > 0
}

// RoundRobinsOp reports whether op participates in per-operation round robin.
// Only meaningful in ModeDispatch; the caller checks the mode.
func (r RoutingConfig) RoundRobinsOp(op string) bool {
	if len(r.RoundRobinOps) == 0 {
		return true
	}
	for _, candidate := range r.RoundRobinOps {
		if strings.EqualFold(candidate, op) {
			return true
		}
	}
	return false
}

// IsHub reports whether this daemon mounts the control plane.
func (c *Config) IsHub() bool { return strings.EqualFold(c.Cluster.Role, RoleHub) }

// ClusterEnabled reports whether the daemon has any multi-instance
// configuration at all. Used as the single guard that keeps the feature inert
// on a config that never mentions [cluster].
func (c *Config) ClusterEnabled() bool {
	role := strings.ToLower(strings.TrimSpace(c.Cluster.Role))
	return (role != "" && role != RoleStandalone) || len(c.Cluster.Instances) > 0
}

// EnabledInstanceIDs returns the ids of every enabled instance, sorted so the
// round-robin order and every API response are deterministic (Go map iteration
// is not).
func (c *Config) EnabledInstanceIDs() []string {
	out := make([]string, 0, len(c.Cluster.Instances))
	for id, inst := range c.Cluster.Instances {
		if inst.IsEnabled() {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// ResolvedInstanceName returns the display name for an instance, falling back
// to its id so the UI never renders an empty label.
func (c *Config) ResolvedInstanceName(id string) string {
	if inst, ok := c.Cluster.Instances[id]; ok && strings.TrimSpace(inst.Name) != "" {
		return inst.Name
	}
	return id
}

// ResolveInstanceToken resolves an instance's API token from whichever of the
// three sources is configured. A missing env var or unreadable file is an error
// rather than an empty token: silently talking to an instance without
// credentials would surface as a confusing 401 much later.
func (c *Config) ResolveInstanceToken(id string) (string, error) {
	inst, ok := c.Cluster.Instances[id]
	if !ok {
		return "", fmt.Errorf("config: cluster.instances has no instance %q", id)
	}
	switch {
	case inst.Token != "":
		return inst.Token, nil
	case inst.TokenEnv != "":
		token := strings.TrimSpace(os.Getenv(inst.TokenEnv))
		if token == "" {
			return "", fmt.Errorf("config: cluster.instances.%s.token_env %q is unset or empty", id, inst.TokenEnv)
		}
		return token, nil
	case inst.TokenFile != "":
		path, err := expandTokenFilePath(inst.TokenFile)
		if err != nil {
			return "", fmt.Errorf("config: cluster.instances.%s.token_file: %w", id, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("config: cluster.instances.%s.token_file %q: %w", id, inst.TokenFile, err)
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return "", fmt.Errorf("config: cluster.instances.%s.token_file %q is empty", id, inst.TokenFile)
		}
		return token, nil
	default:
		return "", fmt.Errorf("config: cluster.instances.%s needs one of token, token_env or token_file", id)
	}
}

// expandTokenFilePath resolves a leading ~ and rejects relative paths, which
// would otherwise resolve against whatever directory the daemon happens to have
// been started from.
func expandTokenFilePath(raw string) (string, error) {
	path := raw
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot expand %q: %w", raw, err)
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%q must be an absolute path or start with ~/", raw)
	}
	return filepath.Clean(path), nil
}

// applyClusterDefaults fills the zero-value scalars. Role is deliberately left
// empty rather than defaulted to "standalone": ClusterEnabled treats empty and
// "standalone" identically, and leaving it empty keeps a config the daemon
// rewrites from growing a [cluster] section nobody asked for.
func (c *Config) applyClusterDefaults() {
	if c.Cluster.Routing.Mode == "" {
		c.Cluster.Routing.Mode = ModeAssignment
	}
	if c.Cluster.ProbeInterval == "" {
		c.Cluster.ProbeInterval = DefaultClusterProbeInterval
	}
}

// ValidateInstanceID validates an instance id used as a [cluster.instances] key
// and as a URL path segment in the hub's proxy route. Exported because the HTTP
// handlers validate ids from request bodies with the same rule.
func ValidateInstanceID(id string) error {
	if !instanceIDPattern.MatchString(id) {
		return fmt.Errorf("config: instance id %q is invalid (allowed: 1-63 chars, alphanumeric start, then alphanumerics, '_' or '-')", id)
	}
	return nil
}

// ValidateInstanceBaseURL enforces an absolute http(s) URL with a host and no
// query, fragment or userinfo. The hub uses this value to build outbound
// requests, so a malformed or exotic URL here is an SSRF-shaped footgun.
func ValidateInstanceBaseURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("config: instance base_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("config: instance base_url %q is invalid: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("config: instance base_url %q must use http or https", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("config: instance base_url %q must include a host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("config: instance base_url %q must not embed credentials", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("config: instance base_url %q must not include a query or fragment", raw)
	}
	return nil
}

// validateCluster enforces the [cluster] invariants at boot. Every failure here
// would otherwise surface as a routing hole: a repo nobody owns, a proxy route
// pointing at a nonexistent instance, or two daemons believing they are the
// same instance.
func (c *Config) validateCluster() error {
	cl := c.Cluster

	switch strings.ToLower(strings.TrimSpace(cl.Role)) {
	case "", RoleStandalone, RoleHub, RoleWorker:
	default:
		return fmt.Errorf("config: cluster.role %q must be one of %q, %q, %q (or empty)",
			cl.Role, RoleStandalone, RoleHub, RoleWorker)
	}

	if cl.InstanceID != "" {
		if err := ValidateInstanceID(cl.InstanceID); err != nil {
			return fmt.Errorf("config: cluster.instance_id: %w", err)
		}
	}
	if err := validatePositiveDuration("cluster.probe_interval", cl.ProbeInterval); err != nil {
		return err
	}

	for id, inst := range cl.Instances {
		if err := ValidateInstanceID(id); err != nil {
			return fmt.Errorf("config: cluster.instances key %q: %w", id, err)
		}
		if err := ValidateInstanceBaseURL(inst.BaseURL); err != nil {
			return fmt.Errorf("config: cluster.instances.%s.base_url: %w", id, err)
		}
		sources := 0
		for _, set := range []bool{inst.Token != "", inst.TokenEnv != "", inst.TokenFile != ""} {
			if set {
				sources++
			}
		}
		if sources == 0 {
			return fmt.Errorf("config: cluster.instances.%s needs one of token, token_env or token_file", id)
		}
		if sources > 1 {
			return fmt.Errorf("config: cluster.instances.%s sets more than one of token, token_env, token_file", id)
		}
		if inst.TokenFile != "" {
			if _, err := expandTokenFilePath(inst.TokenFile); err != nil {
				return fmt.Errorf("config: cluster.instances.%s.token_file: %w", id, err)
			}
		}
	}

	switch strings.ToLower(strings.TrimSpace(cl.Routing.Mode)) {
	case "", ModeAssignment, ModeDispatch:
	default:
		return fmt.Errorf("config: cluster.routing.mode %q must be %q or %q",
			cl.Routing.Mode, ModeAssignment, ModeDispatch)
	}
	for _, op := range cl.Routing.RoundRobinOps {
		switch strings.ToLower(strings.TrimSpace(op)) {
		case OpReview, OpMerge, OpIssue:
		default:
			return fmt.Errorf("config: cluster.routing.round_robin_ops contains %q; allowed: %q, %q, %q",
				op, OpReview, OpMerge, OpIssue)
		}
	}

	// Every id referenced anywhere must exist in the registry. Checked only
	// when the registry is populated so a worker that carries routing rules
	// (pushed by the hub) but no registry of its own still boots.
	if len(cl.Instances) > 0 {
		known := func(id, path string) error {
			// This daemon's own id always counts as known, even when the
			// operator has not written an entry for it: the runtime seeds one
			// (see ensureSelfInstance), so a rule that routes work to the hub
			// must not fail to persist just because config.toml is minimal.
			if id == cl.InstanceID && cl.InstanceID != "" {
				return nil
			}
			if _, ok := cl.Instances[id]; !ok {
				return fmt.Errorf("config: %s references unknown instance %q", path, id)
			}
			return nil
		}
		if cl.DefaultInstance != "" {
			if err := known(cl.DefaultInstance, "cluster.default_instance"); err != nil {
				return err
			}
		}
		for _, id := range cl.Routing.RoundRobinPool {
			if err := known(id, "cluster.routing.round_robin_pool"); err != nil {
				return err
			}
		}
		for org, id := range cl.Routing.Orgs {
			if err := ValidateOrgSlug(org); err != nil {
				return fmt.Errorf("config: cluster.routing.orgs key %q: %w", org, err)
			}
			if err := known(id, fmt.Sprintf("cluster.routing.orgs.%q", org)); err != nil {
				return err
			}
		}
		for repo, id := range cl.Routing.Repos {
			if err := ValidateRepoSlug(repo); err != nil {
				return fmt.Errorf("config: cluster.routing.repos key %q: %w", repo, err)
			}
			if err := known(id, fmt.Sprintf("cluster.routing.repos.%q", repo)); err != nil {
				return err
			}
		}
	}

	// A hub is an instance like any other — the UI lists it, routes repos to it
	// and reads its data through the same code path — but requiring an explicit
	// entry here would be a chicken-and-egg trap: registering the FIRST
	// instance would fail validation because the hub had not registered itself
	// yet. The runtime seeds a self entry instead (see ensureSelfInstance in
	// cmd/heimdallm), so the hub is always visible without the operator having
	// to describe their own machine to it.
	return nil
}
