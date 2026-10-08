package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/harness/openrouter"
	"github.com/heimdallm/daemon/internal/quota"
	"github.com/heimdallm/daemon/internal/server"
)

func testFlowConfig() *config.Config {
	return &config.Config{AI: config.AIConfig{
		Primary:  "claude",
		Fallback: "codex",
		Agents: map[string]config.CLIAgentConfig{
			"copilot": {Model: "gpt-5.5", Effort: "high", ExtraFlags: "--allow-all-tools"},
		},
		Flows: map[string]config.FlowConfig{
			"mixed": {Name: "Mixed", Rules: map[string]config.FlowRule{
				"10": {Agent: "copilot"},
				"20": {Agent: "openrouter"},
				"30": {Agent: "gemini"},
				"40": {Agent: "copilot"},
			}},
		},
		Repos: map[string]config.RepoAI{"acme/api": {Flow: "mixed"}},
	}}
}

func TestFlowAgentsAndWriteAgents(t *testing.T) {
	c := testFlowConfig()
	_, f := c.FlowForRepo("acme/api")
	if got := strings.Join(flowAgents(f), ","); got != "copilot,openrouter,gemini" {
		t.Errorf("flowAgents = %s", got)
	}
	if got := strings.Join(flowWriteAgents(c, "acme/api"), ","); got != "gemini" {
		t.Errorf("flowWriteAgents = %s (read-only agents cannot resolve conflicts)", got)
	}
	if got := strings.Join(flowWriteAgents(c, "other/repo"), ","); got != "claude,codex" {
		t.Errorf("legacy write agents = %s", got)
	}
}

func TestAgentExecOptions(t *testing.T) {
	c := testFlowConfig()
	o := agentExecOptions("copilot", c.AgentConfigFor("copilot"), "30m", "/work", true)
	if o.Model != "gpt-5.5" || o.Effort != "high" || o.WorkDir != "/work" || o.Timeout != 30*time.Minute {
		t.Errorf("copilot options = %+v", o)
	}
	if o.ExtraFlags != "" {
		t.Errorf("a forbidden extra flag must be dropped, got %q", o.ExtraFlags)
	}
	if o := agentExecOptions("claude", config.CLIAgentConfig{}, "", "", true); o.MaxTurns != config.DefaultLimitedMaxTurns {
		t.Errorf("claude caps = %+v", o)
	}
}

func TestMergeTrackAgentSpecFollowsFlow(t *testing.T) {
	c := testFlowConfig()
	c.AI.Flows["writers"] = config.FlowConfig{Rules: map[string]config.FlowRule{"1": {Agent: "copilot"}, "2": {Agent: "opencode"}, "3": {Agent: "codex"}}}
	c.AI.Repos["acme/web"] = config.RepoAI{Flow: "writers"}
	spec := mergeTrackAgentSpec(&c, &sync.Mutex{})("acme/web")
	if spec.Primary != "opencode" || spec.Fallback != "codex" {
		t.Errorf("spec = %+v", spec)
	}
	spec = mergeTrackAgentSpec(&c, &sync.Mutex{})("acme/api")
	if spec.Primary != "gemini" || spec.Fallback != "" {
		t.Errorf("single writer spec = %+v", spec)
	}
}

type staticQuotas map[string]quota.Provider

func (s staticQuotas) Get(_ context.Context, agent string) quota.Provider { return s[agent] }

func TestFlowListingAndSimulation(t *testing.T) {
	c := testFlowConfig()
	listing := flowListing(c)
	if listing["selected"] != config.DefaultFlowID || len(listing["flows"].(map[string]config.FlowConfig)) != 2 {
		t.Errorf("listing = %+v", listing)
	}
	if !strings.Contains(strings.Join(listing["write_capable"].([]string), ","), "claude") {
		t.Errorf("write_capable = %v", listing["write_capable"])
	}
	c.AI.Flow = "mixed"
	if flowListing(c)["selected"] != "mixed" {
		t.Error("selected flow")
	}

	available := func(a string) bool { return a != "openrouter" }
	d, err := simulateFlow(context.Background(), c, server.FlowSimulation{Repo: "acme/api"}, staticQuotas{}, available)
	if err != nil || d.FlowID != "mixed" || strings.Join(d.Candidates, ",") != "copilot,gemini" {
		t.Errorf("simulate repo = %+v, %v", d, err)
	}
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	d, err = simulateFlow(context.Background(), c, server.FlowSimulation{Flow: config.DefaultFlowID, At: at}, staticQuotas{}, available)
	if err != nil || !d.At.Equal(at) || strings.Join(d.Candidates, ",") != "claude,codex" {
		t.Errorf("simulate default = %+v, %v", d, err)
	}
	if _, err := simulateFlow(context.Background(), c, server.FlowSimulation{Flow: "ghost"}, staticQuotas{}, available); err == nil {
		t.Error("an unknown flow must fail")
	}

	candidates := flowCandidates("mixed", c.AI.Flows["mixed"], "acme/api", staticQuotas{}, available)
	if got := strings.Join(candidates(), ","); got != "copilot,gemini" {
		t.Errorf("flowCandidates = %s", got)
	}
}

func TestNewQuotaServiceOpenRouter(t *testing.T) {
	keys := openrouter.NewKeyStore(t.TempDir())
	keys.Getenv = func(string) string { return "" }
	admin := &openrouter.Admin{Client: openrouter.NewClient(keys)}
	svc := newQuotaService(admin)
	if p := svc.Get(context.Background(), "openrouter"); p.Error != quota.ErrNotConfigured {
		t.Errorf("no key = %+v", p)
	}
	if err := keys.Set("sk-or-x"); err != nil {
		t.Fatal(err)
	}
	admin.Client.BaseURL = "http://127.0.0.1:1"
	svc = newQuotaService(admin)
	if p := svc.Get(context.Background(), "openrouter"); p.Error != quota.ErrUnavailable {
		t.Errorf("unreachable = %+v", p)
	}
	if p := newQuotaService(nil).Get(context.Background(), "openrouter"); p.Error != quota.ErrNotConfigured {
		t.Errorf("nil admin = %+v", p)
	}
}

func TestOverrideMapsCarryTheFlow(t *testing.T) {
	if got := repoAIOverrideMap(config.RepoAI{Flow: "night"}); got["flow"] != "night" {
		t.Errorf("repo override = %v", got)
	}
	if got := orgAIOverrideMap(config.OrgAI{Flow: "weekday"}); got["flow"] != "weekday" {
		t.Errorf("org override = %v", got)
	}
	if _, ok := repoAIOverrideMap(config.RepoAI{})["flow"]; ok {
		t.Error("no flow selected must not project one")
	}
}
