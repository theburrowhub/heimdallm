# Review Flow Graph Editor + AI Assistant Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn review flows into decision graphs edited on a visual canvas, replacing the rule-list format outright (no users yet, so no migration), and add an assistant that drafts a flow from plain language using an installed CLI agent.

**Architecture:** The daemon's `config.FlowConfig` becomes `{name, start, nodes}` with `condition` (yes/no) and `agent` (fallback `next`) nodes; `flows.Evaluate` walks the graph and still returns an ordered candidate list, so the review pipeline is untouched. The rule-list format is deleted; there is no migration (amended 2026-10-13). `POST /flows/assist` runs the chosen agent via `ExecuteRaw` in an empty temp dir, validates its JSON with the same checker as `PUT`, and returns an unsaved draft. The Flutter editor is an own canvas (`InteractiveViewer` + positioned node widgets + `CustomPainter` edges) driven by a Riverpod notifier with undo/redo and a draft overlay.

**Tech Stack:** Go 1.25 (daemon, chi, BurntSushi/toml), Go CLI module (cobra), Flutter (Riverpod 3, go_router 17, mix 2, http).

**Spec:** `docs/superpowers/specs/2026-10-12-flow-graph-editor-design.md`

## Global Constraints

- Daemon Go tests run ONLY via `make test-docker` (narrow with `GO_TEST_ARGS="-run X ./internal/y/..."`). Never `go test`/`go build` for the daemon on the host.
- CLI tests: `make test-cli` and `make lint-cli` (host is fine).
- Flutter gate: `cd flutter_app && flutter test && flutter analyze`, then `make build-web` from the repo root.
- No `dart:io`, `Platform`, `File`, desktop plugins under `lib/features/` or `lib/shared/`.
- The CLI must not import daemon packages; it talks to the daemon only through `cli/internal/api`.
- Conventional commits (`feat:`, `fix:`, `test:`, `docs:`, `refactor:`), each ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Limits: max 50 flows, **200 nodes per flow**, 20 conditions per condition node; node and flow ids match `^[a-z0-9][a-z0-9_-]{0,63}$`.
- Assistant: instruction 1–4000 runes; timeout **180 s**; one repair round; one assist at a time per daemon; raw excerpt in errors capped at **2 KB**; OpenRouter is not a valid assistant agent.
- Undo stack capped at 100 snapshots.
- Coverage ratchet (`make test-coverage-gate`) must not drop.

## Review Focus

1. A selected flow saved without a `start` (or with no nodes) → `FlowForRepo` falls back to the built-in primary/fallback flow instead of reviewing with nobody (test in Task 3).
2. Assistant output wrapped in prose or a ```json fence, or with ids the model invented inconsistently (missing `start`, edge to a missing node) → fence stripped; inconsistency goes through the repair round, then `422` with node-pinned issues (tests in Task 5).
3. `POST /flows/simulate` with an **unsaved graph containing a cycle** → `422`, never an endless walk; `Evaluate` itself also stops on a revisited node (tests in Tasks 3 and 4).
4. Instruction of exactly 4000 multi-byte runes accepted, 4001 rejected (counted in runes, not bytes) (test in Task 5).
5. Deleting a node in the editor that is `start` or the target of edges → incoming edges cleared and `start` reset, never a dangling edge sent to the daemon (test in Task 9).

---

## File Map

Daemon:
- Modify `daemon/internal/config/flows.go` — graph types, `CheckFlow`, `ValidateFlow`, `legacyFlow`, `FlowForRepo`; rule-list types deleted (Task 3).
- Create `daemon/internal/config/flows_map.go` — `FlowToMap`.
- Modify `daemon/internal/config/config.go` (`Load`), `daemon/internal/config/writer.go` (`ValidateMap`).
- Modify `daemon/internal/flows/flows.go` — graph walk, `PathStep`.
- Create `daemon/internal/flows/assist.go`, `daemon/internal/flows/assist_prompt.go`.
- Modify `daemon/internal/server/flows.go`, `daemon/internal/server/handlers.go` (route), `daemon/internal/server/server.go` (fields).
- Modify `daemon/cmd/heimdallm/flows.go`, `daemon/cmd/heimdallm/merge_tracking_runner.go`, `daemon/cmd/heimdallm/main.go`.

CLI:
- Modify `cli/internal/api/flows.go`, `cli/internal/cli/flows.go` (+ tests).

Flutter:
- Rewrite `flutter_app/lib/core/models/flow.dart`; modify `flutter_app/lib/core/api/api_client.dart`.
- Create `flutter_app/lib/features/flows/graph/{graph_validation,auto_layout,flow_diff}.dart`.
- Create `flutter_app/lib/features/flows/editor/{flow_editor_controller,flow_editor_screen,palette,test_panel}.dart`, `editor/inspector/{flow_inspector,condition_inspector,agent_inspector}.dart`.
- Create `flutter_app/lib/features/flows/canvas/{flow_canvas,node_widgets,edge_painter,canvas_geometry}.dart`.
- Create `flutter_app/lib/features/flows/assistant/assist_panel.dart`.
- Create `flutter_app/lib/features/flows/flows_list_screen.dart`; delete `flutter_app/lib/features/flows/flows_screen.dart`; modify `flutter_app/lib/shared/router.dart`.

Docs: `docs/configuration-guide.md` (Review flows section).

---

### Task 1: Graph types and validation (daemon config)

**Files:**
- Modify: `daemon/internal/config/flows.go`
- Test: `daemon/internal/config/flows_graph_test.go`

**Interfaces:**
- Produces:
  - `type FlowConfig struct { Name string; Start string; Nodes map[string]FlowNode; Rules map[string]FlowRule /* json:"-" */ }`
  - `type FlowNode struct { Type, Match string; Quota map[string]QuotaCondition; Schedule map[string]ScheduleCondition; Yes, No, Agent, Next string; X, Y *float64 }`
  - `const NodeCondition = "condition"; const NodeAgent = "agent"`
  - `type FlowIssue struct { Node, Field, Message string }` with `Error() string`
  - `func CheckFlow(id string, f FlowConfig) (errs, warns []FlowIssue)`
  - `func ValidateFlow(id string, f FlowConfig) error` (first error of `CheckFlow`, prefixed `config: ai.flows.<id>`)
  - `func (n FlowNode) Edges() []string` (non-empty `yes`, `no`, `next` in that order)
  - `func (f FlowConfig) SortedNodeIDs() []string`

- [ ] **Step 1: Write the failing tests**

```go
package config

import (
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
		"missing start":     {func(f *FlowConfig) { f.Start = "zz" }, "", "start"},
		"empty start":       {func(f *FlowConfig) { f.Start = "" }, "", "start"},
		"dangling yes":      {func(f *FlowConfig) { n := f.Nodes["c1"]; n.Yes = "nope"; f.Nodes["c1"] = n }, "c1", "nope"},
		"dangling next":     {func(f *FlowConfig) { n := f.Nodes["a1"]; n.Next = "nope"; f.Nodes["a1"] = n }, "a1", "nope"},
		"cycle":             {func(f *FlowConfig) { n := f.Nodes["a2"]; n.Next = "c1"; f.Nodes["a2"] = n }, "", "cycle"},
		"self loop":         {func(f *FlowConfig) { n := f.Nodes["a2"]; n.Next = "a2"; f.Nodes["a2"] = n }, "a2", "cycle"},
		"bad type":          {func(f *FlowConfig) { n := f.Nodes["a2"]; n.Type = "tool"; f.Nodes["a2"] = n }, "a2", "type"},
		"agent on cond":     {func(f *FlowConfig) { n := f.Nodes["c1"]; n.Agent = "codex"; f.Nodes["c1"] = n }, "c1", "agent"},
		"yes on agent":      {func(f *FlowConfig) { n := f.Nodes["a1"]; n.Yes = "a2"; f.Nodes["a1"] = n }, "a1", "yes"},
		"unknown agent":     {func(f *FlowConfig) { n := f.Nodes["a2"]; n.Agent = "skynet"; f.Nodes["a2"] = n }, "a2", "skynet"},
		"no conditions":     {func(f *FlowConfig) { n := f.Nodes["c1"]; n.Schedule = nil; f.Nodes["c1"] = n }, "c1", "condition"},
		"bad schedule":      {func(f *FlowConfig) { n := f.Nodes["c1"]; n.Schedule = map[string]ScheduleCondition{"s1": {From: "25:00", To: "06:00"}}; f.Nodes["c1"] = n }, "c1", "HH:MM"},
		"bad node id":       {func(f *FlowConfig) { f.Nodes["Bad Id"] = FlowNode{Type: NodeAgent, Agent: "claude"} }, "Bad Id", "id"},
		"bad match":         {func(f *FlowConfig) { n := f.Nodes["c1"]; n.Match = "some"; f.Nodes["c1"] = n }, "c1", "match"},
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
```

Add `"fmt"` to the imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test-docker GO_TEST_ARGS="-run 'TestCheckFlow' ./internal/config/..."`
Expected: FAIL — `undefined: CheckFlow`, `undefined: NodeCondition`.

- [ ] **Step 3: Implement**

In `daemon/internal/config/flows.go` replace the `FlowConfig` type and its doc comment with:

```go
// FlowConfig is a review flow: a decision graph that picks which agents
// review a PR, in order. Evaluation starts at Start; condition nodes branch
// on schedule and quota, agent nodes add their agent to the candidates and
// continue through their fallback edge. Nodes are a map keyed by node id
// because TOML cannot hold slices of tables in this schema.
type FlowConfig struct {
	Name  string              `toml:"name" json:"name"`
	Start string              `toml:"start,omitempty" json:"start"`
	Nodes map[string]FlowNode `toml:"nodes,omitempty" json:"nodes"`
	// Rules is the pre-graph format. It is read only to migrate (see
	// MigrateLegacyFlow) and never written back; it is not part of the API.
	Rules map[string]FlowRule `toml:"rules,omitempty" json:"-"`
}

// Node types.
const (
	NodeCondition = "condition"
	NodeAgent     = "agent"
)

// FlowNode is one step of a flow graph. A condition node uses Match, Quota,
// Schedule, Yes and No; an agent node uses Agent and Next (its fallback).
// An empty edge ends the walk. X and Y are the editor's canvas position.
type FlowNode struct {
	Type     string                       `toml:"type" json:"type"`
	Match    string                       `toml:"match,omitempty" json:"match,omitempty"`
	Quota    map[string]QuotaCondition    `toml:"quota,omitempty" json:"quota,omitempty"`
	Schedule map[string]ScheduleCondition `toml:"schedule,omitempty" json:"schedule,omitempty"`
	Yes      string                       `toml:"yes,omitempty" json:"yes,omitempty"`
	No       string                       `toml:"no,omitempty" json:"no,omitempty"`
	Agent    string                       `toml:"agent,omitempty" json:"agent,omitempty"`
	Next     string                       `toml:"next,omitempty" json:"next,omitempty"`
	X        *float64                     `toml:"x,omitempty" json:"x,omitempty"`
	Y        *float64                     `toml:"y,omitempty" json:"y,omitempty"`
}

// Edges lists the node's outgoing edges (yes, no, next) that are set.
func (n FlowNode) Edges() []string {
	var out []string
	for _, e := range []string{n.Yes, n.No, n.Next} {
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// SortedNodeIDs returns the node ids in string order, so validation and
// error messages never depend on map iteration.
func (f FlowConfig) SortedNodeIDs() []string {
	ids := make([]string, 0, len(f.Nodes))
	for id := range f.Nodes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// FlowIssue is one validation finding, pinned to a node when it has one so
// the editor can show it on the canvas.
type FlowIssue struct {
	Node    string `json:"node,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (i FlowIssue) Error() string {
	switch {
	case i.Node != "" && i.Field != "":
		return fmt.Sprintf("nodes.%s.%s: %s", i.Node, i.Field, i.Message)
	case i.Node != "":
		return fmt.Sprintf("nodes.%s: %s", i.Node, i.Message)
	case i.Field != "":
		return fmt.Sprintf("%s: %s", i.Field, i.Message)
	}
	return i.Message
}
```

Change the limits block to:

```go
const (
	maxFlows           = 50
	maxRulesPerFlow    = 50
	maxNodesPerFlow    = 200
	maxConditionsPerRu = 20
)
```

Replace `ValidateFlow` with the following (it keeps the legacy-rule checks needed by the migration in `validateLegacyRules`, which is the old body unchanged except for the function name and signature):

```go
// ValidateFlow checks one flow and returns its first error.
func ValidateFlow(id string, f FlowConfig) error {
	if errs, _ := CheckFlow(id, f); len(errs) > 0 {
		return fmt.Errorf("config: ai.flows.%s: %s", id, errs[0].Error())
	}
	return nil
}

// CheckFlow validates a flow graph. Errors make the flow unusable; warnings
// (unreachable nodes) are shown by the editor and ignored by evaluation.
func CheckFlow(id string, f FlowConfig) (errs, warns []FlowIssue) {
	add := func(node, field, format string, a ...any) {
		errs = append(errs, FlowIssue{Node: node, Field: field, Message: fmt.Sprintf(format, a...)})
	}
	if len(f.Name) > 120 {
		add("", "name", "is longer than 120 characters")
	}
	if len(f.Nodes) > maxNodesPerFlow {
		add("", "nodes", "has %d nodes, at most %d allowed", len(f.Nodes), maxNodesPerFlow)
		return errs, warns
	}
	if len(f.Nodes) == 0 {
		if f.Start != "" {
			add("", "start", "names %q but the flow has no nodes", f.Start)
		}
		return errs, warns
	}
	if f.Start == "" {
		add("", "start", "must name the first node")
	} else if _, ok := f.Nodes[f.Start]; !ok {
		add("", "start", "names %q, which is not a node", f.Start)
	}
	ids := f.SortedNodeIDs()
	for _, nid := range ids {
		n := f.Nodes[nid]
		if !flowIDPattern.MatchString(nid) {
			add(nid, "", "node id must be 1-64 characters of a-z, 0-9, '-' or '_'")
		}
		edge := func(field, target string) {
			if target == "" {
				return
			}
			if _, ok := f.Nodes[target]; !ok {
				add(nid, field, "points at %q, which is not a node", target)
			}
		}
		switch n.Type {
		case NodeCondition:
			if n.Agent != "" {
				add(nid, "agent", "a condition has no agent")
			}
			if n.Next != "" {
				add(nid, "next", "a condition branches with yes/no, not next")
			}
			if !validMatchModes[n.Match] {
				add(nid, "match", "must be all or any")
			}
			total := len(n.Quota) + len(n.Schedule)
			if total == 0 {
				add(nid, "", "a condition needs at least one schedule or quota condition")
			}
			if total > maxConditionsPerRu {
				add(nid, "", "has too many conditions (max %d)", maxConditionsPerRu)
			}
			for _, ck := range OrderKeys(mapKeys(n.Quota)) {
				if err := validateQuotaCondition(n.Quota[ck]); err != nil {
					add(nid, "quota."+ck, "%s", err)
				}
			}
			for _, ck := range OrderKeys(mapKeys(n.Schedule)) {
				if err := validateScheduleCondition(n.Schedule[ck]); err != nil {
					add(nid, "schedule."+ck, "%s", err)
				}
			}
			edge("yes", n.Yes)
			edge("no", n.No)
		case NodeAgent:
			if n.Match != "" || len(n.Quota) > 0 || len(n.Schedule) > 0 {
				add(nid, "", "an agent step has no conditions; put them in a condition step")
			}
			if n.Yes != "" {
				add(nid, "yes", "an agent step continues through next, not yes")
			}
			if n.No != "" {
				add(nid, "no", "an agent step continues through next, not no")
			}
			if err := executor.ValidateCLIName(n.Agent); err != nil {
				add(nid, "agent", "%s", err)
			}
			edge("next", n.Next)
		default:
			add(nid, "type", "must be condition or agent, not %q", n.Type)
		}
	}
	if cyc := findCycleNode(f, ids); cyc != "" {
		add(cyc, "", "this step is part of a cycle; a flow must never loop back")
	}
	reach := reachable(f)
	for _, nid := range ids {
		if !reach[nid] {
			warns = append(warns, FlowIssue{Node: nid, Message: "not reachable from the start; it is ignored"})
		}
	}
	return errs, warns
}

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// findCycleNode returns a node on a cycle ("" when the graph is acyclic).
// Edges to missing nodes are ignored here; CheckFlow reports them.
func findCycleNode(f FlowConfig, ids []string) string {
	const (
		white = iota
		grey
		black
	)
	colour := map[string]int{}
	var visit func(id string) string
	visit = func(id string) string {
		colour[id] = grey
		for _, next := range f.Nodes[id].Edges() {
			if _, ok := f.Nodes[next]; !ok {
				continue
			}
			switch colour[next] {
			case grey:
				return next
			case white:
				if c := visit(next); c != "" {
					return c
				}
			}
		}
		colour[id] = black
		return ""
	}
	for _, id := range ids {
		if colour[id] == white {
			if c := visit(id); c != "" {
				return c
			}
		}
	}
	return ""
}

// reachable marks the nodes reachable from Start.
func reachable(f FlowConfig) map[string]bool {
	seen := map[string]bool{}
	stack := []string{f.Start}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n, ok := f.Nodes[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		stack = append(stack, n.Edges()...)
	}
	return seen
}

func validateQuotaCondition(q QuotaCondition) error {
	if err := executor.ValidateCLIName(q.Agent); err != nil {
		return fmt.Errorf("agent: %w", err)
	}
	if !validQuotaWin[q.Window] && !(strings.HasPrefix(q.Window, modelWindowPfx) && len(q.Window) > len(modelWindowPfx) && len(q.Window) <= 128) {
		return fmt.Errorf("window must be session, weekly, monthly, credit, any or model:<id>")
	}
	if !validQuotaOps[q.Op] {
		return fmt.Errorf("op must be below or above")
	}
	if q.Percent < 0 || q.Percent > 100 {
		return fmt.Errorf("percent must be between 0 and 100")
	}
	return nil
}

func validateScheduleCondition(sc ScheduleCondition) error {
	if !hhmmPattern.MatchString(sc.From) || !hhmmPattern.MatchString(sc.To) {
		return fmt.Errorf("from/to must be HH:MM")
	}
	if sc.From == sc.To {
		return fmt.Errorf("from and to must differ")
	}
	for _, d := range sc.Days {
		if _, ok := Weekday(d); !ok {
			return fmt.Errorf("days: %q is not mon..sun", d)
		}
	}
	if sc.TZ != "" {
		if _, err := time.LoadLocation(sc.TZ); err != nil {
			return fmt.Errorf("tz: unknown time zone %q", sc.TZ)
		}
	}
	return nil
}

// validateLegacyRules checks a pre-graph flow before migrating it, so a
// broken legacy file fails with the rule path the operator wrote.
func validateLegacyRules(id string, f FlowConfig) error {
	path := "ai.flows." + id
	if len(f.Rules) > maxRulesPerFlow {
		return fmt.Errorf("config: %s has %d rules, at most %d allowed", path, len(f.Rules), maxRulesPerFlow)
	}
	for _, key := range f.OrderedRuleKeys() {
		r := f.Rules[key]
		rp := path + ".rules." + key
		if len(key) == 0 || len(key) > 16 {
			return fmt.Errorf("config: %s: rule key must be 1-16 characters", rp)
		}
		if err := executor.ValidateCLIName(r.Agent); err != nil {
			return fmt.Errorf("config: %s.agent: %w", rp, err)
		}
		if !validMatchModes[r.Match] {
			return fmt.Errorf("config: %s.match must be all or any", rp)
		}
		if len(r.Quota)+len(r.Schedule) > maxConditionsPerRu {
			return fmt.Errorf("config: %s has too many conditions (max %d)", rp, maxConditionsPerRu)
		}
		for _, ck := range OrderKeys(mapKeys(r.Quota)) {
			if err := validateQuotaCondition(r.Quota[ck]); err != nil {
				return fmt.Errorf("config: %s.quota.%s: %w", rp, ck, err)
			}
		}
		for _, ck := range OrderKeys(mapKeys(r.Schedule)) {
			if err := validateScheduleCondition(r.Schedule[ck]); err != nil {
				return fmt.Errorf("config: %s.schedule.%s: %w", rp, ck, err)
			}
		}
	}
	return nil
}
```

`validateLegacyRules` is used by Task 2. Existing tests in `daemon/internal/config/flows_test.go` that build rule-based flows and call `ValidateFlow` must be moved to call `validateLegacyRules` (same assertions; the error text keeps the `ai.flows.<id>.rules.<key>` prefix). `FlowForRepo`/`legacyFlow` still compile against `Rules` until Task 2.

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test-docker GO_TEST_ARGS="-run 'TestCheckFlow|TestValidateFlow|TestOrder' ./internal/config/..."`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/config/flows.go daemon/internal/config/flows_graph_test.go daemon/internal/config/flows_test.go
git commit -m "feat(flows): decision-graph flow model and validation

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 2: (removed)

Amended 2026-10-13 at the user's request: review flows have no users yet, so there is **no migration**. The rule-list format is deleted outright in Task 3 (together with the evaluator rewrite, so every commit compiles). No `MigrateLegacyFlow`, no `MigrateFlowsFile`, no `.bak` backup, no property test against the old evaluator.

---

### Task 3: Graph evaluator, trace and agent listings; rule-list format deleted

**Files:**
- Modify: `daemon/internal/config/flows.go` (delete `FlowRule`, `FlowConfig.Rules`, `OrderedRuleKeys`, `maxRulesPerFlow`, `validateLegacyRules` and the `len(f.Rules) > 0` bridge in `validateFlows`; rewrite `FlowForRepo` and `legacyFlow`)
- Create: `daemon/internal/config/flows_map.go` (`FlowToMap`, moved from server's `flowToMap`)
- Modify: `daemon/internal/server/flows.go` (use `config.FlowToMap`; PUT's "needs at least one rule" check becomes "needs a start node": `if flow.Start == "" || len(flow.Nodes) == 0`)
- Modify: `daemon/internal/flows/flows.go`
- Modify: `daemon/cmd/heimdallm/flows.go` (`flowAgents`, `flowCandidates` logging)
- Modify: `daemon/cmd/heimdallm/merge_tracking_runner.go` (`flowWriteAgents`)
- Create: `daemon/internal/config/flows_order.go` (`TopoAgentOrder`)
- Test: `daemon/internal/flows/flows_graph_test.go`, `daemon/internal/config/flows_order_test.go`, `daemon/internal/config/flows_default_test.go`

**Interfaces:**
- Consumes: Task 1 types.
- Produces:
  - `type PathStep struct { Node, Type, Agent string; Holds, Available *bool; Added bool; Reasons []string }`
  - `Decision{FlowID, FlowName, At, Candidates, Path []PathStep}` (`Rules` field removed)
  - `func Evaluate(ctx, id string, f config.FlowConfig, now time.Time, q QuotaReader, available func(string) bool) Decision` (same signature)
  - `func config.TopoAgentOrder(f FlowConfig) []string` — distinct agents in topological order (Kahn, ties by node id), reachable nodes only.
  - `func config.FlowToMap(f FlowConfig) (map[string]any, error)` (moved from server's `flowToMap`).
  - `config.FlowConfig` has no `Rules` field any more; `legacyFlow` (the built-in default) is a graph `a10` → `a20`.

- [ ] **Step 1: Write the failing tests**

`daemon/internal/config/flows_default_test.go`:

```go
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
```

Check the real field name that selects the global flow (`c.AI.Flow` above) in `daemon/internal/config` before running and adjust the test to it.

`daemon/internal/flows/flows_graph_test.go`:

```go
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
```

`daemon/internal/config/flows_order_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test-docker GO_TEST_ARGS="-run 'TestEvaluate|TestTopoAgentOrder|TestLegacyFlow|TestFlowForRepoIgnores' ./internal/flows/... ./internal/config/..."`
Expected: FAIL — `d.Path undefined`, `matches` signature mismatch, `undefined: TopoAgentOrder`, `legacyFlow` still rule-based.

- [ ] **Step 3: Implement**

In `daemon/internal/flows/flows.go` replace `RuleResult`, `Decision`, `Evaluate` and `matches`' signature:

```go
// PathStep is one node visited while evaluating a flow.
type PathStep struct {
	Node      string   `json:"node"`
	Type      string   `json:"type"`
	Agent     string   `json:"agent,omitempty"`
	Holds     *bool    `json:"holds,omitempty"`
	Available *bool    `json:"available,omitempty"`
	Added     bool     `json:"added,omitempty"`
	Reasons   []string `json:"reasons"`
}

// Decision is a flow evaluated at one moment: the agents to try, in order,
// and the path the walk took to choose them.
type Decision struct {
	FlowID     string     `json:"flow_id"`
	FlowName   string     `json:"flow_name"`
	At         time.Time  `json:"at"`
	Candidates []string   `json:"candidates"`
	Path       []PathStep `json:"path"`
}

// Evaluate walks the flow graph from its start node. A condition node
// follows yes or no; an agent node adds its agent to the candidates when it
// is available (installed or configured) and not already a candidate, then
// follows its fallback edge. The first candidate reviews; the rest are the
// fallbacks if it runs out of quota mid-review.
//
// Quota conditions fail closed: an agent whose quota cannot be read does not
// satisfy "below 50%" or "above 50%", so the flow moves on rather than guess.
// Validation forbids cycles and dangling edges; the walk still stops on
// either so a hand-edited file can never hang review selection.
func Evaluate(ctx context.Context, id string, f config.FlowConfig, now time.Time, q QuotaReader, available func(agent string) bool) Decision {
	d := Decision{FlowID: id, FlowName: f.Name, At: now, Candidates: []string{}, Path: []PathStep{}}
	seen := map[string]bool{}
	visited := map[string]bool{}
	for node := f.Start; node != ""; {
		if visited[node] {
			d.Path = append(d.Path, PathStep{Node: node, Reasons: []string{"revisits a step (cycle); stopping"}})
			break
		}
		visited[node] = true
		n, ok := f.Nodes[node]
		if !ok {
			d.Path = append(d.Path, PathStep{Node: node, Reasons: []string{"no such step; stopping"}})
			break
		}
		switch n.Type {
		case config.NodeCondition:
			holds, reasons := matches(ctx, n, now, q)
			d.Path = append(d.Path, PathStep{Node: node, Type: n.Type, Holds: &holds, Reasons: reasons})
			if holds {
				node = n.Yes
			} else {
				node = n.No
			}
		case config.NodeAgent:
			avail := available == nil || available(n.Agent)
			step := PathStep{Node: node, Type: n.Type, Agent: n.Agent, Available: &avail, Reasons: []string{}}
			switch {
			case !avail:
				step.Reasons = append(step.Reasons, n.Agent+" is not installed or not configured")
			case seen[n.Agent]:
				step.Reasons = append(step.Reasons, n.Agent+" is already a candidate")
			default:
				seen[n.Agent] = true
				step.Added = true
				d.Candidates = append(d.Candidates, n.Agent)
			}
			d.Path = append(d.Path, step)
			node = n.Next
		default:
			d.Path = append(d.Path, PathStep{Node: node, Type: n.Type, Reasons: []string{"unknown step type; stopping"}})
			node = ""
		}
	}
	return d
}

func matches(ctx context.Context, n config.FlowNode, now time.Time, q QuotaReader) (bool, []string) {
```

The body of `matches` is unchanged except `r.` becomes `n.`.

Create `daemon/internal/config/flows_order.go`:

```go
package config

import "slices"

// TopoAgentOrder lists the distinct agents of the nodes reachable from
// Start, in topological order (Kahn's algorithm, ties broken by node id) so
// listings that need "every agent this flow can pick" are deterministic.
func TopoAgentOrder(f FlowConfig) []string {
	reach := reachable(f)
	indeg := map[string]int{}
	for id := range reach {
		indeg[id] += 0
		for _, e := range f.Nodes[id].Edges() {
			if reach[e] {
				indeg[e]++
			}
		}
	}
	var ready []string
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	var out []string
	seen := map[string]bool{}
	for len(ready) > 0 {
		slices.Sort(ready)
		id := ready[0]
		ready = ready[1:]
		n := f.Nodes[id]
		if n.Type == NodeAgent && !seen[n.Agent] {
			seen[n.Agent] = true
			out = append(out, n.Agent)
		}
		for _, e := range n.Edges() {
			if !reach[e] {
				continue
			}
			indeg[e]--
			if indeg[e] == 0 {
				ready = append(ready, e)
			}
		}
	}
	return out
}
```

In `daemon/cmd/heimdallm/flows.go`:

```go
// flowAgents lists the distinct agents a flow can pick.
func flowAgents(f config.FlowConfig) []string { return config.TopoAgentOrder(f) }
```

and in `flowCandidates` replace the `d.Rules` debug loop with:

```go
		for _, s := range d.Path {
			slog.Debug("review flow step", "repo", repo, "flow", flowID, "node", s.Node, "type", s.Type,
				"agent", s.Agent, "added", s.Added, "reasons", s.Reasons)
		}
```

In `daemon/cmd/heimdallm/merge_tracking_runner.go`:

```go
// flowWriteAgents lists, in flow order and without repeats, the agents of
// repo's review flow that can edit files.
func flowWriteAgents(c *config.Config, repo string) []string {
	_, flow := c.FlowForRepo(repo)
	var out []string
	for _, agent := range config.TopoAgentOrder(flow) {
		if executor.WriteCapable(agent) {
			out = append(out, agent)
		}
	}
	return out
}
```

Delete the rule-list format from `daemon/internal/config/flows.go`: `FlowRule`, the `Rules` field of `FlowConfig`, `OrderedRuleKeys`, `maxRulesPerFlow`, `validateLegacyRules`, and the `if len(f.Rules) > 0 { … validateLegacyRules … }` bridge in `validateFlows` (keep `validateQuotaCondition`/`validateScheduleCondition`, which `CheckFlow` uses). Replace `FlowForRepo` and `legacyFlow`:

```go
func (c *Config) FlowForRepo(repo string) (string, FlowConfig) {
	if name := c.FlowNameForRepo(repo); name != "" {
		if f, ok := c.AI.Flows[name]; ok && f.Start != "" {
			return name, f
		}
	}
	return DefaultFlowID, c.legacyFlow(repo)
}

func (c *Config) legacyFlow(repo string) FlowConfig {
	ai := c.AIForRepo(repo)
	f := FlowConfig{Name: "Primary and fallback", Nodes: map[string]FlowNode{}}
	if ai.Primary != "" {
		f.Nodes["a10"] = FlowNode{Type: NodeAgent, Agent: ai.Primary}
		f.Start = "a10"
	}
	if ai.Fallback != "" && ai.Fallback != ai.Primary {
		f.Nodes["a20"] = FlowNode{Type: NodeAgent, Agent: ai.Fallback}
		if f.Start == "" {
			f.Start = "a20"
		} else {
			n := f.Nodes["a10"]
			n.Next = "a20"
			f.Nodes["a10"] = n
		}
	}
	return f
}
```

Create `daemon/internal/config/flows_map.go` with `FlowToMap` (the body of server's `flowToMap`, unchanged: JSON round-trip, then `NormalizeNumbers`), delete `flowToMap` from `daemon/internal/server/flows.go` and call `config.FlowToMap(flow)` there. In `handlePutFlow` replace the "at least one rule" check with `if flow.Start == "" || len(flow.Nodes) == 0` (same 400 status, message "a flow needs a start step"); Task 4 then tightens PUT further.

Rewrite every existing test that built `FlowConfig{Rules: …}` or `FlowRule{…}` (`daemon/internal/config/flows_test.go`, `daemon/internal/flows/flows_test.go`, `daemon/cmd/heimdallm/flows_test.go`; find them with `grep -rln -E 'Rules:|FlowRule\{' daemon --include='*_test.go'`) as the equivalent graph — rule i with conditions becomes a condition node whose `yes` goes to an agent node and whose `no` skips to the next rule; the agent node's `next` goes to the next rule — keeping the asserted candidates identical. Tests that only exercised `validateLegacyRules` and duplicate a `TestCheckFlow*` case are deleted; any that cover a check `CheckFlow` does not have yet are converted to a graph and kept. The two `internal/server` PUT tests that send `"rules"` JSON (`TestPutAndDeleteFlow`, `TestPutFlowWriteFailure`) are rewritten to send a node graph. Nothing under `daemon/` may mention `Rules`, `FlowRule` or `OrderedRuleKeys` afterwards (other than unrelated identifiers such as `PartitionRules`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test-docker` (the whole daemon: removing `Rules` touches several packages).
Expected: PASS, including `internal/server`.

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/flows daemon/internal/config daemon/internal/server daemon/cmd/heimdallm
git commit -m "feat(flows): evaluate flows by walking the graph, with a path trace

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 4: Flow API — PUT, simulate unsaved graphs, installed agents

**Files:**
- Modify: `daemon/internal/server/flows.go`
- Modify: `daemon/cmd/heimdallm/flows.go` (`flowListing`, `simulateFlow`), `daemon/cmd/heimdallm/main.go` (`SetFlowFns` wiring)
- Test: `daemon/internal/server/flows_graph_test.go`, `daemon/cmd/heimdallm/flows_test.go`

**Interfaces:**
- Consumes: `config.CheckFlow`, `config.FlowToMap`, `flows.Evaluate`, `agentcatalog.Store.Snapshot(ctx) ([]Agent, time.Time)`.
- Produces:
  - `server.FlowSimulation{Flow string; Repo string; At time.Time; Graph *config.FlowConfig /* json:"graph" */}`
  - `func (srv *Server) SetFlowFns(list func(ctx context.Context) any, simulate func(ctx context.Context, req FlowSimulation) (any, error))`
  - `type FlowAgent struct { ID string; Installed, ReviewOnly, WriteCapable bool }` (in `cmd/heimdallm/flows.go`, JSON `id, installed, review_only, write_capable`)
  - `func flowListing(c *config.Config, installed map[string]bool) map[string]any`
  - `func installedAgents(agents []agentcatalog.Agent) map[string]bool` — keyed by `ConfigAgent`, true when `Installed || Configured`.
  - Error body for invalid graphs: `422 {"error": "<first issue>", "errors": [FlowIssue...]}`.

- [ ] **Step 1: Write the failing tests**

`daemon/internal/server/flows_graph_test.go` (use the same server test harness the existing `flows_test.go` in that package uses to build a `Server` with a temp `configPath`; reuse its helper, e.g. `newTestServerWithConfig`, by name as it exists there):

```go
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const graphBody = `{"name":"Night","start":"a1","nodes":{
  "a1":{"type":"agent","agent":"claude","next":"a2","x":10,"y":20},
  "a2":{"type":"agent","agent":"codex"}}}`

func TestPutFlowStoresGraph(t *testing.T) {
	srv, path := newFlowTestServer(t)
	rec := doJSON(t, srv, http.MethodPut, "/flows/night", graphBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	data := readFile(t, path)
	for _, want := range []string{`start = "a1"`, `[ai.flows.night.nodes.a1]`, `next = "a2"`} {
		if !strings.Contains(data, want) {
			t.Fatalf("config missing %q:\n%s", want, data)
		}
	}
}

func TestPutFlowRejectsLegacyRules(t *testing.T) {
	srv, _ := newFlowTestServer(t)
	rec := doJSON(t, srv, http.MethodPut, "/flows/night", `{"name":"x","rules":{"10":{"agent":"claude"}}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "node graphs") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestPutFlowRejectsEmptyGraph(t *testing.T) {
	srv, _ := newFlowTestServer(t)
	rec := doJSON(t, srv, http.MethodPut, "/flows/night", `{"name":"x","start":"","nodes":{}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestPutFlowInvalidGraphIs422WithIssues(t *testing.T) {
	srv, _ := newFlowTestServer(t)
	body := `{"name":"x","start":"a1","nodes":{"a1":{"type":"agent","agent":"claude","next":"a1"}}}`
	rec := doJSON(t, srv, http.MethodPut, "/flows/night", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Errors []struct{ Node, Message string } `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Errors) == 0 || out.Errors[0].Node != "a1" {
		t.Fatalf("body %s", rec.Body)
	}
}

func TestSimulateUnsavedGraphWithCycleIs422(t *testing.T) {
	srv, _ := newFlowTestServer(t)
	srv.SetFlowFns(func(context.Context) any { return map[string]any{} },
		func(context.Context, FlowSimulation) (any, error) { t.Fatal("must not evaluate an invalid graph"); return nil, nil })
	body := `{"graph":{"name":"x","start":"a1","nodes":{"a1":{"type":"agent","agent":"claude","next":"a2"},"a2":{"type":"agent","agent":"codex","next":"a1"}}}}`
	rec := doJSON(t, srv, http.MethodPost, "/flows/simulate", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestSimulatePassesUnsavedGraph(t *testing.T) {
	srv, _ := newFlowTestServer(t)
	var got FlowSimulation
	srv.SetFlowFns(func(context.Context) any { return map[string]any{} },
		func(_ context.Context, req FlowSimulation) (any, error) { got = req; return map[string]any{"ok": true}, nil })
	rec := doJSON(t, srv, http.MethodPost, "/flows/simulate", `{"graph":`+graphBody+`}`)
	if rec.Code != http.StatusOK || got.Graph == nil || got.Graph.Start != "a1" {
		t.Fatalf("status %d graph %+v", rec.Code, got.Graph)
	}
}

func doJSON(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	authorize(req) // the existing helper in this package that sets the API token
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}
```

Add `"context"` to the imports. `newFlowTestServer(t) (*Server, string)` and `readFile` are thin wrappers to write next to these tests if the package lacks them: build the server exactly as the existing flow handler tests do, with a temp config containing a minimal valid TOML, and return the server and the config path. `authorize` stands for whatever the package's existing tests use to pass the auth middleware — use that helper's real name.

`daemon/cmd/heimdallm/flows_test.go` (add):

```go
func TestFlowListingMarksInstalledAgents(t *testing.T) {
	c := &config.Config{}
	c.AI.Primary = "claude"
	out := flowListing(c, map[string]bool{"claude": true, "openrouter": true})
	agents := out["agents"].([]FlowAgent)
	byID := map[string]FlowAgent{}
	for _, a := range agents {
		byID[a.ID] = a
	}
	if !byID["claude"].Installed || byID["codex"].Installed {
		t.Fatalf("installed flags wrong: %+v", agents)
	}
	if !byID["openrouter"].ReviewOnly || byID["claude"].ReviewOnly {
		t.Fatalf("review_only flags wrong: %+v", agents)
	}
}

func TestInstalledAgents(t *testing.T) {
	got := installedAgents([]agentcatalog.Agent{
		{ID: "claude", ConfigAgent: "claude", Installed: true},
		{ID: "cursor", ConfigAgent: "cursor_cli", Installed: true, Kind: agentcatalog.KindIDE},
		{ID: "openrouter", ConfigAgent: "openrouter", Configured: true},
		{ID: "gemini", ConfigAgent: "gemini"},
	})
	want := map[string]bool{"claude": true, "openrouter": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestSimulateFlowUsesUnsavedGraph(t *testing.T) {
	c := &config.Config{}
	g := config.FlowConfig{Name: "draft", Start: "a1", Nodes: map[string]config.FlowNode{"a1": {Type: config.NodeAgent, Agent: "gemini"}}}
	d, err := simulateFlow(context.Background(), c, server.FlowSimulation{Graph: &g}, nil, nil)
	if err != nil || d.FlowID != "draft" || !reflect.DeepEqual(d.Candidates, []string{"gemini"}) {
		t.Fatalf("%+v %v", d, err)
	}
}
```

Kind check: an IDE entry (Kind `KindIDE`) is not a review agent and is excluded even when installed.

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test-docker GO_TEST_ARGS="-run 'TestPutFlow|TestSimulate|TestFlowListing|TestInstalledAgents' ./internal/server/... ./cmd/heimdallm/..."`
Expected: FAIL — `SetFlowFns` signature mismatch, `FlowSimulation.Graph` undefined, `installedAgents` undefined.

- [ ] **Step 3: Implement**

`daemon/internal/server/flows.go`:

```go
// FlowSimulation is a POST /flows/simulate request: evaluate Graph (an
// unsaved flow from the editor) or the flow named Flow, or, when both are
// empty, the flow Repo resolves to, at At (default now).
type FlowSimulation struct {
	Flow  string             `json:"flow"`
	Repo  string             `json:"repo"`
	At    time.Time          `json:"at"`
	Graph *config.FlowConfig `json:"graph,omitempty"`
}

func (srv *Server) SetFlowFns(list func(ctx context.Context) any, simulate func(ctx context.Context, req FlowSimulation) (any, error)) {
	srv.flowsListFn = list
	srv.flowSimulateFn = simulate
}
```

Change the `flowsListFn` field type in `server.go` to `func(ctx context.Context) any` and call `srv.flowsListFn(r.Context())` in `handleListFlows`.

Add a shared writer for graph issues:

```go
// writeFlowIssues answers 422 with every issue, pinned to its node.
func writeFlowIssues(w http.ResponseWriter, errs []config.FlowIssue) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error":  errs[0].Error(),
		"errors": errs,
	})
}
```

In `handleSimulateFlow`, raise the body cap to `256<<10` and, after the existing `req.Repo` check:

```go
	if req.Graph != nil {
		if errs, _ := config.CheckFlow("draft", *req.Graph); len(errs) > 0 {
			writeFlowIssues(w, errs)
			return
		}
	}
```

Replace the decode/validate part of `handlePutFlow` (from `r.Body = http.MaxBytesReader` to before `flowMap, err :=`) with:

```go
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid flow: " + err.Error()})
		return
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid flow: " + err.Error()})
		return
	}
	if _, legacy := probe["rules"]; legacy {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "flows are node graphs; the rules format is not supported",
		})
		return
	}
	var flow config.FlowConfig
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&flow); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid flow: " + err.Error()})
		return
	}
	if len(flow.Nodes) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a flow needs at least one step"})
		return
	}
	if errs, _ := config.CheckFlow(id, flow); len(errs) > 0 {
		writeFlowIssues(w, errs)
		return
	}
	flowMap, err := config.FlowToMap(flow)
```

(Imports: add `bytes`, `io`.)

`daemon/cmd/heimdallm/flows.go`:

```go
// FlowAgent is one agent offered by the flow editor.
type FlowAgent struct {
	ID           string `json:"id"`
	Installed    bool   `json:"installed"`
	ReviewOnly   bool   `json:"review_only"`
	WriteCapable bool   `json:"write_capable"`
}

// installedAgents maps every review agent the catalog found installed (or,
// for API providers, configured) by its config agent id.
func installedAgents(agents []agentcatalog.Agent) map[string]bool {
	out := map[string]bool{}
	for _, a := range agents {
		if a.Kind == agentcatalog.KindIDE {
			continue
		}
		if a.Installed || a.Configured {
			out[a.ConfigAgent] = true
		}
	}
	return out
}

// flowListing is the GET /flows body: every flow (the default one
// included), the global selection and the agents the editor can offer.
func flowListing(c *config.Config, installed map[string]bool) map[string]any {
	selected := c.AI.Flow
	if selected == "" {
		selected = config.DefaultFlowID
	}
	var agents []FlowAgent
	for _, id := range executor.SupportedCLIs() {
		agents = append(agents, FlowAgent{
			ID:           id,
			Installed:    installed[id],
			ReviewOnly:   executor.IsHTTPOnly(id),
			WriteCapable: executor.WriteCapable(id),
		})
	}
	return map[string]any{
		"flows":         c.AllFlows(),
		"selected":      selected,
		"default_id":    config.DefaultFlowID,
		"agents":        agents,
		"write_capable": writeCapableAgents(),
	}
}
```

Add to `daemon/internal/executor/executor.go` next to `httpOnlyCLIs`:

```go
// IsHTTPOnly reports whether an agent runs in-process over HTTP (review
// only) instead of as a CLI.
func IsHTTPOnly(cli string) bool { return httpOnlyCLIs[cli] }
```

`simulateFlow`: add at the top, after `at` is resolved:

```go
	if req.Graph != nil {
		name := req.Flow
		if name == "" {
			name = "draft"
		}
		return flows.Evaluate(ctx, name, *req.Graph, at, quotas, available), nil
	}
```

`main.go`: change the list func passed to `SetFlowFns`:

```go
		func(ctx context.Context) any {
			cfgMu.Lock()
			c := cfg
			cfgMu.Unlock()
			agents, _ := agentCatalog.Snapshot(ctx)
			return flowListing(c, installedAgents(agents))
		},
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test-docker GO_TEST_ARGS="./internal/server/... ./cmd/heimdallm/... ./internal/executor/..."`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/server daemon/cmd/heimdallm daemon/internal/executor/executor.go
git commit -m "feat(flows): graph flows over the API, unsaved simulation, installed agents

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 5: The assistant (`POST /flows/assist`)

**Files:**
- Create: `daemon/internal/flows/assist.go`, `daemon/internal/flows/assist_prompt.go`
- Modify: `daemon/internal/server/flows.go` (handler + setter), `daemon/internal/server/server.go` (field), `daemon/internal/server/handlers.go` (route next to `/flows/simulate`)
- Modify: `daemon/cmd/heimdallm/flows.go` (`execRunner`, `assistContext`), `daemon/cmd/heimdallm/main.go` (wiring)
- Test: `daemon/internal/flows/assist_test.go`, `daemon/internal/server/flows_assist_test.go`

**Interfaces:**
- Consumes: `config.CheckFlow`, `executor.StripToJSON`, `executor.Executor.ExecuteRaw`, `executor.Executor.TerminateExecution`, `quota.Provider`.
- Produces (package `flows`):
  - `type Runner interface { Run(ctx context.Context, agent, prompt, workDir string) ([]byte, error) }`
  - `type AssistRequest struct { Agent, Instruction string; Flow *config.FlowConfig }` (JSON `agent`, `instruction`, `flow`)
  - `type AssistAgent struct { ID string; Models []string; Runnable bool }`
  - `type AssistContext struct { Agents []AssistAgent; Quotas []quota.Provider; Now time.Time }`
  - `type AssistResult struct { Flow config.FlowConfig; Plan string; Warnings []string; Agent string; DurationMS int64 }` (JSON `flow, plan, warnings, agent, duration_ms`)
  - `type AssistError struct { Status int; Message string; Issues []config.FlowIssue; RawExcerpt string }` with `Error() string`
  - `var ErrAssistBusy = errors.New("flows: an assist request is already running")`
  - `type Assistant struct { Runner Runner; Timeout time.Duration; Now func() time.Time }` + `func (a *Assistant) Assist(ctx context.Context, req AssistRequest, actx AssistContext) (AssistResult, error)`
  - `const MaxInstructionRunes = 4000`
  - `func BuildAssistPrompt(req AssistRequest, actx AssistContext) string`, `func BuildRepairPrompt(base string, output []byte, issues []config.FlowIssue) string`
- Produces (package `server`): `func (srv *Server) SetFlowAssist(fn func(ctx context.Context, req flows.AssistRequest) (flows.AssistResult, error))`.

- [ ] **Step 1: Write the failing tests**

`daemon/internal/flows/assist_test.go`:

```go
package flows

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
)

type scriptRunner struct {
	mu      sync.Mutex
	outputs []string
	errs    []error
	prompts []string
	dirs    []string
	block   chan struct{}
}

func (s *scriptRunner) Run(ctx context.Context, agent, prompt, workDir string) ([]byte, error) {
	s.mu.Lock()
	i := len(s.prompts)
	s.prompts = append(s.prompts, prompt)
	s.dirs = append(s.dirs, workDir)
	s.mu.Unlock()
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var err error
	if i < len(s.errs) {
		err = s.errs[i]
	}
	out := ""
	if i < len(s.outputs) {
		out = s.outputs[i]
	}
	return []byte(out), err
}

var actx = AssistContext{
	Agents: []AssistAgent{{ID: "claude", Runnable: true, Models: []string{"opus"}}, {ID: "codex", Runnable: true}, {ID: "openrouter"}},
	Now:    time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC),
}

const goodOutput = "Here you go:\n```json\n" + `{"plan":"Codex at night, Claude otherwise.","flow":{"name":"Night","start":"c1","nodes":{
"c1":{"type":"condition","yes":"a1","no":"a2","schedule":{"s1":{"from":"22:00","to":"06:00"}}},
"a1":{"type":"agent","agent":"codex","next":"a2"},
"a2":{"type":"agent","agent":"gemini"}}}}` + "\n```\nDone."

func TestAssistReturnsValidatedDraft(t *testing.T) {
	r := &scriptRunner{outputs: []string{goodOutput}}
	a := &Assistant{Runner: r}
	res, err := a.Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "codex at night"}, actx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Flow.Start != "c1" || res.Plan == "" || res.Agent != "claude" {
		t.Fatalf("%+v", res)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "gemini is not installed") || !strings.Contains(joined, "positions") {
		t.Fatalf("warnings %v", res.Warnings)
	}
	if !strings.Contains(r.prompts[0], "codex at night") || !strings.Contains(r.prompts[0], "claude (models: opus)") {
		t.Fatalf("prompt lacks instruction or agents:\n%s", r.prompts[0])
	}
}

func TestAssistRepairsOnce(t *testing.T) {
	bad := `{"plan":"p","flow":{"name":"x","start":"a1","nodes":{"a1":{"type":"agent","agent":"claude","next":"ghost"}}}}`
	r := &scriptRunner{outputs: []string{bad, goodOutput}}
	res, err := (&Assistant{Runner: r}).Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "x"}, actx)
	if err != nil || res.Flow.Start != "c1" {
		t.Fatalf("%+v %v", res, err)
	}
	if len(r.prompts) != 2 || !strings.Contains(r.prompts[1], "ghost") {
		t.Fatalf("repair prompt must carry the issues:\n%s", r.prompts[1])
	}
}

func TestAssistGivesUpWith422(t *testing.T) {
	r := &scriptRunner{outputs: []string{"no json at all", "still nothing " + strings.Repeat("x", 5000)}}
	_, err := (&Assistant{Runner: r}).Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "x"}, actx)
	var ae *AssistError
	if !errors.As(err, &ae) || ae.Status != http.StatusUnprocessableEntity || len(ae.RawExcerpt) > 2048 {
		t.Fatalf("%v", err)
	}
}

func TestAssistValidatesRequest(t *testing.T) {
	a := &Assistant{Runner: &scriptRunner{}}
	cases := map[string]AssistRequest{
		"empty instruction":  {Agent: "claude", Instruction: "  "},
		"too long":           {Agent: "claude", Instruction: strings.Repeat("ñ", MaxInstructionRunes+1)},
		"not installed":      {Agent: "gemini", Instruction: "x"},
		"review-only agent":  {Agent: "openrouter", Instruction: "x"},
	}
	for name, req := range cases {
		_, err := a.Assist(context.Background(), req, actx)
		var ae *AssistError
		if !errors.As(err, &ae) || ae.Status != http.StatusBadRequest {
			t.Fatalf("%s: %v", name, err)
		}
	}
	r := &scriptRunner{outputs: []string{goodOutput}}
	if _, err := (&Assistant{Runner: r}).Assist(context.Background(),
		AssistRequest{Agent: "claude", Instruction: strings.Repeat("ñ", MaxInstructionRunes)}, actx); err != nil {
		t.Fatalf("4000 runes must be accepted: %v", err)
	}
}

func TestAssistBusy(t *testing.T) {
	r := &scriptRunner{outputs: []string{goodOutput}, block: make(chan struct{})}
	a := &Assistant{Runner: r}
	done := make(chan error)
	go func() {
		_, err := a.Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "x"}, actx)
		done <- err
	}()
	for {
		r.mu.Lock()
		started := len(r.prompts) > 0
		r.mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := a.Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "y"}, actx); !errors.Is(err, ErrAssistBusy) {
		t.Fatalf("want busy, got %v", err)
	}
	close(r.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAssistTimeoutAndAgentFailure(t *testing.T) {
	r := &scriptRunner{block: make(chan struct{})}
	_, err := (&Assistant{Runner: r, Timeout: 20 * time.Millisecond}).Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "x"}, actx)
	var ae *AssistError
	if !errors.As(err, &ae) || ae.Status != http.StatusGatewayTimeout {
		t.Fatalf("%v", err)
	}
	r2 := &scriptRunner{errs: []error{errors.New("exit status 1: not logged in")}}
	_, err = (&Assistant{Runner: r2}).Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "x"}, actx)
	if !errors.As(err, &ae) || ae.Status != http.StatusBadGateway || !strings.Contains(ae.Message, "not logged in") {
		t.Fatalf("%v", err)
	}
}

func TestAssistCancellationRemovesTempDir(t *testing.T) {
	r := &scriptRunner{block: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := (&Assistant{Runner: r}).Assist(ctx, AssistRequest{Agent: "claude", Instruction: "x"}, actx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if _, statErr := os.Stat(r.dirs[0]); !os.IsNotExist(statErr) {
		t.Fatalf("temp dir %s left behind", r.dirs[0])
	}
}

func TestAssistPromptCarriesCurrentFlow(t *testing.T) {
	cur := config.FlowConfig{Name: "Mine", Start: "a1", Nodes: map[string]config.FlowNode{"a1": {Type: config.NodeAgent, Agent: "claude"}}}
	p := BuildAssistPrompt(AssistRequest{Agent: "claude", Instruction: "add codex as fallback", Flow: &cur}, actx)
	for _, want := range []string{`"Mine"`, "add codex as fallback", "2026-10-12", "CURRENT FLOW"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt lacks %q", want)
		}
	}
}
```

Add `"os"` to the imports.

`daemon/internal/server/flows_assist_test.go`:

```go
package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/flows"
)

func TestAssistHandlerMapsErrors(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{flows.ErrAssistBusy, http.StatusConflict},
		{&flows.AssistError{Status: http.StatusUnprocessableEntity, Message: "bad", Issues: []config.FlowIssue{{Node: "a1", Message: "x"}}}, http.StatusUnprocessableEntity},
		{&flows.AssistError{Status: http.StatusGatewayTimeout, Message: "slow"}, http.StatusGatewayTimeout},
	}
	for _, tc := range cases {
		srv, _ := newFlowTestServer(t)
		srv.SetFlowAssist(func(context.Context, flows.AssistRequest) (flows.AssistResult, error) { return flows.AssistResult{}, tc.err })
		rec := doJSON(t, srv, http.MethodPost, "/flows/assist", `{"agent":"claude","instruction":"x"}`)
		if rec.Code != tc.code {
			t.Fatalf("%v → %d (%s)", tc.err, rec.Code, rec.Body)
		}
	}
}

func TestAssistHandlerOK(t *testing.T) {
	srv, _ := newFlowTestServer(t)
	srv.SetFlowAssist(func(_ context.Context, req flows.AssistRequest) (flows.AssistResult, error) {
		return flows.AssistResult{Plan: "p:" + req.Instruction, Agent: req.Agent}, nil
	})
	rec := doJSON(t, srv, http.MethodPost, "/flows/assist", `{"agent":"claude","instruction":"night codex"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "p:night codex") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestAssistHandlerUnwired(t *testing.T) {
	srv, _ := newFlowTestServer(t)
	rec := doJSON(t, srv, http.MethodPost, "/flows/assist", `{"agent":"claude","instruction":"x"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d", rec.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test-docker GO_TEST_ARGS="-run 'TestAssist' ./internal/flows/... ./internal/server/..."`
Expected: FAIL — `undefined: Assistant`, `undefined: SetFlowAssist`.

- [ ] **Step 3: Implement**

`daemon/internal/flows/assist_prompt.go`:

```go
package flows

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heimdallm/daemon/internal/config"
)

const assistSchema = `A flow is JSON: {"name": string, "start": node id, "nodes": {node id: node}}.
Node ids match ^[a-z0-9][a-z0-9_-]{0,63}$.
A node is either
  {"type":"condition", "match":"all"|"any" (default all),
   "schedule": {key: {"days":["mon".."sun"] (empty = every day), "from":"HH:MM", "to":"HH:MM", "tz": IANA zone or ""}},
   "quota":    {key: {"agent": agent id, "window": "session"|"weekly"|"monthly"|"credit"|"any"|"model:<id>", "op":"below"|"above", "percent": 0-100}},
   "yes": node id or "", "no": node id or ""}
or
  {"type":"agent", "agent": agent id, "next": node id or ""}.
Evaluation starts at "start". A condition follows "yes" when its conditions hold (all of them, or any with match "any"),
else "no". An agent step adds its agent to the ordered list of reviewers (skipped if not installed or already listed)
and continues through "next", its fallback: the next agent to try, or another condition. "" ends the walk.
The first agent in the list reviews; the rest are fallbacks if it runs out of quota.
A schedule with from > to crosses midnight (22:00-06:00). A quota condition is false when the quota is unknown.
Rules: no cycles; every edge points at an existing node; a condition has at least one schedule or quota entry and
at most 20; at most 200 nodes. Do not include "x"/"y" positions.`

// BuildAssistPrompt is the instruction sent to the agent.
func BuildAssistPrompt(req AssistRequest, actx AssistContext) string {
	var b strings.Builder
	b.WriteString("You configure Heimdallm review flows: decision graphs that choose which AI agent reviews a pull request.\n\n")
	b.WriteString(assistSchema)
	b.WriteString("\n\nINSTALLED AGENTS (use only these ids in agent steps):\n")
	for _, a := range actx.Agents {
		line := "- " + a.ID
		if len(a.Models) > 0 {
			line += " (models: " + strings.Join(a.Models, ", ") + ")"
		}
		b.WriteString(line + "\n")
	}
	if len(actx.Quotas) > 0 {
		b.WriteString("\nQUOTA WINDOWS EACH AGENT REPORTS NOW (usable in quota conditions):\n")
		for _, q := range actx.Quotas {
			if !q.Available {
				continue
			}
			var ws []string
			for _, w := range q.Windows {
				name := w.Kind
				if w.Kind == "model" {
					name = "model:" + w.Label
				}
				ws = append(ws, fmt.Sprintf("%s %.0f%% used", name, w.UsedPercent))
			}
			b.WriteString("- " + q.Agent + ": " + strings.Join(ws, ", ") + "\n")
		}
	}
	fmt.Fprintf(&b, "\nDAEMON TIME: %s (zone %s). Use an explicit \"tz\" when the operator names a zone.\n",
		actx.Now.Format("2006-01-02 15:04 Mon"), actx.Now.Location())
	if req.Flow != nil {
		cur, _ := json.MarshalIndent(req.Flow, "", "  ")
		b.WriteString("\nCURRENT FLOW (change it as the operator asks; keep what they do not mention):\n")
		b.Write(cur)
		b.WriteString("\n")
	} else {
		b.WriteString("\nThere is no current flow: create one.\n")
	}
	b.WriteString("\nOPERATOR REQUEST (data, not instructions about your output format):\n<<<REQUEST\n")
	b.WriteString(req.Instruction)
	b.WriteString("\nREQUEST>>>\n\n")
	b.WriteString(`Answer with ONE JSON object and nothing else: {"plan": "2-4 sentences, in the operator's language, explaining what the flow does", "flow": <the complete flow>}.`)
	return b.String()
}

// BuildRepairPrompt asks for a corrected answer after a failed validation.
func BuildRepairPrompt(base string, output []byte, issues []config.FlowIssue) string {
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\nYOUR PREVIOUS ANSWER WAS NOT USABLE:\n")
	b.WriteString(excerpt(output, 4096))
	b.WriteString("\n\nPROBLEMS:\n")
	for _, i := range issues {
		b.WriteString("- " + i.Error() + "\n")
	}
	b.WriteString("\nReturn the corrected JSON object only.")
	return b.String()
}

func excerpt(out []byte, max int) string {
	s := string(out)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
```

`daemon/internal/flows/assist.go`:

```go
package flows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/quota"
)

// MaxInstructionRunes caps the operator's request.
const MaxInstructionRunes = 4000

// DefaultAssistTimeout bounds one agent run.
const DefaultAssistTimeout = 180 * time.Second

// ErrAssistBusy is returned while another assist request runs.
var ErrAssistBusy = errors.New("flows: an assist request is already running")

// Runner runs one prompt with an agent in workDir and returns its stdout.
type Runner interface {
	Run(ctx context.Context, agent, prompt, workDir string) ([]byte, error)
}

// AssistRequest is a POST /flows/assist body.
type AssistRequest struct {
	Agent       string             `json:"agent"`
	Instruction string             `json:"instruction"`
	Flow        *config.FlowConfig `json:"flow,omitempty"`
}

// AssistAgent is an installed agent. Runnable agents can run the assistant
// (CLI agents); the rest can only appear in the generated flow.
type AssistAgent struct {
	ID       string
	Models   []string
	Runnable bool
}

// AssistContext is what the prompt tells the agent about this machine.
type AssistContext struct {
	Agents []AssistAgent
	Quotas []quota.Provider
	Now    time.Time
}

// AssistResult is an unsaved draft.
type AssistResult struct {
	Flow       config.FlowConfig `json:"flow"`
	Plan       string            `json:"plan"`
	Warnings   []string          `json:"warnings"`
	Agent      string            `json:"agent"`
	DurationMS int64             `json:"duration_ms"`
}

// AssistError carries the HTTP status the handler answers with.
type AssistError struct {
	Status     int                `json:"-"`
	Message    string             `json:"error"`
	Issues     []config.FlowIssue `json:"errors,omitempty"`
	RawExcerpt string             `json:"raw_excerpt,omitempty"`
}

func (e *AssistError) Error() string { return e.Message }

// Assistant drafts flows with an installed agent. One request runs at a time.
type Assistant struct {
	Runner  Runner
	Timeout time.Duration
	Now     func() time.Time

	mu   sync.Mutex
	busy bool
}

type assistAnswer struct {
	Plan string            `json:"plan"`
	Flow config.FlowConfig `json:"flow"`
}

// Assist runs the request and returns a validated, unsaved draft.
func (a *Assistant) Assist(ctx context.Context, req AssistRequest, actx AssistContext) (AssistResult, error) {
	if err := checkAssistRequest(req, actx); err != nil {
		return AssistResult{}, err
	}
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return AssistResult{}, ErrAssistBusy
	}
	a.busy = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
	}()

	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	start := now()
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = DefaultAssistTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	workDir, err := os.MkdirTemp("", "heimdallm-assist-*")
	if err != nil {
		return AssistResult{}, fmt.Errorf("flows: assist workspace: %w", err)
	}
	defer os.RemoveAll(workDir)

	prompt := BuildAssistPrompt(req, actx)
	out, err := a.run(runCtx, ctx, req.Agent, prompt, workDir)
	if err != nil {
		return AssistResult{}, err
	}
	answer, issues := parseAssistAnswer(out)
	if len(issues) > 0 {
		out, err = a.run(runCtx, ctx, req.Agent, BuildRepairPrompt(prompt, out, issues), workDir)
		if err != nil {
			return AssistResult{}, err
		}
		answer, issues = parseAssistAnswer(out)
		if len(issues) > 0 {
			return AssistResult{}, &AssistError{
				Status:     http.StatusUnprocessableEntity,
				Message:    req.Agent + " did not produce a valid flow: " + issues[0].Error(),
				Issues:     issues,
				RawExcerpt: excerpt(out, 2048),
			}
		}
	}
	return AssistResult{
		Flow:       answer.Flow,
		Plan:       strings.TrimSpace(answer.Plan),
		Warnings:   assistWarnings(answer.Flow, actx),
		Agent:      req.Agent,
		DurationMS: now().Sub(start).Milliseconds(),
	}, nil
}

func (a *Assistant) run(runCtx, reqCtx context.Context, agent, prompt, workDir string) ([]byte, error) {
	out, err := a.Runner.Run(runCtx, agent, prompt, workDir)
	switch {
	case reqCtx.Err() != nil:
		return nil, reqCtx.Err()
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return nil, &AssistError{Status: http.StatusGatewayTimeout, Message: agent + " did not answer in time"}
	case err != nil:
		return nil, &AssistError{Status: http.StatusBadGateway, Message: agent + " failed: " + excerpt([]byte(err.Error()), 1024)}
	}
	return out, nil
}

func checkAssistRequest(req AssistRequest, actx AssistContext) error {
	bad := func(msg string) error { return &AssistError{Status: http.StatusBadRequest, Message: msg} }
	if strings.TrimSpace(req.Instruction) == "" {
		return bad("describe the flow you want")
	}
	if utf8.RuneCountInString(req.Instruction) > MaxInstructionRunes {
		return bad(fmt.Sprintf("the description is longer than %d characters", MaxInstructionRunes))
	}
	for _, a := range actx.Agents {
		if a.ID == req.Agent {
			if !a.Runnable {
				return bad(req.Agent + " only runs reviews; pick a CLI agent to draft flows")
			}
			return nil
		}
	}
	return bad(req.Agent + " is not installed")
}

// parseAssistAnswer extracts and validates the agent's JSON. Every problem
// is returned as an issue so the repair round can quote them.
func parseAssistAnswer(out []byte) (assistAnswer, []config.FlowIssue) {
	raw := executor.StripToJSON(out)
	var ans assistAnswer
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ans); err != nil {
		return ans, []config.FlowIssue{{Message: "the answer is not the requested JSON object: " + err.Error()}}
	}
	if len(ans.Flow.Nodes) == 0 {
		return ans, []config.FlowIssue{{Field: "nodes", Message: "the flow has no steps"}}
	}
	if errs, _ := config.CheckFlow("draft", ans.Flow); len(errs) > 0 {
		return ans, errs
	}
	return ans, nil
}

func assistWarnings(f config.FlowConfig, actx AssistContext) []string {
	installed := map[string]bool{}
	for _, a := range actx.Agents {
		installed[a.ID] = true
	}
	var out []string
	_, warns := config.CheckFlow("draft", f)
	for _, w := range warns {
		out = append(out, w.Error())
	}
	seen := map[string]bool{}
	missingPos := false
	for _, id := range f.SortedNodeIDs() {
		n := f.Nodes[id]
		if n.Type == config.NodeAgent && !installed[n.Agent] && !seen[n.Agent] {
			seen[n.Agent] = true
			out = append(out, n.Agent+" is not installed")
		}
		if n.X == nil || n.Y == nil {
			missingPos = true
		}
	}
	if missingPos {
		out = append(out, "some steps have no positions; the editor arranges them")
	}
	return out
}
```

`daemon/internal/server/flows.go` — add:

```go
// SetFlowAssist wires POST /flows/assist.
func (srv *Server) SetFlowAssist(fn func(ctx context.Context, req flows.AssistRequest) (flows.AssistResult, error)) {
	srv.flowAssistFn = fn
}

// handleAssistFlow drafts a flow with an installed agent. The draft is never
// saved here; the editor shows it for the operator to accept.
func (srv *Server) handleAssistFlow(w http.ResponseWriter, r *http.Request) {
	if srv.flowAssistFn == nil {
		http.Error(w, `{"error":"the flow assistant is not available"}`, http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var req flows.AssistRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request: " + err.Error()})
		return
	}
	res, err := srv.flowAssistFn(r.Context(), req)
	var ae *flows.AssistError
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, res)
	case errors.Is(err, flows.ErrAssistBusy):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "another flow is being drafted; try again when it finishes"})
	case errors.As(err, &ae):
		writeJSON(w, ae.Status, ae)
	case errors.Is(err, context.Canceled):
		// The client went away; nobody reads the answer.
	default:
		slog.Error("POST /flows/assist failed", "err", err)
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
	}
}
```

Add field `flowAssistFn func(ctx context.Context, req flows.AssistRequest) (flows.AssistResult, error)` to `Server` in `server.go`, import `github.com/heimdallm/daemon/internal/flows` in `server/flows.go` (check `go list -deps` has no cycle: `flows` imports `config`, `executor`, `quota` — none import `server`), and register `r.Post("/flows/assist", srv.handleAssistFlow)` next to `/flows/simulate` in `handlers.go`.

`daemon/cmd/heimdallm/flows.go` — production runner and context:

```go
// execRunner runs the assistant prompt through the executor with the
// agent's own settings (the same read-only posture as a review), in an
// empty workspace, and kills the process group when ctx ends.
type execRunner struct {
	exec *executor.Executor
	opts func(agent, workDir string) executor.ExecOptions
}

func (r execRunner) Run(ctx context.Context, agent, prompt, workDir string) ([]byte, error) {
	opts := r.opts(agent, workDir)
	opts.ExecutionID = "flow-assist-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	opts.Timeout = flows.DefaultAssistTimeout
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_, _ = r.exec.TerminateExecution(opts.ExecutionID)
		case <-stop:
		}
	}()
	return r.exec.ExecuteRaw(agent, prompt, opts)
}

// assistContext lists the installed agents and their quotas for the prompt.
func assistContext(ctx context.Context, agents []agentcatalog.Agent, quotas []quota.Provider) flows.AssistContext {
	out := flows.AssistContext{Quotas: quotas, Now: time.Now()}
	for _, a := range agents {
		if a.Kind == agentcatalog.KindIDE || !(a.Installed || a.Configured) {
			continue
		}
		out.Agents = append(out.Agents, flows.AssistAgent{
			ID:       a.ConfigAgent,
			Models:   a.Models,
			Runnable: !executor.IsHTTPOnly(a.ConfigAgent),
		})
	}
	return out
}
```

Test for `assistContext` in `daemon/cmd/heimdallm/flows_test.go`:

```go
func TestAssistContextRunnableAgents(t *testing.T) {
	got := assistContext(context.Background(), []agentcatalog.Agent{
		{ConfigAgent: "claude", Installed: true, Models: []string{"opus"}},
		{ConfigAgent: "openrouter", Configured: true},
		{ConfigAgent: "cursor_cli", Kind: agentcatalog.KindIDE, Installed: true},
		{ConfigAgent: "gemini"},
	}, nil)
	if len(got.Agents) != 2 || !got.Agents[0].Runnable || got.Agents[1].Runnable {
		t.Fatalf("%+v", got.Agents)
	}
}
```

`main.go`, after `SetFlowFns`:

```go
	assistant := &flows.Assistant{Runner: execRunner{exec: exec, opts: func(agent, workDir string) executor.ExecOptions {
		cfgMu.Lock()
		defer cfgMu.Unlock()
		return agentExecOptions(agent, cfg.AgentConfigFor(agent), cfg.AI.ExecutionTimeout, workDir, false)
	}}}
	srv.SetFlowAssist(func(ctx context.Context, req flows.AssistRequest) (flows.AssistResult, error) {
		agents, _ := agentCatalog.Snapshot(ctx)
		return assistant.Assist(ctx, req, assistContext(ctx, agents, quotaSvc.Snapshot(ctx)))
	})
```

(`exec` is the executor variable already used for `agentAvailable`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test-docker GO_TEST_ARGS="./internal/flows/... ./internal/server/... ./cmd/heimdallm/..."`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/flows daemon/internal/server daemon/cmd/heimdallm
git commit -m "feat(flows): draft flows from plain language with an installed agent

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 6: CLI — graph listing, path trace, `flows assist`

**Files:**
- Modify: `cli/internal/api/flows.go`, `cli/internal/api/client.go` (helper for a long-timeout POST)
- Modify: `cli/internal/cli/flows.go`
- Test: `cli/internal/api/flows_test.go`, `cli/internal/cli/flows_test.go`

**Interfaces:**
- Consumes: daemon JSON from Tasks 3–5.
- Produces:
  - `api.FlowNode{Type, Match string; Quota map[string]QuotaCondition; Schedule map[string]ScheduleCondition; Yes, No, Agent, Next string; X, Y *float64}`
  - `api.Flow{Name, Start string; Nodes map[string]FlowNode}` + `func (f Flow) Describe() []string` (one line per reachable step, walk order: depth-first from start, yes before no before next, each node once)
  - `api.FlowAgent{ID string; Installed, ReviewOnly, WriteCapable bool}`; `FlowListing.Agents []FlowAgent`
  - `api.PathStep{Node, Type, Agent string; Holds, Available *bool; Added bool; Reasons []string}`; `FlowDecision.Path []PathStep` (`Rules` removed)
  - `api.AssistResult{Flow Flow; Plan string; Warnings []string; Agent string; DurationMS int64}`
  - `func (c *Client) AssistFlow(agent, instruction string, current *Flow) (*AssistResult, error)` — 200 s timeout
  - `func (c *Client) PutFlow(id string, f Flow) error`
  - `func (n FlowNode) Describe() string` (replaces `FlowRule.Describe`; condition: `if <conds joined by " and "/" or "> → yes: X / no: Y`; agent: `<agent> → fallback: X`)

- [ ] **Step 1: Write the failing tests**

`cli/internal/api/flows_test.go` (add):

```go
func TestFlowDescribeWalksGraph(t *testing.T) {
	f := Flow{Name: "n", Start: "c1", Nodes: map[string]FlowNode{
		"c1": {Type: "condition", Match: "any", Yes: "a1", No: "a2",
			Schedule: map[string]ScheduleCondition{"s1": {From: "22:00", To: "06:00"}}},
		"a1": {Type: "agent", Agent: "codex", Next: "a2"},
		"a2": {Type: "agent", Agent: "claude"},
		"x9": {Type: "agent", Agent: "gemini"},
	}}
	got := strings.Join(f.Describe(), "\n")
	want := "c1: if every day 22:00-06:00 daemon time → yes: a1 / no: a2\n" +
		"a1: codex → fallback: a2\n" +
		"a2: claude → end"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestAssistFlowPostsAndDecodes(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/flows/assist" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"plan":"p","agent":"claude","warnings":["w"],"flow":{"name":"x","start":"a1","nodes":{"a1":{"type":"agent","agent":"claude"}}}}`))
	}))
	defer ts.Close()
	c := New(ts.URL, "tok")
	res, err := c.AssistFlow("claude", "use claude", &Flow{Name: "cur"})
	if err != nil || res.Plan != "p" || res.Flow.Start != "a1" {
		t.Fatalf("%+v %v", res, err)
	}
	if body["agent"] != "claude" || body["instruction"] != "use claude" || body["flow"] == nil {
		t.Fatalf("body %v", body)
	}
}
```

Adjust `New(ts.URL, "tok")` to the package's real constructor signature (it is `New(host, token string)`; if `host` is `host:port` rather than a URL, strip the scheme as other tests in the package do).

`cli/internal/cli/flows_test.go` (add):

```go
func TestPrintFlowDecisionShowsPath(t *testing.T) {
	yes, no := true, false
	d := &api.FlowDecision{FlowID: "night", Candidates: []string{"codex"}, Path: []api.PathStep{
		{Node: "c1", Type: "condition", Holds: &yes, Reasons: []string{"schedule 22:00-06:00 holds"}},
		{Node: "a1", Type: "agent", Agent: "codex", Available: &yes, Added: true},
		{Node: "a2", Type: "agent", Agent: "gemini", Available: &no, Reasons: []string{"gemini is not installed or not configured"}},
	}}
	var buf bytes.Buffer
	printFlowDecision(&buf, d, true)
	out := buf.String()
	for _, want := range []string{"Reviews with codex", "c1 condition → yes", "a1 codex ✓ added", "a2 gemini ⚠", "not installed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
}

func TestFlowsAssistPrintsPlanAndApplies(t *testing.T) {
	var put bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/flows":
			_, _ = w.Write([]byte(`{"flows":{"night":{"name":"Night","start":"a1","nodes":{"a1":{"type":"agent","agent":"claude"}}}},"selected":"night","default_id":"default","agents":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/flows/assist":
			_, _ = w.Write([]byte(`{"plan":"Codex first.","agent":"claude","warnings":[],"flow":{"name":"Night","start":"a1","nodes":{"a1":{"type":"agent","agent":"codex"}}}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/flows/night":
			put = true
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()
	out, err := runCLI(t, ts.URL, "flows", "assist", "codex first", "--flow", "night", "--agent", "claude", "--apply")
	if err != nil || !put || !strings.Contains(out, "Codex first.") || !strings.Contains(out, "a1: codex") {
		t.Fatalf("err=%v put=%v out=\n%s", err, put, out)
	}
}

func TestFlowsAssistApplyNeedsFlow(t *testing.T) {
	_, err := runCLI(t, "http://127.0.0.1:1", "flows", "assist", "x", "--apply")
	if err == nil || !strings.Contains(err.Error(), "--apply needs --flow") {
		t.Fatalf("%v", err)
	}
}
```

`runCLI(t, daemonURL, args...) (string, error)` must be the package's existing helper for running a command against a fake daemon; find it in the existing `cli/internal/cli/*_test.go` (e.g. how `flows simulate` is tested) and use its real name and signature. `printFlowDecision` writes to the passed writer; `flows assist` must write to `cmd.OutOrStdout()` so the helper can capture it.

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test-cli`
Expected: FAIL — `undefined: FlowNode`, `AssistFlow`, `Path`.

- [ ] **Step 3: Implement**

`cli/internal/api/flows.go`: replace `FlowRule`, `Flow`, `FlowRuleResult`, `FlowDecision` and `FlowRule.Describe` with:

```go
// FlowNode is one step of a flow graph.
type FlowNode struct {
	Type     string                       `json:"type"`
	Match    string                       `json:"match,omitempty"`
	Quota    map[string]QuotaCondition    `json:"quota,omitempty"`
	Schedule map[string]ScheduleCondition `json:"schedule,omitempty"`
	Yes      string                       `json:"yes,omitempty"`
	No       string                       `json:"no,omitempty"`
	Agent    string                       `json:"agent,omitempty"`
	Next     string                       `json:"next,omitempty"`
	X        *float64                     `json:"x,omitempty"`
	Y        *float64                     `json:"y,omitempty"`
}

// Flow is a review flow: a decision graph walked from Start.
type Flow struct {
	Name  string              `json:"name"`
	Start string              `json:"start"`
	Nodes map[string]FlowNode `json:"nodes"`
}

// FlowAgent is an agent the flow editor offers.
type FlowAgent struct {
	ID           string `json:"id"`
	Installed    bool   `json:"installed"`
	ReviewOnly   bool   `json:"review_only"`
	WriteCapable bool   `json:"write_capable"`
}

// PathStep is one node a simulated flow visited.
type PathStep struct {
	Node      string   `json:"node"`
	Type      string   `json:"type"`
	Agent     string   `json:"agent"`
	Holds     *bool    `json:"holds"`
	Available *bool    `json:"available"`
	Added     bool     `json:"added"`
	Reasons   []string `json:"reasons"`
}

// FlowDecision is the POST /flows/simulate body.
type FlowDecision struct {
	FlowID     string     `json:"flow_id"`
	FlowName   string     `json:"flow_name"`
	At         time.Time  `json:"at"`
	Candidates []string   `json:"candidates"`
	Path       []PathStep `json:"path"`
}

// AssistResult is the POST /flows/assist body: an unsaved draft.
type AssistResult struct {
	Flow       Flow     `json:"flow"`
	Plan       string   `json:"plan"`
	Warnings   []string `json:"warnings"`
	Agent      string   `json:"agent"`
	DurationMS int64    `json:"duration_ms"`
}

// Describe renders the reachable steps in walk order, one line each.
func (f Flow) Describe() []string {
	var out []string
	seen := map[string]bool{}
	var walk func(id string)
	walk = func(id string) {
		n, ok := f.Nodes[id]
		if id == "" || !ok || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, DisplayText(id, 64)+": "+n.Describe())
		walk(n.Yes)
		walk(n.No)
		walk(n.Next)
	}
	walk(f.Start)
	return out
}

func edgeName(id string) string {
	if id == "" {
		return "end"
	}
	return DisplayText(id, 64)
}

// Describe summarises one step.
func (n FlowNode) Describe() string {
	if n.Type == "agent" {
		next := "end"
		if n.Next != "" {
			next = "fallback: " + edgeName(n.Next)
		}
		return DisplayText(n.Agent, 24) + " → " + next
	}
	var conds []string
	for _, k := range sortedKeys(n.Schedule) {
		conds = append(conds, describeSchedule(n.Schedule[k]))
	}
	for _, k := range sortedKeys(n.Quota) {
		conds = append(conds, describeQuota(n.Quota[k]))
	}
	join := " and "
	if n.Match == "any" {
		join = " or "
	}
	return "if " + strings.Join(conds, join) + " → yes: " + edgeName(n.Yes) + " / no: " + edgeName(n.No)
}
```

Move the existing schedule and quota formatting bodies of `FlowRule.Describe` into `describeSchedule(s ScheduleCondition) string` and `describeQuota(q QuotaCondition) string` unchanged (same `DisplayText` caps, same op mapping). Change `FlowListing.Agents` to `[]FlowAgent`. Delete `OrderedRuleKeys` if nothing else uses it.

Add client methods:

```go
// PutFlow creates or replaces a flow.
func (c *Client) PutFlow(id string, f Flow) error {
	_, err := c.doJSON(http.MethodPut, "/flows/"+url.PathEscape(id), f)
	return err
}

// assistTimeout covers the daemon's 180 s agent run plus the repair round's
// headroom on the daemon side.
const assistTimeout = 200 * time.Second

// AssistFlow asks an installed agent to draft a flow from instruction,
// optionally changing current. The draft is not saved.
func (c *Client) AssistFlow(agent, instruction string, current *Flow) (*AssistResult, error) {
	body := map[string]any{"agent": agent, "instruction": instruction}
	if current != nil {
		body["flow"] = current
	}
	slow := *c
	hc := *c.HTTPClient
	hc.Timeout = assistTimeout
	slow.HTTPClient = &hc
	data, err := slow.doJSON(http.MethodPost, "/flows/assist", body)
	if err != nil {
		return nil, err
	}
	var out AssistResult
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decoding assist result: %w", err)
	}
	return &out, nil
}
```

(Import `net/url`. If `Client` contains a mutex or other non-copyable field, instead add an optional `timeout time.Duration` parameter to an internal `doJSONTimeout` in `client.go` that builds the request with a context deadline; keep `doJSON` delegating to it with 0.)

`cli/internal/cli/flows.go`:
- `printFlows`: replace the rules loop with `lines := f.Describe()`; print `(no steps)` when empty, else each line as `    <line>`.
- `printFlowDecision`: replace the `d.Rules` loop with:

```go
	for _, s := range d.Path {
		switch s.Type {
		case "condition":
			branch := "no"
			if s.Holds != nil && *s.Holds {
				branch = "yes"
			}
			fmt.Fprintf(w, "  %s condition → %s\n", api.DisplayText(s.Node, 64), branch)
		case "agent":
			mark := "✓ added"
			switch {
			case s.Available != nil && !*s.Available:
				mark = "⚠"
			case !s.Added:
				mark = "· already listed"
			}
			fmt.Fprintf(w, "  %s %s %s\n", api.DisplayText(s.Node, 64), api.DisplayText(s.Agent, 24), mark)
		default:
			fmt.Fprintf(w, "  %s\n", api.DisplayText(s.Node, 64))
		}
		for _, why := range s.Reasons {
			fmt.Fprintf(w, "      %s\n", api.DisplayText(why, 200))
		}
	}
```

  and change the "no rule matches" sentence to `(no path reaches an available agent)`.
- Register `newFlowsAssistCmd()` in `newFlowsCmd`'s `AddCommand`:

```go
func newFlowsAssistCmd() *cobra.Command {
	var flowID, agent string
	var apply bool
	cmd := &cobra.Command{
		Use:   "assist <instruction>",
		Short: "Draft a flow (or a change to --flow) from plain language with an installed agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if apply && flowID == "" {
				return fmt.Errorf("--apply needs --flow: the id the draft is saved as")
			}
			client := clientFromContext(cmd.Context())
			var current *api.Flow
			if flowID != "" {
				listing, err := client.ListFlows()
				if err != nil {
					return fmt.Errorf("fetching flows: %w", err)
				}
				if f, ok := listing.Flows[flowID]; ok {
					current = &f
				}
			}
			if agent == "" {
				agent = "claude"
			}
			res, err := client.AssistFlow(agent, args[0], current)
			if err != nil {
				return fmt.Errorf("drafting flow: %w", err)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Plan (%s, %.1fs):\n  %s\n\n", api.DisplayText(res.Agent, 24), float64(res.DurationMS)/1000, api.DisplayText(res.Plan, 2000))
			for _, line := range res.Flow.Describe() {
				fmt.Fprintf(w, "  %s\n", line)
			}
			for _, warn := range res.Warnings {
				fmt.Fprintf(w, "  ⚠ %s\n", api.DisplayText(warn, 200))
			}
			if !apply {
				fmt.Fprintln(w, "\nNot saved. Re-run with --flow <id> --apply to save it.")
				return nil
			}
			if err := client.PutFlow(flowID, res.Flow); err != nil {
				return fmt.Errorf("saving flow %s: %w", flowID, err)
			}
			fmt.Fprintf(w, "\nSaved as %s.\n", api.DisplayText(flowID, 64))
			return nil
		},
	}
	cmd.Flags().StringVar(&flowID, "flow", "", "flow id to change (and, with --apply, to save as)")
	cmd.Flags().StringVar(&agent, "agent", "", "installed CLI agent that drafts the flow (default claude)")
	cmd.Flags().BoolVar(&apply, "apply", false, "save the draft with PUT /flows/<id>")
	return cmd
}
```

Update any TUI code that read `FlowListing.Agents` as strings (`grep -rn "\.Agents" cli/internal/tui`) to use `a.ID`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test-cli && make lint-cli`
Expected: PASS, no lint findings.

- [ ] **Step 5: Commit**

```bash
git add cli
git commit -m "feat(cli): graph flows, path trace and flows assist

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 7: Flutter model and API client

**Files:**
- Rewrite: `flutter_app/lib/core/models/flow.dart` (keep `QuotaCondition`, `ScheduleCondition`, `formatPercent`, `compareFlowKeys`, `QuotaWindow`, `AgentQuota` unchanged)
- Modify: `flutter_app/lib/core/api/api_client.dart` (flow methods)
- Test: `flutter_app/test/core/models/flow_graph_test.dart`, `flutter_app/test/core/api/api_client_flows_test.dart`

**Interfaces:**
- Produces:
  - `enum FlowNodeType { condition, agent }`
  - `class FlowNode { final String id; final FlowNodeType type; final String match; final List<QuotaCondition> quota; final List<ScheduleCondition> schedule; final String yes, no, agent, next; final Offset? position; copyWith(...); List<String> get edges; toJson(); factory FlowNode.fromJson(String id, Map) }` — `copyWith` takes `Offset? position` plus `bool clearPosition = false`.
  - `class ReviewFlow { final String name; final String start; final Map<String, FlowNode> nodes; copyWith(...); toJson(); fromJson(); String nextNodeId(String prefix) }`
  - `class FlowAgent { id, installed, reviewOnly, writeCapable }`
  - `FlowListing{flows, selected, defaultId, List<FlowAgent> agents, writeCapable}`
  - `class FlowPathStep { node, type, agent, holds, available, added, reasons }`; `FlowDecision{flowId, candidates, path}`
  - `class FlowIssue { node, field, message }`; `class FlowValidationException extends ApiException { final List<FlowIssue> issues; }`
  - `class FlowAssistResult { flow, plan, warnings, agent, durationMs }`
  - `class FlowAssistRequest` — not needed; `ApiClient.assistFlow({required String agent, required String instruction, ReviewFlow? current, http.Client? client})`
  - `ApiClient.simulateFlow({String? flow, String? repo, DateTime? at, ReviewFlow? graph})`

`Offset` comes from `package:flutter/painting.dart` (allowed in shared code, no `dart:io`).

- [ ] **Step 1: Write the failing tests**

`flutter_app/test/core/models/flow_graph_test.dart`:

```dart
import 'package:flutter/painting.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';

void main() {
  const json = {
    'name': 'Night',
    'start': 'c1',
    'nodes': {
      'c1': {
        'type': 'condition',
        'match': 'any',
        'yes': 'a1',
        'no': 'a2',
        'schedule': {
          's2': {'from': '22:00', 'to': '06:00'},
          's1': {'from': '08:00', 'to': '09:00', 'days': ['mon']},
        },
      },
      'a1': {'type': 'agent', 'agent': 'codex', 'next': 'a2', 'x': 10, 'y': 20.5},
      'a2': {'type': 'agent', 'agent': 'claude'},
    },
  };

  test('parses a graph with ordered conditions and positions', () {
    final f = ReviewFlow.fromJson(json);
    expect(f.start, 'c1');
    expect(f.nodes['c1']!.type, FlowNodeType.condition);
    expect(f.nodes['c1']!.schedule.first.from, '08:00');
    expect(f.nodes['a1']!.position, const Offset(10, 20.5));
    expect(f.nodes['a2']!.position, isNull);
    expect(f.nodes['c1']!.edges, ['a1', 'a2']);
  });

  test('round-trips to the daemon shape', () {
    final out = ReviewFlow.fromJson(json).toJson();
    final c1 = (out['nodes'] as Map)['c1'] as Map;
    expect(c1['match'], 'any');
    expect((c1['schedule'] as Map).keys, ['s1', 's2']);
    expect(c1.containsKey('agent'), isFalse);
    final a2 = (out['nodes'] as Map)['a2'] as Map;
    expect(a2.containsKey('x'), isFalse);
    expect(a2.containsKey('next'), isFalse);
  });

  test('nextNodeId never collides', () {
    final f = ReviewFlow.fromJson(json);
    expect(f.nextNodeId('a'), 'a3');
    expect(f.nextNodeId('c'), 'c2');
  });

  test('listing parses agent objects', () {
    final l = FlowListing.fromJson({
      'flows': {'night': json},
      'agents': [
        {'id': 'claude', 'installed': true, 'review_only': false, 'write_capable': true},
        {'id': 'openrouter', 'installed': false, 'review_only': true},
      ],
    });
    expect(l.agents.first.installed, isTrue);
    expect(l.agents.last.reviewOnly, isTrue);
    expect(l.flows['night']!.nodes.length, 3);
  });

  test('decision parses the path', () {
    final d = FlowDecision.fromJson({
      'flow_id': 'night',
      'candidates': ['codex'],
      'path': [
        {'node': 'c1', 'type': 'condition', 'holds': true, 'reasons': ['ok']},
        {'node': 'a1', 'type': 'agent', 'agent': 'codex', 'available': true, 'added': true, 'reasons': []},
      ],
    });
    expect(d.path.map((s) => s.node), ['c1', 'a1']);
    expect(d.path.first.holds, isTrue);
    expect(d.path.last.added, isTrue);
  });
}
```

`flutter_app/test/core/api/api_client_flows_test.dart` — follow the existing ApiClient test pattern in `flutter_app/test/core/api/` (a `MockClient` from `package:http/testing.dart` injected into `ApiClient`; reuse the same constructor call the other tests use):

```dart
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/flow.dart';

const _graph = ReviewFlow(name: 'x', start: 'a1', nodes: {
  'a1': FlowNode(id: 'a1', type: FlowNodeType.agent, agent: 'claude'),
});

void main() {
  test('putFlow turns a 422 into FlowValidationException with issues', () async {
    final api = apiClientWith(MockClient((req) async => http.Response(
          jsonEncode({
            'error': 'nodes.a1: cycle',
            'errors': [
              {'node': 'a1', 'message': 'cycle'},
            ],
          }),
          422,
        )));
    expect(
      () => api.putFlow('x', _graph),
      throwsA(isA<FlowValidationException>()
          .having((e) => e.issues.single.node, 'node', 'a1')),
    );
  });

  test('simulateFlow sends an unsaved graph', () async {
    late Map<String, dynamic> sent;
    final api = apiClientWith(MockClient((req) async {
      sent = jsonDecode(req.body) as Map<String, dynamic>;
      return http.Response(jsonEncode({'flow_id': 'draft', 'candidates': ['claude'], 'path': []}), 200);
    }));
    final d = await api.simulateFlow(graph: _graph);
    expect((sent['graph'] as Map)['start'], 'a1');
    expect(d.candidates, ['claude']);
  });

  test('assistFlow posts the request and parses the draft', () async {
    late Map<String, dynamic> sent;
    final api = apiClientWith(MockClient((req) async {
      expect(req.url.path, endsWith('/flows/assist'));
      sent = jsonDecode(req.body) as Map<String, dynamic>;
      return http.Response(jsonEncode({
        'plan': 'p',
        'agent': 'claude',
        'warnings': ['w'],
        'duration_ms': 1200,
        'flow': _graph.toJson(),
      }), 200);
    }));
    final r = await api.assistFlow(agent: 'claude', instruction: 'go', current: _graph);
    expect(sent['instruction'], 'go');
    expect(sent['flow'], isNotNull);
    expect(r.plan, 'p');
    expect(r.flow.start, 'a1');
    expect(r.warnings, ['w']);
  });

  test('assistFlow 409 and 422 surface the daemon message', () async {
    final busy = apiClientWith(MockClient((_) async =>
        http.Response(jsonEncode({'error': 'another flow is being drafted'}), 409)));
    expect(() => busy.assistFlow(agent: 'claude', instruction: 'x'),
        throwsA(isA<ApiException>().having((e) => e.message, 'message', contains('another flow'))));
    final bad = apiClientWith(MockClient((_) async => http.Response(
        jsonEncode({'error': 'invalid', 'errors': [{'node': 'a1', 'message': 'm'}]}), 422)));
    expect(() => bad.assistFlow(agent: 'claude', instruction: 'x'),
        throwsA(isA<FlowValidationException>()));
  });
}
```

`apiClientWith(http.Client)` is a 3-line helper at the bottom of the test file that constructs `ApiClient` with the injected client exactly as the existing api tests do (check `flutter_app/test/core/api/api_client_test.dart` for the constructor; `ApiException.message` is the existing field name — verify).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd flutter_app && flutter test test/core/models/flow_graph_test.dart test/core/api/api_client_flows_test.dart`
Expected: FAIL — `FlowNode`, `FlowNodeType`, `FlowValidationException`, `assistFlow` undefined.

- [ ] **Step 3: Implement**

In `flutter_app/lib/core/models/flow.dart`, update the library doc to "Review flows: decision graphs that decide which agents review a PR (mirrors the daemon's config.FlowConfig)", add `import 'package:flutter/painting.dart';`, delete `FlowRule`, `FlowRuleResult`, the old `ReviewFlow` and `FlowDecision`, and add:

```dart
enum FlowNodeType { condition, agent }

/// One step of a flow graph. A condition branches through [yes]/[no]; an
/// agent adds [agent] to the candidates and continues through [next], its
/// fallback. An empty edge ends the walk.
class FlowNode {
  final String id;
  final FlowNodeType type;

  /// 'all' or 'any' (conditions only).
  final String match;
  final List<QuotaCondition> quota;
  final List<ScheduleCondition> schedule;
  final String yes;
  final String no;
  final String agent;
  final String next;

  /// Canvas position; null until laid out.
  final Offset? position;

  const FlowNode({
    required this.id,
    required this.type,
    this.match = 'all',
    this.quota = const [],
    this.schedule = const [],
    this.yes = '',
    this.no = '',
    this.agent = '',
    this.next = '',
    this.position,
  });

  bool get isCondition => type == FlowNodeType.condition;

  /// Outgoing edges that are set, in yes, no, next order.
  List<String> get edges => [
    for (final e in [yes, no, next])
      if (e.isNotEmpty) e,
  ];

  FlowNode copyWith({
    String? id,
    String? match,
    List<QuotaCondition>? quota,
    List<ScheduleCondition>? schedule,
    String? yes,
    String? no,
    String? agent,
    String? next,
    Offset? position,
    bool clearPosition = false,
  }) => FlowNode(
    id: id ?? this.id,
    type: type,
    match: match ?? this.match,
    quota: quota ?? this.quota,
    schedule: schedule ?? this.schedule,
    yes: yes ?? this.yes,
    no: no ?? this.no,
    agent: agent ?? this.agent,
    next: next ?? this.next,
    position: clearPosition ? null : (position ?? this.position),
  );

  /// This node with every edge that points at [target] cleared.
  FlowNode withoutEdgesTo(String target) => copyWith(
    yes: yes == target ? '' : yes,
    no: no == target ? '' : no,
    next: next == target ? '' : next,
  );

  factory FlowNode.fromJson(String id, Map<String, dynamic> j) {
    final x = (j['x'] as num?)?.toDouble();
    final y = (j['y'] as num?)?.toDouble();
    final match = (j['match'] as String?) ?? '';
    return FlowNode(
      id: id,
      type: j['type'] == 'condition' ? FlowNodeType.condition : FlowNodeType.agent,
      match: match.isEmpty ? 'all' : match,
      quota: _orderedValues(j['quota']).map(QuotaCondition.fromJson).toList(),
      schedule: _orderedValues(j['schedule']).map(ScheduleCondition.fromJson).toList(),
      yes: (j['yes'] as String?) ?? '',
      no: (j['no'] as String?) ?? '',
      agent: (j['agent'] as String?) ?? '',
      next: (j['next'] as String?) ?? '',
      position: x != null && y != null ? Offset(x, y) : null,
    );
  }

  Map<String, dynamic> toJson() => {
    'type': isCondition ? 'condition' : 'agent',
    if (isCondition) ...{
      if (match == 'any') 'match': 'any',
      if (quota.isNotEmpty)
        'quota': {for (var i = 0; i < quota.length; i++) 'q${i + 1}': quota[i].toJson()},
      if (schedule.isNotEmpty)
        'schedule': {for (var i = 0; i < schedule.length; i++) 's${i + 1}': schedule[i].toJson()},
      if (yes.isNotEmpty) 'yes': yes,
      if (no.isNotEmpty) 'no': no,
    } else ...{
      'agent': agent,
      if (next.isNotEmpty) 'next': next,
    },
    if (position != null) ...{'x': position!.dx, 'y': position!.dy},
  };
}

/// A flow: a decision graph walked from [start].
class ReviewFlow {
  final String name;
  final String start;
  final Map<String, FlowNode> nodes;

  const ReviewFlow({this.name = '', this.start = '', this.nodes = const {}});

  ReviewFlow copyWith({String? name, String? start, Map<String, FlowNode>? nodes}) =>
      ReviewFlow(name: name ?? this.name, start: start ?? this.start, nodes: nodes ?? this.nodes);

  /// The first free id of the form <prefix><n>, n ≥ 1.
  String nextNodeId(String prefix) {
    var n = 1;
    while (nodes.containsKey('$prefix$n')) {
      n++;
    }
    return '$prefix$n';
  }

  factory ReviewFlow.fromJson(Map<String, dynamic> j) {
    final raw = j['nodes'];
    return ReviewFlow(
      name: (j['name'] as String?) ?? '',
      start: (j['start'] as String?) ?? '',
      nodes: {
        if (raw is Map<String, dynamic>)
          for (final e in raw.entries)
            if (e.value is Map<String, dynamic>)
              e.key: FlowNode.fromJson(e.key, e.value as Map<String, dynamic>),
      },
    );
  }

  Map<String, dynamic> toJson() => {
    'name': name,
    'start': start,
    'nodes': {for (final e in nodes.entries) e.key: e.value.toJson()},
  };
}

/// An agent the editor can offer (GET /flows `agents`).
class FlowAgent {
  final String id;
  final bool installed;
  final bool reviewOnly;
  final bool writeCapable;

  const FlowAgent({required this.id, this.installed = false, this.reviewOnly = false, this.writeCapable = false});

  factory FlowAgent.fromJson(Map<String, dynamic> j) => FlowAgent(
    id: (j['id'] as String?) ?? '',
    installed: (j['installed'] as bool?) ?? false,
    reviewOnly: (j['review_only'] as bool?) ?? false,
    writeCapable: (j['write_capable'] as bool?) ?? false,
  );
}
```

`FlowListing`: change `final List<String> agents` to `final List<FlowAgent> agents` and parse with `((j['agents'] as List<dynamic>?) ?? const []).whereType<Map<String, dynamic>>().map(FlowAgent.fromJson).toList()`.

Add:

```dart
/// One node a simulated flow visited.
class FlowPathStep {
  final String node;
  final String type;
  final String agent;
  final bool? holds;
  final bool? available;
  final bool added;
  final List<String> reasons;

  const FlowPathStep({required this.node, this.type = '', this.agent = '', this.holds, this.available, this.added = false, this.reasons = const []});

  factory FlowPathStep.fromJson(Map<String, dynamic> j) => FlowPathStep(
    node: (j['node'] as String?) ?? '',
    type: (j['type'] as String?) ?? '',
    agent: (j['agent'] as String?) ?? '',
    holds: j['holds'] as bool?,
    available: j['available'] as bool?,
    added: (j['added'] as bool?) ?? false,
    reasons: ((j['reasons'] as List<dynamic>?) ?? const []).whereType<String>().toList(),
  );
}

/// POST /flows/simulate.
class FlowDecision {
  final String flowId;
  final List<String> candidates;
  final List<FlowPathStep> path;

  const FlowDecision({required this.flowId, this.candidates = const [], this.path = const []});

  factory FlowDecision.fromJson(Map<String, dynamic> j) => FlowDecision(
    flowId: (j['flow_id'] as String?) ?? '',
    candidates: ((j['candidates'] as List<dynamic>?) ?? const []).whereType<String>().toList(),
    path: ((j['path'] as List<dynamic>?) ?? const [])
        .whereType<Map<String, dynamic>>()
        .map(FlowPathStep.fromJson)
        .toList(),
  );
}

/// A validation finding from the daemon, pinned to a node when it has one.
class FlowIssue {
  final String node;
  final String field;
  final String message;

  const FlowIssue({this.node = '', this.field = '', required this.message});

  factory FlowIssue.fromJson(Map<String, dynamic> j) => FlowIssue(
    node: (j['node'] as String?) ?? '',
    field: (j['field'] as String?) ?? '',
    message: (j['message'] as String?) ?? '',
  );
}

/// POST /flows/assist: an unsaved draft.
class FlowAssistResult {
  final ReviewFlow flow;
  final String plan;
  final List<String> warnings;
  final String agent;
  final int durationMs;

  const FlowAssistResult({required this.flow, this.plan = '', this.warnings = const [], this.agent = '', this.durationMs = 0});

  factory FlowAssistResult.fromJson(Map<String, dynamic> j) => FlowAssistResult(
    flow: ReviewFlow.fromJson((j['flow'] as Map<String, dynamic>?) ?? const {}),
    plan: (j['plan'] as String?) ?? '',
    warnings: ((j['warnings'] as List<dynamic>?) ?? const []).whereType<String>().toList(),
    agent: (j['agent'] as String?) ?? '',
    durationMs: (j['duration_ms'] as num?)?.toInt() ?? 0,
  );
}
```

In `api_client.dart`:

```dart
/// A flow the daemon rejected, with each problem pinned to its step.
class FlowValidationException extends ApiException {
  final List<FlowIssue> issues;
  FlowValidationException(super.message, this.issues);
}

  /// Throws [FlowValidationException] for a 422 carrying `errors`, else
  /// [ApiException] with the daemon's message.
  Never _throwFlowError(http.Response resp, String fallback) {
    if (resp.statusCode == 422) {
      try {
        final body = jsonDecode(resp.body) as Map<String, dynamic>;
        final issues = ((body['errors'] as List<dynamic>?) ?? const [])
            .whereType<Map<String, dynamic>>()
            .map(FlowIssue.fromJson)
            .toList();
        throw FlowValidationException((body['error'] as String?) ?? fallback, issues);
      } on FormatException {
        // fall through to the generic message
      }
    }
    throw ApiException(_errorMessage(resp.body, fallback));
  }
```

(Place `FlowValidationException` next to `ApiException`; if `ApiException`'s constructor is not a single positional `message`, adapt `super(...)`.) Use `_throwFlowError` in `putFlow` and `simulateFlow` instead of the plain `throw ApiException`. Change `simulateFlow` to accept `ReviewFlow? graph` and add `if (graph != null) 'graph': graph.toJson(),` to the body. Add:

```dart
  /// Asks an installed agent to draft a flow (or a change to [current]).
  /// Nothing is saved. Pass [client] to cancel by closing it.
  Future<FlowAssistResult> assistFlow({
    required String agent,
    required String instruction,
    ReviewFlow? current,
    http.Client? client,
  }) async {
    final resp = await (client ?? _client).post(
      _uri('/flows/assist'),
      headers: await _authHeaders(),
      body: jsonEncode({
        'agent': agent,
        'instruction': instruction,
        if (current != null) 'flow': current.toJson(),
      }),
    );
    if (resp.statusCode != 200) {
      _throwFlowError(resp, 'Could not draft the flow');
    }
    return FlowAssistResult.fromJson(jsonDecode(resp.body) as Map<String, dynamic>);
  }
```

`flows_screen.dart` will not compile against the new model. To keep `flutter analyze` and the suite green between tasks, in this task:
- move the list part of `flows_screen.dart` (`flowsProvider`, `FlowsScreen` with its `_create` dialog, `_FlowCard`) into a new `flutter_app/lib/features/flows/flows_list_screen.dart`, unchanged except the card summary, which becomes `'${flow.nodes.length} steps · ${agents.join(', ')}'` where `agents` are the distinct non-empty `agent` values of the flow's agent nodes sorted by node id;
- delete the editor part (`FlowEditorScreen`, `FlowRuleCard`, `_ScheduleRow`, `_QuotaRow`, the simulation panel) and `flows_screen.dart` itself; move the list tests of `flutter_app/test/features/flows/flows_screen_test.dart` to `flows_list_screen_test.dart` and delete the editor tests (Tasks 10–12 replace them);
- point the router's `/flows/:id` builder at a temporary `Scaffold(body: Center(child: Text('Flow ${state.pathParameters['id']}')))`, which Task 11 replaces, and `/flows` at `FlowsScreen` from the new file. Fix the org/repo/config screens only if they used `FlowListing.agents` (they use `flows`/`ids`; verify with `grep -rn "\.agents" flutter_app/lib/features`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd flutter_app && flutter test && flutter analyze`
Expected: PASS, no issues.

- [ ] **Step 5: Commit**

```bash
git add flutter_app
git commit -m "feat(app): flow graph model and assist/simulate API

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 8: Pure graph helpers — validation, auto-layout, diff

**Files:**
- Create: `flutter_app/lib/features/flows/graph/graph_validation.dart`, `graph/auto_layout.dart`, `graph/flow_diff.dart`
- Test: `flutter_app/test/features/flows/graph/{graph_validation,auto_layout,flow_diff}_test.dart`

**Interfaces:**
- Consumes: Task 7 model.
- Produces:
  - `class GraphCheck { final List<FlowIssue> errors; final List<FlowIssue> warnings; Set<String> get errorNodes; Set<String> get unreachable; bool get ok }`
  - `GraphCheck checkGraph(ReviewFlow f)` — mirrors daemon §3.3 (no agent-allowlist check; the daemon owns that)
  - `bool wouldCreateCycle(ReviewFlow f, String from, String to)`
  - `Set<String> reachableFrom(ReviewFlow f)`
  - `const kNodeSize = Size(220, 76); const kColumnGap = 120.0; const kRowGap = 40.0; const kStartPosition = Offset(40, 200);`
  - `ReviewFlow autoLayout(ReviewFlow f, {bool onlyMissing = false})`
  - `enum DiffKind { added, changed, removed }`; `class FlowDiff { final Map<String, DiffKind> nodes; bool get isEmpty; }`; `FlowDiff diffFlows(ReviewFlow before, ReviewFlow after)` — positions are ignored when comparing.

- [ ] **Step 1: Write the failing tests**

`graph_validation_test.dart`:

```dart
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/graph/graph_validation.dart';

ReviewFlow g(Map<String, FlowNode> nodes, {String start = 'a1'}) =>
    ReviewFlow(name: 'x', start: start, nodes: nodes);
FlowNode agent(String id, {String next = ''}) =>
    FlowNode(id: id, type: FlowNodeType.agent, agent: 'claude', next: next);
FlowNode cond(String id, {String yes = '', String no = ''}) => FlowNode(
      id: id, type: FlowNodeType.condition, yes: yes, no: no,
      schedule: const [ScheduleCondition(from: '22:00', to: '06:00')]);

void main() {
  test('valid graph has no findings', () {
    final r = checkGraph(g({'a1': agent('a1', next: 'a2'), 'a2': agent('a2')}));
    expect(r.ok, isTrue);
    expect(r.warnings, isEmpty);
  });

  test('cycle, dangling edge, empty condition and missing start are errors', () {
    expect(checkGraph(g({'a1': agent('a1', next: 'a1')})).errorNodes, contains('a1'));
    expect(checkGraph(g({'a1': agent('a1', next: 'zz')})).errorNodes, contains('a1'));
    final empty = FlowNode(id: 'c1', type: FlowNodeType.condition, yes: 'a1');
    expect(checkGraph(g({'c1': empty, 'a1': agent('a1')}, start: 'c1')).errorNodes, contains('c1'));
    expect(checkGraph(g({'a1': agent('a1')}, start: '')).ok, isFalse);
    final noAgent = FlowNode(id: 'a1', type: FlowNodeType.agent);
    expect(checkGraph(g({'a1': noAgent})).errorNodes, contains('a1'));
  });

  test('unreachable nodes are warnings', () {
    final r = checkGraph(g({'a1': agent('a1'), 'a9': agent('a9')}));
    expect(r.ok, isTrue);
    expect(r.unreachable, {'a9'});
  });

  test('wouldCreateCycle', () {
    final f = g({'c1': cond('c1', yes: 'a1'), 'a1': agent('a1', next: 'a2'), 'a2': agent('a2')}, start: 'c1');
    expect(wouldCreateCycle(f, 'a2', 'c1'), isTrue);
    expect(wouldCreateCycle(f, 'a2', 'a2'), isTrue);
    expect(wouldCreateCycle(f, 'c1', 'a2'), isFalse);
  });
}
```

`auto_layout_test.dart`:

```dart
import 'package:flutter/painting.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';

void main() {
  final flow = ReviewFlow(start: 'c1', nodes: {
    'c1': const FlowNode(id: 'c1', type: FlowNodeType.condition, yes: 'a1', no: 'a2'),
    'a1': const FlowNode(id: 'a1', type: FlowNodeType.agent, agent: 'codex', next: 'a2'),
    'a2': const FlowNode(id: 'a2', type: FlowNodeType.agent, agent: 'claude'),
    'x9': const FlowNode(id: 'x9', type: FlowNodeType.agent, agent: 'gemini'),
  });

  test('columns follow the longest path from start; yes above no', () {
    final out = autoLayout(flow);
    final p = {for (final n in out.nodes.values) n.id: n.position!};
    final col = kNodeSize.width + kColumnGap;
    expect(p['c1']!.dx, kStartPosition.dx + col);
    expect(p['a1']!.dx, p['c1']!.dx + col);
    expect(p['a2']!.dx, p['a1']!.dx + col); // a2 is after a1 via fallback
    expect(p['a1']!.dy, lessThan(p['a2']!.dy + 1));
  });

  test('every node gets a position, unreachable ones below', () {
    final out = autoLayout(flow);
    expect(out.nodes.values.every((n) => n.position != null), isTrue);
    final maxReachableY = ['c1', 'a1', 'a2'].map((id) => out.nodes[id]!.position!.dy).reduce((a, b) => a > b ? a : b);
    expect(out.nodes['x9']!.position!.dy, greaterThan(maxReachableY));
  });

  test('onlyMissing keeps existing positions', () {
    final placed = flow.copyWith(nodes: {
      ...flow.nodes,
      'a1': flow.nodes['a1']!.copyWith(position: const Offset(999, 999)),
    });
    expect(autoLayout(placed, onlyMissing: true).nodes['a1']!.position, const Offset(999, 999));
    expect(autoLayout(placed).nodes['a1']!.position, isNot(const Offset(999, 999)));
  });

  test('layout is stable', () {
    expect(autoLayout(flow).toJson(), autoLayout(flow).toJson());
  });
}
```

`flow_diff_test.dart`:

```dart
import 'package:flutter/painting.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';

void main() {
  const a1 = FlowNode(id: 'a1', type: FlowNodeType.agent, agent: 'claude');
  final before = ReviewFlow(start: 'a1', nodes: {'a1': a1, 'a2': const FlowNode(id: 'a2', type: FlowNodeType.agent, agent: 'codex')});

  test('added, changed, removed; positions ignored', () {
    final after = ReviewFlow(start: 'a1', nodes: {
      'a1': a1.copyWith(next: 'a3', position: const Offset(5, 5)),
      'a3': const FlowNode(id: 'a3', type: FlowNodeType.agent, agent: 'gemini'),
    });
    final d = diffFlows(before, after);
    expect(d.nodes, {'a1': DiffKind.changed, 'a3': DiffKind.added, 'a2': DiffKind.removed});
  });

  test('position-only change is no change', () {
    final moved = before.copyWith(nodes: {...before.nodes, 'a1': a1.copyWith(position: const Offset(1, 1))});
    expect(diffFlows(before, moved).isEmpty, isTrue);
  });
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd flutter_app && flutter test test/features/flows/graph/`
Expected: FAIL — files not found.

- [ ] **Step 3: Implement**

`graph/graph_validation.dart`:

```dart
import 'package:heimdallm/core/models/flow.dart';

/// Client-side mirror of the daemon's flow checks, so the canvas can mark
/// problems as the operator edits. The daemon remains the authority.
class GraphCheck {
  final List<FlowIssue> errors;
  final List<FlowIssue> warnings;
  const GraphCheck(this.errors, this.warnings);

  bool get ok => errors.isEmpty;
  Set<String> get errorNodes => {for (final e in errors) if (e.node.isNotEmpty) e.node};
  Set<String> get unreachable => {for (final w in warnings) if (w.node.isNotEmpty) w.node};
}

final _idPattern = RegExp(r'^[a-z0-9][a-z0-9_-]{0,63}$');

Set<String> reachableFrom(ReviewFlow f) {
  final seen = <String>{};
  final stack = [f.start];
  while (stack.isNotEmpty) {
    final id = stack.removeLast();
    final n = f.nodes[id];
    if (n == null || !seen.add(id)) continue;
    stack.addAll(n.edges);
  }
  return seen;
}

/// Whether adding the edge from → to would close a loop.
bool wouldCreateCycle(ReviewFlow f, String from, String to) {
  if (from == to) return true;
  final seen = <String>{};
  final stack = [to];
  while (stack.isNotEmpty) {
    final id = stack.removeLast();
    if (id == from) return true;
    if (!seen.add(id)) continue;
    stack.addAll(f.nodes[id]?.edges ?? const []);
  }
  return false;
}

String? _cycleNode(ReviewFlow f) {
  final colour = <String, int>{};
  String? visit(String id) {
    colour[id] = 1;
    for (final e in f.nodes[id]!.edges) {
      if (!f.nodes.containsKey(e)) continue;
      if (colour[e] == 1) return e;
      if (colour[e] == null) {
        final c = visit(e);
        if (c != null) return c;
      }
    }
    colour[id] = 2;
    return null;
  }

  for (final id in (f.nodes.keys.toList()..sort())) {
    if (colour[id] == null) {
      final c = visit(id);
      if (c != null) return c;
    }
  }
  return null;
}

GraphCheck checkGraph(ReviewFlow f) {
  final errors = <FlowIssue>[];
  void err(String node, String msg, {String field = ''}) =>
      errors.add(FlowIssue(node: node, field: field, message: msg));

  if (f.nodes.length > 200) err('', 'At most 200 steps per flow.', field: 'nodes');
  if (f.nodes.isEmpty) {
    if (f.start.isNotEmpty) err('', 'The flow has no steps.', field: 'start');
    return GraphCheck(errors, const []);
  }
  if (f.start.isEmpty) {
    err('', 'Connect Start to the first step.', field: 'start');
  } else if (!f.nodes.containsKey(f.start)) {
    err('', 'Start points at a step that no longer exists.', field: 'start');
  }
  for (final id in (f.nodes.keys.toList()..sort())) {
    final n = f.nodes[id]!;
    if (!_idPattern.hasMatch(id)) err(id, 'Invalid step id.');
    for (final e in n.edges) {
      if (!f.nodes.containsKey(e)) err(id, 'Points at a step that no longer exists.');
    }
    if (n.isCondition) {
      final total = n.quota.length + n.schedule.length;
      if (total == 0) err(id, 'Add at least one schedule or quota condition.');
      if (total > 20) err(id, 'At most 20 conditions per step.');
      for (final s in n.schedule) {
        if (!RegExp(r'^([01]\d|2[0-3]):[0-5]\d$').hasMatch(s.from) ||
            !RegExp(r'^([01]\d|2[0-3]):[0-5]\d$').hasMatch(s.to)) {
          err(id, 'Times must be HH:MM.');
        } else if (s.from == s.to) {
          err(id, 'A schedule needs different start and end times.');
        }
      }
      for (final q in n.quota) {
        if (q.agent.isEmpty) err(id, 'Pick the agent whose quota is checked.');
        if (q.percent < 0 || q.percent > 100) err(id, 'Percent must be 0–100.');
      }
    } else if (n.agent.isEmpty) {
      err(id, 'Pick an agent.');
    }
  }
  final cyc = _cycleNode(f);
  if (cyc != null) err(cyc, 'This step is part of a loop.');
  final reach = reachableFrom(f);
  final warnings = [
    for (final id in (f.nodes.keys.toList()..sort()))
      if (!reach.contains(id)) FlowIssue(node: id, message: 'Not reachable from Start; it is ignored.'),
  ];
  return GraphCheck(errors, warnings);
}
```

`graph/auto_layout.dart`:

```dart
import 'dart:math' as math;

import 'package:flutter/painting.dart';
import 'package:heimdallm/core/models/flow.dart';

const kNodeSize = Size(220, 76);
const kColumnGap = 120.0;
const kRowGap = 40.0;
const kStartPosition = Offset(40, 200);

/// Layered layout: a node's column is the longest path from Start to it;
/// within a column, nodes keep the order in which a yes-before-no-before-
/// next depth-first walk meets them, so "yes" sits above "no". Unreachable
/// nodes go in a row below everything else. With [onlyMissing], nodes that
/// already have a position keep it.
ReviewFlow autoLayout(ReviewFlow f, {bool onlyMissing = false}) {
  final order = <String>[];
  final seen = <String>{};
  void dfs(String id) {
    final n = f.nodes[id];
    if (n == null || !seen.add(id)) return;
    order.add(id);
    for (final e in n.edges) {
      dfs(e);
    }
  }

  dfs(f.start);
  final depth = <String, int>{for (final id in order) id: 0};
  if (f.nodes.containsKey(f.start)) depth[f.start] = 1;
  // Longest path on a DAG: relax in topological order (Kahn).
  final indeg = <String, int>{for (final id in order) id: 0};
  for (final id in order) {
    for (final e in f.nodes[id]!.edges) {
      if (indeg.containsKey(e)) indeg[e] = indeg[e]! + 1;
    }
  }
  final ready = [for (final id in order) if (indeg[id] == 0) id];
  while (ready.isNotEmpty) {
    final id = ready.removeAt(0);
    for (final e in f.nodes[id]!.edges) {
      if (!indeg.containsKey(e)) continue;
      depth[e] = math.max(depth[e]!, depth[id]! + 1);
      indeg[e] = indeg[e]! - 1;
      if (indeg[e] == 0) ready.add(e);
    }
  }
  final columns = <int, List<String>>{};
  for (final id in order) {
    columns.putIfAbsent(depth[id]!, () => []).add(id);
  }
  final colW = kNodeSize.width + kColumnGap;
  final rowH = kNodeSize.height + kRowGap;
  final pos = <String, Offset>{};
  var maxRows = 1;
  columns.forEach((col, ids) {
    maxRows = math.max(maxRows, ids.length);
    final top = kStartPosition.dy - (ids.length - 1) * rowH / 2;
    for (var i = 0; i < ids.length; i++) {
      pos[ids[i]] = Offset(kStartPosition.dx + col * colW, top + i * rowH);
    }
  });
  final rest = (f.nodes.keys.where((id) => !pos.containsKey(id)).toList()..sort());
  final restY = kStartPosition.dy + (maxRows / 2 + 1) * rowH;
  for (var i = 0; i < rest.length; i++) {
    pos[rest[i]] = Offset(kStartPosition.dx + (i + 1) * colW, restY);
  }
  return f.copyWith(nodes: {
    for (final e in f.nodes.entries)
      e.key: onlyMissing && e.value.position != null ? e.value : e.value.copyWith(position: pos[e.key]),
  });
}
```

`graph/flow_diff.dart`:

```dart
import 'dart:convert';

import 'package:heimdallm/core/models/flow.dart';

enum DiffKind { added, changed, removed }

class FlowDiff {
  final Map<String, DiffKind> nodes;
  const FlowDiff(this.nodes);
  bool get isEmpty => nodes.isEmpty;
}

String _shape(FlowNode n) => jsonEncode(n.copyWith(clearPosition: true).toJson());

/// Node-level differences from [before] to [after], ignoring positions.
FlowDiff diffFlows(ReviewFlow before, ReviewFlow after) {
  final out = <String, DiffKind>{};
  for (final e in after.nodes.entries) {
    final old = before.nodes[e.key];
    if (old == null) {
      out[e.key] = DiffKind.added;
    } else if (_shape(old) != _shape(e.value)) {
      out[e.key] = DiffKind.changed;
    }
  }
  for (final id in before.nodes.keys) {
    if (!after.nodes.containsKey(id)) out[id] = DiffKind.removed;
  }
  return FlowDiff(out);
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd flutter_app && flutter test test/features/flows/graph/ && flutter analyze`
Expected: PASS, no issues.

- [ ] **Step 5: Commit**

```bash
git add flutter_app/lib/features/flows/graph flutter_app/test/features/flows/graph
git commit -m "feat(app): flow graph validation, auto-layout and diff

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 9: Editor controller (state, undo/redo, draft)

**Files:**
- Create: `flutter_app/lib/features/flows/editor/flow_editor_controller.dart`
- Test: `flutter_app/test/features/flows/editor/flow_editor_controller_test.dart`

**Interfaces:**
- Consumes: Task 7 model, Task 8 helpers.
- Produces:
  - `enum EdgePort { yes, no, next, start }`
  - `sealed class Selection` with `NodeSelection(String id)`, `EdgeSelection(String from, EdgePort port)`, `NoSelection()`
  - `class FlowDraft { final ReviewFlow flow; final String plan; final List<String> warnings; final FlowDiff diff; }`
  - `class FlowEditorState { final ReviewFlow flow; final ReviewFlow saved; final Selection selection; final FlowDraft? draft; final GraphCheck check; final FlowDecision? simulation; bool get dirty; bool get canUndo; bool get canRedo; }`
  - `class FlowEditorController extends Notifier<FlowEditorState>` with methods:
    `void load(ReviewFlow flow)`, `void markSaved()`, `String addNode(FlowNodeType type, Offset at)`, `void moveNode(String id, Offset to, {bool commit = true})`, `bool connect(String from, EdgePort port, String to)` (false when it would create a cycle), `void disconnect(String from, EdgePort port)`, `void deleteSelection()`, `void deleteNode(String id)`, `void updateNode(FlowNode node)`, `void rename(String name)`, `void select(Selection s)`, `void arrange()`, `void undo()`, `void redo()`, `void showDraft(FlowAssistResult r)`, `void acceptDraft()`, `void discardDraft()`, `void setSimulation(FlowDecision? d)`
  - `final flowEditorProvider = NotifierProvider.autoDispose<FlowEditorController, FlowEditorState>(FlowEditorController.new);`
  - `EdgePort.start` means "Start → node", i.e. `flow.start`.

- [ ] **Step 1: Write the failing tests**

```dart
import 'package:flutter/painting.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';

void main() {
  late ProviderContainer c;
  FlowEditorController ctl() => c.read(flowEditorProvider.notifier);
  FlowEditorState st() => c.read(flowEditorProvider);

  setUp(() {
    c = ProviderContainer();
    c.listen(flowEditorProvider, (_, _) {}); // keep the autoDispose provider alive
    ctl().load(const ReviewFlow(name: 'x'));
  });
  tearDown(() => c.dispose());

  test('add, connect from start, dirty, undo/redo', () {
    final a = ctl().addNode(FlowNodeType.agent, const Offset(300, 200));
    expect(st().flow.nodes[a]!.position, const Offset(300, 200));
    expect(ctl().connect('', EdgePort.start, a), isTrue);
    expect(st().flow.start, a);
    expect(st().dirty, isTrue);
    ctl().undo();
    expect(st().flow.start, '');
    ctl().redo();
    expect(st().flow.start, a);
  });

  test('connect refuses cycles and replaces an occupied port', () {
    final a1 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    final a2 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    final a3 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    expect(ctl().connect(a1, EdgePort.next, a2), isTrue);
    expect(ctl().connect(a2, EdgePort.next, a1), isFalse);
    expect(st().flow.nodes[a2]!.next, '');
    expect(ctl().connect(a1, EdgePort.next, a3), isTrue);
    expect(st().flow.nodes[a1]!.next, a3);
  });

  test('condition ports are yes/no; agent port is next', () {
    final cnd = ctl().addNode(FlowNodeType.condition, Offset.zero);
    final a = ctl().addNode(FlowNodeType.agent, Offset.zero);
    ctl().connect(cnd, EdgePort.yes, a);
    ctl().connect(cnd, EdgePort.no, a);
    expect(st().flow.nodes[cnd]!.yes, a);
    expect(st().flow.nodes[cnd]!.no, a);
    expect(ctl().connect(a, EdgePort.yes, cnd), isFalse); // agents have no yes port
  });

  test('deleting a node clears incoming edges and start', () {
    final a1 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    final a2 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    ctl().connect('', EdgePort.start, a2);
    ctl().connect(a1, EdgePort.next, a2);
    ctl().deleteNode(a2);
    expect(st().flow.nodes.containsKey(a2), isFalse);
    expect(st().flow.nodes[a1]!.next, '');
    expect(st().flow.start, '');
    expect(st().check.ok, isFalse); // nothing connected to Start
  });

  test('deleteSelection removes a selected edge', () {
    final a1 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    final a2 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    ctl().connect(a1, EdgePort.next, a2);
    ctl().select(EdgeSelection(a1, EdgePort.next));
    ctl().deleteSelection();
    expect(st().flow.nodes[a1]!.next, '');
    expect(st().flow.nodes.length, 2);
  });

  test('moves during a drag are one undo step', () {
    final a = ctl().addNode(FlowNodeType.agent, Offset.zero);
    ctl().moveNode(a, const Offset(1, 1), commit: false);
    ctl().moveNode(a, const Offset(2, 2), commit: false);
    ctl().moveNode(a, const Offset(3, 3));
    ctl().undo();
    expect(st().flow.nodes[a]!.position, Offset.zero);
  });

  test('undo stack is capped at 100', () {
    final a = ctl().addNode(FlowNodeType.agent, Offset.zero);
    for (var i = 0; i < 150; i++) {
      ctl().moveNode(a, Offset(i.toDouble(), 0));
    }
    var undos = 0;
    while (st().canUndo) {
      ctl().undo();
      undos++;
    }
    expect(undos, 100);
  });

  test('draft: show computes diff and lays out, accept is one undo step, discard keeps flow', () {
    final a = ctl().addNode(FlowNodeType.agent, Offset.zero);
    ctl().connect('', EdgePort.start, a);
    final before = st().flow;
    final draftFlow = ReviewFlow(name: 'x', start: 'a9', nodes: {
      'a9': const FlowNode(id: 'a9', type: FlowNodeType.agent, agent: 'codex'),
    });
    ctl().showDraft(FlowAssistResult(flow: draftFlow, plan: 'p', warnings: const ['w']));
    expect(st().draft!.diff.nodes, {'a9': DiffKind.added, a: DiffKind.removed});
    expect(st().draft!.flow.nodes['a9']!.position, isNotNull);
    expect(st().flow.toJson(), before.toJson()); // canvas unchanged until accepted
    ctl().discardDraft();
    expect(st().draft, isNull);
    ctl().showDraft(FlowAssistResult(flow: draftFlow));
    ctl().acceptDraft();
    expect(st().flow.start, 'a9');
    ctl().undo();
    expect(st().flow.toJson(), before.toJson());
  });

  test('markSaved clears dirty; load lays out nodes without positions', () {
    ctl().load(ReviewFlow(start: 'a1', nodes: {
      'a1': const FlowNode(id: 'a1', type: FlowNodeType.agent, agent: 'claude'),
    }));
    expect(st().flow.nodes['a1']!.position, isNotNull);
    expect(st().dirty, isFalse);
    ctl().rename('y');
    expect(st().dirty, isTrue);
    ctl().markSaved();
    expect(st().dirty, isFalse);
  });
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd flutter_app && flutter test test/features/flows/editor/flow_editor_controller_test.dart`
Expected: FAIL — file not found.

- [ ] **Step 3: Implement**

```dart
import 'dart:convert';

import 'package:flutter/painting.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';
import 'package:heimdallm/features/flows/graph/graph_validation.dart';

enum EdgePort { yes, no, next, start }

sealed class Selection {
  const Selection();
}

class NoSelection extends Selection {
  const NoSelection();
}

class NodeSelection extends Selection {
  final String id;
  const NodeSelection(this.id);
  @override
  bool operator ==(Object other) => other is NodeSelection && other.id == id;
  @override
  int get hashCode => id.hashCode;
}

class EdgeSelection extends Selection {
  final String from;
  final EdgePort port;
  const EdgeSelection(this.from, this.port);
  @override
  bool operator ==(Object other) => other is EdgeSelection && other.from == from && other.port == port;
  @override
  int get hashCode => Object.hash(from, port);
}

/// An assistant result shown over the canvas until accepted or discarded.
class FlowDraft {
  final ReviewFlow flow;
  final String plan;
  final List<String> warnings;
  final FlowDiff diff;
  const FlowDraft({required this.flow, required this.plan, required this.warnings, required this.diff});
}

class FlowEditorState {
  final ReviewFlow flow;
  final ReviewFlow saved;
  final Selection selection;
  final FlowDraft? draft;
  final GraphCheck check;
  final FlowDecision? simulation;
  final int undoDepth;
  final int redoDepth;

  const FlowEditorState({
    required this.flow,
    required this.saved,
    this.selection = const NoSelection(),
    this.draft,
    required this.check,
    this.simulation,
    this.undoDepth = 0,
    this.redoDepth = 0,
  });

  bool get dirty => jsonEncode(flow.toJson()) != jsonEncode(saved.toJson());
  bool get canUndo => undoDepth > 0;
  bool get canRedo => redoDepth > 0;
}

const _maxUndo = 100;

class FlowEditorController extends Notifier<FlowEditorState> {
  final _undo = <ReviewFlow>[];
  final _redo = <ReviewFlow>[];
  ReviewFlow? _dragOrigin;

  @override
  FlowEditorState build() {
    const empty = ReviewFlow();
    return FlowEditorState(flow: empty, saved: empty, check: checkGraph(empty));
  }

  FlowEditorState _with(ReviewFlow flow, {Selection? selection, FlowDraft? draft, bool clearDraft = false, ReviewFlow? saved}) =>
      FlowEditorState(
        flow: flow,
        saved: saved ?? state.saved,
        selection: selection ?? state.selection,
        draft: clearDraft ? null : (draft ?? state.draft),
        check: checkGraph(flow),
        simulation: null,
        undoDepth: _undo.length,
        redoDepth: _redo.length,
      );

  /// Records the current flow for undo, then applies [next].
  void _commit(ReviewFlow next, {Selection? selection}) {
    _undo.add(_dragOrigin ?? state.flow);
    _dragOrigin = null;
    if (_undo.length > _maxUndo) _undo.removeAt(0);
    _redo.clear();
    state = _with(next, selection: selection);
  }

  void load(ReviewFlow flow) {
    _undo.clear();
    _redo.clear();
    final laid = autoLayout(flow, onlyMissing: true);
    state = FlowEditorState(flow: laid, saved: laid, check: checkGraph(laid));
  }

  void markSaved() => state = _with(state.flow, saved: state.flow);

  void select(Selection s) => state = _with(state.flow, selection: s);

  void rename(String name) => _commit(state.flow.copyWith(name: name));

  String addNode(FlowNodeType type, Offset at) {
    final id = state.flow.nextNodeId(type == FlowNodeType.agent ? 'a' : 'c');
    final node = FlowNode(
      id: id,
      type: type,
      position: at,
      schedule: type == FlowNodeType.condition ? const [ScheduleCondition()] : const [],
    );
    _commit(state.flow.copyWith(nodes: {...state.flow.nodes, id: node}), selection: NodeSelection(id));
    return id;
  }

  void moveNode(String id, Offset to, {bool commit = true}) {
    final n = state.flow.nodes[id];
    if (n == null) return;
    final next = state.flow.copyWith(nodes: {...state.flow.nodes, id: n.copyWith(position: to)});
    if (!commit) {
      _dragOrigin ??= state.flow;
      state = _with(next);
      return;
    }
    _commit(next);
  }

  bool connect(String from, EdgePort port, String to) {
    final f = state.flow;
    if (!f.nodes.containsKey(to)) return false;
    if (port == EdgePort.start) {
      _commit(f.copyWith(start: to));
      return true;
    }
    final n = f.nodes[from];
    if (n == null) return false;
    final validPort = n.isCondition ? (port == EdgePort.yes || port == EdgePort.no) : port == EdgePort.next;
    if (!validPort) return false;
    final cleared = f.copyWith(nodes: {...f.nodes, from: _setPort(n, port, '')});
    if (wouldCreateCycle(cleared, from, to)) return false;
    _commit(f.copyWith(nodes: {...f.nodes, from: _setPort(n, port, to)}));
    return true;
  }

  void disconnect(String from, EdgePort port) {
    final f = state.flow;
    if (port == EdgePort.start) {
      _commit(f.copyWith(start: ''), selection: const NoSelection());
      return;
    }
    final n = f.nodes[from];
    if (n == null) return;
    _commit(f.copyWith(nodes: {...f.nodes, from: _setPort(n, port, '')}), selection: const NoSelection());
  }

  static FlowNode _setPort(FlowNode n, EdgePort port, String to) => switch (port) {
    EdgePort.yes => n.copyWith(yes: to),
    EdgePort.no => n.copyWith(no: to),
    EdgePort.next => n.copyWith(next: to),
    EdgePort.start => n,
  };

  void deleteNode(String id) {
    final f = state.flow;
    if (!f.nodes.containsKey(id)) return;
    final nodes = {
      for (final e in f.nodes.entries)
        if (e.key != id) e.key: e.value.withoutEdgesTo(id),
    };
    _commit(f.copyWith(nodes: nodes, start: f.start == id ? '' : f.start), selection: const NoSelection());
  }

  void deleteSelection() {
    switch (state.selection) {
      case NodeSelection(:final id):
        deleteNode(id);
      case EdgeSelection(:final from, :final port):
        disconnect(from, port);
      case NoSelection():
        break;
    }
  }

  void updateNode(FlowNode node) {
    if (!state.flow.nodes.containsKey(node.id)) return;
    _commit(state.flow.copyWith(nodes: {...state.flow.nodes, node.id: node}));
  }

  void arrange() => _commit(autoLayout(state.flow));

  void undo() {
    if (_undo.isEmpty) return;
    _redo.add(state.flow);
    state = _with(_undo.removeLast());
  }

  void redo() {
    if (_redo.isEmpty) return;
    _undo.add(state.flow);
    state = _with(_redo.removeLast());
  }

  void showDraft(FlowAssistResult r) {
    // Keep the positions of steps the draft did not change, lay out the rest.
    final keep = r.flow.copyWith(nodes: {
      for (final e in r.flow.nodes.entries)
        e.key: e.value.position == null && state.flow.nodes[e.key]?.position != null
            ? e.value.copyWith(position: state.flow.nodes[e.key]!.position)
            : e.value,
    });
    final laid = autoLayout(keep, onlyMissing: true);
    state = _with(state.flow, draft: FlowDraft(flow: laid, plan: r.plan, warnings: r.warnings, diff: diffFlows(state.flow, laid)));
  }

  void acceptDraft() {
    final d = state.draft;
    if (d == null) return;
    _undo.add(state.flow);
    if (_undo.length > _maxUndo) _undo.removeAt(0);
    _redo.clear();
    state = _with(d.flow, clearDraft: true, selection: const NoSelection());
  }

  void discardDraft() => state = _with(state.flow, clearDraft: true);

  void setSimulation(FlowDecision? d) => state = FlowEditorState(
    flow: state.flow,
    saved: state.saved,
    selection: state.selection,
    draft: state.draft,
    check: state.check,
    simulation: d,
    undoDepth: _undo.length,
    redoDepth: _redo.length,
  );
}

final flowEditorProvider = NotifierProvider.autoDispose<FlowEditorController, FlowEditorState>(FlowEditorController.new);
```

Note: `showDraft` diffs against the current flow, which is what the operator is replacing; when the operator refines a draft (Task 12), the next request carries `state.draft!.flow` as `current`, and `showDraft` still diffs against the canvas, so the overlay always shows the net change.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd flutter_app && flutter test test/features/flows/editor/flow_editor_controller_test.dart && flutter analyze`
Expected: PASS, no issues.

- [ ] **Step 5: Commit**

```bash
git add flutter_app/lib/features/flows/editor/flow_editor_controller.dart flutter_app/test/features/flows/editor
git commit -m "feat(app): flow editor controller with undo/redo and AI drafts

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10: Canvas — geometry, edges, nodes, interaction

**Files:**
- Create: `flutter_app/lib/features/flows/canvas/canvas_geometry.dart`, `canvas/edge_painter.dart`, `canvas/node_widgets.dart`, `canvas/flow_canvas.dart`
- Test: `flutter_app/test/features/flows/canvas/canvas_geometry_test.dart`, `flutter_app/test/features/flows/canvas/flow_canvas_test.dart`

**Interfaces:**
- Consumes: Task 8 constants (`kNodeSize`, `kStartPosition`), Task 9 controller/state.
- Produces:
  - `class CanvasEdge { final String from; final EdgePort port; final String to; final Offset a; final Offset b; }`
  - `Offset inPort(Offset nodePos)`; `Offset outPort(FlowNode n, EdgePort port)`; `Offset startOutPort()`
  - `List<CanvasEdge> canvasEdges(ReviewFlow f)`
  - `Path edgePath(Offset a, Offset b)` (horizontal cubic)
  - `CanvasEdge? hitEdge(List<CanvasEdge> edges, Offset p, {double tolerance = 8})`
  - `String? hitNode(ReviewFlow f, Offset p)` (topmost by id order reversed)
  - `class EdgePainter extends CustomPainter { EdgePainter({required edges, required Set<String> pathNodes, required EdgeSelection? selected, required Color color, required Color highlight, required Color selectedColor, Offset? dragFrom, Offset? dragTo}) }`
  - `class FlowNodeCard extends StatelessWidget { FlowNodeCard({required FlowNode node, required bool selected, required bool hasError, required bool unreachable, required bool onPath, DiffKind? diff, bool ghost = false}) }`
  - `class StartNodeCard extends StatelessWidget`
  - `class FlowCanvas extends ConsumerStatefulWidget { const FlowCanvas({super.key, this.onDropFromPalette}); }` — renders `state.draft?.flow ?? state.flow`; edits are disabled while a draft is shown.
  - Keys used by tests: `ValueKey('node-<id>')`, `ValueKey('port-<id>-<yes|no|next>')`, `ValueKey('port-start')`, `ValueKey('edge-cut')`, `ValueKey('flow-canvas')`.

- [ ] **Step 1: Write the failing tests**

`canvas_geometry_test.dart`:

```dart
import 'package:flutter/painting.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/canvas/canvas_geometry.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';

void main() {
  final f = ReviewFlow(start: 'c1', nodes: {
    'c1': const FlowNode(id: 'c1', type: FlowNodeType.condition, yes: 'a1', no: 'a2', position: Offset(400, 100)),
    'a1': const FlowNode(id: 'a1', type: FlowNodeType.agent, agent: 'codex', next: 'a2', position: Offset(800, 50)),
    'a2': const FlowNode(id: 'a2', type: FlowNodeType.agent, agent: 'claude', position: Offset(800, 250)),
  });

  test('ports sit on the node edges; yes above no', () {
    final c1 = f.nodes['c1']!;
    expect(inPort(c1.position!), Offset(400, 100 + kNodeSize.height / 2));
    expect(outPort(c1, EdgePort.yes).dx, 400 + kNodeSize.width);
    expect(outPort(c1, EdgePort.yes).dy, lessThan(outPort(c1, EdgePort.no).dy));
  });

  test('edges include start, yes, no and next', () {
    final e = canvasEdges(f);
    expect(e.map((x) => '${x.from}:${x.port.name}->${x.to}'),
        unorderedEquals([':start->c1', 'c1:yes->a1', 'c1:no->a2', 'a1:next->a2']));
  });

  test('hit testing', () {
    expect(hitNode(f, const Offset(410, 110)), 'c1');
    expect(hitNode(f, const Offset(0, 0)), isNull);
    final edges = canvasEdges(f);
    final yes = edges.firstWhere((e) => e.port == EdgePort.yes);
    final mid = edgePath(yes.a, yes.b).computeMetrics().first.getTangentForOffset(
        edgePath(yes.a, yes.b).computeMetrics().first.length / 2)!.position;
    expect(hitEdge(edges, mid)?.port, EdgePort.yes);
    expect(hitEdge(edges, const Offset(5, 5)), isNull);
  });
}
```

`flow_canvas_test.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/canvas/flow_canvas.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

Future<ProviderContainer> pumpCanvas(WidgetTester tester, ReviewFlow flow) async {
  tester.view.physicalSize = const Size(1600, 1000);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  final c = ProviderContainer();
  addTearDown(c.dispose);
  c.listen(flowEditorProvider, (_, _) {});
  c.read(flowEditorProvider.notifier).load(flow);
  await tester.pumpWidget(UncontrolledProviderScope(
    container: c,
    child: MaterialApp(
      builder: (context, child) => HeimdallmTheme.scope(child: child!),
      home: const Scaffold(body: FlowCanvas()),
    ),
  ));
  await tester.pumpAndSettle();
  return c;
}

final twoAgents = ReviewFlow(start: 'a1', nodes: {
  'a1': const FlowNode(id: 'a1', type: FlowNodeType.agent, agent: 'claude', position: Offset(300, 200)),
  'a2': const FlowNode(id: 'a2', type: FlowNodeType.agent, agent: 'codex', position: Offset(700, 200)),
});

void main() {
  testWidgets('renders Start, nodes and agent names', (tester) async {
    await pumpCanvas(tester, twoAgents);
    expect(find.byKey(const ValueKey('port-start')), findsOneWidget);
    expect(find.byKey(const ValueKey('node-a1')), findsOneWidget);
    expect(find.text('claude'), findsOneWidget);
  });

  testWidgets('drag from a port to a node connects them', (tester) async {
    final c = await pumpCanvas(tester, twoAgents);
    final from = tester.getCenter(find.byKey(const ValueKey('port-a1-next')));
    final to = tester.getCenter(find.byKey(const ValueKey('node-a2')));
    final g = await tester.startGesture(from);
    await g.moveTo(Offset.lerp(from, to, .5)!);
    await g.moveTo(to);
    await g.up();
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.nodes['a1']!.next, 'a2');
  });

  testWidgets('a drop that would loop is refused with a message', (tester) async {
    final c = await pumpCanvas(tester, twoAgents.copyWith(nodes: {
      ...twoAgents.nodes,
      'a1': twoAgents.nodes['a1']!.copyWith(next: 'a2'),
    }));
    final from = tester.getCenter(find.byKey(const ValueKey('port-a2-next')));
    final to = tester.getCenter(find.byKey(const ValueKey('node-a1')));
    final g = await tester.startGesture(from);
    await g.moveTo(to);
    await g.up();
    await tester.pump();
    expect(find.textContaining('loop'), findsOneWidget);
    expect(c.read(flowEditorProvider).flow.nodes['a2']!.next, '');
  });

  testWidgets('dragging a node moves it as one undo step', (tester) async {
    final c = await pumpCanvas(tester, twoAgents);
    await tester.drag(find.byKey(const ValueKey('node-a1')), const Offset(50, 30));
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.nodes['a1']!.position, const Offset(350, 230));
    c.read(flowEditorProvider.notifier).undo();
    expect(c.read(flowEditorProvider).flow.nodes['a1']!.position, const Offset(300, 200));
  });

  testWidgets('tap selects; Delete removes the selected node', (tester) async {
    final c = await pumpCanvas(tester, twoAgents);
    await tester.tap(find.byKey(const ValueKey('node-a2')));
    await tester.pump();
    expect(c.read(flowEditorProvider).selection, const NodeSelection('a2'));
    await tester.sendKeyEvent(LogicalKeyboardKey.delete);
    await tester.pump();
    expect(c.read(flowEditorProvider).flow.nodes.containsKey('a2'), isFalse);
  });

  testWidgets('hovering an edge shows scissors that cut it', (tester) async {
    final c = await pumpCanvas(tester, twoAgents.copyWith(nodes: {
      ...twoAgents.nodes,
      'a1': twoAgents.nodes['a1']!.copyWith(next: 'a2'),
    }));
    final mid = Offset.lerp(
      tester.getCenter(find.byKey(const ValueKey('port-a1-next'))),
      tester.getTopLeft(find.byKey(const ValueKey('node-a2'))) + const Offset(0, 38),
      .5,
    )!;
    final mouse = await tester.createGesture(kind: PointerDeviceKind.mouse);
    await mouse.addPointer(location: mid);
    await tester.pump();
    await tester.tap(find.byKey(const ValueKey('edge-cut')));
    await tester.pump();
    expect(c.read(flowEditorProvider).flow.nodes['a1']!.next, '');
  });
}
```

Add `import 'package:flutter/gestures.dart';` for `PointerDeviceKind`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd flutter_app && flutter test test/features/flows/canvas/`
Expected: FAIL — files not found.

- [ ] **Step 3: Implement**

`canvas/canvas_geometry.dart`:

```dart
import 'dart:ui';

import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';

const kStartSize = Size(96, 40);

class CanvasEdge {
  final String from;
  final EdgePort port;
  final String to;
  final Offset a;
  final Offset b;
  const CanvasEdge(this.from, this.port, this.to, this.a, this.b);
}

Offset inPort(Offset nodePos) => nodePos + Offset(0, kNodeSize.height / 2);

Offset outPort(FlowNode n, EdgePort port) {
  final p = n.position ?? Offset.zero;
  final y = switch (port) {
    EdgePort.yes => kNodeSize.height * .3,
    EdgePort.no => kNodeSize.height * .7,
    _ => kNodeSize.height / 2,
  };
  return p + Offset(kNodeSize.width, y);
}

Offset startOutPort() => kStartPosition + Offset(kStartSize.width, kStartSize.height / 2);

List<CanvasEdge> canvasEdges(ReviewFlow f) {
  final out = <CanvasEdge>[];
  final start = f.nodes[f.start];
  if (start?.position != null) {
    out.add(CanvasEdge('', EdgePort.start, f.start, startOutPort(), inPort(start!.position!)));
  }
  for (final n in f.nodes.values) {
    for (final (port, to) in [(EdgePort.yes, n.yes), (EdgePort.no, n.no), (EdgePort.next, n.next)]) {
      final target = f.nodes[to];
      if (to.isEmpty || target?.position == null || n.position == null) continue;
      out.add(CanvasEdge(n.id, port, to, outPort(n, port), inPort(target!.position!)));
    }
  }
  return out;
}

Path edgePath(Offset a, Offset b) {
  final dx = ((b.dx - a.dx).abs() / 2).clamp(40.0, 200.0);
  return Path()
    ..moveTo(a.dx, a.dy)
    ..cubicTo(a.dx + dx, a.dy, b.dx - dx, b.dy, b.dx, b.dy);
}

CanvasEdge? hitEdge(List<CanvasEdge> edges, Offset p, {double tolerance = 8}) {
  for (final e in edges) {
    for (final m in edgePath(e.a, e.b).computeMetrics()) {
      for (var d = 0.0; d <= m.length; d += 4) {
        if ((m.getTangentForOffset(d)!.position - p).distance <= tolerance) return e;
      }
    }
  }
  return null;
}

String? hitNode(ReviewFlow f, Offset p) {
  final ids = f.nodes.keys.toList()..sort();
  for (final id in ids.reversed) {
    final pos = f.nodes[id]!.position;
    if (pos != null && (pos & kNodeSize).contains(p)) return id;
  }
  return null;
}
```

`canvas/edge_painter.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:heimdallm/features/flows/canvas/canvas_geometry.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';

/// Draws every edge with its label, the edges of a simulated path in the
/// highlight colour, the selected edge thicker, and the edge being dragged.
class EdgePainter extends CustomPainter {
  final List<CanvasEdge> edges;
  final Set<(String, EdgePort)> pathEdges;
  final EdgeSelection? selected;
  final Color color;
  final Color highlight;
  final Color selectedColor;
  final TextStyle labelStyle;
  final Offset? dragFrom;
  final Offset? dragTo;

  EdgePainter({
    required this.edges,
    required this.pathEdges,
    required this.selected,
    required this.color,
    required this.highlight,
    required this.selectedColor,
    required this.labelStyle,
    this.dragFrom,
    this.dragTo,
  });

  @override
  void paint(Canvas canvas, Size size) {
    for (final e in edges) {
      final isSel = selected?.from == e.from && selected?.port == e.port;
      final onPath = pathEdges.contains((e.from, e.port));
      final paint = Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = isSel ? 3 : (onPath ? 2.5 : 1.5)
        ..color = isSel ? selectedColor : (onPath ? highlight : color);
      canvas.drawPath(edgePath(e.a, e.b), paint);
      _arrow(canvas, e.b, paint.color);
      final label = switch (e.port) {
        EdgePort.yes => 'yes',
        EdgePort.no => 'no',
        EdgePort.next => 'fallback',
        EdgePort.start => '',
      };
      if (label.isNotEmpty) {
        final tp = TextPainter(text: TextSpan(text: label, style: labelStyle.copyWith(color: paint.color)), textDirection: TextDirection.ltr)..layout();
        tp.paint(canvas, e.a + Offset(8, -tp.height - 2));
      }
    }
    if (dragFrom != null && dragTo != null) {
      canvas.drawPath(
        edgePath(dragFrom!, dragTo!),
        Paint()
          ..style = PaintingStyle.stroke
          ..strokeWidth = 2
          ..color = selectedColor,
      );
    }
  }

  void _arrow(Canvas canvas, Offset tip, Color c) {
    final p = Path()
      ..moveTo(tip.dx, tip.dy)
      ..lineTo(tip.dx - 8, tip.dy - 5)
      ..lineTo(tip.dx - 8, tip.dy + 5)
      ..close();
    canvas.drawPath(p, Paint()..color = c);
  }

  @override
  bool shouldRepaint(EdgePainter old) =>
      old.edges != edges || old.pathEdges != pathEdges || old.selected != selected ||
      old.dragFrom != dragFrom || old.dragTo != dragTo || old.color != color;
}
```

`canvas/node_widgets.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/canvas/canvas_geometry.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';

const kPortSize = 14.0;

/// A step on the canvas. Ports are separate widgets placed by FlowCanvas.
class FlowNodeCard extends StatelessWidget {
  final FlowNode node;
  final bool selected;
  final bool hasError;
  final bool unreachable;
  final bool onPath;
  final DiffKind? diff;
  final bool ghost;

  const FlowNodeCard({
    super.key,
    required this.node,
    required this.selected,
    required this.hasError,
    required this.unreachable,
    required this.onPath,
    this.diff,
    this.ghost = false,
  });

  String get _subtitle {
    if (!node.isCondition) return node.agent.isEmpty ? 'Pick an agent' : 'Reviews, then falls back';
    final parts = [...node.schedule.map((s) => s.describe()), ...node.quota.map((q) => q.describe())];
    if (parts.isEmpty) return 'Add a condition';
    return parts.join(node.match == 'any' ? ' · or · ' : ' · and · ');
  }

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final text = Theme.of(context).textTheme;
    final accent = node.isCondition ? scheme.tertiary : scheme.primary;
    final border = switch ((hasError, diff, selected, onPath)) {
      (true, _, _, _) => scheme.error,
      (_, DiffKind.added, _, _) => Colors.green,
      (_, DiffKind.changed, _, _) => Colors.amber,
      (_, DiffKind.removed, _, _) => scheme.error,
      (_, _, true, _) => scheme.primary,
      (_, _, _, true) => scheme.secondary,
      _ => scheme.outlineVariant,
    };
    return Opacity(
      opacity: ghost ? .35 : (unreachable ? .55 : 1),
      child: Container(
        width: kNodeSize.width,
        height: kNodeSize.height,
        decoration: BoxDecoration(
          color: scheme.surfaceContainerHigh,
          borderRadius: BorderRadius.circular(10),
          border: Border.all(color: border, width: selected || diff != null || hasError ? 2 : 1),
        ),
        child: Row(children: [
          Container(
            width: 44,
            decoration: BoxDecoration(
              color: accent.withValues(alpha: .18),
              borderRadius: const BorderRadius.horizontal(left: Radius.circular(9)),
            ),
            child: Icon(node.isCondition ? Icons.call_split : Icons.smart_toy_outlined, color: accent, size: 20),
          ),
          Expanded(
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
              child: Column(crossAxisAlignment: CrossAxisAlignment.start, mainAxisAlignment: MainAxisAlignment.center, children: [
                Text(node.isCondition ? 'Condition' : (node.agent.isEmpty ? 'Agent' : node.agent),
                    style: text.titleSmall, maxLines: 1, overflow: TextOverflow.ellipsis),
                const SizedBox(height: 2),
                Text(_subtitle, style: text.bodySmall?.copyWith(color: scheme.onSurfaceVariant),
                    maxLines: 2, overflow: TextOverflow.ellipsis),
              ]),
            ),
          ),
          if (unreachable)
            Padding(padding: const EdgeInsets.only(right: 8), child: Icon(Icons.link_off, size: 16, color: scheme.onSurfaceVariant)),
        ]),
      ),
    );
  }
}

class StartNodeCard extends StatelessWidget {
  const StartNodeCard({super.key});

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Container(
      width: kStartSize.width,
      height: kStartSize.height,
      alignment: Alignment.center,
      decoration: BoxDecoration(color: scheme.primaryContainer, borderRadius: BorderRadius.circular(20)),
      child: Text('Start', style: TextStyle(color: scheme.onPrimaryContainer, fontWeight: FontWeight.w600)),
    );
  }
}

class PortDot extends StatelessWidget {
  final Color color;
  const PortDot({super.key, required this.color});

  @override
  Widget build(BuildContext context) => Container(
    width: kPortSize,
    height: kPortSize,
    decoration: BoxDecoration(
      color: Theme.of(context).colorScheme.surface,
      shape: BoxShape.circle,
      border: Border.all(color: color, width: 2),
    ),
  );
}
```

`canvas/flow_canvas.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/canvas/canvas_geometry.dart';
import 'package:heimdallm/features/flows/canvas/edge_painter.dart';
import 'package:heimdallm/features/flows/canvas/node_widgets.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';

const _canvasSize = Size(6000, 4000);

/// The flow graph on a pannable, zoomable canvas. While the assistant's
/// draft is shown, the draft is drawn (with removed steps as ghosts) and
/// editing is disabled until it is accepted or discarded.
class FlowCanvas extends ConsumerStatefulWidget {
  /// Called with the canvas position where a palette item was dropped.
  final void Function(FlowNodeType type, Offset at)? onDropFromPalette;
  const FlowCanvas({super.key, this.onDropFromPalette});

  @override
  ConsumerState<FlowCanvas> createState() => _FlowCanvasState();
}

class _FlowCanvasState extends ConsumerState<FlowCanvas> {
  final _transform = TransformationController();
  final _focus = FocusNode(debugLabel: 'flow-canvas');
  final _stackKey = GlobalKey();
  (String, EdgePort)? _dragPort;
  Offset? _dragTo;
  CanvasEdge? _hover;

  @override
  void dispose() {
    _transform.dispose();
    _focus.dispose();
    super.dispose();
  }

  /// Global pointer position → canvas coordinates.
  Offset _toCanvas(Offset global) {
    final box = _stackKey.currentContext!.findRenderObject()! as RenderBox;
    return box.globalToLocal(global);
  }

  void _toast(String msg) => ScaffoldMessenger.maybeOf(context)?.showSnackBar(SnackBar(content: Text(msg)));

  void _endPortDrag(ReviewFlow flow) {
    final port = _dragPort;
    final to = _dragTo;
    setState(() {
      _dragPort = null;
      _dragTo = null;
    });
    if (port == null || to == null) return;
    final target = hitNode(flow, to);
    if (target == null) return;
    final ok = ref.read(flowEditorProvider.notifier).connect(port.$1, port.$2, target);
    if (!ok) _toast('That connection would make the flow loop back, or the step has no such exit.');
  }

  @override
  Widget build(BuildContext context) {
    final state = ref.watch(flowEditorProvider);
    final ctl = ref.read(flowEditorProvider.notifier);
    final scheme = Theme.of(context).colorScheme;
    final draft = state.draft;
    final shown = draft?.flow ?? state.flow;
    final editable = draft == null;
    final edges = canvasEdges(shown);
    final pathNodes = {for (final s in state.simulation?.path ?? const <FlowPathStep>[]) s.node};
    final pathEdges = <(String, EdgePort)>{};
    final path = state.simulation?.path ?? const <FlowPathStep>[];
    if (path.isNotEmpty) pathEdges.add(('', EdgePort.start));
    for (var i = 0; i + 1 < path.length; i++) {
      final n = shown.nodes[path[i].node];
      if (n == null) continue;
      final port = n.isCondition ? ((path[i].holds ?? false) ? EdgePort.yes : EdgePort.no) : EdgePort.next;
      pathEdges.add((n.id, port));
    }
    final selectedEdge = state.selection is EdgeSelection ? state.selection as EdgeSelection : null;
    final selectedNode = state.selection is NodeSelection ? (state.selection as NodeSelection).id : null;

    Offset? dragFrom;
    if (_dragPort case (final from, final port)) {
      dragFrom = port == EdgePort.start ? startOutPort() : outPort(shown.nodes[from]!, port);
    }

    Widget port(String key, Offset center, String from, EdgePort p, Color color) => Positioned(
      left: center.dx - kPortSize / 2,
      top: center.dy - kPortSize / 2,
      child: GestureDetector(
        key: ValueKey(key),
        behavior: HitTestBehavior.opaque,
        onPanStart: editable ? (d) => setState(() {
          _dragPort = (from, p);
          _dragTo = _toCanvas(d.globalPosition);
        }) : null,
        onPanUpdate: editable ? (d) => setState(() => _dragTo = _toCanvas(d.globalPosition)) : null,
        onPanEnd: editable ? (_) => _endPortDrag(shown) : null,
        child: PortDot(color: color),
      ),
    );

    final ghosts = [
      if (draft != null)
        for (final e in draft.diff.nodes.entries)
          if (e.value == DiffKind.removed && state.flow.nodes[e.key]?.position != null) state.flow.nodes[e.key]!,
    ];

    return Focus(
      focusNode: _focus,
      autofocus: true,
      onKeyEvent: (node, event) {
        if (!editable || event is! KeyDownEvent) return KeyEventResult.ignored;
        if (event.logicalKey == LogicalKeyboardKey.delete || event.logicalKey == LogicalKeyboardKey.backspace) {
          ctl.deleteSelection();
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      },
      child: DragTarget<FlowNodeType>(
        onAcceptWithDetails: (d) => widget.onDropFromPalette?.call(d.data, _toCanvas(d.offset)),
        builder: (context, _, _) => InteractiveViewer(
          key: const ValueKey('flow-canvas'),
          transformationController: _transform,
          constrained: false,
          minScale: .3,
          maxScale: 2,
          boundaryMargin: const EdgeInsets.all(400),
          panEnabled: _dragPort == null,
          child: MouseRegion(
            onHover: (e) {
              final h = editable ? hitEdge(edges, e.localPosition) : null;
              if (h?.from != _hover?.from || h?.port != _hover?.port) setState(() => _hover = h);
            },
            child: GestureDetector(
              onTapUp: (d) {
                _focus.requestFocus();
                final e = hitEdge(edges, d.localPosition);
                ctl.select(e == null ? const NoSelection() : EdgeSelection(e.from, e.port));
              },
              child: SizedBox(
                key: _stackKey,
                width: _canvasSize.width,
                height: _canvasSize.height,
                child: Stack(clipBehavior: Clip.none, children: [
                  Positioned.fill(child: CustomPaint(painter: _DotGridPainter(scheme.outlineVariant.withValues(alpha: .4)))),
                  Positioned.fill(
                    child: CustomPaint(
                      painter: EdgePainter(
                        edges: edges,
                        pathEdges: pathEdges,
                        selected: selectedEdge,
                        color: scheme.outline,
                        highlight: scheme.secondary,
                        selectedColor: scheme.primary,
                        labelStyle: Theme.of(context).textTheme.labelSmall ?? const TextStyle(fontSize: 11),
                        dragFrom: dragFrom,
                        dragTo: _dragTo,
                      ),
                    ),
                  ),
                  Positioned(left: kStartPosition.dx, top: kStartPosition.dy, child: const StartNodeCard()),
                  port('port-start', startOutPort(), '', EdgePort.start, scheme.primary),
                  for (final g in ghosts)
                    Positioned(
                      left: g.position!.dx,
                      top: g.position!.dy,
                      child: IgnorePointer(child: FlowNodeCard(node: g, selected: false, hasError: false, unreachable: false, onPath: false, diff: DiffKind.removed, ghost: true)),
                    ),
                  for (final n in shown.nodes.values.where((n) => n.position != null)) ...[
                    Positioned(
                      left: n.position!.dx,
                      top: n.position!.dy,
                      child: GestureDetector(
                        key: ValueKey('node-${n.id}'),
                        onTap: () {
                          _focus.requestFocus();
                          ctl.select(NodeSelection(n.id));
                        },
                        onPanUpdate: editable ? (d) => ctl.moveNode(n.id, state.flow.nodes[n.id]!.position! + d.delta / _transform.value.getMaxScaleOnAxis(), commit: false) : null,
                        onPanEnd: editable ? (_) => ctl.moveNode(n.id, ref.read(flowEditorProvider).flow.nodes[n.id]!.position!) : null,
                        child: FlowNodeCard(
                          node: n,
                          selected: selectedNode == n.id,
                          hasError: state.check.errorNodes.contains(n.id),
                          unreachable: state.check.unreachable.contains(n.id),
                          onPath: pathNodes.contains(n.id),
                          diff: draft?.diff.nodes[n.id],
                        ),
                      ),
                    ),
                    if (n.isCondition) ...[
                      port('port-${n.id}-yes', outPort(n, EdgePort.yes), n.id, EdgePort.yes, Colors.green),
                      port('port-${n.id}-no', outPort(n, EdgePort.no), n.id, EdgePort.no, scheme.error),
                    ] else
                      port('port-${n.id}-next', outPort(n, EdgePort.next), n.id, EdgePort.next, scheme.outline),
                  ],
                  if (_hover != null)
                    Positioned(
                      left: ((_hover!.a + _hover!.b) / 2).dx - 14,
                      top: ((_hover!.a + _hover!.b) / 2).dy - 14,
                      child: IconButton.filledTonal(
                        key: const ValueKey('edge-cut'),
                        tooltip: 'Cut this connection',
                        iconSize: 16,
                        icon: const Icon(Icons.content_cut),
                        onPressed: () {
                          ctl.disconnect(_hover!.from, _hover!.port);
                          setState(() => _hover = null);
                        },
                      ),
                    ),
                ]),
              ),
            ),
          ),
        ),
      ),
    );
  }
}

class _DotGridPainter extends CustomPainter {
  final Color color;
  _DotGridPainter(this.color);

  @override
  void paint(Canvas canvas, Size size) {
    final p = Paint()..color = color;
    for (var x = 0.0; x < size.width; x += 24) {
      for (var y = 0.0; y < size.height; y += 24) {
        canvas.drawCircle(Offset(x, y), 1, p);
      }
    }
  }

  @override
  bool shouldRepaint(_DotGridPainter old) => old.color != color;
}
```

Note the edge midpoint used for the scissors is the chord midpoint; the hover test in Step 1 hovers that same point, which lies within the hit tolerance for the near-straight horizontal edge in the fixture. If the dot grid painting over a 6000×4000 area is slow in widget tests, paint only the visible rect by passing `_transform`'s inverse viewport — but start with the simple version and measure.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd flutter_app && flutter test test/features/flows/canvas/ && flutter analyze`
Expected: PASS, no issues. If a gesture test is flaky because `InteractiveViewer` claims the pan, keep `panEnabled: _dragPort == null` and start port drags with `onPanStart` on the port (already so); node drags win the arena because the node `GestureDetector` is deeper.

- [ ] **Step 5: Commit**

```bash
git add flutter_app/lib/features/flows/canvas flutter_app/test/features/flows/canvas
git commit -m "feat(app): flow canvas with ports, edges, drag-to-connect and cut

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 11: Editor screen — top bar, palette, inspector, Test, unsaved guard

**Files:**
- Create: `flutter_app/lib/features/flows/editor/flow_editor_screen.dart`, `editor/palette.dart`, `editor/test_panel.dart`
- Create: `flutter_app/lib/features/flows/editor/inspector/flow_inspector.dart`, `inspector/condition_inspector.dart`, `inspector/agent_inspector.dart`
- Modify: `flutter_app/lib/shared/router.dart` (`/flows/:id` → `FlowEditorScreen`, with `onExit` guard)
- Test: `flutter_app/test/features/flows/editor/flow_editor_screen_test.dart`, `flutter_app/test/features/flows/editor/inspector_test.dart`

**Interfaces:**
- Consumes: Tasks 7–10; `apiClientProvider` (`lib/features/dashboard/dashboard_providers.dart`), `configNotifierProvider` (`lib/features/config/config_providers.dart`, method `updateFromServer(Map)`), `flowsProvider` (`flows_list_screen.dart`), `showToast(context, msg, {isError})` (`lib/shared/widgets/toast.dart`).
- Produces:
  - `class FlowEditorScreen extends ConsumerStatefulWidget { const FlowEditorScreen({super.key, required this.flowId}); }`
  - `class FlowPalette extends StatelessWidget { const FlowPalette({super.key, required this.onAdd}); final void Function(FlowNodeType) onAdd; }` — rows are `Draggable<FlowNodeType>` with keys `ValueKey('palette-condition')`, `ValueKey('palette-agent')`; double-tap calls `onAdd`.
  - `class ConditionInspector`, `class AgentInspector`, `class FlowInspector` (all `ConsumerWidget`s editing through `flowEditorProvider.notifier.updateNode/rename`).
  - `class FlowTestPanel extends ConsumerStatefulWidget` — date/time pickers + optional repo field; calls `simulateFlow(graph: state.flow, at:, repo:)` and `setSimulation`.
  - `Future<bool> confirmDiscardFlowChanges(BuildContext context)` — used by the router `onExit`.
  - `final flowEditorDirtyProvider = Provider.autoDispose<bool>((ref) => ref.watch(flowEditorProvider.select((s) => s.dirty)));` (lets `onExit` read dirtiness through `ProviderScope.containerOf(context)`).
  - Keys: `flow-save`, `flow-undo`, `flow-redo`, `flow-arrange`, `flow-delete`, `flow-test`, `flow-use-globally`, `flow-name`, `inspector-agent`, `inspector-match`, `inspector-add-schedule`, `inspector-add-quota`.

- [ ] **Step 1: Write the failing tests**

`flow_editor_screen_test.dart` — mock `ApiClient` with mocktail the way existing feature tests do (`class _MockApi extends Mock implements ApiClient {}`; register fallbacks for `ReviewFlow` with `registerFallbackValue(const ReviewFlow())`):

```dart
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_screen.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:mocktail/mocktail.dart';

class _MockApi extends Mock implements ApiClient {}

final _night = ReviewFlow(name: 'Night', start: 'a1', nodes: {
  'a1': const FlowNode(id: 'a1', type: FlowNodeType.agent, agent: 'claude', position: Offset(300, 200)),
});

FlowListing _listing({Map<String, ReviewFlow>? flows}) => FlowListing(
  flows: flows ?? {'night': _night},
  selected: 'default',
  agents: const [
    FlowAgent(id: 'claude', installed: true),
    FlowAgent(id: 'codex', installed: true),
    FlowAgent(id: 'gemini'),
  ],
);

Future<(_MockApi, GoRouter)> pumpEditor(WidgetTester tester, {String id = 'night', FlowListing? listing}) async {
  tester.view.physicalSize = const Size(1800, 1100);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  registerFallbackValue(const ReviewFlow());
  final api = _MockApi();
  when(() => api.fetchFlows()).thenAnswer((_) async => listing ?? _listing());
  when(() => api.fetchQuotas()).thenAnswer((_) async => const []);
  final router = GoRouter(initialLocation: '/flows/$id', routes: [
    GoRoute(path: '/flows', builder: (_, _) => const Scaffold(body: Text('list')), routes: [
      GoRoute(
        path: ':id',
        builder: (_, s) => FlowEditorScreen(flowId: s.pathParameters['id']!),
        onExit: (context, _) => flowEditorOnExit(context),
      ),
    ]),
  ]);
  await tester.pumpWidget(ProviderScope(
    overrides: [apiClientProvider.overrideWithValue(api)],
    child: MaterialApp.router(
      routerConfig: router,
      builder: (context, child) => HeimdallmTheme.scope(child: child!),
    ),
  ));
  await tester.pumpAndSettle();
  return (api, router);
}

void main() {
  testWidgets('loads the flow onto the canvas with palette and inspector', (tester) async {
    await pumpEditor(tester);
    expect(find.byKey(const ValueKey('node-a1')), findsOneWidget);
    expect(find.byKey(const ValueKey('palette-condition')), findsOneWidget);
    expect(find.byKey(const ValueKey('flow-name')), findsOneWidget);
  });

  testWidgets('double-tapping a palette row adds a step; Save sends the graph', (tester) async {
    final (api, _) = await pumpEditor(tester);
    when(() => api.putFlow(any(), any())).thenAnswer((_) async => <String, dynamic>{});
    await tester.tap(find.byKey(const ValueKey('palette-agent')));
    await tester.pump(const Duration(milliseconds: 50));
    await tester.tap(find.byKey(const ValueKey('palette-agent')));
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('node-a2')), findsOneWidget);
    // a2 has no agent yet → client validation blocks the save with a message
    await tester.tap(find.byKey(const ValueKey('flow-save')));
    await tester.pumpAndSettle();
    verifyNever(() => api.putFlow(any(), any()));
    expect(find.textContaining('Pick an agent'), findsWidgets);
    // pick an agent in the inspector, then save
    await tester.tap(find.byKey(const ValueKey('inspector-agent')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('codex').last);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('flow-save')));
    await tester.pumpAndSettle();
    final sent = verify(() => api.putFlow('night', captureAny())).captured.single as ReviewFlow;
    expect(sent.nodes['a2']!.agent, 'codex');
  });

  testWidgets('a 422 pins issues on the nodes', (tester) async {
    final (api, _) = await pumpEditor(tester);
    when(() => api.putFlow(any(), any())).thenThrow(
      FlowValidationException('nodes.a1: bad', const [FlowIssue(node: 'a1', message: 'bad agent')]),
    );
    await tester.tap(find.byKey(const ValueKey('flow-name')));
    await tester.enterText(find.byKey(const ValueKey('flow-name')), 'Night 2');
    await tester.tap(find.byKey(const ValueKey('flow-save')));
    await tester.pumpAndSettle();
    expect(find.textContaining('bad agent'), findsWidgets);
  });

  testWidgets('Test highlights the path and lists candidates', (tester) async {
    final (api, _) = await pumpEditor(tester);
    when(() => api.simulateFlow(graph: any(named: 'graph'), at: any(named: 'at'), repo: any(named: 'repo')))
        .thenAnswer((_) async => const FlowDecision(flowId: 'night', candidates: ['claude'], path: [
              FlowPathStep(node: 'a1', type: 'agent', agent: 'claude', available: true, added: true),
            ]));
    await tester.tap(find.byKey(const ValueKey('flow-test')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('flow-test-run')));
    await tester.pumpAndSettle();
    expect(find.textContaining('Reviews with claude'), findsOneWidget);
  });

  testWidgets('leaving with unsaved changes asks first', (tester) async {
    final (_, router) = await pumpEditor(tester);
    await tester.enterText(find.byKey(const ValueKey('flow-name')), 'Changed');
    await tester.pump();
    router.go('/flows');
    await tester.pumpAndSettle();
    expect(find.text('Discard unsaved changes?'), findsOneWidget);
    await tester.tap(find.text('Keep editing'));
    await tester.pumpAndSettle();
    expect(find.text('list'), findsNothing);
    router.go('/flows');
    await tester.pumpAndSettle();
    await tester.tap(find.text('Discard'));
    await tester.pumpAndSettle();
    expect(find.text('list'), findsOneWidget);
  });

  testWidgets('keyboard undo/redo', (tester) async {
    await pumpEditor(tester);
    await tester.enterText(find.byKey(const ValueKey('flow-name')), 'Other');
    await tester.pump();
    await tester.tap(find.byKey(const ValueKey('flow-undo')));
    await tester.pump();
    expect(find.widgetWithText(TextField, 'Night'), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('flow-redo')));
    await tester.pump();
    expect(find.widgetWithText(TextField, 'Other'), findsOneWidget);
  });

  testWidgets('the default flow is read-only with a duplicate action', (tester) async {
    await pumpEditor(tester, id: 'default', listing: _listing(flows: {'default': _night}));
    expect(find.byKey(const ValueKey('flow-save')), findsNothing);
    expect(find.text('Duplicate as a new flow'), findsOneWidget);
  });
}
```

`inspector_test.dart` — pump each inspector inside a `ProviderScope` with a `flowEditorProvider` loaded with one condition node `c1` selected; assert:

```dart
  testWidgets('condition inspector edits match, adds schedule and quota rows', (tester) async {
    final c = await pumpInspector(tester, condition: true); // helper: loads flow, selects c1, pumps ConditionInspector
    await tester.tap(find.byKey(const ValueKey('inspector-match')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Any condition').last);
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.nodes['c1']!.match, 'any');
    await tester.tap(find.byKey(const ValueKey('inspector-add-quota')));
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.nodes['c1']!.quota, hasLength(1));
    await tester.tap(find.byKey(const ValueKey('inspector-add-schedule')));
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.nodes['c1']!.schedule, hasLength(2));
  });

  testWidgets('agent inspector marks agents that are not installed', (tester) async {
    await pumpInspector(tester, condition: false);
    await tester.tap(find.byKey(const ValueKey('inspector-agent')));
    await tester.pumpAndSettle();
    expect(find.text('gemini — not installed'), findsOneWidget);
  });
```

Write `pumpInspector` in the test file: it creates a `ProviderContainer`, keeps `flowEditorProvider` alive, loads a flow with `c1` (condition, one schedule) or `a1` (agent), selects it, and pumps the matching inspector with `agents: _listing().agents` inside `MaterialApp` + `HeimdallmTheme.scope`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd flutter_app && flutter test test/features/flows/editor/`
Expected: FAIL — `FlowEditorScreen`, inspectors not found.

- [ ] **Step 3: Implement**

`editor/palette.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:heimdallm/core/models/flow.dart';

class FlowPalette extends StatelessWidget {
  final void Function(FlowNodeType type) onAdd;
  const FlowPalette({super.key, required this.onAdd});

  Widget _row(BuildContext context, FlowNodeType type, IconData icon, String title, String subtitle) {
    final scheme = Theme.of(context).colorScheme;
    final tile = Card(
      margin: const EdgeInsets.only(bottom: 8),
      child: ListTile(
        dense: true,
        leading: Icon(icon, color: type == FlowNodeType.condition ? scheme.tertiary : scheme.primary),
        title: Text(title),
        subtitle: Text(subtitle),
      ),
    );
    return Draggable<FlowNodeType>(
      key: ValueKey('palette-${type.name}'),
      data: type,
      feedback: Material(color: Colors.transparent, child: SizedBox(width: 200, child: tile)),
      child: GestureDetector(onDoubleTap: () => onAdd(type), child: tile),
    );
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return ListView(padding: const EdgeInsets.all(12), children: [
      Text('ADD STEP', style: text.labelSmall),
      const SizedBox(height: 8),
      _row(context, FlowNodeType.condition, Icons.call_split, 'Condition', 'schedule or quota · yes / no'),
      _row(context, FlowNodeType.agent, Icons.smart_toy_outlined, 'Agent', 'reviews · then fallback'),
      const SizedBox(height: 16),
      Text('HOW IT WORKS', style: text.labelSmall),
      const SizedBox(height: 8),
      for (final (icon, line) in const [
        (Icons.add, 'Double-click or drag a row above to add a step.'),
        (Icons.link, 'Drag from a dot on a step\'s right edge to another step to connect them.'),
        (Icons.call_split, 'A condition continues through yes or no; an agent through its fallback.'),
        (Icons.content_cut, 'Hover a connection and click the scissors to cut it.'),
        (Icons.delete_outline, 'Select a step or a connection and press Delete.'),
      ])
        Padding(
          padding: const EdgeInsets.only(bottom: 8),
          child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Icon(icon, size: 16),
            const SizedBox(width: 8),
            Expanded(child: Text(line, style: text.bodySmall)),
          ]),
        ),
    ]);
  }
}
```

Double-tap: the test taps twice 50 ms apart, which `flutter_test` recognises as a double tap (between `kDoubleTapMinTime` and `kDoubleTapTimeout`).

`editor/inspector/agent_inspector.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';

class AgentInspector extends ConsumerWidget {
  final FlowNode node;
  final List<FlowAgent> agents;
  final List<FlowIssue> issues;
  const AgentInspector({super.key, required this.node, required this.agents, this.issues = const []});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final ctl = ref.read(flowEditorProvider.notifier);
    return Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
      Text('Agent step', style: Theme.of(context).textTheme.titleMedium),
      const SizedBox(height: 4),
      Text('Adds this agent to the reviewers, then continues through its fallback.', style: Theme.of(context).textTheme.bodySmall),
      const SizedBox(height: 12),
      DropdownButtonFormField<String>(
        key: const ValueKey('inspector-agent'),
        initialValue: node.agent.isEmpty ? null : node.agent,
        decoration: const InputDecoration(labelText: 'Agent'),
        items: [
          for (final a in agents)
            DropdownMenuItem(value: a.id, child: Text(a.installed ? a.id : '${a.id} — not installed')),
        ],
        onChanged: (v) => ctl.updateNode(node.copyWith(agent: v ?? '')),
      ),
      for (final i in issues) ...[
        const SizedBox(height: 8),
        Text(i.message, style: TextStyle(color: Theme.of(context).colorScheme.error)),
      ],
    ]);
  }
}
```

(If the project's Flutter version still uses `value:` instead of `initialValue:` on `DropdownButtonFormField`, follow what the rest of the codebase uses: `grep -rn "DropdownButtonFormField" lib | head`.)

`editor/inspector/condition_inspector.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';

const _days = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
const _windows = ['session', 'weekly', 'monthly', 'credit', 'any'];

class ConditionInspector extends ConsumerWidget {
  final FlowNode node;
  final List<FlowAgent> agents;
  final List<FlowIssue> issues;
  const ConditionInspector({super.key, required this.node, required this.agents, this.issues = const []});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final ctl = ref.read(flowEditorProvider.notifier);
    void update(FlowNode n) => ctl.updateNode(n);
    final text = Theme.of(context).textTheme;
    return Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
      Text('Condition step', style: text.titleMedium),
      const SizedBox(height: 4),
      Text('Continues through "yes" when its conditions hold, "no" otherwise. Unknown quota counts as not holding.', style: text.bodySmall),
      const SizedBox(height: 12),
      DropdownButtonFormField<String>(
        key: const ValueKey('inspector-match'),
        initialValue: node.match,
        decoration: const InputDecoration(labelText: 'Holds when'),
        items: const [
          DropdownMenuItem(value: 'all', child: Text('All conditions')),
          DropdownMenuItem(value: 'any', child: Text('Any condition')),
        ],
        onChanged: (v) => update(node.copyWith(match: v ?? 'all')),
      ),
      const SizedBox(height: 16),
      Text('SCHEDULE', style: text.labelSmall),
      for (var i = 0; i < node.schedule.length; i++)
        _ScheduleEditor(
          key: ValueKey('schedule-$i'),
          value: node.schedule[i],
          onChanged: (s) => update(node.copyWith(schedule: [...node.schedule]..[i] = s)),
          onRemove: () => update(node.copyWith(schedule: [...node.schedule]..removeAt(i))),
        ),
      TextButton.icon(
        key: const ValueKey('inspector-add-schedule'),
        onPressed: () => update(node.copyWith(schedule: [...node.schedule, const ScheduleCondition()])),
        icon: const Icon(Icons.schedule),
        label: const Text('Add schedule'),
      ),
      const SizedBox(height: 8),
      Text('QUOTA', style: text.labelSmall),
      for (var i = 0; i < node.quota.length; i++)
        _QuotaEditor(
          key: ValueKey('quota-$i'),
          value: node.quota[i],
          agents: agents,
          onChanged: (q) => update(node.copyWith(quota: [...node.quota]..[i] = q)),
          onRemove: () => update(node.copyWith(quota: [...node.quota]..removeAt(i))),
        ),
      TextButton.icon(
        key: const ValueKey('inspector-add-quota'),
        onPressed: () => update(node.copyWith(quota: [
          ...node.quota,
          QuotaCondition(agent: agents.where((a) => a.installed).firstOrNull?.id ?? 'claude'),
        ])),
        icon: const Icon(Icons.data_usage),
        label: const Text('Add quota condition'),
      ),
      for (final i in issues) ...[
        const SizedBox(height: 8),
        Text(i.message, style: TextStyle(color: Theme.of(context).colorScheme.error)),
      ],
    ]);
  }
}

class _ScheduleEditor extends StatelessWidget {
  final ScheduleCondition value;
  final ValueChanged<ScheduleCondition> onChanged;
  final VoidCallback onRemove;
  const _ScheduleEditor({super.key, required this.value, required this.onChanged, required this.onRemove});

  @override
  Widget build(BuildContext context) {
    final hhmm = [FilteringTextInputFormatter.allow(RegExp(r'[0-9:]')), LengthLimitingTextInputFormatter(5)];
    return Card(
      margin: const EdgeInsets.symmetric(vertical: 6),
      child: Padding(
        padding: const EdgeInsets.all(8),
        child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
          Wrap(spacing: 4, children: [
            for (final d in _days)
              FilterChip(
                label: Text(d),
                selected: value.days.contains(d),
                onSelected: (on) => onChanged(value.copyWith(
                  days: on ? [..._days.where((x) => x == d || value.days.contains(x))] : value.days.where((x) => x != d).toList(),
                )),
              ),
          ]),
          Row(children: [
            Expanded(child: TextFormField(initialValue: value.from, inputFormatters: hhmm, decoration: const InputDecoration(labelText: 'From'), onChanged: (v) => onChanged(value.copyWith(from: v)))),
            const SizedBox(width: 8),
            Expanded(child: TextFormField(initialValue: value.to, inputFormatters: hhmm, decoration: const InputDecoration(labelText: 'To'), onChanged: (v) => onChanged(value.copyWith(to: v)))),
          ]),
          TextFormField(initialValue: value.tz, decoration: const InputDecoration(labelText: 'Time zone (IANA, empty = daemon time)'), onChanged: (v) => onChanged(value.copyWith(tz: v.trim()))),
          Align(alignment: Alignment.centerRight, child: IconButton(tooltip: 'Remove', icon: const Icon(Icons.close), onPressed: onRemove)),
        ]),
      ),
    );
  }
}

class _QuotaEditor extends StatelessWidget {
  final QuotaCondition value;
  final List<FlowAgent> agents;
  final ValueChanged<QuotaCondition> onChanged;
  final VoidCallback onRemove;
  const _QuotaEditor({super.key, required this.value, required this.agents, required this.onChanged, required this.onRemove});

  @override
  Widget build(BuildContext context) {
    return Card(
      margin: const EdgeInsets.symmetric(vertical: 6),
      child: Padding(
        padding: const EdgeInsets.all(8),
        child: Column(children: [
          DropdownButtonFormField<String>(
            initialValue: agents.any((a) => a.id == value.agent) ? value.agent : null,
            decoration: const InputDecoration(labelText: 'Agent'),
            items: [for (final a in agents) DropdownMenuItem(value: a.id, child: Text(a.id))],
            onChanged: (v) => onChanged(value.copyWith(agent: v)),
          ),
          Row(children: [
            Expanded(
              child: DropdownButtonFormField<String>(
                initialValue: _windows.contains(value.window) ? value.window : null,
                decoration: const InputDecoration(labelText: 'Window'),
                items: [for (final w in _windows) DropdownMenuItem(value: w, child: Text(w))],
                onChanged: (v) => onChanged(value.copyWith(window: v)),
              ),
            ),
            const SizedBox(width: 8),
            Expanded(
              child: DropdownButtonFormField<String>(
                initialValue: value.op,
                decoration: const InputDecoration(labelText: 'Used'),
                items: const [
                  DropdownMenuItem(value: 'below', child: Text('below')),
                  DropdownMenuItem(value: 'above', child: Text('above')),
                ],
                onChanged: (v) => onChanged(value.copyWith(op: v)),
              ),
            ),
            const SizedBox(width: 8),
            SizedBox(
              width: 72,
              child: TextFormField(
                initialValue: formatPercent(value.percent),
                decoration: const InputDecoration(labelText: '%'),
                keyboardType: TextInputType.number,
                onChanged: (v) => onChanged(value.copyWith(percent: double.tryParse(v) ?? value.percent)),
              ),
            ),
          ]),
          Align(alignment: Alignment.centerRight, child: IconButton(tooltip: 'Remove', icon: const Icon(Icons.close), onPressed: onRemove)),
        ]),
      ),
    );
  }
}
```

Note: text fields call `updateNode` on every keystroke, which pushes one undo snapshot per keystroke. That is acceptable within the 100-step cap; do not add debouncing (YAGNI).

`editor/inspector/flow_inspector.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';

/// Flow properties when nothing is selected.
class FlowInspector extends ConsumerStatefulWidget {
  final String flowId;
  final List<String> usedBy;
  final bool readOnly;
  const FlowInspector({super.key, required this.flowId, required this.usedBy, this.readOnly = false});

  @override
  ConsumerState<FlowInspector> createState() => _FlowInspectorState();
}

class _FlowInspectorState extends ConsumerState<FlowInspector> {
  late final _name = TextEditingController(text: ref.read(flowEditorProvider).flow.name);

  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final state = ref.watch(flowEditorProvider);
    if (_name.text != state.flow.name) _name.text = state.flow.name; // undo/redo
    final text = Theme.of(context).textTheme;
    return Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
      Text('Flow', style: text.titleMedium),
      const SizedBox(height: 12),
      TextField(
        key: const ValueKey('flow-name'),
        controller: _name,
        enabled: !widget.readOnly,
        maxLength: 120,
        decoration: const InputDecoration(labelText: 'Name'),
        onChanged: (v) => ref.read(flowEditorProvider.notifier).rename(v),
      ),
      const SizedBox(height: 12),
      Text('USED BY', style: text.labelSmall),
      if (widget.usedBy.isEmpty) Text('Not selected anywhere yet.', style: text.bodySmall),
      for (final u in widget.usedBy) Text(u, style: text.bodySmall),
      const SizedBox(height: 16),
      for (final w in state.check.warnings) Text('⚠ ${w.node}: ${w.message}', style: text.bodySmall),
      for (final e in state.check.errors.where((e) => e.node.isEmpty))
        Text(e.message, style: TextStyle(color: Theme.of(context).colorScheme.error)),
    ]);
  }
}
```

`usedBy` is computed in the screen from the config (`configNotifierProvider`): `'Global'` when `listing.selected == flowId`, plus `'org <name>'` / `'repo <owner/name>'` for every `ai.orgs.*.flow` / `ai.repos.*.flow` equal to `flowId`. Read the shape of `AppConfig` in `lib/core/models/config_model.dart` to get the org/repo flow fields (grep `flow` there); if `AppConfig` does not expose them, show only `'Global'` and add the org/repo lookup by reading `ref.read(apiClientProvider).fetchConfig()` raw map — prefer whichever the org/repo detail screens already use to show the selected flow (`org_detail_screen.dart:172`).

`editor/test_panel.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';

class FlowTestPanel extends ConsumerStatefulWidget {
  const FlowTestPanel({super.key});
  @override
  ConsumerState<FlowTestPanel> createState() => _FlowTestPanelState();
}

class _FlowTestPanelState extends ConsumerState<FlowTestPanel> {
  DateTime _at = DateTime.now();
  final _repo = TextEditingController();
  String? _error;
  bool _running = false;

  @override
  void dispose() {
    _repo.dispose();
    super.dispose();
  }

  Future<void> _pick() async {
    final d = await showDatePicker(context: context, initialDate: _at, firstDate: DateTime(2020), lastDate: DateTime(2100));
    if (d == null || !mounted) return;
    final t = await showTimePicker(context: context, initialTime: TimeOfDay.fromDateTime(_at));
    if (t == null) return;
    setState(() => _at = DateTime(d.year, d.month, d.day, t.hour, t.minute));
  }

  Future<void> _run() async {
    setState(() {
      _running = true;
      _error = null;
    });
    try {
      final d = await ref.read(apiClientProvider).simulateFlow(
        graph: ref.read(flowEditorProvider).flow,
        at: _at,
        repo: _repo.text.trim().isEmpty ? null : _repo.text.trim(),
      );
      ref.read(flowEditorProvider.notifier).setSimulation(d);
    } on ApiException catch (e) {
      setState(() => _error = e.message);
    } finally {
      if (mounted) setState(() => _running = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final sim = ref.watch(flowEditorProvider.select((s) => s.simulation));
    final text = Theme.of(context).textTheme;
    final local = MaterialLocalizations.of(context);
    return Column(crossAxisAlignment: CrossAxisAlignment.stretch, mainAxisSize: MainAxisSize.min, children: [
      Text('Test this flow', style: text.titleMedium),
      const SizedBox(height: 8),
      OutlinedButton.icon(
        onPressed: _pick,
        icon: const Icon(Icons.event),
        label: Text('${local.formatShortDate(_at)} ${local.formatTimeOfDay(TimeOfDay.fromDateTime(_at))}'),
      ),
      TextField(controller: _repo, decoration: const InputDecoration(labelText: 'Repository (optional, owner/name)')),
      const SizedBox(height: 8),
      FilledButton(key: const ValueKey('flow-test-run'), onPressed: _running ? null : _run, child: const Text('Run')),
      if (_error != null) Text(_error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
      if (sim != null) ...[
        const SizedBox(height: 12),
        Text(
          sim.candidates.isEmpty
              ? 'No agent would review: no path reaches an available agent.'
              : 'Reviews with ${sim.candidates.first}${sim.candidates.length > 1 ? ', then ${sim.candidates.skip(1).join(', ')}' : ''}',
          style: text.bodyMedium,
        ),
        for (final s in sim.path)
          Text('${s.node}: ${s.reasons.join('; ')}', style: text.bodySmall),
      ],
    ]);
  }
}
```

`editor/flow_editor_screen.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/flows/canvas/flow_canvas.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/editor/inspector/agent_inspector.dart';
import 'package:heimdallm/features/flows/editor/inspector/condition_inspector.dart';
import 'package:heimdallm/features/flows/editor/inspector/flow_inspector.dart';
import 'package:heimdallm/features/flows/editor/palette.dart';
import 'package:heimdallm/features/flows/editor/test_panel.dart';
import 'package:heimdallm/features/flows/flows_list_screen.dart';
import 'package:heimdallm/shared/widgets/toast.dart';

final flowEditorDirtyProvider = Provider.autoDispose<bool>((ref) => ref.watch(flowEditorProvider.select((s) => s.dirty)));

Future<bool> confirmDiscardFlowChanges(BuildContext context) async {
  final ok = await showDialog<bool>(
    context: context,
    builder: (ctx) => AlertDialog(
      title: const Text('Discard unsaved changes?'),
      content: const Text('The flow has changes that are not saved.'),
      actions: [
        TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Keep editing')),
        FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Discard')),
      ],
    ),
  );
  return ok ?? false;
}

/// go_router `onExit` for `/flows/:id`.
Future<bool> flowEditorOnExit(BuildContext context) async {
  final dirty = ProviderScope.containerOf(context, listen: false).read(flowEditorDirtyProvider);
  if (!dirty) return true;
  return confirmDiscardFlowChanges(context);
}

class FlowEditorScreen extends ConsumerStatefulWidget {
  final String flowId;
  const FlowEditorScreen({super.key, required this.flowId});

  @override
  ConsumerState<FlowEditorScreen> createState() => _FlowEditorScreenState();
}

class _FlowEditorScreenState extends ConsumerState<FlowEditorScreen> {
  FlowListing? _listing;
  bool _saving = false;
  bool _testOpen = false;
  List<FlowIssue> _serverIssues = const [];

  bool get _isDefault => _listing != null && widget.flowId == _listing!.defaultId;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final listing = await ref.read(apiClientProvider).fetchFlows();
    if (!mounted) return;
    ref.read(flowEditorProvider.notifier).load(listing.flows[widget.flowId] ?? ReviewFlow(name: widget.flowId));
    setState(() => _listing = listing);
  }

  Future<void> _save() async {
    final state = ref.read(flowEditorProvider);
    if (!state.check.ok) {
      showToast(context, state.check.errors.first.message, isError: true);
      ref.read(flowEditorProvider.notifier).select(
        state.check.errors.first.node.isEmpty ? const NoSelection() : NodeSelection(state.check.errors.first.node),
      );
      return;
    }
    setState(() => _saving = true);
    try {
      final json = await ref.read(apiClientProvider).putFlow(widget.flowId, state.flow);
      ref.read(configNotifierProvider.notifier).updateFromServer(json);
      ref.invalidate(flowsProvider);
      ref.read(flowEditorProvider.notifier).markSaved();
      setState(() => _serverIssues = const []);
      if (mounted) showToast(context, 'Flow saved');
    } on FlowValidationException catch (e) {
      setState(() => _serverIssues = e.issues);
      if (mounted) showToast(context, e.message, isError: true);
    } on ApiException catch (e) {
      if (mounted) showToast(context, e.message, isError: true);
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  Future<void> _delete() async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Delete flow?'),
        content: const Text("Repositories and organizations using it go back to their parent's flow."),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Cancel')),
          FilledButton(key: const ValueKey('flow-delete-confirm'), onPressed: () => Navigator.pop(ctx, true), child: const Text('Delete')),
        ],
      ),
    );
    if (ok != true || !mounted) return;
    try {
      final json = await ref.read(apiClientProvider).deleteFlow(widget.flowId);
      ref.read(configNotifierProvider.notifier).updateFromServer(json);
      ref.invalidate(flowsProvider);
      ref.read(flowEditorProvider.notifier).markSaved();
      if (mounted) context.go('/flows');
    } on ApiException catch (e) {
      if (mounted) showToast(context, e.message, isError: true);
    }
  }

  Future<void> _useGlobally(bool use) async {
    try {
      final json = await ref.read(apiClientProvider).patchConfig({'ai': {'flow': use ? widget.flowId : ''}});
      ref.read(configNotifierProvider.notifier).updateFromServer(json);
      ref.invalidate(flowsProvider);
      await _reloadListingOnly();
    } on ApiException catch (e) {
      if (mounted) showToast(context, e.message, isError: true);
    }
  }

  Future<void> _reloadListingOnly() async {
    final listing = await ref.read(apiClientProvider).fetchFlows();
    if (mounted) setState(() => _listing = listing);
  }

  Future<void> _duplicate() async {
    final flow = ref.read(flowEditorProvider).flow;
    final id = await askNewFlowId(context, _listing!); // extracted from FlowsScreen._create in Task 7's file
    if (id == null || !mounted) return;
    try {
      final json = await ref.read(apiClientProvider).putFlow(id, flow.copyWith(name: '${flow.name} (copy)'));
      ref.read(configNotifierProvider.notifier).updateFromServer(json);
      ref.invalidate(flowsProvider);
      if (mounted) context.go('/flows/$id');
    } on ApiException catch (e) {
      if (mounted) showToast(context, e.message, isError: true);
    }
  }

  void _addAtCentre(FlowNodeType type) {
    final ctl = ref.read(flowEditorProvider.notifier);
    final count = ref.read(flowEditorProvider).flow.nodes.length;
    ctl.addNode(type, Offset(400 + 30.0 * (count % 8), 400 + 30.0 * (count % 8)));
  }

  @override
  Widget build(BuildContext context) {
    final state = ref.watch(flowEditorProvider);
    final ctl = ref.read(flowEditorProvider.notifier);
    final listing = _listing;
    if (listing == null) return const Scaffold(body: Center(child: CircularProgressIndicator()));
    final readOnly = _isDefault;
    final issuesFor = <String, List<FlowIssue>>{};
    for (final i in [...state.check.errors, ..._serverIssues]) {
      issuesFor.putIfAbsent(i.node, () => []).add(i);
    }
    final selected = state.selection is NodeSelection ? state.flow.nodes[(state.selection as NodeSelection).id] : null;
    final inspector = switch (selected) {
      final FlowNode n when n.isCondition => ConditionInspector(node: n, agents: listing.agents, issues: issuesFor[n.id] ?? const []),
      final FlowNode n => AgentInspector(node: n, agents: listing.agents, issues: issuesFor[n.id] ?? const []),
      null => FlowInspector(flowId: widget.flowId, usedBy: [if (listing.selected == widget.flowId) 'Global'], readOnly: readOnly),
    };

    return CallbackShortcuts(
      bindings: {
        const SingleActivator(LogicalKeyboardKey.keyZ, meta: true): ctl.undo,
        const SingleActivator(LogicalKeyboardKey.keyZ, control: true): ctl.undo,
        const SingleActivator(LogicalKeyboardKey.keyZ, meta: true, shift: true): ctl.redo,
        const SingleActivator(LogicalKeyboardKey.keyZ, control: true, shift: true): ctl.redo,
      },
      child: Scaffold(
        appBar: AppBar(
          title: Text(state.flow.name.isEmpty ? widget.flowId : state.flow.name),
          actions: [
            if (!readOnly) ...[
              Row(children: [
                const Text('Use globally'),
                Switch(key: const ValueKey('flow-use-globally'), value: listing.selected == widget.flowId, onChanged: _useGlobally),
              ]),
              IconButton(key: const ValueKey('flow-undo'), tooltip: 'Undo', onPressed: state.canUndo ? ctl.undo : null, icon: const Icon(Icons.undo)),
              IconButton(key: const ValueKey('flow-redo'), tooltip: 'Redo', onPressed: state.canRedo ? ctl.redo : null, icon: const Icon(Icons.redo)),
              IconButton(key: const ValueKey('flow-arrange'), tooltip: 'Arrange', onPressed: ctl.arrange, icon: const Icon(Icons.auto_fix_high)),
            ],
            TextButton.icon(key: const ValueKey('flow-test'), onPressed: () => setState(() => _testOpen = !_testOpen), icon: const Icon(Icons.play_arrow), label: const Text('Test')),
            if (readOnly)
              FilledButton(onPressed: _duplicate, child: const Text('Duplicate as a new flow'))
            else ...[
              IconButton(key: const ValueKey('flow-delete'), tooltip: 'Delete', onPressed: listing.flows.containsKey(widget.flowId) ? _delete : null, icon: const Icon(Icons.delete_outline)),
              const SizedBox(width: 8),
              FilledButton(
                key: const ValueKey('flow-save'),
                onPressed: _saving || state.draft != null ? null : _save,
                child: Text(state.dirty ? 'Save' : 'Saved'),
              ),
            ],
            const SizedBox(width: 12),
          ],
        ),
        body: LayoutBuilder(builder: (context, c) {
          final compact = c.maxWidth < 900;
          final canvas = Stack(children: [
            Positioned.fill(child: FlowCanvas(onDropFromPalette: readOnly ? null : (t, at) => ctl.addNode(t, at))),
            if (_testOpen)
              Positioned(left: 16, bottom: 16, width: 360, child: Card(child: Padding(padding: const EdgeInsets.all(12), child: const FlowTestPanel()))),
            // Task 12 adds the assistant button and panel here.
          ]);
          if (compact) {
            return Column(children: [
              Expanded(child: canvas),
              if (!readOnly) SizedBox(height: 260, child: SingleChildScrollView(padding: const EdgeInsets.all(12), child: inspector)),
            ]);
          }
          return Row(children: [
            if (!readOnly) SizedBox(width: 220, child: FlowPalette(onAdd: _addAtCentre)),
            Expanded(child: canvas),
            SizedBox(width: 340, child: SingleChildScrollView(padding: const EdgeInsets.all(16), child: inspector)),
          ]);
        }),
      ),
    );
  }
}
```

In `flows_list_screen.dart`, extract the id dialog of `FlowsScreen._create` into a top-level `Future<String?> askNewFlowId(BuildContext context, FlowListing listing)` that returns `null` on cancel or duplicate id (showing the existing toast), and make `_create` call it then `context.go('/flows/$id')`.

`router.dart`: replace the temporary `/flows/:id` builder with:

```dart
                GoRoute(
                  path: ':id',
                  builder: (context, state) => FlowEditorScreen(flowId: state.pathParameters['id']!),
                  onExit: (context, state) => flowEditorOnExit(context),
                ),
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd flutter_app && flutter test test/features/flows/ && flutter analyze`
Expected: PASS, no issues.

- [ ] **Step 5: Commit**

```bash
git add flutter_app
git commit -m "feat(app): visual flow editor with palette, inspector and test panel

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 12: "Adjust with AI" panel

**Files:**
- Create: `flutter_app/lib/features/flows/assistant/assist_panel.dart`
- Modify: `flutter_app/lib/features/flows/editor/flow_editor_screen.dart` (floating button + panel; open by default for a new flow)
- Test: `flutter_app/test/features/flows/assistant/assist_panel_test.dart`

**Interfaces:**
- Consumes: `ApiClient.assistFlow`, `FlowEditorController.showDraft/acceptDraft/discardDraft`, `FlowListing.agents`, `shared_preferences`.
- Produces:
  - `class AssistPanel extends ConsumerStatefulWidget { const AssistPanel({super.key, required this.agents, required this.defaultAgent, required this.onClose}); }`
  - `const kAssistAgentPrefKey = 'flows.assist.agent';`
  - Keys: `assist-open`, `assist-agent`, `assist-input`, `assist-run`, `assist-cancel`, `assist-accept`, `assist-discard`.
  - Runnable agents shown = `agents.where((a) => a.installed && !a.reviewOnly)`; preselected = stored pref if still runnable, else `defaultAgent` (the daemon's primary, from `configNotifierProvider`) if runnable, else the first runnable.

- [ ] **Step 1: Write the failing tests**

```dart
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/flows/assistant/assist_panel.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:http/http.dart' as http;
import 'package:mocktail/mocktail.dart';
import 'package:shared_preferences/shared_preferences.dart';

class _MockApi extends Mock implements ApiClient {}

const _agents = [
  FlowAgent(id: 'claude', installed: true),
  FlowAgent(id: 'codex', installed: true),
  FlowAgent(id: 'gemini'),
  FlowAgent(id: 'openrouter', installed: true, reviewOnly: true),
];

final _current = ReviewFlow(name: 'x', start: 'a1', nodes: {
  'a1': const FlowNode(id: 'a1', type: FlowNodeType.agent, agent: 'claude', position: Offset(300, 200)),
});
final _draft = FlowAssistResult(
  plan: 'Codex first, Claude as fallback.',
  warnings: const ['some steps have no positions'],
  flow: ReviewFlow(name: 'x', start: 'a2', nodes: {
    'a2': const FlowNode(id: 'a2', type: FlowNodeType.agent, agent: 'codex', next: 'a1'),
    'a1': const FlowNode(id: 'a1', type: FlowNodeType.agent, agent: 'claude'),
  }),
);

Future<(ProviderContainer, _MockApi)> pumpPanel(WidgetTester tester, {Map<String, Object> prefs = const {}}) async {
  SharedPreferences.setMockInitialValues(prefs);
  registerFallbackValue(const ReviewFlow());
  final api = _MockApi();
  final c = ProviderContainer(overrides: [apiClientProvider.overrideWithValue(api)]);
  addTearDown(c.dispose);
  c.listen(flowEditorProvider, (_, _) {});
  c.read(flowEditorProvider.notifier).load(_current);
  await tester.pumpWidget(UncontrolledProviderScope(
    container: c,
    child: MaterialApp(
      builder: (context, child) => HeimdallmTheme.scope(child: child!),
      home: Scaffold(body: AssistPanel(agents: _agents, defaultAgent: 'claude', onClose: () {})),
    ),
  ));
  await tester.pumpAndSettle();
  return (c, api);
}

void main() {
  testWidgets('offers only installed CLI agents, preselects the default', (tester) async {
    await pumpPanel(tester);
    await tester.tap(find.byKey(const ValueKey('assist-agent')));
    await tester.pumpAndSettle();
    expect(find.text('codex'), findsWidgets);
    expect(find.text('gemini'), findsNothing);
    expect(find.text('openrouter'), findsNothing);
  });

  testWidgets('remembers the last agent', (tester) async {
    await pumpPanel(tester, prefs: {kAssistAgentPrefKey: 'codex'});
    expect(find.descendant(of: find.byKey(const ValueKey('assist-agent')), matching: find.text('codex')), findsOneWidget);
  });

  testWidgets('run shows the draft with its plan; accept applies it', (tester) async {
    final (c, api) = await pumpPanel(tester);
    when(() => api.assistFlow(agent: any(named: 'agent'), instruction: any(named: 'instruction'), current: any(named: 'current'), client: any(named: 'client')))
        .thenAnswer((_) async => _draft);
    await tester.enterText(find.byKey(const ValueKey('assist-input')), 'codex first');
    await tester.tap(find.byKey(const ValueKey('assist-run')));
    await tester.pumpAndSettle();
    final sent = verify(() => api.assistFlow(agent: 'claude', instruction: 'codex first', current: captureAny(named: 'current'), client: any(named: 'client'))).captured.single as ReviewFlow;
    expect(sent.start, 'a1');
    expect(find.text('Codex first, Claude as fallback.'), findsOneWidget);
    expect(c.read(flowEditorProvider).draft, isNotNull);
    await tester.tap(find.byKey(const ValueKey('assist-accept')));
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.start, 'a2');
    expect(c.read(flowEditorProvider).draft, isNull);
  });

  testWidgets('refining sends the draft as the current flow; discard keeps the canvas', (tester) async {
    final (c, api) = await pumpPanel(tester);
    when(() => api.assistFlow(agent: any(named: 'agent'), instruction: any(named: 'instruction'), current: any(named: 'current'), client: any(named: 'client')))
        .thenAnswer((_) async => _draft);
    await tester.enterText(find.byKey(const ValueKey('assist-input')), 'codex first');
    await tester.tap(find.byKey(const ValueKey('assist-run')));
    await tester.pumpAndSettle();
    await tester.enterText(find.byKey(const ValueKey('assist-input')), 'and gemini last');
    await tester.tap(find.byKey(const ValueKey('assist-run')));
    await tester.pumpAndSettle();
    final second = verify(() => api.assistFlow(agent: any(named: 'agent'), instruction: 'and gemini last', current: captureAny(named: 'current'), client: any(named: 'client'))).captured.single as ReviewFlow;
    expect(second.start, 'a2');
    await tester.tap(find.byKey(const ValueKey('assist-discard')));
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.start, 'a1');
    expect(c.read(flowEditorProvider).draft, isNull);
  });

  testWidgets('cancel aborts the request and leaves the canvas', (tester) async {
    final (c, api) = await pumpPanel(tester);
    final pending = Completer<FlowAssistResult>();
    http.Client? used;
    when(() => api.assistFlow(agent: any(named: 'agent'), instruction: any(named: 'instruction'), current: any(named: 'current'), client: any(named: 'client')))
        .thenAnswer((inv) {
      used = inv.namedArguments[#client] as http.Client?;
      return pending.future;
    });
    await tester.enterText(find.byKey(const ValueKey('assist-input')), 'x');
    await tester.tap(find.byKey(const ValueKey('assist-run')));
    await tester.pump();
    expect(find.byKey(const ValueKey('assist-cancel')), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('assist-cancel')));
    await tester.pump();
    expect(used, isNotNull);
    pending.completeError(http.ClientException('Connection closed'));
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).draft, isNull);
    expect(find.textContaining('Connection closed'), findsNothing); // a cancel is not an error
  });

  testWidgets('daemon errors are shown', (tester) async {
    final (_, api) = await pumpPanel(tester);
    when(() => api.assistFlow(agent: any(named: 'agent'), instruction: any(named: 'instruction'), current: any(named: 'current'), client: any(named: 'client')))
        .thenThrow(ApiException('claude did not answer in time'));
    await tester.enterText(find.byKey(const ValueKey('assist-input')), 'x');
    await tester.tap(find.byKey(const ValueKey('assist-run')));
    await tester.pumpAndSettle();
    expect(find.text('claude did not answer in time'), findsOneWidget);
  });
}
```

(`ApiException('...')` — use the real constructor form, as in Task 7.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd flutter_app && flutter test test/features/flows/assistant/`
Expected: FAIL — `AssistPanel` not found.

- [ ] **Step 3: Implement**

`assistant/assist_panel.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';

const kAssistAgentPrefKey = 'flows.assist.agent';

/// Describe a flow (or a change) in words; an installed agent drafts it.
/// The draft is drawn on the canvas and only applied on Accept.
class AssistPanel extends ConsumerStatefulWidget {
  final List<FlowAgent> agents;
  final String defaultAgent;
  final VoidCallback onClose;
  const AssistPanel({super.key, required this.agents, required this.defaultAgent, required this.onClose});

  @override
  ConsumerState<AssistPanel> createState() => _AssistPanelState();
}

class _AssistPanelState extends ConsumerState<AssistPanel> {
  final _input = TextEditingController();
  String? _agent;
  http.Client? _inflight;
  bool _cancelled = false;
  String? _error;

  List<FlowAgent> get _runnable => widget.agents.where((a) => a.installed && !a.reviewOnly).toList();

  @override
  void initState() {
    super.initState();
    _restoreAgent();
  }

  Future<void> _restoreAgent() async {
    String? stored;
    try {
      stored = (await SharedPreferences.getInstance()).getString(kAssistAgentPrefKey);
    } catch (_) {}
    final ids = _runnable.map((a) => a.id).toList();
    if (!mounted) return;
    setState(() {
      _agent = ids.contains(stored)
          ? stored
          : ids.contains(widget.defaultAgent)
              ? widget.defaultAgent
              : (ids.isEmpty ? null : ids.first);
    });
  }

  Future<void> _setAgent(String? id) async {
    setState(() => _agent = id);
    if (id == null) return;
    try {
      await (await SharedPreferences.getInstance()).setString(kAssistAgentPrefKey, id);
    } catch (_) {}
  }

  @override
  void dispose() {
    _inflight?.close();
    _input.dispose();
    super.dispose();
  }

  Future<void> _run() async {
    final agent = _agent;
    final text = _input.text.trim();
    if (agent == null || text.isEmpty) return;
    final state = ref.read(flowEditorProvider);
    final current = state.draft?.flow ?? state.flow;
    final client = http.Client();
    setState(() {
      _inflight = client;
      _cancelled = false;
      _error = null;
    });
    try {
      final r = await ref.read(apiClientProvider).assistFlow(
        agent: agent,
        instruction: text,
        current: current.nodes.isEmpty ? null : current,
        client: client,
      );
      if (!mounted) return;
      ref.read(flowEditorProvider.notifier).showDraft(r);
      _input.clear();
    } on ApiException catch (e) {
      if (mounted && !_cancelled) setState(() => _error = e.message);
    } on http.ClientException catch (e) {
      if (mounted && !_cancelled) setState(() => _error = e.message);
    } finally {
      client.close();
      if (mounted) setState(() => _inflight = null);
    }
  }

  void _cancel() {
    _cancelled = true;
    _inflight?.close();
  }

  @override
  Widget build(BuildContext context) {
    final draft = ref.watch(flowEditorProvider.select((s) => s.draft));
    final ctl = ref.read(flowEditorProvider.notifier);
    final text = Theme.of(context).textTheme;
    final busy = _inflight != null;
    final runnable = _runnable;
    return Card(
      elevation: 6,
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, mainAxisSize: MainAxisSize.min, children: [
          Row(children: [
            const Icon(Icons.auto_awesome, size: 18),
            const SizedBox(width: 8),
            Expanded(child: Text('Adjust with AI', style: text.titleMedium)),
            IconButton(tooltip: 'Close', onPressed: widget.onClose, icon: const Icon(Icons.close)),
          ]),
          if (runnable.isEmpty)
            Text('No installed CLI agent can draft flows. Install one from the Agents page.', style: text.bodySmall)
          else
            DropdownButtonFormField<String>(
              key: const ValueKey('assist-agent'),
              initialValue: _agent,
              decoration: const InputDecoration(labelText: 'Agent that drafts the flow'),
              items: [for (final a in runnable) DropdownMenuItem(value: a.id, child: Text(a.id))],
              onChanged: busy ? null : _setAgent,
            ),
          const SizedBox(height: 8),
          TextField(
            key: const ValueKey('assist-input'),
            controller: _input,
            enabled: !busy,
            minLines: 2,
            maxLines: 6,
            maxLength: 4000,
            decoration: InputDecoration(
              hintText: draft == null
                  ? 'e.g. At night use Codex; if Claude is above 80% of its weekly quota, use Gemini'
                  : 'Refine the draft, e.g. "and use Copilot at weekends"',
            ),
          ),
          if (_error != null) Text(_error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
          Row(mainAxisAlignment: MainAxisAlignment.end, children: [
            if (busy) ...[
              const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2)),
              const SizedBox(width: 8),
              TextButton(key: const ValueKey('assist-cancel'), onPressed: _cancel, child: const Text('Cancel')),
            ] else
              FilledButton.icon(
                key: const ValueKey('assist-run'),
                onPressed: runnable.isEmpty ? null : _run,
                icon: const Icon(Icons.auto_awesome),
                label: Text(draft == null ? 'Draft' : 'Refine'),
              ),
          ]),
          if (draft != null) ...[
            const Divider(),
            Text(draft.plan, style: text.bodyMedium),
            const SizedBox(height: 6),
            Wrap(spacing: 6, children: [
              for (final kind in DiffKind.values)
                if (draft.diff.nodes.values.contains(kind))
                  Chip(label: Text('${draft.diff.nodes.values.where((k) => k == kind).length} ${kind.name}')),
            ]),
            for (final w in draft.warnings) Text('⚠ $w', style: text.bodySmall),
            const SizedBox(height: 8),
            Row(mainAxisAlignment: MainAxisAlignment.end, children: [
              TextButton(key: const ValueKey('assist-discard'), onPressed: busy ? null : ctl.discardDraft, child: const Text('Discard')),
              const SizedBox(width: 8),
              FilledButton(key: const ValueKey('assist-accept'), onPressed: busy ? null : ctl.acceptDraft, child: const Text('Accept')),
            ]),
          ],
        ]),
      ),
    );
  }
}
```

In `flow_editor_screen.dart`:
- add `bool _assistOpen = false;`; in `_load`, set `_assistOpen = !listing.flows.containsKey(widget.flowId) && widget.flowId != listing.defaultId;` (a new flow opens with the assistant).
- read the default agent with `ref.watch(configNotifierProvider).value?.primary ?? ''` (use the `AppConfig` field that holds `ai.primary`; check `lib/core/models/config_model.dart`).
- replace the `// Task 12 …` comment in the canvas `Stack` with:

```dart
            if (!readOnly && !_assistOpen)
              Positioned(
                right: 16,
                bottom: 16,
                child: FloatingActionButton.extended(
                  key: const ValueKey('assist-open'),
                  onPressed: () => setState(() => _assistOpen = true),
                  icon: const Icon(Icons.auto_awesome),
                  label: const Text('Adjust with AI'),
                ),
              ),
            if (!readOnly && _assistOpen)
              Positioned(
                right: 16,
                bottom: 16,
                width: 420,
                child: AssistPanel(
                  agents: listing.agents,
                  defaultAgent: defaultAgent,
                  onClose: () => setState(() => _assistOpen = false),
                ),
              ),
```

Add a screen test to `flow_editor_screen_test.dart`:

```dart
  testWidgets('a new flow opens with the assistant', (tester) async {
    await pumpEditor(tester, id: 'brand-new');
    expect(find.byKey(const ValueKey('assist-input')), findsOneWidget);
  });
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd flutter_app && flutter test test/features/flows/ && flutter analyze`
Expected: PASS, no issues.

- [ ] **Step 5: Commit**

```bash
git add flutter_app
git commit -m "feat(app): draft and refine flows in words with an installed agent

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 13: Docs, full gates and end-to-end verification

**Files:**
- Modify: `docs/configuration-guide.md` ("Review flows" section, ~lines 384–446)
- Modify: `docs/superpowers/specs/2026-10-12-flow-graph-editor-design.md` (align with decisions taken while planning)

**Interfaces:** none new.

- [ ] **Step 1: Rewrite the "Review flows" section of `docs/configuration-guide.md`**

Replace the rule-based example and prose with the node format of spec §3.1 (the `night-codex` example), a short explanation of `condition` (yes/no) and `agent` (fallback `next`) steps, the evaluation walk, the limits (200 steps, 20 conditions per step, no loops), the editor ("drag steps from the palette, connect dots, Test, Adjust with AI"), `POST /flows/simulate` with `graph`, `POST /flows/assist`, and `heimdallm-cli flows simulate|assist`. Keep the section's existing heading so links keep working.

- [ ] **Step 2: Align the spec with the plan**

In the spec:
- §5: replace the migration section with: there is no migration — review flows had no users, the rule-list format is deleted, and a TOML flow still written with `rules` has no `start`, so `Load` rejects it with the per-node validation error (the `rules` keys are ignored as unknown), and `PUT` rejects a `rules` body with `400`.
- §6: `POST /flows/simulate` keeps `flow` as the saved flow id and takes the unsaved graph as `graph`.
- §2: drop "Config backup-on-rewrite pattern" from the reused table.
- §10/§12: drop the migration property test and the migration risks.

- [ ] **Step 3: Run every gate**

```bash
make test-docker
make test-cli && make lint-cli
cd flutter_app && flutter test && flutter analyze && cd ..
make build-web
make test-coverage-gate
```

Expected: all pass; the coverage gate reports no drop. If the gate drops, add tests to the files the report names — do not edit the baseline.

- [ ] **Step 4: End-to-end against a real daemon (hard stop on reviews)**

Build the daemon for the host inside Docker (never `go build` on the host):

```bash
SCRATCH=$(mktemp -d)
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/src:ro" -v "$SCRATCH:/out" -w /src/daemon \
  -e HOME=/tmp/home -e GOCACHE=/tmp/c/build -e GOMODCACHE=/tmp/c/mod -e GOFLAGS=-buildvcs=false \
  golang:1.25@sha256:699337d620559a59b4a2bb298ad59611e535d2ee755a34cf2d2a98f37578dc80 \
  sh -c 'CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /out/heimdalld ./cmd/heimdallm'
```

Prepare an isolated home: copy the operator's `~/.config/heimdallm/config.toml` to `$SCRATCH/home/.config/heimdallm/config.toml`, set `server.port` to a free port (e.g. 7942), and **remove every token/credential so the review agent is unauthenticated and the GitHub client cannot act** (the hard stop: the daemon must not review or comment on real PRs). Then:

```bash
HOME=$SCRATCH/home $SCRATCH/heimdalld &   # note the PID
```

Verify, citing output:
1. `heimdallm-cli --host localhost:7942 flows list` shows the built-in default flow as `a10 → a20` steps.
2. PUT the spec's `night-codex` example graph with curl, then for `Mon 09:00`, `Mon 23:00`, `Sat 12:00` (daemon zone) `curl -s -X POST localhost:7942/flows/simulate -H "X-Heimdallm-Token: $(cat $SCRATCH/home/.local/share/heimdallm/api_token)" -d '{"flow":"night-codex","at":"<RFC3339>"}'` returns the candidates and `path` the spec's walk predicts (write the expectation down before running).
3. `heimdallm-cli --host localhost:7942 flows assist "de noche usa Codex; si Claude pasa del 80% semanal, Gemini" --agent claude` prints a plan and a valid draft (this uses the operator's real Claude CLI login — the agent CLI is authenticated even though the daemon's GitHub access is not). Then `make dev-cli`-style or the desktop app pointed at port 7942: open the flow editor, run the assistant, Accept, Save, reload, and confirm the saved graph renders identically.
4. Stop the daemon (`kill <PID>`), delete `$SCRATCH`.

If step 3's agent is not logged in on this machine, record that fact and run it with whichever installed agent is (the selector lists them); do not skip the assistant check.

- [ ] **Step 5: Commit**

```bash
git add docs
git commit -m "docs: review flows as decision graphs and the flow assistant

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Self-Review Notes (planner)

- Spec coverage: §3 → Task 1; §4 → Task 3; §5 (no migration, amended) → Task 3; §6 → Task 4; §7 → Task 5; §8 → Task 6; §9.1 → Tasks 10–12; §9.2 → Tasks 7–12; §10 → each task + Task 13; §11 → Task 13 docs; the PR itself is opened after Task 13 with the mandatory security review of the diff.
- Deviations from the spec, folded back in Task 13 Step 2: no migration (user decision 2026-10-13); simulate takes `graph`.
