package config

import (
	"strings"
	"testing"
)

// ResolvedPollInterval gives [polling].poll_interval precedence over
// [github].poll_interval, and only the latter used to be validated. That made
// the newer section a way around the 1m floor the older one enforces to protect
// the API quota — the opposite of what this feature is for.
func TestValidatePolling_PollIntervalHonoursTheQuotaFloor(t *testing.T) {
	c := &Config{Polling: PollingConfig{PollInterval: "1s"}}
	err := c.ValidatePolling()
	if err == nil {
		t.Fatal("polling.poll_interval = 1s must be rejected; it bypasses the 1m floor")
	}
	if !strings.Contains(err.Error(), "poll_interval") {
		t.Errorf("error should name the offending field, got: %v", err)
	}

	// And the resolver must not be reachable with such a value in the first
	// place — this documents the coupling between the two.
	if got := c.ResolvedPollInterval(); got.Seconds() != 1 {
		t.Logf("resolver would have used %s had validation not rejected it", got)
	}
}

func TestValidatePolling_RangesPerField(t *testing.T) {
	tests := []struct {
		name    string
		cfg     PollingConfig
		wantErr bool
	}{
		{"empty section is valid", PollingConfig{}, false},
		{"typical values", PollingConfig{
			PollInterval: "5m", MinInterval: "1m", MaxInterval: "15m",
			DiscoveryInterval: "5m", Tier3Interval: "30s",
		}, false},

		{"poll_interval below floor", PollingConfig{PollInterval: "30s"}, true},
		{"poll_interval above ceiling", PollingConfig{PollInterval: "48h"}, true},
		{"poll_interval unparseable", PollingConfig{PollInterval: "5"}, true},
		{"min_interval below floor", PollingConfig{MinInterval: "1s"}, true},
		{"discovery_interval below floor", PollingConfig{DiscoveryInterval: "10s"}, true},

		// tier3_interval drives a local scan, not GitHub traffic, so sub-minute
		// values are legitimate there — but not unbounded.
		{"tier3_interval sub-minute is allowed", PollingConfig{Tier3Interval: "30s"}, false},
		{"tier3_interval 1ms is not", PollingConfig{Tier3Interval: "1ms"}, true},
		{"tier3_interval above ceiling", PollingConfig{Tier3Interval: "2h"}, true},

		{"negative threshold", PollingConfig{RateLimitSafetyThreshold: -1}, true},
		{"zero threshold means unset", PollingConfig{RateLimitSafetyThreshold: 0}, false},

		{"min above max", PollingConfig{MinInterval: "20m", MaxInterval: "10m"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{Polling: tt.cfg}
			err := c.ValidatePolling()
			if tt.wantErr && err == nil {
				t.Errorf("expected a validation error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
		})
	}
}

// Validate() is the entry point the load and reload paths call; the polling
// checks are worthless if they are not wired into it.
func TestValidate_RejectsBadPollingSection(t *testing.T) {
	c := &Config{}
	c.AI.Primary = "claude"
	c.GitHub.PollInterval = "5m"
	c.Polling.PollInterval = "1s"

	if err := c.Validate(); err == nil {
		t.Fatal("Validate() must reject an out-of-range [polling].poll_interval")
	}

	c.Polling.PollInterval = "5m"
	if err := c.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}
