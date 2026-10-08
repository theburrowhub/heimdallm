package main

import (
	"testing"

	"github.com/heimdallm/daemon/internal/config"
)

func TestLimitedExploration(t *testing.T) {
	cases := []struct {
		cli        string
		turns      int
		effort     string
		limit      bool
		wantTurns  int
		wantEffort string
		wantSoft   bool
	}{
		{"claude", 0, "", true, config.DefaultLimitedMaxTurns, config.DefaultLimitedEffort, true},
		{"claude", 5, "high", true, 5, "high", false},
		{"claude", 0, "", false, 0, "", false},
		{"codex", 0, "", true, 0, "", false},
	}
	for _, c := range cases {
		turns, effort, soft := limitedExploration(c.cli, c.turns, c.effort, c.limit)
		if turns != c.wantTurns || effort != c.wantEffort || soft != c.wantSoft {
			t.Errorf("%+v → (%d, %q, %v)", c, turns, effort, soft)
		}
	}
}

func TestTokenSavingProjections(t *testing.T) {
	cfg := &config.Config{}
	m := resolvedTokenSavingMap(cfg.TokenSavingForRepo(""))
	if m["incremental_diff"] != true || m["noise_globs_default"] != true {
		t.Errorf("global projection = %v", m)
	}
	off := false
	if got := tokenSavingOverrideMap(&config.TokenSavingConfig{CompactPrompt: &off, NoiseGlobs: []string{"x"}}); got["compact_prompt"] != false || got["noise_globs"] == nil || len(got) != 2 {
		t.Errorf("override projection = %v", got)
	}
	if tokenSavingOverrideMap(nil) != nil || tokenSavingOverrideMap(&config.TokenSavingConfig{}) != nil {
		t.Error("an empty override must project to nil")
	}
	ts := pipelineTokenSaving(config.ResolvedTokenSaving{IncrementalDiff: true, NoiseGlobs: []string{"a"}})
	if !ts.IncrementalDiff || ts.FilterNoise || len(ts.NoiseGlobs) != 1 {
		t.Errorf("pipelineTokenSaving = %+v", ts)
	}
}

func TestOverrideMapsExposeTokenSaving(t *testing.T) {
	on := true
	if m := repoAIOverrideMap(config.RepoAI{TokenSaving: &config.TokenSavingConfig{FilterNoise: &on}}); m["token_saving"] == nil {
		t.Errorf("repo override map is missing token_saving: %v", m)
	}
	if m := orgAIOverrideMap(config.OrgAI{TokenSaving: &config.TokenSavingConfig{FilterNoise: &on}}); m["token_saving"] == nil {
		t.Errorf("org override map is missing token_saving: %v", m)
	}
}
