package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// QuotaCondition is a flow rule condition on an agent's used quota.
type QuotaCondition struct {
	Agent   string  `json:"agent"`
	Window  string  `json:"window"`
	Op      string  `json:"op"`
	Percent float64 `json:"percent"`
}

// ScheduleCondition is a flow rule condition on weekday and time of day.
type ScheduleCondition struct {
	Days []string `json:"days"`
	From string   `json:"from"`
	To   string   `json:"to"`
	TZ   string   `json:"tz"`
}

// FlowRule picks Agent when its conditions hold.
type FlowRule struct {
	Agent    string                       `json:"agent"`
	Match    string                       `json:"match"`
	Quota    map[string]QuotaCondition    `json:"quota"`
	Schedule map[string]ScheduleCondition `json:"schedule"`
}

// Flow is a review flow: rules keyed by order ("10", "20", …).
type Flow struct {
	Name  string              `json:"name"`
	Rules map[string]FlowRule `json:"rules"`
}

// FlowListing is the GET /flows body.
type FlowListing struct {
	Flows        map[string]Flow `json:"flows"`
	Selected     string          `json:"selected"`
	DefaultID    string          `json:"default_id"`
	Agents       []string        `json:"agents"`
	WriteCapable []string        `json:"write_capable"`
}

// FlowRuleResult explains one rule of a simulated flow.
type FlowRuleResult struct {
	Key       string   `json:"key"`
	Agent     string   `json:"agent"`
	Matched   bool     `json:"matched"`
	Available bool     `json:"available"`
	Reasons   []string `json:"reasons"`
}

// FlowDecision is the POST /flows/simulate body.
type FlowDecision struct {
	FlowID     string           `json:"flow_id"`
	FlowName   string           `json:"flow_name"`
	At         time.Time        `json:"at"`
	Candidates []string         `json:"candidates"`
	Rules      []FlowRuleResult `json:"rules"`
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
// at (zero = now) and explains every rule.
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

// OrderedRuleKeys returns the rule keys in evaluation order (see orderKeys).
func (f Flow) OrderedRuleKeys() []string {
	keys := make([]string, 0, len(f.Rules))
	for k := range f.Rules {
		keys = append(keys, k)
	}
	return orderKeys(keys)
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

// Describe reads a rule as "<agent> when <conditions>". Daemon-supplied text
// goes through DisplayText.
func (r FlowRule) Describe() string {
	var conds []string
	for _, k := range sortedKeys(r.Schedule) {
		s := r.Schedule[k]
		days := "every day"
		if len(s.Days) > 0 {
			days = strings.Join(s.Days, ",")
		}
		tz := s.TZ
		if tz == "" {
			tz = "daemon time"
		}
		d := fmt.Sprintf("%s %s-%s %s", days, s.From, s.To, tz)
		conds = append(conds, DisplayText(d, 80))
	}
	for _, k := range sortedKeys(r.Quota) {
		q := r.Quota[k]
		op := "<"
		if q.Op == "above" {
			op = ">"
		}
		conds = append(conds, DisplayText(fmt.Sprintf("%s %s quota %s %g%%", q.Agent, q.Window, op, q.Percent), 80))
	}
	agent := DisplayText(r.Agent, 24)
	if len(conds) == 0 {
		return agent + " always"
	}
	join := " and "
	if r.Match == "any" {
		join = " or "
	}
	return agent + " when " + strings.Join(conds, join)
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
