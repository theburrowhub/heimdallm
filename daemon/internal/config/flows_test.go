package config

import (
	"strings"
	"testing"
)

func TestFlowForRepo_ResolutionAndLegacy(t *testing.T) {
	cfg, err := loadReviewLimitsTOML(t, `
fallback = "codex"
flow = "global"

[ai.flows.global]
name = "Global"
[ai.flows.global.rules.10]
agent = "gemini"

[ai.flows.night.rules.1]
agent = "opencode"

[ai.orgs.acme]
flow = "night"

[ai.repos."acme/api"]
flow = "global"

[ai.repos."acme/legacy"]
primary = "copilot"
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for repo, want := range map[string]string{"acme/api": "global", "acme/web": "night", "other/x": "global"} {
		if id, _ := cfg.FlowForRepo(repo); id != want {
			t.Errorf("FlowForRepo(%s) = %s, want %s", repo, id, want)
		}
	}
	cfg.AI.Flow = ""
	id, f := cfg.FlowForRepo("other/x")
	if id != DefaultFlowID || f.Rules["10"].Agent != "claude" || f.Rules["20"].Agent != "codex" {
		t.Errorf("legacy flow = %s %+v", id, f)
	}
	if cfg.FlowNameForRepo("acme/legacy") != "night" {
		t.Error("org flow applies to repos without their own")
	}
	all := cfg.AllFlows()
	if len(all) != 3 || all[DefaultFlowID].Rules["10"].Agent != "claude" {
		t.Errorf("AllFlows = %+v", all)
	}
}

func TestLegacyFlowSkipsDuplicateFallback(t *testing.T) {
	c := &Config{AI: AIConfig{Primary: "claude", Fallback: "claude"}}
	if f := c.legacyFlow(""); len(f.Rules) != 1 {
		t.Errorf("legacy flow = %+v", f)
	}
	c.AI.Primary = ""
	c.AI.Fallback = ""
	if f := c.legacyFlow(""); len(f.Rules) != 0 {
		t.Errorf("empty legacy flow = %+v", f)
	}
}

func TestOrderedRuleKeys(t *testing.T) {
	f := FlowConfig{Rules: map[string]FlowRule{"100": {}, "9": {}, "b": {}, "20": {}, "a": {}}}
	if got := strings.Join(f.OrderedRuleKeys(), ","); got != "9,20,100,a,b" {
		t.Errorf("order = %s", got)
	}
}

func TestValidateFlows(t *testing.T) {
	bad := map[string]string{
		"bad id":       "[ai.flows.\"Bad Id\".rules.1]\nagent = \"claude\"\n",
		"missing ref":  "flow = \"ghost\"\n",
		"org ref":      "[ai.orgs.acme]\nflow = \"ghost\"\n",
		"repo ref":     "[ai.repos.\"acme/api\"]\nflow = \"ghost\"\n",
		"long key":     "[ai.flows.x.rules.\"12345678901234567\"]\nagent = \"claude\"\n",
		"quota agent":  "[ai.flows.x.rules.1]\nagent = \"claude\"\n[ai.flows.x.rules.1.quota.a]\nagent = \"nope\"\nwindow = \"session\"\nop = \"below\"\npercent = 1\n",
		"model window": "[ai.flows.x.rules.1]\nagent = \"claude\"\n[ai.flows.x.rules.1.quota.a]\nagent = \"gemini\"\nwindow = \"model:\"\nop = \"below\"\npercent = 1\n",
		"long name":    "[ai.flows.x]\nname = \"" + strings.Repeat("n", 121) + "\"\n[ai.flows.x.rules.1]\nagent = \"claude\"\n",
	}
	for name, body := range bad {
		if _, err := loadReviewLimitsTOML(t, body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := loadReviewLimitsTOML(t, "flow = \"default\"\n[ai.flows.x.rules.1]\nagent = \"gemini\"\n[ai.flows.x.rules.1.quota.a]\nagent = \"gemini\"\nwindow = \"model:gemini-2.5-pro\"\nop = \"above\"\npercent = 10\n"); err != nil {
		t.Errorf("valid flow rejected: %v", err)
	}
	many := FlowConfig{Rules: map[string]FlowRule{}}
	for i := 0; i <= maxRulesPerFlow; i++ {
		many.Rules[strings.Repeat("1", 1)+string(rune('a'+i%26))+string(rune('a'+i/26))] = FlowRule{Agent: "claude"}
	}
	if ValidateFlow("x", many) == nil {
		t.Error("too many rules accepted")
	}
	conds := FlowRule{Agent: "claude", Schedule: map[string]ScheduleCondition{}}
	for i := 0; i <= maxConditionsPerRu; i++ {
		conds.Schedule[string(rune('a'+i))] = ScheduleCondition{From: "01:00", To: "02:00"}
	}
	if ValidateFlow("x", FlowConfig{Rules: map[string]FlowRule{"1": conds}}) == nil {
		t.Error("too many conditions accepted")
	}
	c := &Config{AI: AIConfig{Flows: map[string]FlowConfig{}}}
	for i := 0; i <= maxFlows; i++ {
		c.AI.Flows[string(rune('a'+i%26))+string(rune('a'+i/26))] = FlowConfig{}
	}
	if c.validateFlows() == nil {
		t.Error("too many flows accepted")
	}
	if _, ok := Weekday(" Mon "); !ok {
		t.Error("weekday parsing must trim and ignore case")
	}
}
