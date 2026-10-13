package config

import "testing"

func TestLegacyFlowIsAGraph(t *testing.T) {
	c := &Config{}
	c.AI.Primary, c.AI.Fallback = "claude", "codex"
	f := c.legacyFlow("")
	if f.Start != "a10" || f.Nodes["a10"].Next != "a20" || f.Nodes["a20"].Agent != "codex" {
		t.Fatalf("legacy flow %#v", f)
	}
	if errs, _ := CheckFlow(DefaultFlowID, f); len(errs) != 0 {
		t.Fatalf("default flow invalid: %v", errs)
	}
}

func TestLegacyFlowSameAgentOnce(t *testing.T) {
	c := &Config{}
	c.AI.Primary, c.AI.Fallback = "claude", "claude"
	f := c.legacyFlow("")
	if f.Start != "a10" || len(f.Nodes) != 1 {
		t.Fatalf("legacy flow %#v", f)
	}
}

func TestFlowForRepoIgnoresFlowWithoutStart(t *testing.T) {
	c := &Config{}
	c.AI.Primary = "claude"
	c.AI.Flow = "empty"
	c.AI.Flows = map[string]FlowConfig{"empty": {Name: "Empty"}}
	id, f := c.FlowForRepo("o/r")
	if id != DefaultFlowID || f.Start != "a10" {
		t.Fatalf("id=%q flow=%#v", id, f)
	}
}
