package config

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

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
	//
	// 0 means "unset" and is replaced by the default below — there is no way to
	// express "disable proactive throttling" through this field, deliberately:
	// the whole point of the section is to stop exhausting the quota.
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
	DefaultPollingMinInterval              = "1m"
	DefaultPollingMaxInterval              = "15m"
	DefaultPollingDiscoveryInterval        = "5m"
	DefaultPollingTier3Interval            = "30s"
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
		// Reaching here means a non-empty value was discarded. Validate()
		// rejects these at load, so this is the defence-in-depth path (rows
		// written before validation existed, or a caller that skipped it);
		// warn rather than silently running at a cadence nobody asked for.
		//
		// Logged once per distinct value, not per call: the Resolved* helpers
		// run hot — the tier3 poller calls ResolvedTier3Interval on every tick
		// — so an unconditional warn would repeat forever every few seconds.
		warnInvalidDurationOnce(s)
		return fallback
	}
	return d
}

// invalidDurationWarned records which discarded values have already been
// logged, so a hot-path caller cannot turn one bad config row into an endless
// log stream.
var invalidDurationWarned sync.Map

func warnInvalidDurationOnce(value string) {
	if _, loaded := invalidDurationWarned.LoadOrStore(value, struct{}{}); loaded {
		return
	}
	// The effective value is resolved by the caller's own fallback chain (for
	// poll_interval it continues on to [github].poll_interval), so the message
	// deliberately does not claim to know it.
	slog.Warn("config: ignoring invalid [polling] duration, falling back", "value", value)
}

// pollingDurationBounds are the accepted ranges for each [polling] duration.
// They differ per field: tier3_interval is a cheap local scan that legitimately
// runs on a sub-minute tick, while poll_interval drives per-repo GitHub traffic
// and shares the daemon-wide 1m floor that exists to protect the API quota.
var pollingDurationBounds = []struct {
	name     string
	value    func(PollingConfig) string
	min, max time.Duration
}{
	{"poll_interval", func(p PollingConfig) string { return p.PollInterval }, minPollInterval, maxPollInterval},
	{"min_interval", func(p PollingConfig) string { return p.MinInterval }, minPollInterval, maxPollInterval},
	{"max_interval", func(p PollingConfig) string { return p.MaxInterval }, minPollInterval, maxPollInterval},
	{"discovery_interval", func(p PollingConfig) string { return p.DiscoveryInterval }, minPollInterval, maxPollInterval},
	{"tier3_interval", func(p PollingConfig) string { return p.Tier3Interval }, time.Second, time.Hour},
}

// ValidatePolling checks every duration in the [polling] section and the
// safety threshold.
//
// Without this, [polling].poll_interval bypassed the quota guard entirely:
// ResolvedPollInterval gives it precedence over [github].poll_interval, which
// IS validated, so `[polling] poll_interval = "1s"` was accepted and the new
// section silently disabled the protection the old one enforced.
func (c *Config) ValidatePolling() error {
	for _, b := range pollingDurationBounds {
		raw := b.value(c.Polling)
		if raw == "" {
			continue
		}
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("invalid polling.%s %q: %w", b.name, raw, err)
		}
		if d < b.min || d > b.max {
			return fmt.Errorf("polling.%s %q out of range (must be between %s and %s)",
				b.name, raw, b.min, b.max)
		}
	}
	if c.Polling.RateLimitSafetyThreshold < 0 {
		return fmt.Errorf("polling.rate_limit_safety_threshold %d must not be negative",
			c.Polling.RateLimitSafetyThreshold)
	}
	if min, max := c.ResolvedMinInterval(), c.ResolvedMaxInterval(); min > max {
		return fmt.Errorf("polling.min_interval (%s) must not exceed polling.max_interval (%s)", min, max)
	}
	return nil
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
