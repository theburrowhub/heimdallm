package store_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

// TestSetIssueClaimedByAutonomous verifies the claimed_by_autonomous flag
// can be set and retrieved round-trip.
func TestSetIssueClaimedByAutonomous(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	id, err := s.UpsertIssue(&store.Issue{
		GithubID: 5001, Repo: "org/r", Number: 1, Title: "t", Author: "a",
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	// Default should be false.
	claimed, err := s.IsIssueClaimedByAutonomous(id)
	if err != nil {
		t.Fatalf("IsIssueClaimedByAutonomous initial: %v", err)
	}
	if claimed {
		t.Error("expected claimed_by_autonomous = false for new issue")
	}

	// Set to true.
	if err := s.SetIssueClaimedByAutonomous(id, true); err != nil {
		t.Fatalf("SetIssueClaimedByAutonomous(true): %v", err)
	}
	claimed, err = s.IsIssueClaimedByAutonomous(id)
	if err != nil {
		t.Fatalf("IsIssueClaimedByAutonomous after set true: %v", err)
	}
	if !claimed {
		t.Error("expected claimed_by_autonomous = true after set")
	}

	// Set back to false.
	if err := s.SetIssueClaimedByAutonomous(id, false); err != nil {
		t.Fatalf("SetIssueClaimedByAutonomous(false): %v", err)
	}
	claimed, err = s.IsIssueClaimedByAutonomous(id)
	if err != nil {
		t.Fatalf("IsIssueClaimedByAutonomous after set false: %v", err)
	}
	if claimed {
		t.Error("expected claimed_by_autonomous = false after clearing")
	}
}

// TestHasOpenAutoImplementPR verifies the three key scenarios for the
// autonomous selector's "not started" predicate.
func TestHasOpenAutoImplementPR(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	// Seed an issue with github_id = 7001. Capture the STORE ROW ID: that is
	// what production links into prs.auto_implement_issue_id (the return of
	// UpsertIssue passed to MarkPRAutoImplementOrigin), NOT the github id.
	const issueGithubID int64 = 7001
	issueRowID, err := s.UpsertIssue(&store.Issue{
		GithubID: issueGithubID, Repo: "org/r", Number: 10, Title: "feat", Author: "alice",
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	// --- Case 1: no PR at all → false ---
	has, err := s.HasOpenAutoImplementPR(issueGithubID)
	if err != nil {
		t.Fatalf("HasOpenAutoImplementPR (no PR): %v", err)
	}
	if has {
		t.Error("expected false when no PR exists")
	}

	// --- Case 1b: unknown github id → false (subquery yields NULL safely) ---
	has, err = s.HasOpenAutoImplementPR(999999)
	if err != nil {
		t.Fatalf("HasOpenAutoImplementPR (unknown github id): %v", err)
	}
	if has {
		t.Error("expected false for an unknown github id")
	}

	// --- Case 2: open PR linked to the issue STORE ROW ID → true ---
	prID, err := s.UpsertPR(&store.PR{
		GithubID: 8001, Repo: "org/r", Number: 20, Title: "auto-impl",
		Author: "bot", URL: "https://github.com/org/r/pull/20",
		State: "open", UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	// Mirror production: link by store row id, query by github id.
	if err := s.MarkPRAutoImplementOrigin(prID, issueRowID); err != nil {
		t.Fatalf("MarkPRAutoImplementOrigin: %v", err)
	}

	has, err = s.HasOpenAutoImplementPR(issueGithubID)
	if err != nil {
		t.Fatalf("HasOpenAutoImplementPR (open PR): %v", err)
	}
	if !has {
		t.Error("expected true when an open auto_implement PR is linked")
	}

	// --- Case 3: PR is closed → false ---
	if err := s.UpdatePRState(prID, "closed"); err != nil {
		t.Fatalf("UpdatePRState(closed): %v", err)
	}
	has, err = s.HasOpenAutoImplementPR(issueGithubID)
	if err != nil {
		t.Fatalf("HasOpenAutoImplementPR (closed PR): %v", err)
	}
	if has {
		t.Error("expected false when the linked PR is closed")
	}

	// --- Case 4: issue with TWO linked PRs (one closed, one open) → true ---
	// prID above is now closed; add a second OPEN PR linked to the same
	// issue store row id. The open one must make the predicate true.
	openPRID, err := s.UpsertPR(&store.PR{
		GithubID: 8002, Repo: "org/r", Number: 21, Title: "auto-impl-2",
		Author: "bot", URL: "https://github.com/org/r/pull/21",
		State: "open", UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert second pr: %v", err)
	}
	if err := s.MarkPRAutoImplementOrigin(openPRID, issueRowID); err != nil {
		t.Fatalf("MarkPRAutoImplementOrigin (second): %v", err)
	}
	has, err = s.HasOpenAutoImplementPR(issueGithubID)
	if err != nil {
		t.Fatalf("HasOpenAutoImplementPR (one closed + one open): %v", err)
	}
	if !has {
		t.Error("expected true when one of the linked PRs is open")
	}
}
