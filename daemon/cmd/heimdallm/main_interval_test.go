package main

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
)

func TestParseDiscoveryIntervalFallsBackToPollInterval(t *testing.T) {
	got := parseDiscoveryInterval("", "1m")
	if got != time.Minute {
		t.Fatalf("parseDiscoveryInterval(empty, 1m) = %v, want 1m", got)
	}
}

func TestParseDiscoveryIntervalPreservesExplicitInterval(t *testing.T) {
	got := parseDiscoveryInterval("30m", "1m")
	if got != 30*time.Minute {
		t.Fatalf("parseDiscoveryInterval(30m, 1m) = %v, want 30m", got)
	}
}

func TestParseDiscoveryIntervalNegativeFallsBackToPollInterval(t *testing.T) {
	got := parseDiscoveryInterval("-5m", "1m")
	if got != time.Minute {
		t.Fatalf("parseDiscoveryInterval(-5m, 1m) = %v, want 1m", got)
	}
}

func TestParseDiscoveryIntervalUsesPollDefaultWhenBothInvalid(t *testing.T) {
	want := parsePollInterval("nope")
	got := parseDiscoveryInterval("", "nope")
	if got != want {
		t.Fatalf("parseDiscoveryInterval(empty, invalid) = %v, want %v", got, want)
	}
}

func TestResolveRefinementTimeoutPrecedence(t *testing.T) {
	got := resolveRefinementTimeout("30m", "20m", "5m")
	if got != 30*time.Minute {
		t.Fatalf("resolveRefinementTimeout(30m, 20m, 5m) = %v, want 30m", got)
	}
}

func TestResolveRefinementTimeoutFallsBackToAgentThenGlobal(t *testing.T) {
	got := resolveRefinementTimeout("", "20m", "5m")
	if got != 5*time.Minute {
		t.Fatalf("resolveRefinementTimeout(empty, 20m, 5m) = %v, want 5m", got)
	}

	got = resolveRefinementTimeout("", "20m", "")
	if got != 20*time.Minute {
		t.Fatalf("resolveRefinementTimeout(empty, 20m, empty) = %v, want 20m", got)
	}
}

func TestResolveExecutionTimeoutDefaultsToTwentyMinutes(t *testing.T) {
	if got := resolveExecutionTimeout("", ""); got != 20*time.Minute {
		t.Fatalf("resolveExecutionTimeout(empty, empty) = %v, want 20m", got)
	}
}

func TestResolveExecutionTimeoutPrecedence(t *testing.T) {
	if got := resolveExecutionTimeout("20m", "30m"); got != 30*time.Minute {
		t.Fatalf("resolveExecutionTimeout(20m, 30m) = %v, want 30m", got)
	}
	if got := resolveExecutionTimeout("30m", ""); got != 30*time.Minute {
		t.Fatalf("resolveExecutionTimeout(30m, empty) = %v, want 30m", got)
	}
}

func TestConfigReloadRequiresPollerRestartSkipsDynamicOnlyChanges(t *testing.T) {
	oldCfg := reloadRestartBaseConfig()
	newCfg := cloneReloadRestartConfig(oldCfg)
	newCfg.AI.Primary = "codex"
	newCfg.AI.ReviewMode = "multi"
	newCfg.GitHub.IssueTracking.DefaultAction = "review_only"

	if configReloadRequiresPollerRestart(oldCfg, newCfg) {
		t.Fatal("dynamic AI/issue config changes should not restart pollers")
	}
}

func TestConfigReloadRequiresPollerRestartForCadenceAndRepoStream(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{
			name: "poll interval",
			mutate: func(c *config.Config) {
				c.GitHub.PollInterval = "2m"
			},
		},
		{
			name: "discovery interval",
			mutate: func(c *config.Config) {
				c.GitHub.DiscoveryInterval = "10m"
			},
		},
		{
			name: "rename probe interval",
			mutate: func(c *config.Config) {
				c.AI.RepoRenameCheckInterval = "2h"
			},
		},
		{
			name: "repositories",
			mutate: func(c *config.Config) {
				c.GitHub.Repositories = append(c.GitHub.Repositories, "org/repo2")
			},
		},
		{
			name: "non monitored",
			mutate: func(c *config.Config) {
				c.GitHub.NonMonitored = append(c.GitHub.NonMonitored, "org/off2")
			},
		},
		{
			name: "discovery topic",
			mutate: func(c *config.Config) {
				c.GitHub.DiscoveryTopic = "new-topic"
			},
		},
		{
			name: "discovery orgs",
			mutate: func(c *config.Config) {
				c.GitHub.DiscoveryOrgs = append(c.GitHub.DiscoveryOrgs, "other-org")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldCfg := reloadRestartBaseConfig()
			newCfg := cloneReloadRestartConfig(oldCfg)
			tc.mutate(newCfg)
			if !configReloadRequiresPollerRestart(oldCfg, newCfg) {
				t.Fatal("expected poller restart")
			}
		})
	}
}

func TestConfigReloadRequiresPollerRestartTreatsNilAndEmptySlicesAsSame(t *testing.T) {
	oldCfg := reloadRestartBaseConfig()
	oldCfg.GitHub.Repositories = nil
	oldCfg.GitHub.NonMonitored = nil
	oldCfg.GitHub.DiscoveryOrgs = nil

	newCfg := cloneReloadRestartConfig(oldCfg)
	newCfg.GitHub.Repositories = []string{}
	newCfg.GitHub.NonMonitored = []string{}
	newCfg.GitHub.DiscoveryOrgs = []string{}

	if configReloadRequiresPollerRestart(oldCfg, newCfg) {
		t.Fatal("nil and empty repo/discovery slices should not restart pollers")
	}
}

func TestConfigReloadRequiresPollerRestartDefaultsUnknownFieldsToRestart(t *testing.T) {
	oldCfg := reloadRestartBaseConfig()
	newCfg := cloneReloadRestartConfig(oldCfg)
	newCfg.Server.MaxConcurrentWorkers = oldCfg.Server.MaxConcurrentWorkers + 1

	if !configReloadRequiresPollerRestart(oldCfg, newCfg) {
		t.Fatal("non-dynamic fields should restart pollers by default")
	}
}

func reloadRestartBaseConfig() *config.Config {
	return &config.Config{
		GitHub: config.GitHubConfig{
			PollInterval:      "1m",
			Repositories:      []string{"org/repo1"},
			NonMonitored:      []string{"org/off1"},
			DiscoveryTopic:    "heimdallm-review",
			DiscoveryOrgs:     []string{"org"},
			DiscoveryInterval: "5m",
			IssueTracking: config.IssueTrackingConfig{
				DefaultAction: "ignore",
			},
		},
		AI: config.AIConfig{
			Primary:                 "claude",
			ReviewMode:              "single",
			RepoRenameCheckInterval: "1h",
		},
	}
}

func cloneReloadRestartConfig(c *config.Config) *config.Config {
	clone := *c
	clone.GitHub.Repositories = append([]string(nil), c.GitHub.Repositories...)
	clone.GitHub.NonMonitored = append([]string(nil), c.GitHub.NonMonitored...)
	clone.GitHub.DiscoveryOrgs = append([]string(nil), c.GitHub.DiscoveryOrgs...)
	return &clone
}
