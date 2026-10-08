package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadReviewLimitsTOML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "[ai]\nprimary = \"claude\"\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestReviewLimits_LoadAndResolveScopes(t *testing.T) {
	cfg, err := loadReviewLimitsTOML(t, `
[review_limits]
per_minute = 2
per_day = 100

[ai.orgs.acme.review_limits]
per_hour = 10

[ai.repos."acme/api".review_limits]
per_day = 5

[ai.repos."acme/quiet"]
review_mode = "single"

[ai.agents.claude.review_limits]
per_hour = 3
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	scopes := cfg.ReviewLimitScopesForRepo("acme/api")
	if len(scopes) != 3 {
		t.Fatalf("scopes for acme/api = %+v, want global+org+repo", scopes)
	}
	want := []ReviewLimitScope{
		{Kind: "global", Limits: ReviewLimitsConfig{PerMinute: 2, PerDay: 100}},
		{Kind: "org", Key: "acme", Limits: ReviewLimitsConfig{PerHour: 10}},
		{Kind: "repo", Key: "acme/api", Limits: ReviewLimitsConfig{PerDay: 5}},
	}
	for i := range want {
		if scopes[i] != want[i] {
			t.Errorf("scope[%d] = %+v, want %+v", i, scopes[i], want[i])
		}
	}

	if got := cfg.ReviewLimitScopesForRepo("acme/quiet"); len(got) != 2 {
		t.Errorf("acme/quiet has no repo budget, want global+org, got %+v", got)
	}
	if got := cfg.ReviewLimitScopesForRepo("other/repo"); len(got) != 1 || got[0].Kind != "global" {
		t.Errorf("other/repo should only see the global budget, got %+v", got)
	}

	agent := cfg.AgentReviewLimits("claude")
	if agent == nil || agent.Limits.PerHour != 3 || agent.Kind != "agent" {
		t.Errorf("claude budget = %+v, want per_hour 3", agent)
	}
	if cfg.AgentReviewLimits("codex") != nil {
		t.Error("codex has no budget configured")
	}

	all := cfg.AllReviewLimitScopes()
	var kinds []string
	for _, s := range all {
		kinds = append(kinds, s.Kind+":"+s.Key)
	}
	if got := strings.Join(kinds, ","); got != "global:,org:acme,repo:acme/api,agent:claude" {
		t.Errorf("AllReviewLimitScopes = %s", got)
	}
}

func TestReviewLimits_EmptyMeansUnlimited(t *testing.T) {
	cfg, err := loadReviewLimitsTOML(t, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.ReviewLimitScopesForRepo("acme/api"); len(got) != 0 {
		t.Errorf("no [review_limits] must mean no budgets, got %+v", got)
	}
	if got := cfg.AllReviewLimitScopes(); len(got) != 0 {
		t.Errorf("AllReviewLimitScopes = %+v, want none", got)
	}
}

func TestReviewLimits_RejectsOutOfRange(t *testing.T) {
	for name, body := range map[string]string{
		"global negative": "[review_limits]\nper_minute = -1\n",
		"global too big":  "[review_limits]\nper_day = 100001\n",
		"org":             "[ai.orgs.acme.review_limits]\nper_hour = -2\n",
		"repo":            "[ai.repos.\"acme/api\".review_limits]\nper_day = -3\n",
		"agent":           "[ai.agents.claude.review_limits]\nper_minute = -4\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadReviewLimitsTOML(t, body); err == nil || !strings.Contains(err.Error(), "review_limits") {
				t.Fatalf("err = %v, want a review_limits validation error", err)
			}
		})
	}
}
