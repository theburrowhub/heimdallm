package main

import (
	"fmt"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/scheduler"
)

// rateLimitBucketView is one GitHub rate-limit bucket as served by GET
// /github/rate_limit. The limit/remaining/used/reset fields mirror
// gh.RateLimitResource so the Flutter client's existing parsing keeps working
// unchanged; source and observed_at are additive.
type rateLimitBucketView struct {
	Limit      int    `json:"limit"`
	Remaining  int    `json:"remaining"`
	Used       int    `json:"used"`
	Reset      int64  `json:"reset"`
	Source     string `json:"source"`      // "observed" | "window_reset" | "endpoint"
	ObservedAt int64  `json:"observed_at"` // unix seconds; 0 when source == "endpoint"
}

// rateLimitView is the full GET /github/rate_limit response body.
type rateLimitView struct {
	Core    rateLimitBucketView `json:"core"`
	Search  rateLimitBucketView `json:"search"`
	GraphQL rateLimitBucketView `json:"graphql"`
}

// rateLimitResources maps the tracker's resource names to gh.RateLimit's
// bucket names, in the order the response reports them.
var rateLimitResources = []string{"core", "search", "graphql"}

// buildRateLimitView assembles the GET /github/rate_limit response from the
// scheduler's live tracker, which is fed by the real X-RateLimit-* headers
// GitHub returns on every API response. GitHub's own GET /rate_limit endpoint
// is used only as a fallback for a bucket the daemon hasn't made a single
// request against yet — for some tokens that endpoint reports an always-empty
// quota (remaining == limit, reset == now+window on every call) that never
// reflects real usage, so it must never be the primary source once a bucket
// has live data.
//
// now is passed in (rather than read via time.Now()) so tests can control the
// reset-window boundary precisely.
func buildRateLimitView(now time.Time, snaps map[string]scheduler.RateSnapshot, fallback func() (*gh.RateLimit, error)) (*rateLimitView, error) {
	view := &rateLimitView{}
	buckets := map[string]*rateLimitBucketView{
		"core":    &view.Core,
		"search":  &view.Search,
		"graphql": &view.GraphQL,
	}

	var missing []string
	for _, resource := range rateLimitResources {
		snap, ok := snaps[resource]
		if !ok {
			missing = append(missing, resource)
			continue
		}
		*buckets[resource] = observedBucketView(now, snap)
	}

	if len(missing) == 0 {
		return view, nil
	}

	fb, err := fallback()
	if err != nil {
		// A bucket was never observed and GitHub's own endpoint is also
		// unavailable (e.g. a circuit-breaker cooldown). Only fail the whole
		// request if there is truly nothing to report; otherwise serve what
		// was observed and leave the unobserved buckets zeroed.
		if len(missing) == len(rateLimitResources) {
			return nil, fmt.Errorf("rate limit: no observations yet and fallback failed: %w", err)
		}
		return view, nil
	}

	fbBuckets := map[string]gh.RateLimitResource{
		"core":    fb.Core,
		"search":  fb.Search,
		"graphql": fb.GraphQL,
	}
	for _, resource := range missing {
		r := fbBuckets[resource]
		*buckets[resource] = rateLimitBucketView{
			Limit:     r.Limit,
			Remaining: r.Remaining,
			Used:      r.Used,
			Reset:     r.Reset,
			Source:    "endpoint",
		}
	}
	return view, nil
}

// observedBucketView renders a tracker snapshot for a resource that has at
// least one observation. If the observed reset window has already elapsed,
// the snapshot is stale (GitHub has since rolled the window over and the
// bucket is back at full quota) — reporting it verbatim would show an
// exhausted bucket forever, so it's replaced with a fresh, empty window.
func observedBucketView(now time.Time, snap scheduler.RateSnapshot) rateLimitBucketView {
	if !snap.Reset.IsZero() && !now.Before(snap.Reset) {
		return rateLimitBucketView{
			Limit:     snap.Limit,
			Remaining: snap.Limit,
			Used:      0,
			Reset:     0,
			Source:    "window_reset",
		}
	}
	var resetUnix int64
	if !snap.Reset.IsZero() {
		resetUnix = snap.Reset.Unix()
	}
	var observedAtUnix int64
	if !snap.ObservedAt.IsZero() {
		observedAtUnix = snap.ObservedAt.Unix()
	}
	return rateLimitBucketView{
		Limit:      snap.Limit,
		Remaining:  snap.Remaining,
		Used:       snap.Used,
		Reset:      resetUnix,
		Source:     "observed",
		ObservedAt: observedAtUnix,
	}
}
