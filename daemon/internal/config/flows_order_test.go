package config

import (
	"reflect"
	"testing"
)

func TestTopoAgentOrder(t *testing.T) {
	f := FlowConfig{Start: "c1", Nodes: map[string]FlowNode{
		"c1":     {Type: NodeCondition, Yes: "a1", No: "a2", Schedule: map[string]ScheduleCondition{"s": {From: "01:00", To: "02:00"}}},
		"a1":     {Type: NodeAgent, Agent: "codex", Next: "a2"},
		"a2":     {Type: NodeAgent, Agent: "claude", Next: "a3"},
		"a3":     {Type: NodeAgent, Agent: "codex"},
		"orphan": {Type: NodeAgent, Agent: "gemini"},
	}}
	if got := TopoAgentOrder(f); !reflect.DeepEqual(got, []string{"codex", "claude"}) {
		t.Fatalf("got %v", got)
	}
}
