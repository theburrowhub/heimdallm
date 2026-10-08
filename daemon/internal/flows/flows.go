// Package flows evaluates review flows: the ordered rules that decide which
// agent reviews a PR from the time of day and each agent's remaining quota.
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

// RuleResult explains one rule's outcome.
type RuleResult struct {
	Key       string   `json:"key"`
	Agent     string   `json:"agent"`
	Matched   bool     `json:"matched"`
	Available bool     `json:"available"`
	Reasons   []string `json:"reasons"`
}

// Decision is a flow evaluated at one moment: the agents to try, in order,
// and why each rule did or did not apply.
type Decision struct {
	FlowID     string       `json:"flow_id"`
	FlowName   string       `json:"flow_name"`
	At         time.Time    `json:"at"`
	Candidates []string     `json:"candidates"`
	Rules      []RuleResult `json:"rules"`
}

// Evaluate walks the flow's rules in order. A rule contributes its agent to
// the candidates when its conditions hold and the agent is available
// (installed or configured). The first candidate reviews; the rest are the
// fallbacks if it runs out of quota mid-review.
//
// Quota conditions fail closed: an agent whose quota cannot be read does not
// satisfy "below 50%" or "above 50%", so the flow moves on rather than guess.
func Evaluate(ctx context.Context, id string, f config.FlowConfig, now time.Time, q QuotaReader, available func(agent string) bool) Decision {
	d := Decision{FlowID: id, FlowName: f.Name, At: now, Candidates: []string{}}
	seen := map[string]bool{}
	for _, key := range f.OrderedRuleKeys() {
		rule := f.Rules[key]
		res := RuleResult{Key: key, Agent: rule.Agent}
		res.Matched, res.Reasons = matches(ctx, rule, now, q)
		res.Available = available == nil || available(rule.Agent)
		if res.Matched && !res.Available {
			res.Reasons = append(res.Reasons, rule.Agent+" is not installed or not configured")
		}
		if res.Matched && res.Available && !seen[rule.Agent] {
			seen[rule.Agent] = true
			d.Candidates = append(d.Candidates, rule.Agent)
		}
		d.Rules = append(d.Rules, res)
	}
	return d
}

func matches(ctx context.Context, r config.FlowRule, now time.Time, q QuotaReader) (bool, []string) {
	var outcomes []bool
	var reasons []string
	for _, key := range sortedKeys(r.Schedule) {
		ok, why := scheduleHolds(r.Schedule[key], now)
		outcomes, reasons = append(outcomes, ok), append(reasons, why)
	}
	for _, key := range sortedKeys(r.Quota) {
		ok, why := quotaHolds(ctx, r.Quota[key], q)
		outcomes, reasons = append(outcomes, ok), append(reasons, why)
	}
	if len(outcomes) == 0 {
		return true, []string{"no conditions (always applies)"}
	}
	if r.Match == "any" {
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

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Same numeric-first order as rules, so reasons read in a stable order.
	f := config.FlowConfig{Rules: map[string]config.FlowRule{}}
	for _, k := range keys {
		f.Rules[k] = config.FlowRule{}
	}
	return f.OrderedRuleKeys()
}

func minutesOf(hhmm string) int {
	h, _ := strconv.Atoi(hhmm[:2])
	m, _ := strconv.Atoi(hhmm[3:])
	return h*60 + m
}

// scheduleHolds checks a weekday + time-of-day window. A window that crosses
// midnight belongs to the day it starts on.
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
	switch {
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
	if len(s.Days) > 0 {
		desc = strings.Join(s.Days, ",") + " " + desc
	}
	if s.TZ != "" {
		desc += " " + s.TZ
	}
	now24 := t.Format("Mon 15:04")
	if inWindow && dayOK {
		return true, fmt.Sprintf("schedule %s holds (now %s)", desc, now24)
	}
	return false, fmt.Sprintf("schedule %s does not hold (now %s)", desc, now24)
}

func quotaHolds(ctx context.Context, c config.QuotaCondition, q QuotaReader) (bool, string) {
	label := fmt.Sprintf("%s %s quota %s %.0f%%", c.Agent, c.Window, c.Op, c.Percent)
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
