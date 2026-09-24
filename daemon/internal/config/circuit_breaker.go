package config

// CircuitBreakerConfig caps the number of reviews per PR and per repo to
// prevent cost-runaway loops. The defaults are conservative — users with
// high-volume workflows must explicitly raise them. See
// theburrowhub/heimdallm#243 for the incident that prompted these caps.
//
// Field semantics: applyDefaults() treats 0 as "unset" and substitutes
// the documented default. There is currently no way to express
// "unlimited" through TOML — set the cap high (e.g. 99999) if you need
// near-unbounded behaviour.
type CircuitBreakerConfig struct {
	// PerPR24h caps PR reviews on the same PR HEAD SHA over any 24-hour
	// window. A new commit gets its own allowance; an unresolved HEAD SHA
	// falls back to the whole-PR cap. Default 3; set to 0 to apply the
	// default.
	PerPR24h int `toml:"per_pr_24h"`
	// PerRepoHr caps PR reviews on the same repo over any 1-hour window.
	// Default 20; set to 0 to apply the default.
	PerRepoHr int `toml:"per_repo_hr"`
	// PerReviewFailureRepoHr caps failed or still-running PR-review executions
	// across a repo over any 1-hour window. It controls automatic retry spend
	// independently from completed-review circuit breakers and never emits a
	// breaker trip. Default 20; set to 0 to apply the default.
	PerReviewFailureRepoHr int `toml:"per_review_failure_repo_hr"`
}

// DefaultCircuitBreakerConfig returns the safe defaults applied when the
// [circuit_breaker] TOML section is missing or zero-valued.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		PerPR24h:               3,
		PerRepoHr:              20,
		PerReviewFailureRepoHr: 20,
	}
}
