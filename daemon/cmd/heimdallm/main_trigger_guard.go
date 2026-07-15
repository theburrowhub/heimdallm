package main

import "sync"

// triggerGuard is an in-process, per-PR-ID mutex for the manual "Re-review"
// trigger (POST /prs/{id}/review). It closes the double-run window that the
// persistent (pr_id, head_sha) in-flight claim cannot cover: when the HEAD SHA
// lookup in the trigger callback fails, the ghPR carries an empty Head.SHA, the
// persistent claim is skipped, and — because RunOptions.Force removes the
// pipeline's own SHA dedup and circuit breaker — two rapid double-clicks during
// a GitHub API blip would otherwise run two full concurrent reviews and
// double-publish.
//
// Keyed on the store PR ID (not the SHA), so it does not depend on SHA
// resolution succeeding. It is a process-local backstop only; the persistent
// claim remains the cross-restart / poll-path coordination mechanism. Both are
// intentionally kept.
type triggerGuard struct {
	mu       sync.Mutex
	inFlight map[int64]struct{}
}

func newTriggerGuard() *triggerGuard {
	return &triggerGuard{inFlight: make(map[int64]struct{})}
}

// tryAcquire returns true if the caller took the guard for prID, or false if a
// manual review for that PR is already running in this process. On success the
// caller MUST release(prID) when done (defer). It never blocks — a concurrent
// second click is rejected, not queued, so the button stays responsive.
func (g *triggerGuard) tryAcquire(prID int64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, running := g.inFlight[prID]; running {
		return false
	}
	g.inFlight[prID] = struct{}{}
	return true
}

// release frees the guard for prID so a subsequent click can re-review.
func (g *triggerGuard) release(prID int64) {
	g.mu.Lock()
	delete(g.inFlight, prID)
	g.mu.Unlock()
}
