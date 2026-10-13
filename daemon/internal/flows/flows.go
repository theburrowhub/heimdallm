// Package flows evaluates review flows: the decision graphs that decide which
// agents review a PR from the time of day and each agent's remaining quota.
package flows

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/quota"
)

// QuotaReader returns an agent's quota (quota.Service in production).
type QuotaReader interface {
	Get(ctx context.Context, agent string) quota.Provider
}

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
	var outcomes []bool
	var reasons []string
	for _, key := range sortedKeys(n.Schedule) {
		ok, why := scheduleHolds(n.Schedule[key], now)
		outcomes, reasons = append(outcomes, ok), append(reasons, why)
	}
	for _, key := range sortedKeys(n.Quota) {
		ok, why := quotaHolds(ctx, n.Quota[key], q)
		outcomes, reasons = append(outcomes, ok), append(reasons, why)
	}
	if len(outcomes) == 0 {
		return true, []string{"no conditions (always applies)"}
	}
	if n.Match == "any" {
		for _, ok := range outcomes {
			if ok {
				return true, reasons
			}
		}
		return false, reasons
	}
	for _, ok := range outcomes {
		if !ok {
			return false, reasons
		}
	}
	return true, reasons
}

// sortedKeys orders a condition step's keys in config.OrderKeys order, so
// reasons read in a stable order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return config.OrderKeys(keys)
}

func minutesOf(hhmm string) int {
	h, _ := strconv.Atoi(hhmm[:2])
	m, _ := strconv.Atoi(hhmm[3:])
	return h*60 + m
}

// scheduleHolds checks a weekday + time-of-day window. A window that crosses
// midnight belongs to the day it starts on. from == to is the whole day.
func scheduleHolds(s config.ScheduleCondition, now time.Time) (bool, string) {
	loc := time.Local
	if s.TZ != "" {
		if l, err := time.LoadLocation(s.TZ); err == nil {
			loc = l
		}
	}
	t := now.In(loc)
	cur := t.Hour()*60 + t.Minute()
	from, to := minutesOf(s.From), minutesOf(s.To)
	day := t.Weekday()
	inWindow := false
	allDay := from == to
	switch {
	case allDay:
		inWindow = true
	case from < to:
		inWindow = cur >= from && cur < to
	case cur >= from:
		inWindow = true
	case cur < to:
		// After midnight: the window started yesterday.
		inWindow = true
		day = (day + 6) % 7
	}
	dayOK := len(s.Days) == 0
	for _, name := range s.Days {
		if d, ok := config.Weekday(name); ok && d == day {
			dayOK = true
		}
	}
	desc := fmt.Sprintf("%s-%s", s.From, s.To)
	if allDay {
		desc = "all day"
	}
	if len(s.Days) > 0 {
		desc = strings.Join(s.Days, ",") + " " + desc
	}
	switch {
	case allDay && len(s.Days) == 0:
		desc = "always"
	case s.TZ != "":
		desc += " " + s.TZ
	case !allDay:
		desc += " daemon time"
	}
	now24 := t.Format("Mon 15:04")
	if inWindow && dayOK {
		return true, fmt.Sprintf("schedule %s holds (now %s)", desc, now24)
	}
	return false, fmt.Sprintf("schedule %s does not hold (now %s)", desc, now24)
}

func quotaHolds(ctx context.Context, c config.QuotaCondition, q QuotaReader) (bool, string) {
	label := fmt.Sprintf("%s %s quota %s %g%%", c.Agent, c.Window, c.Op, c.Percent)
	if q == nil {
		return false, label + ": quota unknown"
	}
	p := q.Get(ctx, c.Agent)
	if !p.Available {
		why := p.Error
		if why == "" {
			why = "unavailable"
		}
		return false, fmt.Sprintf("%s: quota unknown (%s)", label, why)
	}
	var w quota.Window
	var ok bool
	switch {
	case c.Window == "any":
		w, ok = p.MaxUsed()
	case strings.HasPrefix(c.Window, "model:"):
		w, ok = p.Window(quota.KindModel, strings.TrimPrefix(c.Window, "model:"))
	default:
		w, ok = p.Window(c.Window, "")
	}
	if !ok {
		return false, fmt.Sprintf("%s: %s reports no %s window", label, c.Agent, c.Window)
	}
	holds := w.UsedPercent < c.Percent
	if c.Op == "above" {
		holds = w.UsedPercent > c.Percent
	}
	verdict := "does not hold"
	if holds {
		verdict = "holds"
	}
	return holds, fmt.Sprintf("%s %s (used %.0f%%)", label, verdict, w.UsedPercent)
}
