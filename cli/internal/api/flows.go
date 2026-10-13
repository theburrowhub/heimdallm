package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// QuotaCondition is a condition step's check on an agent's used quota.
type QuotaCondition struct {
	Agent   string  `json:"agent"`
	Window  string  `json:"window"`
	Op      string  `json:"op"`
	Percent float64 `json:"percent"`
}

// ScheduleCondition is a condition step's check on weekday and time of day.
type ScheduleCondition struct {
	Days []string `json:"days"`
	From string   `json:"from"`
	To   string   `json:"to"`
	TZ   string   `json:"tz"`
}

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

// FlowListing is the GET /flows body.
type FlowListing struct {
	Flows     map[string]Flow `json:"flows"`
	Selected  string          `json:"selected"`
	DefaultID string          `json:"default_id"`
	Agents    []FlowAgent     `json:"agents"`
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

// QuotaWindow is one rate-limit window of an agent.
type QuotaWindow struct {
	Kind        string    `json:"kind"`
	Label       string    `json:"label"`
	UsedPercent float64   `json:"used_percent"`
	ResetsAt    time.Time `json:"resets_at"`
}

// AgentQuota is one agent's entry of GET /quotas.
type AgentQuota struct {
	Agent     string        `json:"agent"`
	Available bool          `json:"available"`
	Windows   []QuotaWindow `json:"windows"`
	Error     string        `json:"error"`
}

// ListFlows returns the configured flows and the global selection.
func (c *Client) ListFlows() (*FlowListing, error) {
	data, err := c.do(http.MethodGet, "/flows")
	if err != nil {
		return nil, err
	}
	var out FlowListing
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decoding flows: %w", err)
	}
	return &out, nil
}

// SimulateFlow evaluates flow (or, when empty, the flow repo resolves to) at
// at (zero = now) and returns the path it walked.
func (c *Client) SimulateFlow(flow, repo string, at time.Time) (*FlowDecision, error) {
	body := map[string]any{}
	if flow != "" {
		body["flow"] = flow
	}
	if repo != "" {
		body["repo"] = repo
	}
	if !at.IsZero() {
		body["at"] = at.UTC().Format(time.RFC3339)
	}
	data, err := c.doJSON(http.MethodPost, "/flows/simulate", body)
	if err != nil {
		return nil, err
	}
	var out FlowDecision
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decoding simulation: %w", err)
	}
	return &out, nil
}

// PutFlow creates or replaces a flow.
func (c *Client) PutFlow(id string, f Flow) error {
	_, err := c.doJSON(http.MethodPut, "/flows/"+url.PathEscape(id), f)
	return err
}

// assistTimeout covers the longest the daemon may hold an assist response
// (its 180 s agent budget plus a 30 s margin) with a little headroom.
const assistTimeout = 220 * time.Second

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

// GetQuotas returns every agent's remaining quota.
func (c *Client) GetQuotas() ([]AgentQuota, error) {
	data, err := c.do(http.MethodGet, "/quotas")
	if err != nil {
		return nil, err
	}
	var out []AgentQuota
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decoding quotas: %w", err)
	}
	return out, nil
}

// orderKeys sorts flow map keys like the daemon: numeric keys ascending, then
// the rest alphabetically; numerically equal keys ("010", "10") by text.
func orderKeys(keys []string) []string {
	sort.Slice(keys, func(i, j int) bool { return keyLess(keys[i], keys[j]) })
	return keys
}

// keyLess is orderKeys' comparison, named so every branch is tested directly
// (a sort only reaches some of them, depending on map iteration order).
func keyLess(a, b string) bool {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil && ai != bi:
		return ai < bi
	case aerr == nil && berr != nil:
		return true
	case aerr != nil && berr == nil:
		return false
	}
	return a < b
}

// Describe renders the reachable steps in walk order (depth-first from Start,
// yes before no before next), one line each.
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

// Describe summarises one step. Daemon-supplied text goes through DisplayText.
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

func describeSchedule(s ScheduleCondition) string {
	// A whole-day window on no particular days holds at every moment.
	if s.From == s.To && len(s.Days) == 0 {
		return "always"
	}
	days := "every day"
	if len(s.Days) > 0 {
		days = strings.Join(s.Days, ",")
	}
	text := days + " " + s.From + "-" + s.To
	if s.From == s.To {
		text = days + " all day"
	}
	switch {
	case s.TZ != "":
		text += " " + s.TZ
	case s.From != s.To:
		text += " daemon time"
	}
	return DisplayText(text, 80)
}

func describeQuota(q QuotaCondition) string {
	// Show an op this CLI does not know as the daemon sent it, rather than as
	// a comparison the daemon may not be making. An empty op is "below", as
	// the daemon evaluates it.
	op := q.Op
	switch q.Op {
	case "below", "":
		op = "<"
	case "above":
		op = ">"
	}
	return DisplayText(fmt.Sprintf("%s %s quota %s %g%%", q.Agent, q.Window, op, q.Percent), 80)
}

// Summary reads an agent's quota windows as "5h 34% · 7d 80%".
func (q AgentQuota) Summary() string {
	if !q.Available {
		if q.Error == "" {
			return "unknown"
		}
		return "unknown (" + DisplayText(q.Error, 40) + ")"
	}
	if len(q.Windows) == 0 {
		return "no limits"
	}
	parts := make([]string, 0, len(q.Windows))
	for _, w := range q.Windows {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", w.ShortLabel(), w.UsedPercent))
	}
	return strings.Join(parts, " · ")
}

// ShortLabel names a quota window compactly.
func (w QuotaWindow) ShortLabel() string {
	switch w.Kind {
	case "session":
		return "5h"
	case "weekly":
		return "7d"
	case "monthly":
		return "month"
	case "model":
		return DisplayText(w.Label, 32)
	}
	return DisplayText(w.Kind, 16)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return orderKeys(keys)
}
