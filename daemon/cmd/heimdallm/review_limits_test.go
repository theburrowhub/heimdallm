package main

import (
	"testing"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/pipeline"
)

func TestReviewBudgetsFor(t *testing.T) {
	c := &config.Config{
		ReviewLimits: config.ReviewLimitsConfig{PerMinute: 1},
		AI: config.AIConfig{
			Orgs:  map[string]config.OrgAI{"acme": {ReviewLimits: &config.ReviewLimitsConfig{PerHour: 4}}},
			Repos: map[string]config.RepoAI{"acme/api": {ReviewLimits: &config.ReviewLimitsConfig{PerDay: 9}}},
			Agents: map[string]config.CLIAgentConfig{
				"claude": {ReviewLimits: &config.ReviewLimitsConfig{PerHour: 2}},
				"codex":  {Model: "o3"},
			},
		},
	}
	b := reviewBudgetsFor(c, "acme/api")
	want := []pipeline.ReviewBudgetScope{
		{Kind: "global", Limits: pipeline.ReviewWindowLimits{PerMinute: 1}},
		{Kind: "org", Key: "acme", Limits: pipeline.ReviewWindowLimits{PerHour: 4}},
		{Kind: "repo", Key: "acme/api", Limits: pipeline.ReviewWindowLimits{PerDay: 9}},
	}
	if len(b.Scopes) != len(want) {
		t.Fatalf("scopes = %+v", b.Scopes)
	}
	for i := range want {
		if b.Scopes[i] != want[i] {
			t.Errorf("scope[%d] = %+v, want %+v", i, b.Scopes[i], want[i])
		}
	}
	if len(b.Agents) != 1 || b.Agents["claude"].PerHour != 2 {
		t.Errorf("agents = %+v, want only claude per_hour 2", b.Agents)
	}

	if b := reviewBudgetsFor(&config.Config{}, "acme/api"); len(b.Scopes) != 0 || b.Agents != nil {
		t.Errorf("no limits configured: %+v", b)
	}
}

func TestReviewLimitsMap(t *testing.T) {
	got := reviewLimitsMap(config.ReviewLimitsConfig{PerHour: 3})
	if got["per_minute"] != 0 || got["per_hour"] != 3 || got["per_day"] != 0 {
		t.Errorf("reviewLimitsMap = %v", got)
	}
}

func TestOverrideMapsExposeReviewLimits(t *testing.T) {
	limits := &config.ReviewLimitsConfig{PerDay: 7}
	if m := repoAIOverrideMap(config.RepoAI{ReviewLimits: limits}); m["review_limits"] == nil {
		t.Errorf("repo override map is missing review_limits: %v", m)
	}
	if m := orgAIOverrideMap(config.OrgAI{ReviewLimits: limits}); m["review_limits"] == nil {
		t.Errorf("org override map is missing review_limits: %v", m)
	}
	if m := orgAIOverrideMap(config.OrgAI{}); m["review_limits"] != nil {
		t.Errorf("unset org review_limits must be omitted: %v", m)
	}
}
