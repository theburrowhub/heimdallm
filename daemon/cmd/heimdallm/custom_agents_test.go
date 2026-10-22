package main

import (
	"reflect"
	"testing"

	"github.com/heimdallm/daemon/internal/agentschema"
	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/flows"
)

func registerTestCustomAgent(t *testing.T) *agentschema.Schema {
	t.Helper()
	schema, err := agentschema.Parse([]byte(`{"fields":[
	  {"key":"model","type":"select","label":"Model","flag":"--model","allow_custom":true},
	  {"key":"fast","type":"bool","label":"Fast","flag":"--fast"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.SetCustomAgents([]executor.CustomAgent{{Name: "aider", Binary: "aider", Schema: schema}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executor.SetCustomAgents(nil) })
	return schema
}

func TestAgentExecOptionsCarryCustomValues(t *testing.T) {
	registerTestCustomAgent(t)
	cfg := config.CLIAgentConfig{Binary: "aider", Model: "m", Options: config.AgentOptions{"fast": true}}
	o := agentExecOptions("aider", cfg, "", "", false)
	if got := o.CustomValues.Map(); !reflect.DeepEqual(got, map[string]any{"fast": true}) || o.Model != "m" {
		t.Fatalf("options = %+v %v", o, got)
	}
	// The map handed to the executor is a copy.
	cfg.Options["fast"] = false
	if o.CustomValues.Map()["fast"] != true {
		t.Fatal("custom values must be copied")
	}
	if o := agentExecOptions("claude", config.CLIAgentConfig{}, "", "", false); o.CustomValues != nil {
		t.Fatal("built-in agents carry no custom values")
	}
	// Declared in the config but not (yet) registered: nothing to pass.
	if v := customAgentValues("other", config.CLIAgentConfig{Binary: "other"}); v != nil {
		t.Fatal("an unregistered agent gets no values")
	}
}

func TestAgentConfigMap(t *testing.T) {
	limits := &config.ReviewLimitsConfig{PerDay: 3}
	m := agentConfigMap(config.CLIAgentConfig{Model: "x", ExecutionTimeout: "5m", ReviewLimits: limits})
	if m["model"] != "x" || m["execution_timeout"] != "5m" || m["review_limits"] == nil {
		t.Fatalf("built-in map = %v", m)
	}
	if _, ok := m["binary"]; ok {
		t.Fatal("built-in agents have no binary")
	}
	m = agentConfigMap(config.CLIAgentConfig{Binary: "aider", Options: config.AgentOptions{"fast": true}})
	if m["binary"] != "aider" || !reflect.DeepEqual(m["options"], map[string]any{"fast": true}) {
		t.Fatalf("custom map = %v", m)
	}
}

func TestSameCustomAgents(t *testing.T) {
	schema, _ := agentschema.Parse([]byte(`{"fields":[]}`))
	a, b := &config.Config{}, &config.Config{}
	if !sameCustomAgents(a, b) {
		t.Fatal("two configs without custom agents are the same")
	}
	b.AI.Agents = map[string]config.CLIAgentConfig{"aider": {Binary: "aider", Schema: &config.AgentSchemaValue{Schema: schema}}}
	if sameCustomAgents(a, b) {
		t.Fatal("adding a custom agent is a change")
	}
}

func TestFlowListingIncludesCustomAgentsAsReviewOnly(t *testing.T) {
	registerTestCustomAgent(t)
	listing := flowListing(&config.Config{}, map[string]bool{"aider": true})
	var found *FlowAgent
	for _, a := range listing["agents"].([]FlowAgent) {
		if a.ID == "aider" {
			a := a
			found = &a
		}
	}
	if found == nil || !found.Installed || found.WriteCapable || found.CanDraft || found.ReviewOnly {
		t.Fatalf("aider listing = %+v", found)
	}
	if flows.CanDraft("aider") {
		t.Fatal("the assistant never drafts with a custom agent")
	}
}
