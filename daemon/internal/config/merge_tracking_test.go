package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
)

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

// A config that never mentions [merge_tracking] must leave every automation
// off: enabling the feature has to be a deliberate act.
func TestMergeTracking_AbsentSectionIsInert(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[ai]\nprimary = \"claude\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	mt := cfg.MergeTrackingForRepo("acme/widgets")
	for name, on := range map[string]bool{
		"enabled":           mt.Enabled,
		"enable_auto_merge": mt.EnableAutoMerge,
		"update_branch":     mt.UpdateBranch,
		"resolve_conflicts": mt.ResolveConflicts,
		"merge":             mt.Merge,
	} {
		if on {
			t.Errorf("%s defaulted to true; every automation must be opt-in", name)
		}
	}
	if mt.MergeMethod != config.MergeMethodSquash {
		t.Errorf("merge_method = %q, want squash", mt.MergeMethod)
	}
	if mt.MaxPRsPerTick != config.DefaultMergeTrackingMaxPRsPerTick {
		t.Errorf("max_prs_per_tick = %d, want the default", mt.MaxPRsPerTick)
	}
}

func TestMergeTrackingForRepo_PrecedenceIsRepoOverOrgOverGlobal(t *testing.T) {
	cfg := &config.Config{MergeTracking: config.MergeTrackingConfig{
		Enabled:     false,
		MergeMethod: "squash",
		Orgs: map[string]config.MergeTrackingOverride{
			"acme": {Enabled: boolPtr(true), UpdateBranch: boolPtr(true), MergeMethod: "merge"},
		},
		Repos: map[string]config.MergeTrackingOverride{
			"acme/widgets": {Merge: boolPtr(true), MergeMethod: "rebase"},
		},
	}}

	global := cfg.MergeTrackingForRepo("other/thing")
	if global.Enabled {
		t.Error("a repo outside the org must not inherit the org override")
	}

	org := cfg.MergeTrackingForRepo("acme/other")
	if !org.Enabled || !org.UpdateBranch {
		t.Errorf("org override not applied: %+v", org)
	}
	if org.MergeMethod != "merge" {
		t.Errorf("org merge_method = %q, want merge", org.MergeMethod)
	}

	repo := cfg.MergeTrackingForRepo("acme/widgets")
	if !repo.Enabled {
		t.Error("repo config should still inherit enabled from the org")
	}
	if !repo.UpdateBranch {
		t.Error("repo config should still inherit update_branch from the org")
	}
	if !repo.Merge {
		t.Error("repo override not applied")
	}
	if repo.MergeMethod != "rebase" {
		t.Errorf("repo merge_method = %q, want rebase to win over the org's", repo.MergeMethod)
	}
}

// A nil pointer means inherit, not false. Getting this backwards would let a
// repo override silently disable an org-wide setting.
func TestMergeTrackingOverride_NilPointerInherits(t *testing.T) {
	cfg := &config.Config{MergeTracking: config.MergeTrackingConfig{
		Enabled: true, Merge: true,
		Repos: map[string]config.MergeTrackingOverride{
			"acme/widgets": {UpdateBranch: boolPtr(true)},
		},
	}}
	got := cfg.MergeTrackingForRepo("acme/widgets")
	if !got.Enabled || !got.Merge {
		t.Errorf("unset override fields must inherit: %+v", got)
	}
	if !got.UpdateBranch {
		t.Error("the set field should apply")
	}
}

// An explicit false in an override must turn a global true off.
func TestMergeTrackingOverride_ExplicitFalseDisables(t *testing.T) {
	cfg := &config.Config{MergeTracking: config.MergeTrackingConfig{
		Enabled: true, Merge: true,
		Repos: map[string]config.MergeTrackingOverride{
			"acme/widgets": {Merge: boolPtr(false)},
		},
	}}
	if cfg.MergeTrackingForRepo("acme/widgets").Merge {
		t.Error("an explicit false must switch the inherited true off")
	}
}

func TestMergeTrackingOverride_IntOverridesApply(t *testing.T) {
	cfg := &config.Config{MergeTracking: config.MergeTrackingConfig{
		MaxUpdateAttempts: 3, MaxResolveAttempts: 2, MaxMergeAttempts: 3,
		Repos: map[string]config.MergeTrackingOverride{
			"acme/widgets": {MaxResolveAttempts: intPtr(0)},
		},
	}}
	got := cfg.MergeTrackingForRepo("acme/widgets")
	if got.MaxResolveAttempts != 0 {
		t.Errorf("max_resolve_attempts = %d, want 0 to disable retries for this repo", got.MaxResolveAttempts)
	}
	if got.MaxUpdateAttempts != 3 {
		t.Errorf("max_update_attempts = %d, want the inherited 3", got.MaxUpdateAttempts)
	}
}

// An invalid merge_method would otherwise surface as a 422 from GitHub on every
// merge attempt, once per cycle, forever. [autonomous] has that bug; this must
// not inherit it.
func TestValidate_RejectsInvalidMergeMethod(t *testing.T) {
	for _, scope := range []string{"global", "org", "repo"} {
		t.Run(scope, func(t *testing.T) {
			cfg := &config.Config{AI: config.AIConfig{Primary: "claude"}}
			switch scope {
			case "global":
				cfg.MergeTracking.MergeMethod = "rebase-and-pray"
			case "org":
				cfg.MergeTracking.Orgs = map[string]config.MergeTrackingOverride{
					"acme": {MergeMethod: "rebase-and-pray"},
				}
			case "repo":
				cfg.MergeTracking.Repos = map[string]config.MergeTrackingOverride{
					"acme/widgets": {MergeMethod: "rebase-and-pray"},
				}
			}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("an invalid merge_method must fail validation")
			}
			if !strings.Contains(err.Error(), "merge_method") {
				t.Errorf("error should name the field: %v", err)
			}
		})
	}
}

// Validate runs against literals that never went through applyDefaults, so an
// empty merge_method has to mean "use the default", not "invalid".
func TestValidate_EmptyMergeMethodIsAccepted(t *testing.T) {
	cfg := &config.Config{AI: config.AIConfig{Primary: "claude"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("an unset merge_method must be accepted: %v", err)
	}
}

func TestValidate_RejectsBadDurationsAndNegativeCaps(t *testing.T) {
	cases := map[string]func(*config.Config){
		"poll_interval":   func(c *config.Config) { c.MergeTracking.PollInterval = "soon" },
		"action_cooldown": func(c *config.Config) { c.MergeTracking.ActionCooldown = "-5m" },
		"resolve_timeout": func(c *config.Config) { c.MergeTracking.ResolveTimeout = "0s" },
		"negative cap":    func(c *config.Config) { c.MergeTracking.MaxMergeAttempts = -1 },
		"bad effort":      func(c *config.Config) { c.MergeTracking.ResolveEffort = "maximum" },
		"bad repo key": func(c *config.Config) {
			c.MergeTracking.Repos = map[string]config.MergeTrackingOverride{"not-a-slug": {}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{AI: config.AIConfig{Primary: "claude"}}
			mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("%s should fail validation", name)
			}
		})
	}
}

// A TOML round trip proves the section survives the canonical projection in
// canonical_map.go, which drops anything it does not recognise.
func TestMergeTracking_SurvivesTOMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	toml := `
[ai]
primary = "claude"

[merge_tracking]
enabled = true
enable_auto_merge = true
update_branch = true
resolve_conflicts = true
merge = true
merge_method = "rebase"
include_assigned = true
require_approval = true
poll_interval = "3m"
max_prs_per_tick = 5
max_update_attempts = 1
max_resolve_attempts = 1
max_merge_attempts = 1
action_cooldown = "7m"
resolve_timeout = "12m"
resolve_effort = "medium"

[merge_tracking.orgs."acme"]
merge = false

[merge_tracking.repos."acme/widgets"]
merge = true
merge_method = "squash"
`
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	global := cfg.MergeTracking
	if !global.Enabled || !global.EnableAutoMerge || !global.UpdateBranch ||
		!global.ResolveConflicts || !global.Merge {
		t.Errorf("toggles did not survive the round trip: %+v", global)
	}
	if global.MergeMethod != "rebase" || global.PollInterval != "3m" ||
		global.MaxPRsPerTick != 5 || global.ResolveEffort != "medium" {
		t.Errorf("scalars did not survive: %+v", global)
	}
	if !global.IncludeAssigned || !global.RequireApproval {
		t.Errorf("include_assigned/require_approval did not survive: %+v", global)
	}
	if got := cfg.MergeTrackingForRepo("acme/other"); got.Merge {
		t.Error("the org override should have switched merge off")
	}
	if got := cfg.MergeTrackingForRepo("acme/widgets"); !got.Merge || got.MergeMethod != "squash" {
		t.Errorf("repo override did not survive: %+v", got)
	}
}

// Drift guard: every scalar on MergeTrackingConfig must have a matching field
// on MergeTrackingOverride with the same toml name, or a field added later is
// silently not overridable per repo.
//
// PollInterval and MaxPRsPerTick are exempt: there is one reconciler loop, so
// a per-repo cadence or batch size has no meaning.
func TestMergeTrackingOverride_CoversEveryOverridableField(t *testing.T) {
	exempt := map[string]bool{
		"orgs": true, "repos": true,
		"poll_interval": true, "max_prs_per_tick": true,
	}

	overrideTags := map[string]bool{}
	ot := reflect.TypeOf(config.MergeTrackingOverride{})
	for i := 0; i < ot.NumField(); i++ {
		tag := strings.Split(ot.Field(i).Tag.Get("toml"), ",")[0]
		overrideTags[tag] = true
	}

	ct := reflect.TypeOf(config.MergeTrackingConfig{})
	for i := 0; i < ct.NumField(); i++ {
		tag := strings.Split(ct.Field(i).Tag.Get("toml"), ",")[0]
		if tag == "" || exempt[tag] {
			continue
		}
		if !overrideTags[tag] {
			t.Errorf("MergeTrackingConfig.%s (toml %q) has no counterpart on MergeTrackingOverride, "+
				"so it cannot be set per repo or per org", ct.Field(i).Name, tag)
		}
	}
}
