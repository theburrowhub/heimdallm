package main

import (
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/pipeline"
)

func pipelineWindowLimits(l config.ReviewLimitsConfig) pipeline.ReviewWindowLimits {
	return pipeline.ReviewWindowLimits{PerMinute: l.PerMinute, PerHour: l.PerHour, PerDay: l.PerDay}
}

func pipelineBudgetScopes(scopes []config.ReviewLimitScope) []pipeline.ReviewBudgetScope {
	out := make([]pipeline.ReviewBudgetScope, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, pipeline.ReviewBudgetScope{Kind: s.Kind, Key: s.Key, Limits: pipelineWindowLimits(s.Limits)})
	}
	return out
}

// reviewBudgetsFor resolves every review budget that applies to a review of
// repo: the global, org and repo scopes, plus each limited agent so the
// pipeline can fall back from an agent that is out of budget.
func reviewBudgetsFor(c *config.Config, repo string) pipeline.ReviewBudgets {
	b := pipeline.ReviewBudgets{Scopes: pipelineBudgetScopes(c.ReviewLimitScopesForRepo(repo))}
	for cli := range c.AI.Agents {
		if s := c.AgentReviewLimits(cli); s != nil {
			if b.Agents == nil {
				b.Agents = make(map[string]pipeline.ReviewWindowLimits)
			}
			b.Agents[cli] = pipelineWindowLimits(s.Limits)
		}
	}
	return b
}

// reviewLimitsMap projects a budget for GET /config. Always the full shape so
// the settings screen can render zeros ("no limit") instead of guessing.
func reviewLimitsMap(l config.ReviewLimitsConfig) map[string]any {
	return map[string]any{
		"per_minute": l.PerMinute,
		"per_hour":   l.PerHour,
		"per_day":    l.PerDay,
	}
}

// agentCatalogRefreshInterval is how often the installed-agent catalog is
// rescanned in the background.
const agentCatalogRefreshInterval = 10 * time.Minute
