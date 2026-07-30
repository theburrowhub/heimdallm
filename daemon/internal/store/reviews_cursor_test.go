package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func TestReview_TimelineCursorAndSuccessorPendingRoundTrip(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	prID, err := s.UpsertPR(&store.PR{
		GithubID:  41,
		Repo:      "org/repo",
		Number:    7,
		Title:     "cursor persistence",
		Author:    "alice",
		URL:       "https://github.com/org/repo/pull/7",
		State:     "open",
		UpdatedAt: now,
		FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}

	reviewID, err := s.InsertReview(&store.Review{
		PRID:                prID,
		CLIUsed:             "codex",
		Summary:             "reviewed",
		Issues:              "[]",
		Suggestions:         "[]",
		Severity:            "low",
		CreatedAt:           now,
		HeadSHA:             "abc123",
		BaseSHA:             "base456",
		Event:               "COMMENT",
		TimelineCursorID:    987654321,
		SuccessorPending:    true,
		SuccessorEventID:    123456789,
		AuthorizationSource: "successor",
	})
	if err != nil {
		t.Fatalf("InsertReview: %v", err)
	}

	assertCursorState := func(source string, got *store.Review, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		if got.TimelineCursorID != 987654321 {
			t.Errorf("%s TimelineCursorID = %d, want 987654321", source, got.TimelineCursorID)
		}
		if !got.SuccessorPending {
			t.Errorf("%s SuccessorPending = false, want true", source)
		}
		if got.BaseSHA != "base456" {
			t.Errorf("%s BaseSHA = %q, want base456", source, got.BaseSHA)
		}
		if got.SuccessorEventID != 123456789 {
			t.Errorf("%s SuccessorEventID = %d, want 123456789", source, got.SuccessorEventID)
		}
		if got.AuthorizationSource != "successor" {
			t.Errorf("%s AuthorizationSource = %q, want successor", source, got.AuthorizationSource)
		}
	}

	got, err := s.GetReview(reviewID)
	assertCursorState("GetReview", got, err)

	got, err = s.LatestReviewForPR(prID)
	assertCursorState("LatestReviewForPR", got, err)

	reviews, err := s.ListReviewsForPR(prID)
	if err != nil {
		t.Fatalf("ListReviewsForPR: %v", err)
	}
	if len(reviews) != 1 {
		t.Fatalf("ListReviewsForPR length = %d, want 1", len(reviews))
	}
	assertCursorState("ListReviewsForPR", reviews[0], nil)

	unpublished, err := s.ListUnpublishedReviews()
	if err != nil {
		t.Fatalf("ListUnpublishedReviews: %v", err)
	}
	if len(unpublished) != 1 {
		t.Fatalf("ListUnpublishedReviews length = %d, want 1", len(unpublished))
	}
	assertCursorState("ListUnpublishedReviews", unpublished[0], nil)

	if err := s.SetReviewSuccessorPending(reviewID, false); err != nil {
		t.Fatalf("SetReviewSuccessorPending(false): %v", err)
	}
	got, err = s.GetReview(reviewID)
	if err != nil {
		t.Fatalf("GetReview after SetReviewSuccessorPending: %v", err)
	}
	if got.SuccessorPending {
		t.Error("SuccessorPending = true after clearing it")
	}
	if got.SuccessorEventID != 0 {
		t.Errorf("SuccessorEventID = %d after clearing, want 0", got.SuccessorEventID)
	}
	if got.BaseSHA != "base456" {
		t.Errorf("BaseSHA changed while clearing successor: got %q", got.BaseSHA)
	}
	if got.TimelineCursorID != 987654321 {
		t.Errorf("TimelineCursorID changed while clearing successor: got %d", got.TimelineCursorID)
	}
}

func TestStore_Migration_AddsReviewCursorAndSuccessorColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-reviews.db")
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE reviews (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			pr_id       INTEGER NOT NULL,
			cli_used    TEXT NOT NULL,
			summary     TEXT NOT NULL,
			issues      TEXT NOT NULL,
			suggestions TEXT NOT NULL,
			severity    TEXT NOT NULL,
			created_at  DATETIME NOT NULL
		)
	`); err != nil {
		legacy.Close()
		t.Fatalf("create legacy reviews table: %v", err)
	}
	createdAt := time.Now().UTC().Truncate(time.Second)
	if _, err := legacy.Exec(`
		INSERT INTO reviews (
			pr_id, cli_used, summary, issues, suggestions, severity, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, 1, "claude", "legacy", "[]", "[]", "low", createdAt.Format(time.RFC3339)); err != nil {
		legacy.Close()
		t.Fatalf("insert legacy review: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	got, err := s.GetReview(1)
	if err != nil {
		t.Fatalf("GetReview after migration: %v", err)
	}
	if got.TimelineCursorID != 0 {
		t.Errorf("legacy TimelineCursorID = %d, want 0", got.TimelineCursorID)
	}
	if got.SuccessorPending {
		t.Error("legacy SuccessorPending = true, want false")
	}
	if got.BaseSHA != "" {
		t.Errorf("legacy BaseSHA = %q, want empty", got.BaseSHA)
	}
	if got.SuccessorEventID != 0 {
		t.Errorf("legacy SuccessorEventID = %d, want 0", got.SuccessorEventID)
	}
	if got.AuthorizationSource != "" {
		t.Errorf("legacy AuthorizationSource = %q, want empty", got.AuthorizationSource)
	}

	if err := s.SetReviewSuccessorPending(got.ID, true); err != nil {
		t.Fatalf("SetReviewSuccessorPending after migration: %v", err)
	}
	got, err = s.GetReview(got.ID)
	if err != nil {
		t.Fatalf("GetReview after successor update: %v", err)
	}
	if !got.SuccessorPending {
		t.Error("migrated SuccessorPending remained false after update")
	}

	if err := s.SetReviewSuccessorEvidence(got.ID, 777); err != nil {
		t.Fatalf("SetReviewSuccessorEvidence after migration: %v", err)
	}
	got, err = s.GetReview(got.ID)
	if err != nil {
		t.Fatalf("GetReview after successor evidence: %v", err)
	}
	if !got.SuccessorPending || got.SuccessorEventID != 777 {
		t.Errorf("migrated successor evidence = (pending=%v,event=%d), want (true,777)",
			got.SuccessorPending, got.SuccessorEventID)
	}
}
