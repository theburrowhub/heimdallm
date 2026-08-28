package mergetrack_test

// Regressions from the review of PR #738 (theburrowhub/heimdallm). Each test
// here failed before the fix it names, and each protects a path that produced
// wrong behaviour on a real, ordinary PR rather than a corner case.

import (
	"context"
	"errors"
	"testing"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// errNetwork stands in for any transient GitHub failure.
var errNetwork = errors.New("connection reset by peer")

// GitHub refuses enablePullRequestAutoMerge on a PR it would merge right now
// ("Pull request is in clean status"). That refusal used to fall through to the
// generic error path: no attempt counter, a flat one-minute cooldown, and a
// decision that picked arming again on the very next cycle. Any PR that was
// already green when first considered — daemon start, a repo just enabled, an
// approval landing after CI — looped arm → fail → cooldown forever and never
// merged, even with merge = true.
func TestReconcilePR_CleanStatusRefusalMergesInsteadOfLooping(t *testing.T) {
	cfg := enabledCfg()
	cfg.EnableAutoMerge = true
	cfg.Merge = true

	gw := &fakeGateway{
		statuses: []*gh.MergeStatus{cleanStatus()},
		autoErr: &gh.AutoMergeUnavailableError{
			Reason: gh.AutoMergeReasonCleanStatus,
			Body:   "Pull request is in clean status",
		},
	}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.mergeCalls != 1 {
		t.Fatalf("merge calls = %d, want 1: a refusal that means 'already mergeable' is an instruction to merge", gw.mergeCalls)
	}
	row := h.row(t)
	if row.Phase != store.MergePhaseMerged {
		t.Errorf("phase = %q, want merged", row.Phase)
	}
	if row.LastError != "" {
		t.Errorf("last_error = %q, want empty", row.LastError)
	}
}

// Same refusal with merge = false: there is nothing left to automate, so the
// row must park with an explanation rather than retrying every minute.
func TestReconcilePR_CleanStatusRefusalWithoutMergeParksWithAReason(t *testing.T) {
	cfg := enabledCfg()
	cfg.EnableAutoMerge = true

	gw := &fakeGateway{
		statuses: []*gh.MergeStatus{cleanStatus()},
		autoErr: &gh.AutoMergeUnavailableError{
			Reason: gh.AutoMergeReasonCleanStatus,
			Body:   "Pull request is in clean status",
		},
	}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.mergeCalls != 0 {
		t.Fatalf("merge calls = %d, want 0: merge_tracking.merge is off", gw.mergeCalls)
	}
	row := h.row(t)
	if row.BlockReason == "" {
		t.Error("the operator must be told why nothing happened")
	}
	if !row.CooldownUntil.After(h.now.Add(30 * time.Minute)) {
		t.Errorf("cooldown = %v, want a long park: nothing about this changes in a minute", row.CooldownUntil)
	}
}

// An arming failure with no counter of its own used to back off by a flat
// minute forever. It now grows like every other attempt.
func TestReconcilePR_ArmingFailuresBackOffProgressively(t *testing.T) {
	cfg := enabledCfg()
	cfg.EnableAutoMerge = true

	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBlocked
	st.ReviewDecision = gh.ReviewDecisionReviewRequired
	st.Reviews = nil

	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}, autoErr: errNetwork}
	h := newHarness(t, cfg, gw)

	var cooldowns []time.Duration
	for i := 0; i < 3; i++ {
		if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err == nil {
			t.Fatal("an arming failure must be reported")
		}
		row := h.row(t)
		cooldowns = append(cooldowns, row.CooldownUntil.Sub(h.now))
		if err := h.st.ClearMergeTrackingCooldown(h.prID); err != nil {
			t.Fatalf("clear cooldown: %v", err)
		}
	}
	if !(cooldowns[0] < cooldowns[1] && cooldowns[1] < cooldowns[2]) {
		t.Errorf("cooldowns = %v, want a growing backoff", cooldowns)
	}
	if h.row(t).ArmAttempts != 3 {
		t.Errorf("arm attempts = %d, want 3", h.row(t).ArmAttempts)
	}
}

// The maxUnknownWaits cap was dead: nothing incremented the counter, so a PR
// GitHub never finishes computing was re-queried every 45 seconds forever on a
// GraphQL budget shared with the review pipeline.
func TestReconcilePR_UnknownMergeabilityIsCountedAndCapped(t *testing.T) {
	st := cleanStatus()
	st.Mergeable = gh.MergeableUnknown
	st.MergeStateStatus = gh.MergeStateUnknown

	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}}
	h := newHarness(t, enabledCfg(), gw)

	for i := 1; i <= 5; i++ {
		if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
		if got := h.row(t).UnknownWaits; got != i {
			t.Fatalf("after %d cycles unknown_waits = %d, want %d", i, got, i)
		}
	}

	// The sixth read is past the cap: stop polling every 45 seconds.
	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	row := h.row(t)
	if !row.CooldownUntil.After(h.now.Add(30 * time.Minute)) {
		t.Errorf("cooldown = %v, want the capped long wait", row.CooldownUntil.Sub(h.now))
	}
}

// A base branch that keeps moving makes GitHub recompute, so the counter has to
// clear as soon as a real answer arrives — otherwise a long-lived PR reaches
// the cap on a head GitHub is perfectly willing to compute.
func TestReconcilePR_UnknownWaitCounterClearsOnAKnownState(t *testing.T) {
	unknown := cleanStatus()
	unknown.Mergeable = gh.MergeableUnknown
	unknown.MergeStateStatus = gh.MergeStateUnknown

	gw := &fakeGateway{statuses: []*gh.MergeStatus{unknown, cleanStatus()}}
	h := newHarness(t, enabledCfg(), gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if h.row(t).UnknownWaits != 1 {
		t.Fatalf("unknown_waits = %d, want 1", h.row(t).UnknownWaits)
	}
	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := h.row(t).UnknownWaits; got != 0 {
		t.Errorf("unknown_waits = %d, want 0 once GitHub answered", got)
	}
}

// syncRow re-anchored an auto-merge GitHub reports for another commit using
// GitHub's own enabledAt, typically well in the past. The freshly read row then
// satisfied "armed before this pass started" in the same pass, so the wait-a-
// pass rule — the one that gives GitHub its turn before Heimdallm merges
// directly — never applied on that path.
func TestReconcilePR_ReanchoredAutoMergeStillWaitsAPass(t *testing.T) {
	cfg := enabledCfg()
	cfg.EnableAutoMerge = true
	cfg.Merge = true

	st := cleanStatus()
	st.AutoMerge = &gh.AutoMergeRequest{
		MergeMethod: "SQUASH",
		EnabledAt:   time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC), // long before this tick
	}

	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.mergeCalls != 0 {
		t.Fatalf("merge calls = %d, want 0: GitHub has not had a pass at this commit yet", gw.mergeCalls)
	}
	row := h.row(t)
	if !row.AutoMergeArmedAt.Equal(h.now) {
		t.Errorf("armed at = %v, want the current tick %v", row.AutoMergeArmedAt, h.now)
	}

	// Next pass, past the cooldown the evaluation set: GitHub had its turn and
	// did not merge, so Heimdallm does.
	h.now = h.now.Add(time.Hour)
	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.mergeCalls != 1 {
		t.Errorf("merge calls = %d, want 1 on the following pass", gw.mergeCalls)
	}
}

// recordFailure and every decision write happen around the claim, so a failed
// automation must still be announced. The Flutter listing refreshes on this
// event and on nothing else after a failure.
func TestReconcilePR_FailedAutomationEmitsAnErrorEvent(t *testing.T) {
	cfg := enabledCfg()
	cfg.UpdateBranch = true

	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBehind

	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}, updateErr: errNetwork}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err == nil {
		t.Fatal("the update failure must be reported")
	}
	if !h.pub.has(sse.EventMergeTrackError) {
		t.Error("a failed automation must emit merge_track_error: it is the only event the UI gets")
	}
}
