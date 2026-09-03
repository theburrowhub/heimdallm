package main

import (
	"errors"
	"testing"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/scheduler"
)

// TestBuildRateLimitView_ObservedWithinWindow verifies that a bucket with a
// live observation whose reset window hasn't elapsed is served verbatim from
// the tracker, tagged "observed", and never touches the fallback.
func TestBuildRateLimitView_ObservedWithinWindow(t *testing.T) {
	now := time.Now()
	snaps := map[string]scheduler.RateSnapshot{
		"core":    {Limit: 5000, Remaining: 4937, Used: 63, Reset: now.Add(48 * time.Minute), ObservedAt: now.Add(-time.Second)},
		"search":  {Limit: 30, Remaining: 28, Used: 2, Reset: now.Add(41 * time.Second), ObservedAt: now.Add(-time.Second)},
		"graphql": {Limit: 5000, Remaining: 5000, Used: 0, Reset: now.Add(59 * time.Minute), ObservedAt: now.Add(-time.Second)},
	}
	fallbackCalls := 0
	fallback := func() (*gh.RateLimit, error) {
		fallbackCalls++
		return nil, errors.New("fallback must not be called when every bucket is observed")
	}

	view, err := buildRateLimitView(now, snaps, fallback)
	if err != nil {
		t.Fatalf("buildRateLimitView: %v", err)
	}
	if fallbackCalls != 0 {
		t.Errorf("fallback called %d times, want 0", fallbackCalls)
	}

	if view.Core.Limit != 5000 || view.Core.Remaining != 4937 || view.Core.Used != 63 {
		t.Errorf("Core = %+v, want the observed snapshot verbatim", view.Core)
	}
	if view.Core.Source != "observed" {
		t.Errorf("Core.Source = %q, want %q", view.Core.Source, "observed")
	}
	if view.Search.Remaining != 28 || view.Search.Source != "observed" {
		t.Errorf("Search = %+v, want observed remaining=28", view.Search)
	}
	if view.GraphQL.Remaining != 5000 || view.GraphQL.Source != "observed" {
		t.Errorf("GraphQL = %+v, want observed remaining=5000", view.GraphQL)
	}
}

// TestBuildRateLimitView_WindowAlreadyReset verifies that an observation whose
// reset time has already passed is NOT served stale: the bucket reports a
// fresh window (remaining = limit, used = 0, reset = 0) rather than showing an
// exhausted quota forever.
func TestBuildRateLimitView_WindowAlreadyReset(t *testing.T) {
	now := time.Now()
	snaps := map[string]scheduler.RateSnapshot{
		"core":    {Limit: 5000, Remaining: 4937, Used: 63, Reset: now.Add(48 * time.Minute), ObservedAt: now},
		"search":  {Limit: 30, Remaining: 0, Used: 30, Reset: now.Add(-5 * time.Second), ObservedAt: now.Add(-65 * time.Second)},
		"graphql": {Limit: 5000, Remaining: 5000, Used: 0, Reset: now.Add(59 * time.Minute), ObservedAt: now},
	}
	fallback := func() (*gh.RateLimit, error) {
		t.Fatal("fallback must not be called: every bucket has an observation")
		return nil, nil
	}

	view, err := buildRateLimitView(now, snaps, fallback)
	if err != nil {
		t.Fatalf("buildRateLimitView: %v", err)
	}

	if view.Search.Remaining != view.Search.Limit {
		t.Errorf("Search.Remaining = %d, want == Limit (%d) after window reset", view.Search.Remaining, view.Search.Limit)
	}
	if view.Search.Used != 0 {
		t.Errorf("Search.Used = %d, want 0 after window reset", view.Search.Used)
	}
	if view.Search.Reset != 0 {
		t.Errorf("Search.Reset = %d, want 0 after window reset", view.Search.Reset)
	}
	if view.Search.Source != "window_reset" {
		t.Errorf("Search.Source = %q, want %q", view.Search.Source, "window_reset")
	}
}

// TestBuildRateLimitView_NeverObservedUsesFallback verifies that a bucket with
// no observation at all falls back to GitHub's GET /rate_limit, and that the
// fallback is invoked at most once even when multiple buckets are missing.
func TestBuildRateLimitView_NeverObservedUsesFallback(t *testing.T) {
	now := time.Now()
	snaps := map[string]scheduler.RateSnapshot{
		"core": {Limit: 5000, Remaining: 4937, Used: 63, Reset: now.Add(48 * time.Minute), ObservedAt: now},
		// search and graphql never observed.
	}
	fallbackCalls := 0
	fallback := func() (*gh.RateLimit, error) {
		fallbackCalls++
		return &gh.RateLimit{
			Search:  gh.RateLimitResource{Limit: 30, Remaining: 30, Used: 0, Reset: now.Add(time.Hour).Unix()},
			GraphQL: gh.RateLimitResource{Limit: 5000, Remaining: 5000, Used: 0, Reset: now.Add(time.Hour).Unix()},
		}, nil
	}

	view, err := buildRateLimitView(now, snaps, fallback)
	if err != nil {
		t.Fatalf("buildRateLimitView: %v", err)
	}
	if fallbackCalls != 1 {
		t.Errorf("fallback called %d times, want exactly 1", fallbackCalls)
	}
	if view.Core.Source != "observed" {
		t.Errorf("Core.Source = %q, want %q (core was observed, must not use fallback)", view.Core.Source, "observed")
	}
	if view.Search.Source != "endpoint" || view.Search.Remaining != 30 {
		t.Errorf("Search = %+v, want fallback-sourced remaining=30", view.Search)
	}
	if view.GraphQL.Source != "endpoint" || view.GraphQL.Remaining != 5000 {
		t.Errorf("GraphQL = %+v, want fallback-sourced remaining=5000", view.GraphQL)
	}
}

// TestBuildRateLimitView_FallbackErrorWithPartialObservationsStillSucceeds
// verifies the resilience the fallback-only design exists for: if GitHub's
// rate_limit endpoint is unavailable (e.g. mid circuit-breaker cooldown) but
// at least one bucket has already been observed, the handler must not 502 —
// it serves what it knows and leaves the unobserved bucket zeroed.
func TestBuildRateLimitView_FallbackErrorWithPartialObservationsStillSucceeds(t *testing.T) {
	now := time.Now()
	snaps := map[string]scheduler.RateSnapshot{
		"core": {Limit: 5000, Remaining: 4937, Used: 63, Reset: now.Add(48 * time.Minute), ObservedAt: now},
	}
	fallback := func() (*gh.RateLimit, error) {
		return nil, errors.New("circuit breaker: rate limit cooldown active")
	}

	view, err := buildRateLimitView(now, snaps, fallback)
	if err != nil {
		t.Fatalf("buildRateLimitView returned error with a partial observation available: %v", err)
	}
	if view.Core.Source != "observed" || view.Core.Remaining != 4937 {
		t.Errorf("Core = %+v, want the still-valid observed snapshot", view.Core)
	}
}

// TestBuildRateLimitView_NoObservationsAndFallbackErrorReturnsError verifies
// the one case that must still fail: nothing has ever been observed and
// GitHub's endpoint is unreachable, so there is truly nothing to report.
func TestBuildRateLimitView_NoObservationsAndFallbackErrorReturnsError(t *testing.T) {
	now := time.Now()
	snaps := map[string]scheduler.RateSnapshot{}
	fallback := func() (*gh.RateLimit, error) {
		return nil, errors.New("github unreachable")
	}

	_, err := buildRateLimitView(now, snaps, fallback)
	if err == nil {
		t.Fatal("expected an error when no bucket was ever observed and the fallback fails")
	}
}
