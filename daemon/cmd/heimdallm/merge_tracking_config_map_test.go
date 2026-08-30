package main

// GET /config never emitted the merge_tracking section. The app PATCHed it
// successfully, the daemon stored and honoured it, and the settings screen read
// it back as defaults on the next load — so the toggle appeared to reset itself
// every time the operator left the screen and came back.

import (
	"testing"

	"github.com/heimdallm/daemon/internal/config"
)

func TestMergeTrackingConfigMap_CarriesEveryFieldBack(t *testing.T) {
	got := mergeTrackingConfigMap(config.MergeTrackingConfig{
		Enabled:            true,
		EnableAutoMerge:    true,
		UpdateBranch:       true,
		ResolveConflicts:   true,
		Merge:              true,
		MergeMethod:        "rebase",
		IncludeAssigned:    true,
		RequireApproval:    true,
		PollInterval:       "2m",
		MaxPRsPerTick:      5,
		MaxUpdateAttempts:  9,
		MaxResolveAttempts: 8,
		MaxMergeAttempts:   7,
		ActionCooldown:     "1m",
		ResolveTimeout:     "45m",
		ResolveEffort:      "max",
	})

	want := map[string]any{
		"enabled": true, "enable_auto_merge": true, "update_branch": true,
		"resolve_conflicts": true, "merge": true, "merge_method": "rebase",
		"include_assigned": true, "require_approval": true, "poll_interval": "2m",
		"max_prs_per_tick": 5, "max_update_attempts": 9,
		"max_resolve_attempts": 8, "max_merge_attempts": 7,
		"action_cooldown": "1m", "resolve_timeout": "45m", "resolve_effort": "max",
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("%s = %v, want %v", key, got[key], expected)
		}
	}
	// A settings screen that cannot read a field cannot render it, so a missing
	// key is the same failure as a wrong one.
	for _, key := range []string{"orgs", "repos"} {
		if _, ok := got[key]; !ok {
			t.Errorf("%s missing from the projection", key)
		}
	}
}

func TestMergeTrackingConfigMap_ProjectsOverrides(t *testing.T) {
	yes, no := true, false
	got := mergeTrackingConfigMap(config.MergeTrackingConfig{
		Enabled: true,
		Orgs: map[string]config.MergeTrackingOverride{
			"acme": {Enabled: &no},
		},
		Repos: map[string]config.MergeTrackingOverride{
			"acme/widgets": {Enabled: &yes, MergeMethod: "squash"},
		},
	})

	orgs := got["orgs"].(map[string]any)
	acme := orgs["acme"].(map[string]any)
	if acme["enabled"] != false {
		t.Errorf("org override = %v, want false", acme["enabled"])
	}
	repos := got["repos"].(map[string]any)
	widgets := repos["acme/widgets"].(map[string]any)
	if widgets["enabled"] != true || widgets["merge_method"] != "squash" {
		t.Errorf("repo override = %v", widgets)
	}
}

// Unset pointers must be omitted, not rendered as false: the client has to be
// able to tell "inherit" from "explicitly off", which is the whole point of the
// override shape.
func TestMergeTrackingOverrideMap_OmitsUnsetFields(t *testing.T) {
	got := mergeTrackingOverrideMap(config.MergeTrackingOverride{})
	if len(got) != 0 {
		t.Errorf("an empty override must project as {}, got %v", got)
	}

	off := false
	got = mergeTrackingOverrideMap(config.MergeTrackingOverride{Enabled: &off})
	if v, ok := got["enabled"]; !ok || v != false {
		t.Errorf("an explicit false must survive, got %v", got)
	}
	if _, ok := got["merge"]; ok {
		t.Error("an unset field must not appear at all")
	}
}

// Every field of the override has to survive the round trip, or a setting the
// operator made in config.toml would be invisible in the app.
func TestMergeTrackingOverrideMap_CarriesEveryField(t *testing.T) {
	yes := true
	nine := 9
	got := mergeTrackingOverrideMap(config.MergeTrackingOverride{
		Enabled:            &yes,
		EnableAutoMerge:    &yes,
		UpdateBranch:       &yes,
		ResolveConflicts:   &yes,
		Merge:              &yes,
		MergeMethod:        "rebase",
		IncludeAssigned:    &yes,
		RequireApproval:    &yes,
		MaxUpdateAttempts:  &nine,
		MaxResolveAttempts: &nine,
		MaxMergeAttempts:   &nine,
		ActionCooldown:     "1m",
		ResolveTimeout:     "45m",
		ResolveEffort:      "max",
	})

	want := map[string]any{
		"enabled": true, "enable_auto_merge": true, "update_branch": true,
		"resolve_conflicts": true, "merge": true, "merge_method": "rebase",
		"include_assigned": true, "require_approval": true,
		"max_update_attempts": 9, "max_resolve_attempts": 9,
		"max_merge_attempts": 9, "action_cooldown": "1m",
		"resolve_timeout": "45m", "resolve_effort": "max",
	}
	if len(got) != len(want) {
		t.Errorf("projected %d fields, want %d: %v", len(got), len(want), got)
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("%s = %v, want %v", key, got[key], expected)
		}
	}
}
