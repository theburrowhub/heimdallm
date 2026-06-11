package config

import "testing"

func TestAutonomousForRepo_Precedence(t *testing.T) {
	tru := true
	fls := false
	c := &Config{
		Autonomous: AutonomousConfig{
			Enabled:         false,
			AutoMerge:       false,
			MergeMethod:     "squash",
			TakeOthersTasks: true,
			ReassignOnTake:  true,
			DevMaxTurns:     0,
			DevEffort:       "high",
			DevTimeout:      "45m",
			Orgs: map[string]AutonomousOverride{
				// Org DISABLES autonomous and overrides merge_method. This lets
				// the repo case below prove repo beats an *opposing* org value,
				// and the org-only case prove org beats global.
				"acme": {Enabled: &fls, MergeMethod: "merge"},
			},
			Repos: map[string]AutonomousOverride{
				// Repo RE-ENABLES, directly opposing the org's disable.
				"acme/widget": {Enabled: &tru},
			},
		},
	}

	// Repo (true) must win over the opposing org (false).
	if got := c.AutonomousForRepo("acme/widget"); !got.Enabled {
		t.Errorf("repo override (enabled) should win over opposing org (disabled), got disabled")
	}
	// Org-only repo: org (false) must win over global (false-but-distinct intent).
	if got := c.AutonomousForRepo("acme/other"); got.Enabled {
		t.Errorf("org-only override (disabled) should win over global, got enabled")
	}
	// String override: org sets merge_method=merge; org-only repo must see it.
	// Exercises the empty-string-sentinel overlay branch for string fields.
	if got := c.AutonomousForRepo("acme/other"); got.MergeMethod != "merge" {
		t.Errorf("org string override should win over global: got merge_method %q", got.MergeMethod)
	}
	// Repo outside the org inherits global verbatim.
	if got := c.AutonomousForRepo("none/none"); got.Enabled {
		t.Errorf("unknown repo should inherit global (disabled)")
	}
	if got := c.AutonomousForRepo("none/none"); got.MergeMethod != "squash" || got.DevEffort != "high" {
		t.Errorf("scalars should inherit global: %+v", got)
	}
}

// TestAutonomousForRepo_OrgEnablesOverGlobal proves an org override can flip a
// disabled global to enabled for an org-only repo (the positive direction of
// the org>global edge, complementing the disable case above).
func TestAutonomousForRepo_OrgEnablesOverGlobal(t *testing.T) {
	tru := true
	c := &Config{
		Autonomous: AutonomousConfig{
			Enabled: false,
			Orgs:    map[string]AutonomousOverride{"acme": {Enabled: &tru}},
		},
	}
	if got := c.AutonomousForRepo("acme/other"); !got.Enabled {
		t.Errorf("org override (enabled) should win over global (disabled), got disabled")
	}
}

func TestAutonomousDefaults(t *testing.T) {
	c := &Config{}
	c.applyAutonomousDefaults()
	if c.Autonomous.MergeMethod != "squash" {
		t.Errorf("default merge_method want squash, got %q", c.Autonomous.MergeMethod)
	}
	if c.Autonomous.DevEffort != "high" {
		t.Errorf("default dev_effort want high, got %q", c.Autonomous.DevEffort)
	}
	if c.Autonomous.DevTimeout != "45m" {
		t.Errorf("default dev_timeout want 45m, got %q", c.Autonomous.DevTimeout)
	}
}
