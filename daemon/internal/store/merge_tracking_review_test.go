package store_test

// Regressions from the review of PR #738 (theburrowhub/heimdallm): writes that
// could release a single-flight claim, and a terminal row that could never come
// back.

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func trackedPR(t *testing.T, s *store.Store) int64 {
	t.Helper()
	now := time.Now().UTC()
	prID, err := s.UpsertPR(&store.PR{
		GithubID: 321, Repo: "acme/widgets", Number: 9, State: "open",
		UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if _, err := s.EnsureMergeTracking(prID, "acme/widgets", 9); err != nil {
		t.Fatalf("ensure tracking: %v", err)
	}
	// A claim is anchored to the head SHA it was decided against, so the row
	// needs one before any of these tests can claim it.
	if err := s.ResetMergeTrackingForNewHead(prID, "head1", now); err != nil {
		t.Fatalf("anchor head: %v", err)
	}
	return prID
}

// An evaluation is derived from a snapshot read before the claim. A concurrent
// one — the manual evaluate endpoint, or a second daemon on the same database —
// used to write its stale 'idle' over a live 'merging', releasing the lock
// mid-action so a second claim (and a second merge) could succeed.
func TestRecordMergeTrackingDecision_DoesNotReleaseAnInFlightClaim(t *testing.T) {
	s := newTestStore(t)
	prID := trackedPR(t, s)
	now := time.Now().UTC()

	claimed, err := s.ClaimMergeTrackingAction(prID, "head1", store.MergePhaseMerging, now)
	if err != nil || !claimed {
		t.Fatalf("claim: %v claimed=%v", err, claimed)
	}

	// The stale evaluation lands now.
	if err := s.RecordMergeTrackingDecision(prID, store.MergeDecisionRecord{
		Phase:       store.MergePhaseIdle,
		HeadSHA:     "head1",
		BlockReason: "checks_pending",
		BlockDetail: "waiting on build",
		At:          now,
	}); err != nil {
		t.Fatalf("record decision: %v", err)
	}

	row, err := s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Phase != store.MergePhaseMerging {
		t.Errorf("phase = %q, want merging: the claim must survive a concurrent evaluation", row.Phase)
	}
	// The explanation is still refreshed — only the lock is protected.
	if row.BlockReason != "checks_pending" {
		t.Errorf("block reason = %q, want the fresh one", row.BlockReason)
	}

	again, err := s.ClaimMergeTrackingAction(prID, "head1", store.MergePhaseMerging, now)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if again {
		t.Error("a second claim succeeded: the single-flight lock was released")
	}
}

// ArmNativeAutoMerge and the new-head reset have the same reach into the phase
// column and the same obligation.
func TestPhaseWrites_NeverClobberAnInFlightAction(t *testing.T) {
	now := time.Now().UTC()
	cases := map[string]func(*store.Store, int64) error{
		"arm": func(s *store.Store, prID int64) error {
			return s.ArmNativeAutoMerge(prID, "head1", "SQUASH", now)
		},
		"reset for new head": func(s *store.Store, prID int64) error {
			return s.ResetMergeTrackingForNewHead(prID, "head2", now)
		},
	}
	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			prID := trackedPR(t, s)
			if claimed, err := s.ClaimMergeTrackingAction(prID, "head1", store.MergePhaseResolving, now); err != nil || !claimed {
				t.Fatalf("claim: %v claimed=%v", err, claimed)
			}
			if err := write(s, prID); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			row, err := s.GetMergeTracking(prID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if row.Phase != store.MergePhaseResolving {
				t.Errorf("phase = %q, want resolving", row.Phase)
			}
		})
	}
}

// A PR that was abandoned — closed then reopened, include_assigned toggled off
// and on, a transient failure reading its status — is re-discovered every tick.
// Without reviving the row it was never evaluated again and the UI showed a PR
// Heimdallm can plainly see as untracked, forever.
func TestEnsureMergeTracking_RevivesAnAbandonedRow(t *testing.T) {
	s := newTestStore(t)
	prID := trackedPR(t, s)
	now := time.Now().UTC()

	if err := s.MarkMergeTrackingAbandoned(prID, "closed", now); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	row, err := s.EnsureMergeTracking(prID, "acme/widgets", 9)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if row.Phase != store.MergePhaseIdle {
		t.Errorf("phase = %q, want idle", row.Phase)
	}
	if row.TerminalReason != "" {
		t.Errorf("terminal reason = %q, want cleared", row.TerminalReason)
	}
	if !row.CooldownUntil.IsZero() {
		t.Errorf("cooldown = %v, want cleared so the next cycle picks it up", row.CooldownUntil)
	}

	due, err := s.ListMergeTrackingDue(now.Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 1 {
		t.Errorf("due rows = %d, want the revived one", len(due))
	}
}

// A merged PR does not come back, and discovery never offers one.
func TestEnsureMergeTracking_LeavesAMergedRowAlone(t *testing.T) {
	s := newTestStore(t)
	prID := trackedPR(t, s)
	if err := s.MarkMergeTrackingMerged(prID, time.Now().UTC()); err != nil {
		t.Fatalf("mark merged: %v", err)
	}
	row, err := s.EnsureMergeTracking(prID, "acme/widgets", 9)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if row.Phase != store.MergePhaseMerged {
		t.Errorf("phase = %q, want merged", row.Phase)
	}
}

// Arming has its own counter now, so a repeated failure backs off instead of
// retrying on a flat one-minute cooldown forever.
func TestBumpMergeTrackingAttempt_CountsArmingSeparately(t *testing.T) {
	s := newTestStore(t)
	prID := trackedPR(t, s)
	now := time.Now().UTC()

	for i := 0; i < 2; i++ {
		if err := s.BumpMergeTrackingAttempt(prID, store.MergeAttemptArm, now, "boom"); err != nil {
			t.Fatalf("bump: %v", err)
		}
	}
	row, err := s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.ArmAttempts != 2 {
		t.Errorf("arm attempts = %d, want 2", row.ArmAttempts)
	}
	if row.MergeAttempts != 0 || row.UpdateAttempts != 0 {
		t.Error("arming must not spend another action's budget")
	}

	// A push is a fresh start for every per-commit counter.
	if err := s.ResetMergeTrackingForNewHead(prID, "head9", now); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if row, err = s.GetMergeTracking(prID); err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.ArmAttempts != 0 {
		t.Errorf("arm attempts = %d after a push, want 0", row.ArmAttempts)
	}
}

// The counter that bounds mergeability polling has to come back down when
// GitHub finally answers.
func TestClearMergeTrackingUnknownWaits(t *testing.T) {
	s := newTestStore(t)
	prID := trackedPR(t, s)
	now := time.Now().UTC()

	if err := s.BumpMergeTrackingAttempt(prID, store.MergeAttemptUnknown, now, ""); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if err := s.ClearMergeTrackingUnknownWaits(prID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	row, err := s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.UnknownWaits != 0 {
		t.Errorf("unknown waits = %d, want 0", row.UnknownWaits)
	}
}
