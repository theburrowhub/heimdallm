package config

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
)

// FlowConfig is a review flow: a decision graph that picks which agents
// review a PR, in order. Evaluation starts at Start; condition nodes branch
// on schedule and quota, agent nodes add their agent to the candidates and
// continue through their fallback edge. Nodes are a map keyed by node id
// because TOML cannot hold slices of tables in this schema.
type FlowConfig struct {
	Name  string              `toml:"name" json:"name"`
	Start string              `toml:"start,omitempty" json:"start"`
	Nodes map[string]FlowNode `toml:"nodes,omitempty" json:"nodes"`
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

// QuotaCondition compares an agent's used quota with a threshold.
type QuotaCondition struct {
	Agent string `toml:"agent" json:"agent"`
	// Window is session (5h), weekly, monthly, credit, any (most-used
	// window), or model:<id> for a per-model bucket.
	Window string `toml:"window" json:"window"`
	// Op is "below" or "above" the used Percent.
	Op      string  `toml:"op" json:"op"`
	Percent float64 `toml:"percent" json:"percent"`
}

// ScheduleCondition holds on the given weekdays within [From, To) in TZ.
// From > To spans midnight (e.g. 22:00-06:00).
type ScheduleCondition struct {
	Days []string `toml:"days,omitempty" json:"days,omitempty"` // mon..sun; empty = every day
	From string   `toml:"from" json:"from"`                     // HH:MM
	To   string   `toml:"to" json:"to"`                         // HH:MM
	TZ   string   `toml:"tz,omitempty" json:"tz,omitempty"`     // IANA zone; empty = daemon local
}

// DefaultFlowID names the flow synthesised from primary/fallback when no flow
// is selected.
const DefaultFlowID = "default"

// Flow limits.
const (
	maxFlows           = 50
	maxNodesPerFlow    = 200
	maxConditionsPerRu = 20
)

var (
	flowIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	hhmmPattern     = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	modelWindowPfx  = "model:"
	validWeekdays   = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}
	validQuotaWin   = map[string]bool{"session": true, "weekly": true, "monthly": true, "credit": true, "any": true}
	validQuotaOps   = map[string]bool{"below": true, "above": true}
	validMatchModes = map[string]bool{"": true, "all": true, "any": true}
)

// Weekday parses a mon..sun day name.
func Weekday(name string) (time.Weekday, bool) {
	d, ok := validWeekdays[strings.ToLower(strings.TrimSpace(name))]
	return d, ok
}

// OrderKeys sorts flow map keys in evaluation order: numeric keys ascending,
// then non-numeric keys alphabetically. Keys that are numerically equal
// ("010" and "10") fall back to string order, so the order never depends on
// map iteration.
func OrderKeys(keys []string) []string {
	slices.SortFunc(keys, compareKeys)
	return keys
}

// compareKeys is OrderKeys' comparison. It is a named function so its
// branches are tested directly: which ones a sort reaches depends on the
// input order, and flow keys come from map iteration.
func compareKeys(a, b string) int {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil && ai != bi:
		return ai - bi
	case aerr == nil && berr != nil:
		return -1
	case aerr != nil && berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

// FlowNameForRepo returns the flow selected for repo through repo > org >
// global, or "" when none is selected.
func (c *Config) FlowNameForRepo(repo string) string {
	if r, ok := c.AI.Repos[repo]; ok && r.Flow != "" {
		return r.Flow
	}
	if org, _, ok := strings.Cut(repo, "/"); ok {
		if o, found := c.AI.Orgs[org]; found && o.Flow != "" {
			return o.Flow
		}
	}
	return c.AI.Flow
}

// FlowForRepo resolves the flow that decides which agent reviews repo. A
// selected flow that exists and has a start node wins; otherwise the flow is
// synthesised from the legacy primary/fallback settings (repo > org >
// global), so configs written before flows keep their behaviour unchanged.
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

// AllFlows lists the configured flows plus the synthesised default, for the
// API and the editor.
func (c *Config) AllFlows() map[string]FlowConfig {
	out := make(map[string]FlowConfig, len(c.AI.Flows)+1)
	for id, f := range c.AI.Flows {
		out[id] = f
	}
	if _, taken := out[DefaultFlowID]; !taken {
		out[DefaultFlowID] = c.legacyFlow("")
	}
	return out
}

// ValidateFlowID checks a flow identifier (map key and URL segment).
func ValidateFlowID(id string) error {
	if !flowIDPattern.MatchString(id) {
		return fmt.Errorf("config: flow id %q must be 1-64 characters of a-z, 0-9, '-' or '_'", id)
	}
	return nil
}

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

// validateScheduleCondition accepts from == to: that is the whole day.
func validateScheduleCondition(sc ScheduleCondition) error {
	if !hhmmPattern.MatchString(sc.From) || !hhmmPattern.MatchString(sc.To) {
		return fmt.Errorf("from/to must be HH:MM")
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

func (c *Config) validateFlows() error {
	if len(c.AI.Flows) > maxFlows {
		return fmt.Errorf("config: at most %d flows allowed", maxFlows)
	}
	for id, f := range c.AI.Flows {
		if err := ValidateFlowID(id); err != nil {
			return err
		}
		if err := ValidateFlow(id, f); err != nil {
			return err
		}
	}
	check := func(path, name string) error {
		if name == "" || name == DefaultFlowID {
			return nil
		}
		if _, ok := c.AI.Flows[name]; !ok {
			return fmt.Errorf("config: %s = %q names a flow that does not exist", path, name)
		}
		return nil
	}
	if err := check("ai.flow", c.AI.Flow); err != nil {
		return err
	}
	for org, o := range c.AI.Orgs {
		if err := check("ai.orgs."+org+".flow", o.Flow); err != nil {
			return err
		}
	}
	for repo, r := range c.AI.Repos {
		if err := check("ai.repos."+repo+".flow", r.Flow); err != nil {
			return err
		}
	}
	return nil
}
