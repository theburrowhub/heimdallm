package config

// CircuitBreakerConfig caps the number of reviews per PR and per repo to
// prevent cost-runaway loops. The defaults are conservative — users with
// high-volume workflows must explicitly raise them. See
// theburrowhub/heimdallm#243 for the incident that prompted these caps,
// and theburrowhub/heimdallm#292 for the issue-side extension.
//
// Field semantics: applyDefaults() treats 0 as "unset" and substitutes
// the documented default. There is currently no way to express
// "unlimited" through TOML — set the cap high (e.g. 99999) if you need
// near-unbounded behaviour. The PR-side and issue-side per-repo axes
// are independent: a busy repo running both PR review and issue triage
// can consume PerRepoHr PR reviews AND PerIssueRepoHr triages per hour.
type CircuitBreakerConfig struct {
	// PerPR24h caps PR reviews on the same PR over any 24-hour window.
	// Default 3; set to 0 to apply the default.
	PerPR24h int `toml:"per_pr_24h"`
	// PerRepoHr caps PR reviews on the same repo over any 1-hour window.
	// Default 20; set to 0 to apply the default.
	PerRepoHr int `toml:"per_repo_hr"`
	// PerIssue24h caps issue triages on the same issue over any 24-hour
	// window. Default 3 (same as the PR cap); set to 0 to apply the default.
	PerIssue24h int `toml:"per_issue_24h"`
	// PerIssueRepoHr caps issue triages on the same repo over any 1-hour
	// window. Default 10 — tighter than the PR cap because each triage is
	// a full-context Claude run; set to 0 to apply the default.
	PerIssueRepoHr int `toml:"per_issue_repo_hr"`
}

// DefaultCircuitBreakerConfig returns the safe defaults applied when the
// [circuit_breaker] TOML section is missing or zero-valued.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		PerPR24h:       3,
		PerRepoHr:      20,
		PerIssue24h:    3,
		PerIssueRepoHr: 10,
	}
}
