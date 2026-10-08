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

// FlowConfig is a review flow: an ordered list of rules that decides which
// agent reviews a PR. Rules are keyed by an order string ("10", "20", …,
// compared numerically) because TOML cannot hold slices of tables in this
// schema; the first rule whose conditions hold and whose agent is available
// wins, and later matching rules are the fallbacks if that agent runs out of
// quota mid-review.
type FlowConfig struct {
	Name  string              `toml:"name" json:"name"`
	Rules map[string]FlowRule `toml:"rules" json:"rules"`
}

// FlowRule picks Agent when its conditions hold. No conditions means "always"
// (the catch-all at the end of a flow).
type FlowRule struct {
	Agent string `toml:"agent" json:"agent"`
	// Match is "all" (default: every condition holds) or "any".
	Match    string                       `toml:"match,omitempty" json:"match,omitempty"`
	Quota    map[string]QuotaCondition    `toml:"quota,omitempty" json:"quota,omitempty"`
	Schedule map[string]ScheduleCondition `toml:"schedule,omitempty" json:"schedule,omitempty"`
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
	maxRulesPerFlow    = 50
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

// OrderedRuleKeys returns a flow's rule keys in evaluation order: numeric
// keys ascending, then non-numeric keys alphabetically.
func (f FlowConfig) OrderedRuleKeys() []string {
	keys := make([]string, 0, len(f.Rules))
	for k := range f.Rules {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		ai, aerr := strconv.Atoi(a)
		bi, berr := strconv.Atoi(b)
		switch {
		case aerr == nil && berr == nil:
			return ai - bi
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		}
		return strings.Compare(a, b)
	})
	return keys
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
// selected flow that exists wins; otherwise the flow is synthesised from the
// legacy primary/fallback settings (repo > org > global), so configs written
// before flows keep their behaviour unchanged.
func (c *Config) FlowForRepo(repo string) (string, FlowConfig) {
	if name := c.FlowNameForRepo(repo); name != "" {
		if f, ok := c.AI.Flows[name]; ok && len(f.Rules) > 0 {
			return name, f
		}
	}
	return DefaultFlowID, c.legacyFlow(repo)
}

func (c *Config) legacyFlow(repo string) FlowConfig {
	ai := c.AIForRepo(repo)
	f := FlowConfig{Name: "Primary and fallback", Rules: map[string]FlowRule{}}
	if ai.Primary != "" {
		f.Rules["10"] = FlowRule{Agent: ai.Primary}
	}
	if ai.Fallback != "" && ai.Fallback != ai.Primary {
		f.Rules["20"] = FlowRule{Agent: ai.Fallback}
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

// ValidateFlow checks one flow's rules and conditions.
func ValidateFlow(id string, f FlowConfig) error {
	path := "ai.flows." + id
	if len(f.Name) > 120 {
		return fmt.Errorf("config: %s.name is longer than 120 characters", path)
	}
	if len(f.Rules) > maxRulesPerFlow {
		return fmt.Errorf("config: %s has %d rules, at most %d allowed", path, len(f.Rules), maxRulesPerFlow)
	}
	for key, r := range f.Rules {
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
		for ck, q := range r.Quota {
			qp := rp + ".quota." + ck
			if err := executor.ValidateCLIName(q.Agent); err != nil {
				return fmt.Errorf("config: %s.agent: %w", qp, err)
			}
			if !validQuotaWin[q.Window] && !(strings.HasPrefix(q.Window, modelWindowPfx) && len(q.Window) > len(modelWindowPfx) && len(q.Window) <= 128) {
				return fmt.Errorf("config: %s.window must be session, weekly, monthly, credit, any or model:<id>", qp)
			}
			if !validQuotaOps[q.Op] {
				return fmt.Errorf("config: %s.op must be below or above", qp)
			}
			if q.Percent < 0 || q.Percent > 100 {
				return fmt.Errorf("config: %s.percent must be between 0 and 100", qp)
			}
		}
		for ck, sc := range r.Schedule {
			sp := rp + ".schedule." + ck
			if !hhmmPattern.MatchString(sc.From) || !hhmmPattern.MatchString(sc.To) {
				return fmt.Errorf("config: %s: from/to must be HH:MM", sp)
			}
			if sc.From == sc.To {
				return fmt.Errorf("config: %s: from and to must differ", sp)
			}
			for _, d := range sc.Days {
				if _, ok := Weekday(d); !ok {
					return fmt.Errorf("config: %s.days: %q is not mon..sun", sp, d)
				}
			}
			if sc.TZ != "" {
				if _, err := time.LoadLocation(sc.TZ); err != nil {
					return fmt.Errorf("config: %s.tz: unknown time zone %q", sp, sc.TZ)
				}
			}
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
