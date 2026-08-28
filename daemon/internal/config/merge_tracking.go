package config

import (
	"fmt"

	"github.com/heimdallm/daemon/internal/executor"
)

// Merge method values accepted by GitHub's merge and auto-merge APIs.
const (
	MergeMethodSquash = "squash"
	MergeMethodMerge  = "merge"
	MergeMethodRebase = "rebase"
)

// Defaults for [merge_tracking]. Every boolean deliberately keeps Go's zero
// value (false) so the whole feature is opt-in: a config that does not mention
// [merge_tracking] at all never writes to a repository.
const (
	DefaultMergeTrackingMergeMethod        = MergeMethodSquash
	DefaultMergeTrackingMaxPRsPerTick      = 20
	DefaultMergeTrackingMaxUpdateAttempts  = 3
	DefaultMergeTrackingMaxResolveAttempts = 2
	DefaultMergeTrackingMaxMergeAttempts   = 3
	DefaultMergeTrackingActionCooldown     = "10m"
	DefaultMergeTrackingResolveTimeout     = "30m"
	DefaultMergeTrackingResolveEffort      = "high"
)

// MergeTrackingConfig configures the merge-readiness reconciler for PRs the
// authenticated user authored or is assigned to. It is resolved per repo via
// MergeTrackingForRepo (repo > org > global).
//
// Deliberately separate from AutonomousConfig: [autonomous] governs PRs the
// agent itself opened from issues, while [merge_tracking] governs the human's
// own PRs. Sharing one section would mean every decision needed a flag to tell
// the two origins apart, and enabling one would drag in the other's risks.
type MergeTrackingConfig struct {
	Enabled bool `toml:"enabled"` // master switch / kill-switch

	// The four automation levels. Each is independent: resolving conflicts
	// without merging, or updating branches without merging, are valid setups.
	EnableAutoMerge  bool `toml:"enable_auto_merge"` // arm GitHub's native auto-merge
	UpdateBranch     bool `toml:"update_branch"`     // bring out-of-date branches up to date
	ResolveConflicts bool `toml:"resolve_conflicts"` // run the agent on merge conflicts
	Merge            bool `toml:"merge"`             // merge once every requirement is met

	MergeMethod     string `toml:"merge_method"`     // squash|merge|rebase
	IncludeAssigned bool   `toml:"include_assigned"` // also track PRs merely assigned to the user
	RequireApproval bool   `toml:"require_approval"` // demand APPROVED even when branch protection does not

	PollInterval string `toml:"poll_interval"` // empty inherits [polling]/[github].poll_interval

	MaxPRsPerTick      int    `toml:"max_prs_per_tick"`     // API-cost bound per cycle
	MaxUpdateAttempts  int    `toml:"max_update_attempts"`  // per PR, per head SHA
	MaxResolveAttempts int    `toml:"max_resolve_attempts"` // per PR, per head SHA
	MaxMergeAttempts   int    `toml:"max_merge_attempts"`   // per PR, per head SHA
	ActionCooldown     string `toml:"action_cooldown"`      // between write actions on one PR
	ResolveTimeout     string `toml:"resolve_timeout"`      // wall clock for the conflict agent
	ResolveEffort      string `toml:"resolve_effort"`       // agent effort: low|medium|high|max

	Orgs  map[string]MergeTrackingOverride `toml:"orgs"`  // [merge_tracking.orgs."org"]
	Repos map[string]MergeTrackingOverride `toml:"repos"` // [merge_tracking.repos."org/repo"]
}

// MergeTrackingOverride is the per-org / per-repo override shape. Pointer
// fields are nil when unset (inherit); set fields replace the inherited value.
// Strings use the empty value as "inherit", matching AutonomousOverride.
type MergeTrackingOverride struct {
	Enabled            *bool  `toml:"enabled,omitempty"`
	EnableAutoMerge    *bool  `toml:"enable_auto_merge,omitempty"`
	UpdateBranch       *bool  `toml:"update_branch,omitempty"`
	ResolveConflicts   *bool  `toml:"resolve_conflicts,omitempty"`
	Merge              *bool  `toml:"merge,omitempty"`
	MergeMethod        string `toml:"merge_method,omitempty"`
	IncludeAssigned    *bool  `toml:"include_assigned,omitempty"`
	RequireApproval    *bool  `toml:"require_approval,omitempty"`
	MaxUpdateAttempts  *int   `toml:"max_update_attempts,omitempty"`
	MaxResolveAttempts *int   `toml:"max_resolve_attempts,omitempty"`
	MaxMergeAttempts   *int   `toml:"max_merge_attempts,omitempty"`
	ActionCooldown     string `toml:"action_cooldown,omitempty"`
	ResolveTimeout     string `toml:"resolve_timeout,omitempty"`
	ResolveEffort      string `toml:"resolve_effort,omitempty"`
}

// MergeTrackingForRepo resolves merge-tracking config for a repo:
// repo > org > global. PollInterval and MaxPRsPerTick are intentionally
// global-only — they bound the poller as a whole, not a single repo.
func (c *Config) MergeTrackingForRepo(repo string) MergeTrackingConfig {
	out := c.MergeTracking
	if org := repoOrg(repo); org != "" && c.MergeTracking.Orgs != nil {
		if o, ok := c.MergeTracking.Orgs[org]; ok {
			applyMergeTrackingOverride(&out, o)
		}
	}
	if c.MergeTracking.Repos != nil {
		if r, ok := c.MergeTracking.Repos[repo]; ok {
			applyMergeTrackingOverride(&out, r)
		}
	}
	return out
}

func applyMergeTrackingOverride(out *MergeTrackingConfig, o MergeTrackingOverride) {
	if o.Enabled != nil {
		out.Enabled = *o.Enabled
	}
	if o.EnableAutoMerge != nil {
		out.EnableAutoMerge = *o.EnableAutoMerge
	}
	if o.UpdateBranch != nil {
		out.UpdateBranch = *o.UpdateBranch
	}
	if o.ResolveConflicts != nil {
		out.ResolveConflicts = *o.ResolveConflicts
	}
	if o.Merge != nil {
		out.Merge = *o.Merge
	}
	if o.MergeMethod != "" {
		out.MergeMethod = o.MergeMethod
	}
	if o.IncludeAssigned != nil {
		out.IncludeAssigned = *o.IncludeAssigned
	}
	if o.RequireApproval != nil {
		out.RequireApproval = *o.RequireApproval
	}
	if o.MaxUpdateAttempts != nil {
		out.MaxUpdateAttempts = *o.MaxUpdateAttempts
	}
	if o.MaxResolveAttempts != nil {
		out.MaxResolveAttempts = *o.MaxResolveAttempts
	}
	if o.MaxMergeAttempts != nil {
		out.MaxMergeAttempts = *o.MaxMergeAttempts
	}
	if o.ActionCooldown != "" {
		out.ActionCooldown = o.ActionCooldown
	}
	if o.ResolveTimeout != "" {
		out.ResolveTimeout = o.ResolveTimeout
	}
	if o.ResolveEffort != "" {
		out.ResolveEffort = o.ResolveEffort
	}
}

// applyMergeTrackingDefaults fills zero-value scalars with safe defaults.
//
// The booleans are deliberately left alone: Go's zero value (false) IS the
// default, so a fresh install never touches a repository until the operator
// flips a flag. RequireApproval and IncludeAssigned are the two exceptions
// where `true` would be the safer/more useful default, but a bool cannot
// distinguish "unset" from "explicitly false" here — so they stay false and
// the documentation states it. Operators who want them set them.
func (c *Config) applyMergeTrackingDefaults() {
	if c.MergeTracking.MergeMethod == "" {
		c.MergeTracking.MergeMethod = DefaultMergeTrackingMergeMethod
	}
	if c.MergeTracking.MaxPRsPerTick == 0 {
		c.MergeTracking.MaxPRsPerTick = DefaultMergeTrackingMaxPRsPerTick
	}
	if c.MergeTracking.MaxUpdateAttempts == 0 {
		c.MergeTracking.MaxUpdateAttempts = DefaultMergeTrackingMaxUpdateAttempts
	}
	if c.MergeTracking.MaxResolveAttempts == 0 {
		c.MergeTracking.MaxResolveAttempts = DefaultMergeTrackingMaxResolveAttempts
	}
	if c.MergeTracking.MaxMergeAttempts == 0 {
		c.MergeTracking.MaxMergeAttempts = DefaultMergeTrackingMaxMergeAttempts
	}
	if c.MergeTracking.ActionCooldown == "" {
		c.MergeTracking.ActionCooldown = DefaultMergeTrackingActionCooldown
	}
	if c.MergeTracking.ResolveTimeout == "" {
		c.MergeTracking.ResolveTimeout = DefaultMergeTrackingResolveTimeout
	}
	if c.MergeTracking.ResolveEffort == "" {
		c.MergeTracking.ResolveEffort = DefaultMergeTrackingResolveEffort
	}
}

// ValidateMergeMethod reports whether method is one GitHub accepts. Exported
// because both the config validator and the merge-readiness evaluator need it
// (the evaluator also checks the repo actually allows that method).
//
// An empty method is valid and means "use the default": applyDefaults fills it
// with squash. This mirrors validatePositiveDuration and keeps Validate usable
// on a Config literal that has not been through applyDefaults — which is how
// most of the config test suite calls it.
func ValidateMergeMethod(path, method string) error {
	switch method {
	case "", MergeMethodSquash, MergeMethodMerge, MergeMethodRebase:
		return nil
	default:
		return fmt.Errorf("config: %s %q must be one of %q, %q, %q (or empty for the default)",
			path, method, MergeMethodSquash, MergeMethodMerge, MergeMethodRebase)
	}
}

// validateMergeTracking enforces the [merge_tracking] invariants. Unlike
// [autonomous].merge_method — which is historically unvalidated — an invalid
// merge_method here is a hard config error: it would otherwise surface as a
// 422 from GitHub on every merge attempt, once per poll cycle, forever.
func (c *Config) validateMergeTracking() error {
	mt := c.MergeTracking

	if err := ValidateMergeMethod("merge_tracking.merge_method", mt.MergeMethod); err != nil {
		return err
	}
	if err := executor.ValidateEffort(mt.ResolveEffort); err != nil {
		return fmt.Errorf("config: merge_tracking.resolve_effort: %w", err)
	}
	if err := validatePositiveDuration("merge_tracking.poll_interval", mt.PollInterval); err != nil {
		return err
	}
	if err := validatePositiveDuration("merge_tracking.action_cooldown", mt.ActionCooldown); err != nil {
		return err
	}
	if err := validatePositiveDuration("merge_tracking.resolve_timeout", mt.ResolveTimeout); err != nil {
		return err
	}
	for _, f := range []struct {
		path string
		val  int
	}{
		{"merge_tracking.max_prs_per_tick", mt.MaxPRsPerTick},
		{"merge_tracking.max_update_attempts", mt.MaxUpdateAttempts},
		{"merge_tracking.max_resolve_attempts", mt.MaxResolveAttempts},
		{"merge_tracking.max_merge_attempts", mt.MaxMergeAttempts},
	} {
		if f.val < 0 {
			return fmt.Errorf("config: %s %d must not be negative", f.path, f.val)
		}
	}

	// Overrides are validated with the same rules, so a per-repo typo fails at
	// boot rather than at the first merge attempt on that repo.
	for org, o := range mt.Orgs {
		if err := validateMergeTrackingOverride(fmt.Sprintf("merge_tracking.orgs.%q", org), o); err != nil {
			return err
		}
	}
	for repo, o := range mt.Repos {
		if err := ValidateRepoSlug(repo); err != nil {
			return fmt.Errorf("config: merge_tracking.repos key %q: %w", repo, err)
		}
		if err := validateMergeTrackingOverride(fmt.Sprintf("merge_tracking.repos.%q", repo), o); err != nil {
			return err
		}
	}
	return nil
}

func validateMergeTrackingOverride(path string, o MergeTrackingOverride) error {
	if err := ValidateMergeMethod(path+".merge_method", o.MergeMethod); err != nil {
		return err
	}
	if o.ResolveEffort != "" {
		if err := executor.ValidateEffort(o.ResolveEffort); err != nil {
			return fmt.Errorf("config: %s.resolve_effort: %w", path, err)
		}
	}
	if err := validatePositiveDuration(path+".action_cooldown", o.ActionCooldown); err != nil {
		return err
	}
	if err := validatePositiveDuration(path+".resolve_timeout", o.ResolveTimeout); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		val  *int
	}{
		{"max_update_attempts", o.MaxUpdateAttempts},
		{"max_resolve_attempts", o.MaxResolveAttempts},
		{"max_merge_attempts", o.MaxMergeAttempts},
	} {
		if f.val != nil && *f.val < 0 {
			return fmt.Errorf("config: %s.%s %d must not be negative", path, f.name, *f.val)
		}
	}
	return nil
}
