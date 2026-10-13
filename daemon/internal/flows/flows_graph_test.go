package flows

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
)

var monNight = time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC) // Monday 23:00 UTC
var monNoon = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func nightGraph() config.FlowConfig {
	return config.FlowConfig{Name: "night", Start: "c1", Nodes: map[string]config.FlowNode{
		"c1": {Type: config.NodeCondition, Yes: "a1", No: "a2",
			Schedule: map[string]config.ScheduleCondition{"s1": {From: "22:00", To: "06:00", TZ: "UTC"}}},
		"a1": {Type: config.NodeAgent, Agent: "codex", Next: "a2"},
		"a2": {Type: config.NodeAgent, Agent: "claude", Next: "a3"},
		"a3": {Type: config.NodeAgent, Agent: "codex"}, // duplicate: never added twice
	}}
}

func TestEvaluateWalksYesBranchAndFallback(t *testing.T) {
	d := Evaluate(context.Background(), "night", nightGraph(), monNight, nil, nil)
	if !reflect.DeepEqual(d.Candidates, []string{"codex", "claude"}) {
		t.Fatalf("candidates %v", d.Candidates)
	}
	var nodes []string
	for _, s := range d.Path {
		nodes = append(nodes, s.Node)
	}
	if !reflect.DeepEqual(nodes, []string{"c1", "a1", "a2", "a3"}) {
		t.Fatalf("path %v", nodes)
	}
	if d.Path[3].Added {
		t.Fatal("duplicate agent must not be added")
	}
}

func TestEvaluateNoBranch(t *testing.T) {
	d := Evaluate(context.Background(), "night", nightGraph(), monNoon, nil, nil)
	if !reflect.DeepEqual(d.Candidates, []string{"claude", "codex"}) {
		t.Fatalf("candidates %v", d.Candidates)
	}
	if *d.Path[0].Holds {
		t.Fatal("c1 must not hold at noon")
	}
}

func TestEvaluateSkipsUnavailable(t *testing.T) {
	avail := func(a string) bool { return a != "codex" }
	d := Evaluate(context.Background(), "night", nightGraph(), monNight, nil, avail)
	if !reflect.DeepEqual(d.Candidates, []string{"claude"}) {
		t.Fatalf("candidates %v", d.Candidates)
	}
	if *d.Path[1].Available || d.Path[1].Added {
		t.Fatalf("a1 step %+v", d.Path[1])
	}
}

func TestEvaluateFallbackIntoCondition(t *testing.T) {
	f := config.FlowConfig{Start: "a1", Nodes: map[string]config.FlowNode{
		"a1": {Type: config.NodeAgent, Agent: "claude", Next: "c1"},
		"c1": {Type: config.NodeCondition, Yes: "a2",
			Schedule: map[string]config.ScheduleCondition{"s1": {From: "22:00", To: "06:00", TZ: "UTC"}}},
		"a2": {Type: config.NodeAgent, Agent: "gemini"},
	}}
	d := Evaluate(context.Background(), "x", f, monNight, nil, nil)
	if !reflect.DeepEqual(d.Candidates, []string{"claude", "gemini"}) {
		t.Fatalf("candidates %v", d.Candidates)
	}
}

func TestEvaluateStopsOnCycleAndMissingNode(t *testing.T) {
	cyc := config.FlowConfig{Start: "a1", Nodes: map[string]config.FlowNode{
		"a1": {Type: config.NodeAgent, Agent: "claude", Next: "a2"},
		"a2": {Type: config.NodeAgent, Agent: "codex", Next: "a1"},
	}}
	d := Evaluate(context.Background(), "x", cyc, monNoon, nil, nil)
	if len(d.Path) != 3 || d.Path[2].Node != "a1" {
		t.Fatalf("cycle path %+v", d.Path)
	}
	missing := config.FlowConfig{Start: "a1", Nodes: map[string]config.FlowNode{
		"a1": {Type: config.NodeAgent, Agent: "claude", Next: "ghost"},
	}}
	d = Evaluate(context.Background(), "x", missing, monNoon, nil, nil)
	if len(d.Path) != 2 || d.Path[1].Node != "ghost" {
		t.Fatalf("missing path %+v", d.Path)
	}
}

func TestEvaluateEmptyFlow(t *testing.T) {
	d := Evaluate(context.Background(), "x", config.FlowConfig{}, monNoon, nil, nil)
	if len(d.Candidates) != 0 || len(d.Path) != 0 {
		t.Fatalf("%+v", d)
	}
}
