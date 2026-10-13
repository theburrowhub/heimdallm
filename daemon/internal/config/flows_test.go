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
start = "a1"
[ai.flows.global.nodes.a1]
type = "agent"
agent = "gemini"

[ai.flows.night]
start = "a1"
[ai.flows.night.nodes.a1]
type = "agent"
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
	if id != DefaultFlowID || f.Start != "a10" || f.Nodes["a10"].Agent != "claude" || f.Nodes["a20"].Agent != "codex" {
		t.Errorf("legacy flow = %s %+v", id, f)
	}
	if cfg.FlowNameForRepo("acme/legacy") != "night" {
		t.Error("org flow applies to repos without their own")
	}
	all := cfg.AllFlows()
	if len(all) != 3 || all[DefaultFlowID].Nodes["a10"].Agent != "claude" {
		t.Errorf("AllFlows = %+v", all)
	}
}

func TestLegacyFlowSkipsDuplicateFallback(t *testing.T) {
	c := &Config{AI: AIConfig{Primary: "claude", Fallback: "claude"}}
	if f := c.legacyFlow(""); len(f.Nodes) != 1 {
		t.Errorf("legacy flow = %+v", f)
	}
	c.AI.Primary = ""
	c.AI.Fallback = ""
	if f := c.legacyFlow(""); len(f.Nodes) != 0 || f.Start != "" {
		t.Errorf("empty legacy flow = %+v", f)
	}
}

// Numerically equal keys sort by their text, so evaluation order never
// depends on map iteration.
func TestCompareKeys(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"9", "10", -1}, {"10", "9", 1}, {"10", "a", -1}, {"a", "10", 1},
		{"010", "10", -1}, {"10", "010", 1}, {"a", "b", -1}, {"b", "b", 0},
	} {
		if got := compareKeys(c.a, c.b); (got < 0) != (c.want < 0) || (got > 0) != (c.want > 0) {
			t.Errorf("compareKeys(%q, %q) = %d, want sign of %d", c.a, c.b, got, c.want)
		}
	}
}

func TestOrderKeysIsDeterministic(t *testing.T) {
	for i := 0; i < 20; i++ {
		if got := strings.Join(OrderKeys([]string{"10", "010", "+10", "b", "2"}), ","); got != "2,+10,010,10,b" {
			t.Fatalf("OrderKeys = %s", got)
		}
	}
}

func TestValidateFlows(t *testing.T) {
	node := func(extra string) string {
		return "[ai.flows.x]\nstart = \"c\"\n[ai.flows.x.nodes.c]\ntype = \"condition\"\nyes = \"a\"\n" + extra +
			"[ai.flows.x.nodes.a]\ntype = \"agent\"\nagent = \"claude\"\n"
	}
	bad := map[string]string{
		"bad id":       "[ai.flows.\"Bad Id\"]\nstart = \"a\"\n[ai.flows.\"Bad Id\".nodes.a]\ntype = \"agent\"\nagent = \"claude\"\n",
		"missing ref":  "flow = \"ghost\"\n",
		"org ref":      "[ai.orgs.acme]\nflow = \"ghost\"\n",
		"repo ref":     "[ai.repos.\"acme/api\"]\nflow = \"ghost\"\n",
		"quota agent":  node("[ai.flows.x.nodes.c.quota.q]\nagent = \"nope\"\nwindow = \"session\"\nop = \"below\"\npercent = 1\n"),
		"model window": node("[ai.flows.x.nodes.c.quota.q]\nagent = \"gemini\"\nwindow = \"model:\"\nop = \"below\"\npercent = 1\n"),
		"long name":    "[ai.flows.x]\nname = \"" + strings.Repeat("n", 121) + "\"\nstart = \"a\"\n[ai.flows.x.nodes.a]\ntype = \"agent\"\nagent = \"claude\"\n",
	}
	for name, body := range bad {
		if _, err := loadReviewLimitsTOML(t, body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := loadReviewLimitsTOML(t, "flow = \"default\"\n"+node("[ai.flows.x.nodes.c.quota.q]\nagent = \"gemini\"\nwindow = \"model:gemini-2.5-pro\"\nop = \"above\"\npercent = 10\n")); err != nil {
		t.Errorf("valid flow rejected: %v", err)
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
