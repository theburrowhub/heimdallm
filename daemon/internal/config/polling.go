package config

import "time"

// PollingConfig is the [polling] TOML section that controls polling cadence,
// rate-limit safety, and feature kill-switches for the GitHub API efficiency
// feature (C5). Fields are all optional; applyPollingDefaults fills safe values
// that reproduce the existing daemon behaviour when the section is absent.
//
// Adaptive scheduling (PollInterval → MinInterval / MaxInterval backoff) is
// reserved for a separate task; the fields exist here so the config schema is
// stable before the engine lands.
type PollingConfig struct {
	// PollInterval is the base Tier 2 (per-repo PR/issue) poll cadence.
	// Empty string inherits from [github].poll_interval. Accepts any
	// Go time.ParseDuration value (e.g. "5m", "1m30s").
	PollInterval string `toml:"poll_interval"`

	// MinInterval is the adaptive lower bound. The adaptive engine (separate
	// task) will never schedule a tick faster than this. Default "1m".
	MinInterval string `toml:"min_interval"`

	// MaxInterval is the adaptive upper bound. The adaptive engine will never
	// back off slower than this. Default "15m".
	MaxInterval string `toml:"max_interval"`

	// Adaptive enables the adaptive back-off engine (separate task). Default
	// false — opt-in only. When false, MinInterval/MaxInterval are ignored.
	Adaptive bool `toml:"adaptive"`

	// DiscoveryInterval controls Tier 1 (topic-based repo discovery) cadence.
	// Default "5m" — matches the current hardcoded value in startPollers.
	DiscoveryInterval string `toml:"discovery_interval"`

	// Tier3Interval controls the state-check (watch) scanner tick. Default
	// "30s" — matches the current hardcoded value in the state-poller goroutine.
	Tier3Interval string `toml:"tier3_interval"`

	// RateLimitSafetyThreshold is the minimum X-RateLimit-Remaining for the
	// "core" resource below which the TierDiscovery tier starts throttling.
	// The scheduler already uses per-tier offsets (TierRepo = base-25,
	// TierWatch = base-75). Default 100, matching the current hardcoded value
	// of tierSafetyThreshold[TierDiscovery].
	RateLimitSafetyThreshold int `toml:"rate_limit_safety_threshold"`

	// UseETag is a kill-switch for the conditional ETag cache (C1). Defaults
	// to true (cache enabled). Set to false to disable If-None-Match and force
	// full 200 responses on every GET — useful when debugging or when the
	// upstream server doesn't honour ETags correctly.
	UseETag *bool `toml:"use_etag,omitempty"`

	// UseGraphQL is reserved for Phase 3 (GraphQL-based polling). Defaults to
	// false. Set to true once the GraphQL engine is ready to enable it.
	UseGraphQL *bool `toml:"use_graphql,omitempty"`
}

// Default values for [polling] — centralised so applyPollingDefaults and the
// Resolved* helpers can't drift.
const (
	DefaultPollingMinInterval             = "1m"
	DefaultPollingMaxInterval             = "15m"
	DefaultPollingDiscoveryInterval       = "5m"
	DefaultPollingTier3Interval           = "30s"
	DefaultPollingRateLimitSafetyThreshold = 100
)

// applyPollingDefaults fills zero-value scalars with safe defaults. Called from
// applyDefaults so the section is always fully populated after Load/LoadOrCreate.
func (c *Config) applyPollingDefaults() {
	p := &c.Polling
	if p.MinInterval == "" {
		p.MinInterval = DefaultPollingMinInterval
	}
	if p.MaxInterval == "" {
		p.MaxInterval = DefaultPollingMaxInterval
	}
	if p.DiscoveryInterval == "" {
		p.DiscoveryInterval = DefaultPollingDiscoveryInterval
	}
	if p.Tier3Interval == "" {
		p.Tier3Interval = DefaultPollingTier3Interval
	}
	if p.RateLimitSafetyThreshold == 0 {
		p.RateLimitSafetyThreshold = DefaultPollingRateLimitSafetyThreshold
	}
	if p.UseETag == nil {
		v := true
		p.UseETag = &v
	}
	if p.UseGraphQL == nil {
		v := false
		p.UseGraphQL = &v
	}
	// PollInterval: left as-is (empty = inherit from [github].poll_interval).
	// Adaptive: left as-is (false = opt-in).
}

// parseDurationWithFallback parses s as a Go duration. Returns fallback when s
// is empty or invalid, or when the parsed value is non-positive.
func parseDurationWithFallback(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// ResolvedPollInterval returns the effective Tier 2 base poll interval.
// Resolution order:
//  1. [polling].poll_interval when set and valid.
//  2. [github].poll_interval (may also be empty or invalid — falls back to 5m).
func (c *Config) ResolvedPollInterval() time.Duration {
	if c.Polling.PollInterval != "" {
		if d := parseDurationWithFallback(c.Polling.PollInterval, 0); d > 0 {
			return d
		}
	}
	// Fall back to [github].poll_interval → 5 min default.
	return parseDurationWithFallback(c.GitHub.PollInterval, 5*time.Minute)
}

// ResolvedMinInterval returns the adaptive lower bound for the poll interval.
func (c *Config) ResolvedMinInterval() time.Duration {
	return parseDurationWithFallback(c.Polling.MinInterval, time.Minute)
}

// ResolvedMaxInterval returns the adaptive upper bound for the poll interval.
func (c *Config) ResolvedMaxInterval() time.Duration {
	return parseDurationWithFallback(c.Polling.MaxInterval, 15*time.Minute)
}

// ResolvedDiscoveryInterval returns the Tier 1 discovery cadence.
func (c *Config) ResolvedDiscoveryInterval() time.Duration {
	return parseDurationWithFallback(c.Polling.DiscoveryInterval, 5*time.Minute)
}

// ResolvedTier3Interval returns the Tier 3 state-check scanner tick.
func (c *Config) ResolvedTier3Interval() time.Duration {
	return parseDurationWithFallback(c.Polling.Tier3Interval, 30*time.Second)
}

// ETagEnabled reports whether the conditional ETag cache is active.
// Returns true unless UseETag is explicitly set to false.
func (c *Config) ETagEnabled() bool {
	if c.Polling.UseETag == nil {
		return true
	}
	return *c.Polling.UseETag
}

// GraphQLEnabled reports whether GraphQL-based polling is active.
// Returns false unless UseGraphQL is explicitly set to true.
func (c *Config) GraphQLEnabled() bool {
	if c.Polling.UseGraphQL == nil {
		return false
	}
	return *c.Polling.UseGraphQL
}
