package config

import (
	"testing"
	"time"
)

// boolPtr returns a pointer to b, used to set *bool fields in tests without a
// named variable.
func boolPtr(b bool) *bool { return &b }

// TestApplyPollingDefaults_FillsDocumentedDefaults verifies that calling
// applyPollingDefaults on a zero-value PollingConfig produces exactly the
// documented defaults.
func TestApplyPollingDefaults_FillsDocumentedDefaults(t *testing.T) {
	var c Config
	c.applyPollingDefaults()

	p := c.Polling
	if p.MinInterval != DefaultPollingMinInterval {
		t.Errorf("MinInterval: got %q, want %q", p.MinInterval, DefaultPollingMinInterval)
	}
	if p.MaxInterval != DefaultPollingMaxInterval {
		t.Errorf("MaxInterval: got %q, want %q", p.MaxInterval, DefaultPollingMaxInterval)
	}
	if p.DiscoveryInterval != DefaultPollingDiscoveryInterval {
		t.Errorf("DiscoveryInterval: got %q, want %q", p.DiscoveryInterval, DefaultPollingDiscoveryInterval)
	}
	if p.Tier3Interval != DefaultPollingTier3Interval {
		t.Errorf("Tier3Interval: got %q, want %q", p.Tier3Interval, DefaultPollingTier3Interval)
	}
	if p.RateLimitSafetyThreshold != DefaultPollingRateLimitSafetyThreshold {
		t.Errorf("RateLimitSafetyThreshold: got %d, want %d",
			p.RateLimitSafetyThreshold, DefaultPollingRateLimitSafetyThreshold)
	}
	if p.UseETag == nil {
		t.Fatal("UseETag: got nil, want non-nil")
	}
	if !*p.UseETag {
		t.Errorf("UseETag: got false, want true (default enabled)")
	}
	if p.UseGraphQL == nil {
		t.Fatal("UseGraphQL: got nil, want non-nil")
	}
	if *p.UseGraphQL {
		t.Errorf("UseGraphQL: got true, want false (default disabled)")
	}
	if p.PollInterval != "" {
		t.Errorf("PollInterval: got %q, want empty string (inherits from [github])", p.PollInterval)
	}
	if p.Adaptive {
		t.Errorf("Adaptive: got true, want false (opt-in)")
	}
}

// TestApplyPollingDefaults_DoesNotOverwriteExplicitValues verifies that
// explicitly-set values survive the defaults pass untouched.
func TestApplyPollingDefaults_DoesNotOverwriteExplicitValues(t *testing.T) {
	useETag := false
	useGraphQL := true
	c := Config{
		Polling: PollingConfig{
			PollInterval:             "2m",
			MinInterval:              "30s",
			MaxInterval:              "10m",
			Adaptive:                 true,
			DiscoveryInterval:        "3m",
			Tier3Interval:            "1m",
			RateLimitSafetyThreshold: 200,
			UseETag:                  &useETag,
			UseGraphQL:               &useGraphQL,
		},
	}
	c.applyPollingDefaults()

	p := c.Polling
	if p.PollInterval != "2m" {
		t.Errorf("PollInterval overwritten: got %q", p.PollInterval)
	}
	if p.MinInterval != "30s" {
		t.Errorf("MinInterval overwritten: got %q", p.MinInterval)
	}
	if p.MaxInterval != "10m" {
		t.Errorf("MaxInterval overwritten: got %q", p.MaxInterval)
	}
	if !p.Adaptive {
		t.Errorf("Adaptive overwritten")
	}
	if p.DiscoveryInterval != "3m" {
		t.Errorf("DiscoveryInterval overwritten: got %q", p.DiscoveryInterval)
	}
	if p.Tier3Interval != "1m" {
		t.Errorf("Tier3Interval overwritten: got %q", p.Tier3Interval)
	}
	if p.RateLimitSafetyThreshold != 200 {
		t.Errorf("RateLimitSafetyThreshold overwritten: got %d", p.RateLimitSafetyThreshold)
	}
	if p.UseETag == nil || *p.UseETag {
		t.Errorf("UseETag overwritten: got %v", p.UseETag)
	}
	if p.UseGraphQL == nil || !*p.UseGraphQL {
		t.Errorf("UseGraphQL overwritten: got %v", p.UseGraphQL)
	}
}

// TestResolvedPollInterval covers all resolution branches.
func TestResolvedPollInterval(t *testing.T) {
	tests := []struct {
		name           string
		pollingPoll    string
		githubPoll     string
		wantDuration   time.Duration
	}{
		{
			name:         "polling.poll_interval takes precedence over github.poll_interval",
			pollingPoll:  "2m",
			githubPoll:   "5m",
			wantDuration: 2 * time.Minute,
		},
		{
			name:         "falls back to github.poll_interval when polling empty",
			pollingPoll:  "",
			githubPoll:   "1m",
			wantDuration: time.Minute,
		},
		{
			name:         "falls back to 5m when both empty",
			pollingPoll:  "",
			githubPoll:   "",
			wantDuration: 5 * time.Minute,
		},
		{
			name:         "invalid polling string falls back to github.poll_interval",
			pollingPoll:  "not-a-duration",
			githubPoll:   "10m",
			wantDuration: 10 * time.Minute,
		},
		{
			name:         "invalid polling AND invalid github falls back to 5m",
			pollingPoll:  "bad",
			githubPoll:   "also-bad",
			wantDuration: 5 * time.Minute,
		},
		{
			name:         "non-positive polling value falls back",
			pollingPoll:  "-1m",
			githubPoll:   "3m",
			wantDuration: 3 * time.Minute,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := Config{
				GitHub:  GitHubConfig{PollInterval: tc.githubPoll},
				Polling: PollingConfig{PollInterval: tc.pollingPoll},
			}
			got := c.ResolvedPollInterval()
			if got != tc.wantDuration {
				t.Errorf("ResolvedPollInterval() = %v, want %v", got, tc.wantDuration)
			}
		})
	}
}

// TestResolvedMinMaxIntervals verifies parse-with-fallback behaviour.
func TestResolvedMinMaxIntervals(t *testing.T) {
	t.Run("valid values parse correctly", func(t *testing.T) {
		c := Config{Polling: PollingConfig{MinInterval: "30s", MaxInterval: "20m"}}
		if got := c.ResolvedMinInterval(); got != 30*time.Second {
			t.Errorf("ResolvedMinInterval() = %v, want 30s", got)
		}
		if got := c.ResolvedMaxInterval(); got != 20*time.Minute {
			t.Errorf("ResolvedMaxInterval() = %v, want 20m", got)
		}
	})
	t.Run("empty falls back to defaults", func(t *testing.T) {
		var c Config
		if got := c.ResolvedMinInterval(); got != time.Minute {
			t.Errorf("ResolvedMinInterval() empty = %v, want 1m", got)
		}
		if got := c.ResolvedMaxInterval(); got != 15*time.Minute {
			t.Errorf("ResolvedMaxInterval() empty = %v, want 15m", got)
		}
	})
	t.Run("invalid falls back to defaults", func(t *testing.T) {
		c := Config{Polling: PollingConfig{MinInterval: "xyz", MaxInterval: "abc"}}
		if got := c.ResolvedMinInterval(); got != time.Minute {
			t.Errorf("ResolvedMinInterval() invalid = %v, want 1m", got)
		}
		if got := c.ResolvedMaxInterval(); got != 15*time.Minute {
			t.Errorf("ResolvedMaxInterval() invalid = %v, want 15m", got)
		}
	})
}

// TestResolvedDiscoveryInterval confirms the discovery interval fallback and
// that the default matches the current hardcoded value in startPollers (5m).
func TestResolvedDiscoveryInterval(t *testing.T) {
	t.Run("explicit value", func(t *testing.T) {
		c := Config{Polling: PollingConfig{DiscoveryInterval: "3m"}}
		if got := c.ResolvedDiscoveryInterval(); got != 3*time.Minute {
			t.Errorf("got %v, want 3m", got)
		}
	})
	t.Run("empty falls back to 5m (matches current hardcoded default)", func(t *testing.T) {
		var c Config
		want := 5 * time.Minute
		if got := c.ResolvedDiscoveryInterval(); got != want {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("invalid falls back to 5m", func(t *testing.T) {
		c := Config{Polling: PollingConfig{DiscoveryInterval: "garbage"}}
		if got := c.ResolvedDiscoveryInterval(); got != 5*time.Minute {
			t.Errorf("got %v, want 5m", got)
		}
	})
}

// TestResolvedTier3Interval confirms the Tier 3 tick fallback and that the
// default matches the current hardcoded value in the state-poller (30s).
func TestResolvedTier3Interval(t *testing.T) {
	t.Run("explicit value", func(t *testing.T) {
		c := Config{Polling: PollingConfig{Tier3Interval: "1m"}}
		if got := c.ResolvedTier3Interval(); got != time.Minute {
			t.Errorf("got %v, want 1m", got)
		}
	})
	t.Run("empty falls back to 30s (matches current hardcoded default)", func(t *testing.T) {
		var c Config
		want := 30 * time.Second
		if got := c.ResolvedTier3Interval(); got != want {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("invalid falls back to 30s", func(t *testing.T) {
		c := Config{Polling: PollingConfig{Tier3Interval: "not-valid"}}
		if got := c.ResolvedTier3Interval(); got != 30*time.Second {
			t.Errorf("got %v, want 30s", got)
		}
	})
}

// TestETagEnabled verifies kill-switch semantics (default true).
func TestETagEnabled(t *testing.T) {
	tests := []struct {
		name    string
		useETag *bool
		want    bool
	}{
		{"nil (default) → true", nil, true},
		{"explicit true → true", boolPtr(true), true},
		{"explicit false → false (kill-switch)", boolPtr(false), false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := Config{Polling: PollingConfig{UseETag: tc.useETag}}
			if got := c.ETagEnabled(); got != tc.want {
				t.Errorf("ETagEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGraphQLEnabled verifies reserved-feature semantics (default false).
func TestGraphQLEnabled(t *testing.T) {
	tests := []struct {
		name       string
		useGraphQL *bool
		want       bool
	}{
		{"nil (default) → false", nil, false},
		{"explicit false → false", boolPtr(false), false},
		{"explicit true → true", boolPtr(true), true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := Config{Polling: PollingConfig{UseGraphQL: tc.useGraphQL}}
			if got := c.GraphQLEnabled(); got != tc.want {
				t.Errorf("GraphQLEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPollingDefaultsMatchCurrentBehaviour is a regression test that confirms
// the defaults reproduce the behaviour existing BEFORE this config section was
// introduced. If this test breaks, we've changed a default that could affect
// operators running with no [polling] section.
func TestPollingDefaultsMatchCurrentBehaviour(t *testing.T) {
	var c Config
	c.applyPollingDefaults()

	// Discovery: previously parseDiscoveryInterval("", "5m") = 5m.
	if got := c.ResolvedDiscoveryInterval(); got != 5*time.Minute {
		t.Errorf("DiscoveryInterval default changed from 5m to %v — behaviour regression", got)
	}

	// Tier 3 scan: previously time.NewTicker(30 * time.Second).
	if got := c.ResolvedTier3Interval(); got != 30*time.Second {
		t.Errorf("Tier3Interval default changed from 30s to %v — behaviour regression", got)
	}

	// Rate-limit safety: previously tierSafetyThreshold[TierDiscovery] = 100.
	if c.Polling.RateLimitSafetyThreshold != 100 {
		t.Errorf("RateLimitSafetyThreshold default changed from 100 to %d — behaviour regression",
			c.Polling.RateLimitSafetyThreshold)
	}

	// ETag: previously always enabled.
	if !c.ETagEnabled() {
		t.Errorf("ETag default changed to disabled — behaviour regression")
	}

	// GraphQL: previously not implemented, effectively false.
	if c.GraphQLEnabled() {
		t.Errorf("GraphQL default changed to enabled — behaviour regression")
	}
}

// TestApplyDefaultsCallsPollingDefaults verifies the top-level applyDefaults
// calls applyPollingDefaults so callers don't need to invoke it separately.
func TestApplyDefaultsCallsPollingDefaults(t *testing.T) {
	c := Config{AI: AIConfig{Primary: "claude"}}
	c.applyDefaults()

	if c.Polling.Tier3Interval == "" {
		t.Error("applyDefaults did not populate Polling.Tier3Interval")
	}
	if c.Polling.DiscoveryInterval == "" {
		t.Error("applyDefaults did not populate Polling.DiscoveryInterval")
	}
	if c.Polling.UseETag == nil {
		t.Error("applyDefaults did not populate Polling.UseETag")
	}
}
