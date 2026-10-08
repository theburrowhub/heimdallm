package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ReviewLimitsConfig caps how many AI reviews may start in a rolling minute,
// hour and day. Unlike the circuit breaker, which exists to stop runaway
// loops on one PR or repo, these are a spend budget the operator sets on
// purpose: a PR that hits one is deferred (not failed) and picked up again
// once the window has room.
//
// Zero means "no limit" for that window, so an absent section keeps the
// pre-limit behaviour.
type ReviewLimitsConfig struct {
	PerMinute int `toml:"per_minute" json:"per_minute"`
	PerHour   int `toml:"per_hour" json:"per_hour"`
	PerDay    int `toml:"per_day" json:"per_day"`
}

// MaxReviewLimit bounds every window so a typo cannot turn into an integer the
// UI or the counters mishandle; nobody needs more than this many reviews a day.
const MaxReviewLimit = 100000

// Any reports whether at least one window is limited.
func (l ReviewLimitsConfig) Any() bool {
	return l.PerMinute > 0 || l.PerHour > 0 || l.PerDay > 0
}

func (l ReviewLimitsConfig) validate(path string) error {
	for _, w := range []struct {
		name  string
		value int
	}{{"per_minute", l.PerMinute}, {"per_hour", l.PerHour}, {"per_day", l.PerDay}} {
		if w.value < 0 || w.value > MaxReviewLimit {
			return fmt.Errorf("config: %s.%s must be between 0 and %d, got %d", path, w.name, MaxReviewLimit, w.value)
		}
	}
	return nil
}

// ReviewLimitScope is one budget a review has to fit in. Scopes stack: a
// review in org/repo counts against the global budget, the org's and the
// repo's at the same time, and must fit in all of them.
type ReviewLimitScope struct {
	// Kind is "global", "org", "repo" or "agent".
	Kind string
	// Key is empty for global, the org slug, the "owner/name" slug, or the
	// agent id.
	Key    string
	Limits ReviewLimitsConfig
}

// ReviewLimitScopesForRepo returns every non-empty review budget that applies
// to a review of repo, independent of which agent runs it. Each scope counts
// only the reviews inside it (an org limit counts that org's reviews, not the
// whole estate's).
func (c *Config) ReviewLimitScopesForRepo(repo string) []ReviewLimitScope {
	var out []ReviewLimitScope
	if c.ReviewLimits.Any() {
		out = append(out, ReviewLimitScope{Kind: "global", Limits: c.ReviewLimits})
	}
	if org, _, ok := strings.Cut(repo, "/"); ok {
		if o, found := c.AI.Orgs[org]; found && o.ReviewLimits != nil && o.ReviewLimits.Any() {
			out = append(out, ReviewLimitScope{Kind: "org", Key: org, Limits: *o.ReviewLimits})
		}
	}
	if r, found := c.AI.Repos[repo]; found && r.ReviewLimits != nil && r.ReviewLimits.Any() {
		out = append(out, ReviewLimitScope{Kind: "repo", Key: repo, Limits: *r.ReviewLimits})
	}
	return out
}

// AgentReviewLimits returns the per-agent budget for cli, or nil when the
// agent is unlimited.
func (c *Config) AgentReviewLimits(cli string) *ReviewLimitScope {
	a, ok := c.AI.Agents[cli]
	if !ok || a.ReviewLimits == nil || !a.ReviewLimits.Any() {
		return nil
	}
	return &ReviewLimitScope{Kind: "agent", Key: cli, Limits: *a.ReviewLimits}
}

// AllReviewLimitScopes lists every configured budget (global, each org, each
// repo, each agent) for the status endpoint, in a stable order.
func (c *Config) AllReviewLimitScopes() []ReviewLimitScope {
	var out []ReviewLimitScope
	if c.ReviewLimits.Any() {
		out = append(out, ReviewLimitScope{Kind: "global", Limits: c.ReviewLimits})
	}
	for _, org := range slices.Sorted(maps.Keys(c.AI.Orgs)) {
		if o := c.AI.Orgs[org]; o.ReviewLimits != nil && o.ReviewLimits.Any() {
			out = append(out, ReviewLimitScope{Kind: "org", Key: org, Limits: *o.ReviewLimits})
		}
	}
	for _, repo := range slices.Sorted(maps.Keys(c.AI.Repos)) {
		if r := c.AI.Repos[repo]; r.ReviewLimits != nil && r.ReviewLimits.Any() {
			out = append(out, ReviewLimitScope{Kind: "repo", Key: repo, Limits: *r.ReviewLimits})
		}
	}
	for _, cli := range slices.Sorted(maps.Keys(c.AI.Agents)) {
		if s := c.AgentReviewLimits(cli); s != nil {
			out = append(out, *s)
		}
	}
	return out
}

func (c *Config) validateReviewLimits() error {
	if err := c.ReviewLimits.validate("review_limits"); err != nil {
		return err
	}
	for org, o := range c.AI.Orgs {
		if o.ReviewLimits != nil {
			if err := o.ReviewLimits.validate("ai.orgs." + org + ".review_limits"); err != nil {
				return err
			}
		}
	}
	for repo, r := range c.AI.Repos {
		if r.ReviewLimits != nil {
			if err := r.ReviewLimits.validate("ai.repos." + repo + ".review_limits"); err != nil {
				return err
			}
		}
	}
	for cli, a := range c.AI.Agents {
		if a.ReviewLimits != nil {
			if err := a.ReviewLimits.validate("ai.agents." + cli + ".review_limits"); err != nil {
				return err
			}
		}
	}
	return nil
}
