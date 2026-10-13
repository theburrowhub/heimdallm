package config

import (
	"fmt"
	"strings"
	"testing"
)

func ptr(f float64) *float64 { return &f }

func validGraph() FlowConfig {
	return FlowConfig{
		Name:  "night",
		Start: "c1",
		Nodes: map[string]FlowNode{
			"c1": {Type: NodeCondition, Match: "any", Yes: "a1", No: "a2",
				Schedule: map[string]ScheduleCondition{"s1": {From: "22:00", To: "06:00"}}},
			"a1": {Type: NodeAgent, Agent: "codex", Next: "a2", X: ptr(10), Y: ptr(20)},
			"a2": {Type: NodeAgent, Agent: "claude"},
		},
	}
}

func TestCheckFlowAcceptsValidGraph(t *testing.T) {
	errs, warns := CheckFlow("night", validGraph())
	if len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("errs=%v warns=%v", errs, warns)
	}
}

func TestCheckFlowRejects(t *testing.T) {
	cases := map[string]struct {
		mutate func(*FlowConfig)
		node   string
		want   string
	}{
		"missing start": {func(f *FlowConfig) { f.Start = "zz" }, "", "start"},
		"empty start":   {func(f *FlowConfig) { f.Start = "" }, "", "start"},
		"dangling yes":  {func(f *FlowConfig) { n := f.Nodes["c1"]; n.Yes = "nope"; f.Nodes["c1"] = n }, "c1", "nope"},
		"dangling next": {func(f *FlowConfig) { n := f.Nodes["a1"]; n.Next = "nope"; f.Nodes["a1"] = n }, "a1", "nope"},
		"cycle":         {func(f *FlowConfig) { n := f.Nodes["a2"]; n.Next = "c1"; f.Nodes["a2"] = n }, "", "cycle"},
		"self loop":     {func(f *FlowConfig) { n := f.Nodes["a2"]; n.Next = "a2"; f.Nodes["a2"] = n }, "a2", "cycle"},
		"bad type":      {func(f *FlowConfig) { n := f.Nodes["a2"]; n.Type = "tool"; f.Nodes["a2"] = n }, "a2", "type"},
		"agent on cond": {func(f *FlowConfig) { n := f.Nodes["c1"]; n.Agent = "codex"; f.Nodes["c1"] = n }, "c1", "agent"},
		"yes on agent":  {func(f *FlowConfig) { n := f.Nodes["a1"]; n.Yes = "a2"; f.Nodes["a1"] = n }, "a1", "yes"},
		"unknown agent": {func(f *FlowConfig) { n := f.Nodes["a2"]; n.Agent = "skynet"; f.Nodes["a2"] = n }, "a2", "skynet"},
		"no conditions": {func(f *FlowConfig) { n := f.Nodes["c1"]; n.Schedule = nil; f.Nodes["c1"] = n }, "c1", "condition"},
		"bad schedule": {func(f *FlowConfig) {
			n := f.Nodes["c1"]
			n.Schedule = map[string]ScheduleCondition{"s1": {From: "25:00", To: "06:00"}}
			f.Nodes["c1"] = n
		}, "c1", "HH:MM"},
		"bad node id": {func(f *FlowConfig) { f.Nodes["Bad Id"] = FlowNode{Type: NodeAgent, Agent: "claude"} }, "Bad Id", "id"},
		"bad match":   {func(f *FlowConfig) { n := f.Nodes["c1"]; n.Match = "some"; f.Nodes["c1"] = n }, "c1", "match"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := validGraph()
			tc.mutate(&f)
			errs, _ := CheckFlow("night", f)
			if len(errs) == 0 {
				t.Fatal("expected an error")
			}
			found := false
			for _, e := range errs {
				if (tc.node == "" || e.Node == tc.node) && strings.Contains(e.Error(), tc.want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("no error on node %q mentioning %q in %v", tc.node, tc.want, errs)
			}
		})
	}
}

func TestCheckFlowNodeLimit(t *testing.T) {
	f := FlowConfig{Start: "n0", Nodes: map[string]FlowNode{}}
	for i := 0; i <= maxNodesPerFlow; i++ {
		f.Nodes[fmt.Sprintf("n%d", i)] = FlowNode{Type: NodeAgent, Agent: "claude"}
	}
	if errs, _ := CheckFlow("big", f); len(errs) == 0 {
		t.Fatal("expected node-limit error")
	}
}

func TestCheckFlowWarnsUnreachable(t *testing.T) {
	f := validGraph()
	f.Nodes["orphan"] = FlowNode{Type: NodeAgent, Agent: "gemini"}
	errs, warns := CheckFlow("night", f)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs %v", errs)
	}
	if len(warns) != 1 || warns[0].Node != "orphan" {
		t.Fatalf("warns=%v", warns)
	}
}

func TestCheckFlowEmptyGraphIsValid(t *testing.T) {
	if errs, _ := CheckFlow("empty", FlowConfig{Name: "x"}); len(errs) != 0 {
		t.Fatalf("errs=%v", errs)
	}
}

func TestCheckFlowConditionLimit(t *testing.T) {
	build := func(n int) FlowConfig {
		sched := map[string]ScheduleCondition{}
		for i := 0; i < n; i++ {
			sched[fmt.Sprintf("s%02d", i)] = ScheduleCondition{From: "01:00", To: "02:00"}
		}
		return FlowConfig{Start: "c1", Nodes: map[string]FlowNode{
			"c1": {Type: NodeCondition, Yes: "a1", Schedule: sched},
			"a1": {Type: NodeAgent, Agent: "claude"},
		}}
	}
	if errs, _ := CheckFlow("x", build(maxConditionsPerNode)); len(errs) != 0 {
		t.Fatalf("exactly the limit must be accepted: %v", errs)
	}
	errs, _ := CheckFlow("x", build(maxConditionsPerNode+1))
	if len(errs) != 1 || errs[0].Node != "c1" || !strings.Contains(errs[0].Message, "too many conditions") {
		t.Fatalf("errs=%v", errs)
	}
}

func TestCheckFlowAcceptsAllDaySchedule(t *testing.T) {
	f := validGraph()
	n := f.Nodes["c1"]
	n.Schedule = map[string]ScheduleCondition{"s1": {Days: []string{"sat", "sun"}, From: "00:00", To: "00:00"}}
	f.Nodes["c1"] = n
	if errs, warns := CheckFlow("weekend", f); len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("from == to means all day: errs=%v warns=%v", errs, warns)
	}
	n.Schedule = map[string]ScheduleCondition{"s1": {From: "0:00", To: "0:00"}}
	f.Nodes["c1"] = n
	if errs, _ := CheckFlow("weekend", f); len(errs) == 0 {
		t.Fatal("equal but malformed times must still be rejected")
	}
}
