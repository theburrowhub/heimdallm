package pipeline

import "time"

// GraceDefault is the standard updated_at grace window applied by both the
// PR review dedup (see PRAlreadyReviewed in cmd/heimdallm/main.go) and the
// issue triage dedup (see internal/issues/fetcher.go). 2 minutes absorbs
// GitHub replication lag plus any peer-bot submission timing without
// suppressing legitimate human activity (a human push within 2 min of a
// review is rare enough that we accept "picked up on the next tick" as the
// trade-off).
//
// See theburrowhub/heimdallm#243 for the incident and grace-duration
// analysis. Do NOT widen past 5 minutes without revisiting that analysis —
// longer windows blind the daemon to real human activity.
const GraceDefault = 2 * time.Minute

// ReviewFreshEnough returns true when observed (the GitHub updated_at we
// just fetched) is within grace of anchor (the local timestamp we recorded
// when the review was successfully posted). Used by both the PR and issue
// dedup paths so the two cannot drift apart.
//
// anchor.IsZero() is treated as "no fresh anchor, cannot dedup" — the
// caller decides whether to fall back to an older anchor (e.g. CreatedAt
// for legacy rows) or allow the review to run.
//
// The boundary is inclusive: observed == anchor + grace returns true
// (still fresh). Inclusivity matches the intent — a review posted at
// exactly the grace-window edge should still dedup. Do not "fix" to an
// exclusive comparison without revisiting this contract.
func ReviewFreshEnough(anchor, observed time.Time, grace time.Duration) bool {
	if anchor.IsZero() {
		return false
	}
	return !observed.After(anchor.Add(grace))
}
