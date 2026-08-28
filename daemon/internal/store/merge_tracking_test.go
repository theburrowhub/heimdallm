package store_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func newMergeTrackingStore(t *testing.T) (*store.Store, int64) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Now().UTC()
	prID, err := s.UpsertPR(&store.PR{
		GithubID: 1, Repo: "acme/widgets", Number: 7, Title: "t", Author: "octocat",
		URL: "u", State: "open", UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if _, err := s.EnsureMergeTracking(prID, "acme/widgets", 7); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	return s, prID
}

func TestEnsureMergeTracking_IsIdempotent(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	row, err := s.EnsureMergeTracking(prID, "acme/widgets", 7)
	if err != nil {
		t.Fatalf("ensure again: %v", err)
	}
	if row.Phase != store.MergePhaseIdle {
		t.Errorf("phase = %q, want idle", row.Phase)
	}
	rows, err := s.ListMergeTracking()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("got %d rows, want 1 — ensure must not duplicate", len(rows))
	}
}

// The claim is the persistent single-flight guard: an in-memory lock would be
// lost on restart and would not span two daemons on one database.
func TestClaimMergeTrackingAction_IsExclusive(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	now := time.Now().UTC()
	if err := s.ResetMergeTrackingForNewHead(prID, "abc123", now); err != nil {
		t.Fatalf("anchor: %v", err)
	}

	ok, err := s.ClaimMergeTrackingAction(prID, "abc123", store.MergePhaseMerging, now)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	ok, err = s.ClaimMergeTrackingAction(prID, "abc123", store.MergePhaseMerging, now)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if ok {
		t.Fatal("a second claim on an in-flight row must fail")
	}

	if err := s.ReleaseMergeTrackingAction(prID, store.MergePhaseIdle, time.Time{}, ""); err != nil {
		t.Fatalf("release: %v", err)
	}
	ok, err = s.ClaimMergeTrackingAction(prID, "abc123", store.MergePhaseMerging, now)
	if err != nil || !ok {
		t.Fatalf("claim after release: ok=%v err=%v", ok, err)
	}
}

// A claim anchored to a commit that is no longer the head must fail: the plan
// it would execute was made for a different commit.
func TestClaimMergeTrackingAction_RefusesAStaleSHA(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	now := time.Now().UTC()
	if err := s.ResetMergeTrackingForNewHead(prID, "abc123", now); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	ok, err := s.ClaimMergeTrackingAction(prID, "stale", store.MergePhaseMerging, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if ok {
		t.Fatal("a claim for a stale head sha must be refused")
	}
}

func TestClaimMergeTrackingAction_RespectsCooldown(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	now := time.Now().UTC()
	if err := s.ResetMergeTrackingForNewHead(prID, "abc123", now); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if err := s.BumpMergeTrackingAttempt(prID, store.MergeAttemptMerge, now.Add(time.Hour), "boom"); err != nil {
		t.Fatalf("bump: %v", err)
	}
	ok, err := s.ClaimMergeTrackingAction(prID, "abc123", store.MergePhaseMerging, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if ok {
		t.Fatal("a claim during a cooldown must be refused")
	}
}

// A push invalidates every per-commit conclusion.
func TestResetMergeTrackingForNewHead_ClearsPerCommitState(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	now := time.Now().UTC()

	if err := s.ResetMergeTrackingForNewHead(prID, "old", now); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if err := s.ArmNativeAutoMerge(prID, "old", "squash", now); err != nil {
		t.Fatalf("arm: %v", err)
	}
	for _, kind := range []string{store.MergeAttemptUpdate, store.MergeAttemptConflict, store.MergeAttemptMerge} {
		if err := s.BumpMergeTrackingAttempt(prID, kind, now.Add(time.Hour), "boom"); err != nil {
			t.Fatalf("bump %s: %v", kind, err)
		}
	}

	if err := s.ResetMergeTrackingForNewHead(prID, "new", now); err != nil {
		t.Fatalf("reset: %v", err)
	}
	row, err := s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.HeadSHA != "new" {
		t.Errorf("head sha = %q, want new", row.HeadSHA)
	}
	if row.UpdateAttempts != 0 || row.ConflictAttempts != 0 || row.MergeAttempts != 0 {
		t.Errorf("counters not reset: %+v", row)
	}
	if !row.CooldownUntil.IsZero() || row.LastError != "" {
		t.Errorf("cooldown/error not cleared: %v %q", row.CooldownUntil, row.LastError)
	}
	// The merge licence was granted for a commit that no longer exists.
	if row.AutoMergeHeadSHA != "" || row.Phase == store.MergePhaseAutoMergeArmed {
		t.Errorf("stale auto-merge not cleared: sha=%q phase=%q", row.AutoMergeHeadSHA, row.Phase)
	}
}

// Re-anchoring to the SAME head must keep the armed state: no push happened.
func TestResetMergeTrackingForNewHead_KeepsArmingForTheSameSHA(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	now := time.Now().UTC()
	if err := s.ArmNativeAutoMerge(prID, "abc123", "squash", now); err != nil {
		t.Fatalf("arm: %v", err)
	}
	if err := s.ResetMergeTrackingForNewHead(prID, "abc123", now); err != nil {
		t.Fatalf("reset: %v", err)
	}
	row, err := s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !row.AutoMergeArmedFor("abc123") {
		t.Errorf("arming for the same head must survive: %+v", row)
	}
}

func TestClearNativeAutoMerge_ReturnsToIdle(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	now := time.Now().UTC()
	if err := s.ArmNativeAutoMerge(prID, "abc123", "squash", now); err != nil {
		t.Fatalf("arm: %v", err)
	}
	if err := s.ClearNativeAutoMerge(prID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	row, err := s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Phase != store.MergePhaseIdle || row.AutoMergeHeadSHA != "" {
		t.Errorf("clear left stale state: %+v", row)
	}
}

func TestListMergeTrackingDue_SkipsTerminalInFlightAndCooledDownRows(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	mk := func(githubID int64, number int) int64 {
		id, err := s.UpsertPR(&store.PR{
			GithubID: githubID, Repo: "acme/widgets", Number: number, Title: "t",
			Author: "octocat", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now,
		})
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if _, err := s.EnsureMergeTracking(id, "acme/widgets", number); err != nil {
			t.Fatalf("ensure: %v", err)
		}
		return id
	}

	due := mk(1, 1)
	merged := mk(2, 2)
	inFlight := mk(3, 3)
	cooling := mk(4, 4)
	excluded := mk(5, 5)

	if err := s.MarkMergeTrackingMerged(merged, now); err != nil {
		t.Fatalf("merged: %v", err)
	}
	if err := s.ResetMergeTrackingForNewHead(inFlight, "sha", now); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if ok, err := s.ClaimMergeTrackingAction(inFlight, "sha", store.MergePhaseResolving, now); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := s.BumpMergeTrackingAttempt(cooling, store.MergeAttemptMerge, now.Add(time.Hour), "wait"); err != nil {
		t.Fatalf("cooldown: %v", err)
	}
	if err := s.SetMergeTrackingExcluded(excluded, true); err != nil {
		t.Fatalf("exclude: %v", err)
	}

	rows, err := s.ListMergeTrackingDue(now, 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(rows) != 1 || rows[0].PRID != due {
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.PRID
		}
		t.Fatalf("due rows = %v, want only %d", ids, due)
	}
}

func TestListMergeTrackingDue_HonoursTheLimit(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	now := time.Now().UTC()
	for i := 1; i <= 5; i++ {
		id, err := s.UpsertPR(&store.PR{
			GithubID: int64(i), Repo: "acme/widgets", Number: i, Title: "t",
			Author: "octocat", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now,
		})
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if _, err := s.EnsureMergeTracking(id, "acme/widgets", i); err != nil {
			t.Fatalf("ensure: %v", err)
		}
	}
	rows, err := s.ListMergeTrackingDue(now, 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("got %d rows, want the limit of 2 — the cap bounds API spend", len(rows))
	}
}

// The listing sorts what needs human attention to the top.
func TestListMergeTracking_SortsCheckProblemsFirst(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	now := time.Now().UTC()

	mk := func(i int, failing, pending int) int64 {
		id, err := s.UpsertPR(&store.PR{
			GithubID: int64(i), Repo: "acme/widgets", Number: i, Title: "t",
			Author: "octocat", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now,
		})
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if _, err := s.EnsureMergeTracking(id, "acme/widgets", i); err != nil {
			t.Fatalf("ensure: %v", err)
		}
		if err := s.RecordMergeTrackingDecision(id, store.MergeDecisionRecord{
			Phase: store.MergePhaseIdle, HeadSHA: "sha",
			ChecksRequiredFailing: failing, ChecksRequiredPending: pending, At: now,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
		return id
	}

	clean := mk(1, 0, 0)
	pending := mk(2, 0, 1)
	failing := mk(3, 1, 0)

	rows, err := s.ListMergeTracking()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].PRID != failing {
		t.Errorf("first row = %d, want the one with a failing check (%d)", rows[0].PRID, failing)
	}
	if rows[1].PRID != pending {
		t.Errorf("second row = %d, want the pending one (%d)", rows[1].PRID, pending)
	}
	if rows[2].PRID != clean {
		t.Errorf("third row = %d, want the clean one (%d)", rows[2].PRID, clean)
	}
}

func TestBlockMergeTracking_PersistsTheReason(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	until := time.Now().UTC().Add(10 * time.Minute)
	if err := s.BlockMergeTracking(prID, "head_sha_moved", "a commit landed", until); err != nil {
		t.Fatalf("block: %v", err)
	}
	row, err := s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Phase != store.MergePhaseBlocked || row.BlockReason != "head_sha_moved" {
		t.Errorf("row = %+v", row)
	}
	if row.BlockDetail != "a commit landed" {
		t.Errorf("detail = %q", row.BlockDetail)
	}
}

func TestBumpMergeTrackingAttempt_RejectsUnknownKinds(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	if err := s.BumpMergeTrackingAttempt(prID, "not-a-kind", time.Time{}, ""); err == nil {
		t.Fatal("an unknown counter kind must be rejected, not silently ignored")
	}
}

func TestPruneMergeTracking_DropsOrphansAndOldTerminalRows(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	old := time.Now().UTC().Add(-48 * time.Hour)
	if err := s.MarkMergeTrackingMerged(prID, old); err != nil {
		t.Fatalf("merged: %v", err)
	}
	n, err := s.PruneMergeTracking(time.Now().UTC())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want 1", n)
	}
	if _, err := s.GetMergeTracking(prID); err == nil {
		t.Error("the pruned row should be gone")
	}
}

// A repo rename must move merge_tracking rows with the rest. Without this the
// rows are orphaned: the reconciler looks them up by the new slug, finds
// nothing, and re-enrols the PR from scratch — losing its attempt counters and
// its armed auto-merge.
func TestRenameRepo_MovesMergeTrackingRows(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	now := time.Now().UTC()
	if err := s.ArmNativeAutoMerge(prID, "abc123", "squash", now); err != nil {
		t.Fatalf("arm: %v", err)
	}

	applied, err := s.RenameRepo("acme/widgets", "acme/gadgets")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if !applied {
		t.Fatal("rename reported no change")
	}

	row, err := s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Repo != "acme/gadgets" {
		t.Errorf("repo = %q, want the renamed slug", row.Repo)
	}
	if !row.AutoMergeArmedFor("abc123") {
		t.Error("the armed state must survive a rename")
	}
}
