package issues_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/issues"
)

func mkRev(login, state string, ts time.Time) github.PRReview {
	return github.PRReview{User: github.User{Login: login}, State: state, SubmittedAt: ts}
}

// TestLatestExternalReviewState_ChangesRequestedDominates pins the
// dominance rule the GitHub UI uses (#482): even one CHANGES_REQUESTED
// in the list wins over any number of APPROVED / COMMENTED.
func TestLatestExternalReviewState_ChangesRequestedDominates(t *testing.T) {
	t0 := time.Now().Add(-1 * time.Hour)
	t1 := t0.Add(10 * time.Minute)
	t2 := t1.Add(10 * time.Minute)
	revs := []github.PRReview{
		mkRev("alice", "COMMENTED", t0),
		mkRev("bob", "APPROVED", t1),
		mkRev("carol", "CHANGES_REQUESTED", t2),
	}
	state, reviewer, at := issues.LatestExternalReviewState(revs, "heimdallm-bot")
	if state != "CHANGES_REQUESTED" {
		t.Errorf("state = %q, want CHANGES_REQUESTED", state)
	}
	if reviewer != "carol" {
		t.Errorf("reviewer = %q, want carol", reviewer)
	}
	if !at.Equal(t2) {
		t.Errorf("at = %v, want %v", at, t2)
	}
}

// TestLatestExternalReviewState_BotReviewsIgnored ensures the
// aggregator filters out the daemon's own bot login so a daemon-posted
// review never round-trips into an "external" state and creates a
// self-loop. The bot login compare is case-insensitive — GitHub
// normalises logins to lowercase on the wire but the daemon may load
// the bot login from operator config in any case.
func TestLatestExternalReviewState_BotReviewsIgnored(t *testing.T) {
	now := time.Now()
	revs := []github.PRReview{
		mkRev("Heimdallm-Bot", "APPROVED", now.Add(-time.Minute)),
		mkRev("alice", "COMMENTED", now),
	}
	state, reviewer, _ := issues.LatestExternalReviewState(revs, "heimdallm-bot")
	if state != "COMMENTED" {
		t.Errorf("state = %q, want COMMENTED (bot review must be filtered)", state)
	}
	if reviewer != "alice" {
		t.Errorf("reviewer = %q, want alice", reviewer)
	}
}

// TestLatestExternalReviewState_DismissedIgnored verifies that
// DISMISSED reviews do not count toward the aggregate.
func TestLatestExternalReviewState_DismissedIgnored(t *testing.T) {
	now := time.Now()
	revs := []github.PRReview{
		mkRev("alice", "CHANGES_REQUESTED", now.Add(-time.Hour)),
		mkRev("alice", "DISMISSED", now), // her CR is no longer current
	}
	state, _, _ := issues.LatestExternalReviewState(revs, "bot")
	if state != "" {
		t.Errorf("state = %q, want empty (only DISMISSED remains after filter)", state)
	}
}

// TestLatestExternalReviewState_StaleStateSupersededByDecision pins that
// the aggregator collapses per-reviewer state with the GitHub-UI rule:
// only the latest non-COMMENTED state from a reviewer counts as their
// "decision". A reviewer who left CHANGES_REQUESTED and later APPROVED
// must no longer contribute CR.
func TestLatestExternalReviewState_StaleStateSupersededByDecision(t *testing.T) {
	now := time.Now()
	revs := []github.PRReview{
		mkRev("alice", "CHANGES_REQUESTED", now.Add(-time.Hour)),
		mkRev("alice", "APPROVED", now), // alice resolved her own CR
	}
	state, reviewer, _ := issues.LatestExternalReviewState(revs, "bot")
	if state != "APPROVED" {
		t.Errorf("state = %q, want APPROVED (latest non-COMMENTED wins per reviewer)", state)
	}
	if reviewer != "alice" {
		t.Errorf("reviewer = %q, want alice", reviewer)
	}
}

// TestLatestExternalReviewState_LatestCommentedFromSameReviewerWins
// pins the bug found in PR review: when a reviewer leaves two
// COMMENTED reviews back-to-back, the aggregator must surface the
// LATEST one (so its SubmittedAt advances and the Tier 3
// "fresh same-state" gate can re-dispatch the Responder). The earlier
// collapse kept the FIRST COMMENTED, freezing the timestamp at T1
// and silently swallowing T2.
func TestLatestExternalReviewState_LatestCommentedFromSameReviewerWins(t *testing.T) {
	t1 := time.Now().Add(-2 * time.Hour)
	t2 := t1.Add(time.Hour)
	revs := []github.PRReview{
		mkRev("alice", "COMMENTED", t1),
		mkRev("alice", "COMMENTED", t2),
	}
	state, reviewer, at := issues.LatestExternalReviewState(revs, "bot")
	if state != "COMMENTED" {
		t.Errorf("state = %q, want COMMENTED", state)
	}
	if reviewer != "alice" {
		t.Errorf("reviewer = %q, want alice", reviewer)
	}
	if !at.Equal(t2) {
		t.Errorf("at = %v, want %v (latest COMMENTED must win)", at, t2)
	}
}

// TestLatestExternalReviewState_LatestDecisionFromSameReviewerWins
// is the symmetric pin for the decision case — alice's later
// APPROVED supersedes her earlier CR for HER state, even when both
// arrive in the same reviews list.
func TestLatestExternalReviewState_LatestDecisionFromSameReviewerWins(t *testing.T) {
	t1 := time.Now().Add(-2 * time.Hour)
	t2 := t1.Add(time.Hour)
	revs := []github.PRReview{
		mkRev("alice", "CHANGES_REQUESTED", t1),
		mkRev("alice", "APPROVED", t2),
	}
	state, _, at := issues.LatestExternalReviewState(revs, "bot")
	if state != "APPROVED" {
		t.Errorf("state = %q, want APPROVED", state)
	}
	if !at.Equal(t2) {
		t.Errorf("at = %v, want %v", at, t2)
	}
}

// TestLatestExternalReviewState_DecisionThenCommentKeepsDecision
// guards against the inverse failure: a COMMENTED arriving after a
// reviewer has already submitted a non-COMMENTED decision must NOT
// downgrade their state.
func TestLatestExternalReviewState_DecisionThenCommentKeepsDecision(t *testing.T) {
	t1 := time.Now().Add(-2 * time.Hour)
	t2 := t1.Add(time.Hour)
	revs := []github.PRReview{
		mkRev("alice", "APPROVED", t1),
		mkRev("alice", "COMMENTED", t2),
	}
	state, _, _ := issues.LatestExternalReviewState(revs, "bot")
	if state != "APPROVED" {
		t.Errorf("state = %q, want APPROVED (later COMMENTED must not downgrade)", state)
	}
}

// TestLatestExternalReviewState_EmptyListYieldsEmptyState is the
// no-reviews path the dashboard relies on to render an "in flight"
// chip rather than a misleading default.
func TestLatestExternalReviewState_EmptyListYieldsEmptyState(t *testing.T) {
	state, reviewer, at := issues.LatestExternalReviewState(nil, "bot")
	if state != "" || reviewer != "" || !at.IsZero() {
		t.Errorf("empty list returned (%q, %q, %v), want all zero", state, reviewer, at)
	}
}
