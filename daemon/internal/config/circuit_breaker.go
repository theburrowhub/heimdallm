package config

// CircuitBreakerConfig caps the number of reviews per PR and per repo to
// prevent cost-runaway loops. The defaults are conservative — users with
// high-volume workflows must explicitly raise them. See
// theburrowhub/heimdallm#243 for the incident that prompted these caps.
type CircuitBreakerConfig struct {
	// PerPR24h caps reviews on the same PR over any 24-hour window.
	// 0 = unlimited. Default 3.
	PerPR24h int `toml:"per_pr_24h"`
	// PerRepoHr caps reviews on the same repo over any 1-hour window.
	// 0 = unlimited. Default 20.
	PerRepoHr int `toml:"per_repo_hr"`
}

// DefaultCircuitBreakerConfig returns the safe defaults applied when the
// [circuit_breaker] TOML section is missing or zero-valued.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		PerPR24h:  3,
		PerRepoHr: 20,
	}
}
