package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

// Retention is measured from terminal_at, which a Re-check (identity refresh),
// an Exclude or a manual evaluate must not move. With updated_at as the anchor,
// every click restarted the clock and a merged PR never aged out.
func TestPruneMergeTracking_InteractionsDoNotRestartRetention(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	mergedAt := time.Now().UTC().Add(-30 * time.Hour)
	if err := s.MarkMergeTrackingMerged(prID, mergedAt); err != nil {
		t.Fatalf("merged: %v", err)
	}
	if err := s.UpdateMergeTrackingIdentity(prID, "node", "main", "feat", true, false); err != nil {
		t.Fatalf("identity: %v", err)
	}
	if err := s.SetMergeTrackingExcluded(prID, true); err != nil {
		t.Fatalf("exclude: %v", err)
	}
	if err := s.ClearMergeTrackingCooldown(prID); err != nil {
		t.Fatalf("clear cooldown: %v", err)
	}
	// A second mark (the evaluation after a Re-check) keeps the first time.
	if err := s.MarkMergeTrackingMerged(prID, time.Now().UTC()); err != nil {
		t.Fatalf("merged again: %v", err)
	}
	row, err := s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !row.TerminalAt.Equal(mergedAt.Truncate(time.Second)) {
		t.Errorf("terminal_at = %v, want the original merge time %v", row.TerminalAt, mergedAt)
	}

	n, err := s.PruneMergeTracking(time.Now().UTC().Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want the merged row despite the recent clicks", n)
	}
}

func TestPruneMergeTracking_KeepsRowsInsideTheWindow(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	if err := s.MarkMergeTrackingAbandoned(prID, "closed", time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	n, err := s.PruneMergeTracking(time.Now().UTC().Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 0 {
		t.Errorf("pruned %d rows, want 0 for a PR closed an hour ago", n)
	}
	row, err := s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.TerminalAt.IsZero() {
		t.Error("abandoning must record terminal_at")
	}
}

// Reviving an abandoned row (the PR was reopened) clears its terminal time, or
// the next close would inherit a stale one and be pruned immediately.
func TestEnsureMergeTracking_RevivalClearsTerminalAt(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	if err := s.MarkMergeTrackingAbandoned(prID, "closed", time.Now().UTC().Add(-72*time.Hour)); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	row, err := s.EnsureMergeTracking(prID, "acme/widgets", 7)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if row.Phase != store.MergePhaseIdle || !row.TerminalAt.IsZero() {
		t.Errorf("revived row: phase=%q terminal_at=%v, want idle with no terminal time", row.Phase, row.TerminalAt)
	}
}

func TestDeleteUntrackedMergeTracking(t *testing.T) {
	s, keepID := newMergeTrackingStore(t)
	now := time.Now().UTC()
	mk := func(ghID int64, repo string, number int) int64 {
		t.Helper()
		id, err := s.UpsertPR(&store.PR{GithubID: ghID, Repo: repo, Number: number, State: "open", UpdatedAt: now, FetchedAt: now})
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if _, err := s.EnsureMergeTracking(id, repo, number); err != nil {
			t.Fatalf("ensure: %v", err)
		}
		return id
	}
	dropID := mk(2, "acme/other", 1)
	mergedID := mk(3, "acme/other", 2)
	if err := s.MarkMergeTrackingMerged(mergedID, now); err != nil {
		t.Fatalf("merged: %v", err)
	}

	n, err := s.DeleteUntrackedMergeTracking([]string{"acme/widgets"})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d rows, want 1", n)
	}
	if _, err := s.GetMergeTracking(keepID); err != nil {
		t.Error("the tracked repo's row must stay")
	}
	if _, err := s.GetMergeTracking(dropID); err == nil {
		t.Error("the live row of an untracked repo must go")
	}
	if _, err := s.GetMergeTracking(mergedID); err != nil {
		t.Error("terminal rows are left to the retention prune")
	}

	// No active repo at all: every live row goes.
	if _, err := s.DeleteUntrackedMergeTracking(nil); err != nil {
		t.Fatalf("delete all: %v", err)
	}
	if _, err := s.GetMergeTracking(keepID); err == nil {
		t.Error("with nothing tracked, no live row survives")
	}
}

func TestRecordMergeTrackingDecision_PersistsMyPRsFields(t *testing.T) {
	s, prID := newMergeTrackingStore(t)
	activity := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	notified := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	if err := s.RecordMergeTrackingDecision(prID, store.MergeDecisionRecord{
		Phase: store.MergePhaseBlocked, Attention: "action",
		LastActivityAt: activity, StaleNotifiedAt: notified, At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// A later evaluation that learned neither timestamp keeps them.
	if err := s.RecordMergeTrackingDecision(prID, store.MergeDecisionRecord{
		Phase: store.MergePhaseIdle, Attention: "ready", At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record again: %v", err)
	}
	row, err := s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Attention != "ready" {
		t.Errorf("attention = %q, want ready", row.Attention)
	}
	if !row.LastActivityAt.Equal(activity) || !row.StaleNotifiedAt.Equal(notified) {
		t.Errorf("timestamps not kept: activity=%v notified=%v", row.LastActivityAt, row.StaleNotifiedAt)
	}
	if err := s.MarkMergeTrackingMerged(prID, time.Now().UTC()); err != nil {
		t.Fatalf("merged: %v", err)
	}
	if row, _ = s.GetMergeTracking(prID); row.Attention != "" {
		t.Errorf("a merged PR needs no attention, got %q", row.Attention)
	}
}

// An existing database gains terminal_at with a backfill from merged_at, or
// from updated_at for rows that never recorded a merge time, so the rows that
// were piling up are pruned on the first cycle after the upgrade.
func TestStore_Migration_BackfillsTerminalAt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE merge_tracking (
			pr_id INTEGER PRIMARY KEY, repo TEXT NOT NULL, number INTEGER NOT NULL,
			node_id TEXT NOT NULL DEFAULT '', phase TEXT NOT NULL DEFAULT 'idle',
			head_sha TEXT NOT NULL DEFAULT '', base_ref TEXT NOT NULL DEFAULT '', head_ref TEXT NOT NULL DEFAULT '',
			is_author INTEGER NOT NULL DEFAULT 0, is_assignee INTEGER NOT NULL DEFAULT 0, excluded INTEGER NOT NULL DEFAULT 0,
			auto_merge_armed_at TEXT NOT NULL DEFAULT '', auto_merge_head_sha TEXT NOT NULL DEFAULT '', auto_merge_method TEXT NOT NULL DEFAULT '',
			block_reason TEXT NOT NULL DEFAULT '', block_detail TEXT NOT NULL DEFAULT '', decision_json TEXT NOT NULL DEFAULT '',
			checks_required_failing INTEGER NOT NULL DEFAULT 0, checks_required_pending INTEGER NOT NULL DEFAULT 0,
			unknown_waits INTEGER NOT NULL DEFAULT 0, update_attempts INTEGER NOT NULL DEFAULT 0,
			conflict_attempts INTEGER NOT NULL DEFAULT 0, merge_attempts INTEGER NOT NULL DEFAULT 0,
			pre_rebase_sha TEXT NOT NULL DEFAULT '', last_attempt_at TEXT NOT NULL DEFAULT '', cooldown_until TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '', evaluated_at TEXT NOT NULL DEFAULT '', merged_at TEXT NOT NULL DEFAULT '',
			terminal_reason TEXT NOT NULL DEFAULT '', updated_at DATETIME NOT NULL
		)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO merge_tracking (pr_id, repo, number, phase, merged_at, updated_at) VALUES (1, 'a/b', 1, 'merged', '2026-08-01T10:00:00Z', '2026-09-29T10:00:00Z')`,
		`INSERT INTO merge_tracking (pr_id, repo, number, phase, updated_at) VALUES (2, 'a/b', 2, 'abandoned', '2026-08-02T10:00:00Z')`,
		`INSERT INTO merge_tracking (pr_id, repo, number, phase, updated_at) VALUES (3, 'a/b', 3, 'blocked', '2026-08-03T10:00:00Z')`,
	} {
		if _, err := legacy.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	legacy.Close()

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	want := map[int64]string{1: "2026-08-01T10:00:00Z", 2: "2026-08-02T10:00:00Z", 3: ""}
	for id, w := range want {
		row, err := s.GetMergeTracking(id)
		if err != nil {
			t.Fatalf("get %d: %v", id, err)
		}
		got := ""
		if !row.TerminalAt.IsZero() {
			got = row.TerminalAt.Format(time.RFC3339)
		}
		if got != w {
			t.Errorf("row %d terminal_at = %q, want %q", id, got, w)
		}
	}
}
