package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Defaults for [my_prs].
const (
	// DefaultMyPRsStaleAfter is how long an open PR can go without any activity
	// on GitHub before it is reported as stale.
	DefaultMyPRsStaleAfter = "3d"
	// DefaultMyPRsDigestTime is the local time of day of the daily digest.
	DefaultMyPRsDigestTime = "10:00"
)

// MyPRsConfig configures the watch over the operator's own PRs (authored or
// assigned) in the monitored repositories.
//
// Watching is observation only: it evaluates the PR and reports what it is
// waiting on, and never writes to GitHub. The write-side automation stays in
// [merge_tracking], which a repo with merge tracking enabled keeps using as is.
//
// Enabled, IncludeAssigned and DigestEnabled are pointers because their default
// is true and a plain bool cannot tell "unset" from "explicitly false".
type MyPRsConfig struct {
	Enabled         *bool `toml:"enabled,omitempty"`          // default true
	IncludeAssigned *bool `toml:"include_assigned,omitempty"` // default true
	// StaleAfter is the inactivity threshold ("90m", "12h", "3d"). "0" turns
	// stale detection off; empty means DefaultMyPRsStaleAfter.
	StaleAfter        string `toml:"stale_after"`
	NotifyTransitions bool   `toml:"notify_transitions"`       // desktop notification when a PR needs you
	DigestEnabled     *bool  `toml:"digest_enabled,omitempty"` // default true
	DigestTime        string `toml:"digest_time"`              // local HH:MM
}

// MyPRsEnabled reports whether the watch is on. Defaults to true.
func (c *Config) MyPRsEnabled() bool { return boolOrTrue(c.MyPRs.Enabled) }

// MyPRsIncludeAssigned reports whether assigned-only PRs are watched too.
// Defaults to true.
func (c *Config) MyPRsIncludeAssigned() bool { return boolOrTrue(c.MyPRs.IncludeAssigned) }

// MyPRsDigestEnabled reports whether the daily digest is on. Defaults to true.
func (c *Config) MyPRsDigestEnabled() bool { return boolOrTrue(c.MyPRs.DigestEnabled) }

// MyPRsStaleAfter returns the parsed inactivity threshold. Zero means stale
// detection is off. An unparseable value (rejected by Validate, so only
// reachable on an unvalidated Config) also disables it rather than guessing.
func (c *Config) MyPRsStaleAfter() time.Duration {
	raw := strings.TrimSpace(c.MyPRs.StaleAfter)
	if raw == "" {
		raw = DefaultMyPRsStaleAfter
	}
	d, err := ParseHumanDuration(raw)
	if err != nil || d < 0 {
		return 0
	}
	return d
}

func boolOrTrue(b *bool) bool { return b == nil || *b }

// EffectiveMergeTrackingForRepo is the config the merge-tracking reconciler
// runs with for repo.
//
// A repo with merge tracking enabled gets its own config unchanged. Otherwise,
// with [my_prs] on, it gets a watch-only copy: enabled so the PR is evaluated
// and shown, but with every write switch forced off and WatchOnly set so the
// evaluator skips the rules that only matter to automation (write permission,
// cross-fork heads, the allowed merge method).
func (c *Config) EffectiveMergeTrackingForRepo(repo string) MergeTrackingConfig {
	mt := c.MergeTrackingForRepo(repo)
	if mt.Enabled || !c.MyPRsEnabled() {
		return mt
	}
	return c.watchOnly(mt)
}

// EffectiveMergeTrackingGlobal is the global counterpart of
// EffectiveMergeTrackingForRepo, used for the settings that bound the poller
// as a whole and for the global include_assigned fallback.
func (c *Config) EffectiveMergeTrackingGlobal() MergeTrackingConfig {
	mt := c.MergeTracking
	if mt.Enabled || !c.MyPRsEnabled() {
		return mt
	}
	return c.watchOnly(mt)
}

func (c *Config) watchOnly(mt MergeTrackingConfig) MergeTrackingConfig {
	mt.Enabled = true
	mt.WatchOnly = true
	mt.EnableAutoMerge = false
	mt.UpdateBranch = false
	mt.ResolveConflicts = false
	mt.Merge = false
	mt.IncludeAssigned = c.MyPRsIncludeAssigned()
	return mt
}

// humanDurationDays matches a whole or fractional number of days, the one unit
// operators reach for that time.ParseDuration does not accept.
var humanDurationDays = regexp.MustCompile(`^(\d+(?:\.\d+)?)d$`)

// ParseHumanDuration parses a Go duration ("90m", "1h30m") or a number of
// days ("3d", "1.5d"). Days are 24h; a threshold measured in days does not
// care about DST.
func ParseHumanDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "0" {
		return 0, nil
	}
	if m := humanDurationDays.FindStringSubmatch(raw); m != nil {
		days, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	return time.ParseDuration(raw)
}

// ParseDigestTime parses "HH:MM" (24h) into hour and minute.
func ParseDigestTime(raw string) (hour, minute int, err error) {
	raw = strings.TrimSpace(raw)
	t, err := time.Parse("15:04", raw)
	if err != nil {
		return 0, 0, fmt.Errorf("must be HH:MM (24h): %w", err)
	}
	return t.Hour(), t.Minute(), nil
}

func (c *Config) applyMyPRsDefaults() {
	if strings.TrimSpace(c.MyPRs.StaleAfter) == "" {
		c.MyPRs.StaleAfter = DefaultMyPRsStaleAfter
	}
	if strings.TrimSpace(c.MyPRs.DigestTime) == "" {
		c.MyPRs.DigestTime = DefaultMyPRsDigestTime
	}
}

func (c *Config) validateMyPRs() error {
	if raw := strings.TrimSpace(c.MyPRs.StaleAfter); raw != "" {
		d, err := ParseHumanDuration(raw)
		if err != nil {
			return fmt.Errorf("config: my_prs.stale_after %q is invalid (use e.g. 90m, 12h, 3d, or 0 to disable): %w", raw, err)
		}
		if d < 0 {
			return fmt.Errorf("config: my_prs.stale_after %q must not be negative", raw)
		}
	}
	if raw := strings.TrimSpace(c.MyPRs.DigestTime); raw != "" {
		if _, _, err := ParseDigestTime(raw); err != nil {
			return fmt.Errorf("config: my_prs.digest_time %q %w", raw, err)
		}
	}
	return nil
}
