package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func seedRetryPR(t *testing.T, s *store.Store, githubID int64) int64 {
	t.Helper()
	prID, err := s.UpsertPR(&store.PR{
		GithubID: githubID, Repo: "org/repo", Number: int(githubID),
		Title: "t", State: "open", UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("upsert PR: %v", err)
	}
	return prID
}

func TestReviewRetryBackoff_ExponentialDelayAndCap(t *testing.T) {
	s := newTestStore(t)
	prID := seedRetryPR(t, s, 1)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	wantDelays := []time.Duration{
		5 * time.Minute,
		10 * time.Minute,
		20 * time.Minute,
		40 * time.Minute,
		80 * time.Minute,
		160 * time.Minute,
		320 * time.Minute,
		6 * time.Hour,
		6 * time.Hour,
	}

	for i, wantDelay := range wantDelays {
		if err := s.AdvanceReviewRetryBackoff(prID, "sha", at); err != nil {
			t.Fatalf("advance %d: %v", i+1, err)
		}
		blocked, retryAt, attempts, err := s.CheckReviewRetryBackoff(prID, "sha", at)
		if err != nil {
			t.Fatalf("check %d: %v", i+1, err)
		}
		if !blocked {
			t.Fatalf("attempt %d: backoff not active", i+1)
		}
		if attempts != i+1 {
			t.Errorf("attempt %d: stored attempts = %d", i+1, attempts)
		}
		wantRetryAt := at.Add(wantDelay)
		if !retryAt.Equal(wantRetryAt) {
			t.Errorf("attempt %d: retryAt = %v, want %v", i+1, retryAt, wantRetryAt)
		}
		blockedAtBoundary, _, _, err := s.CheckReviewRetryBackoff(prID, "sha", wantRetryAt)
		if err != nil {
			t.Fatalf("boundary check %d: %v", i+1, err)
		}
		if blockedAtBoundary {
			t.Errorf("attempt %d: exact retry boundary is still blocked", i+1)
		}
		at = wantRetryAt
	}
}

func TestReviewRetryBackoff_IsScopedAndClearedExactly(t *testing.T) {
	s := newTestStore(t)
	firstPR := seedRetryPR(t, s, 11)
	secondPR := seedRetryPR(t, s, 12)
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.AdvanceReviewRetryBackoff(firstPR, "old-sha", now); err != nil {
		t.Fatal(err)
	}
	if blocked, _, _, err := s.CheckReviewRetryBackoff(firstPR, "new-sha", now); err != nil {
		t.Fatal(err)
	} else if blocked {
		t.Fatal("an old HEAD cooldown blocked a new HEAD")
	}
	if blocked, _, _, err := s.CheckReviewRetryBackoff(secondPR, "old-sha", now); err != nil {
		t.Fatal(err)
	} else if blocked {
		t.Fatal("one PR's cooldown blocked another PR")
	}

	if err := s.ClearReviewRetryBackoff(firstPR, "new-sha"); err != nil {
		t.Fatal(err)
	}
	if blocked, _, _, err := s.CheckReviewRetryBackoff(firstPR, "old-sha", now); err != nil {
		t.Fatal(err)
	} else if !blocked {
		t.Fatal("clearing a different HEAD removed the active cooldown")
	}
	if err := s.ClearReviewRetryBackoff(firstPR, "old-sha"); err != nil {
		t.Fatal(err)
	}
	if blocked, _, attempts, err := s.CheckReviewRetryBackoff(firstPR, "old-sha", now); err != nil {
		t.Fatal(err)
	} else if blocked || attempts != 0 {
		t.Fatalf("cleared cooldown = blocked %v, attempts %d", blocked, attempts)
	}
}

func TestReviewRetryBackoff_FailureRefreshesDelayOrigin(t *testing.T) {
	s := newTestStore(t)
	prID := seedRetryPR(t, s, 13)
	startedAt := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	failedAt := startedAt.Add(5 * time.Minute)

	if err := s.AdvanceReviewRetryBackoff(prID, "sha", startedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReviewRetryFailure(prID, "sha", failedAt); err != nil {
		t.Fatal(err)
	}
	blocked, retryAt, attempts, err := s.CheckReviewRetryBackoff(prID, "sha", failedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked || attempts != 1 || !retryAt.Equal(failedAt.Add(5*time.Minute)) {
		t.Fatalf("refreshed cooldown = blocked %v, retryAt %v, attempts %d", blocked, retryAt, attempts)
	}
}

func TestReviewRetryBackoff_PersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "retry.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	prID := seedRetryPR(t, s, 21)
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.AdvanceReviewRetryBackoff(prID, "sha", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	blocked, retryAt, attempts, err := s.CheckReviewRetryBackoff(prID, "sha", now)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked || attempts != 1 || !retryAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("reopened cooldown = blocked %v, retryAt %v, attempts %d", blocked, retryAt, attempts)
	}
}

func TestPruneReviewRetryBackoffs_RemovesOnlyExpiredState(t *testing.T) {
	s := newTestStore(t)
	oldPR := seedRetryPR(t, s, 31)
	recentPR := seedRetryPR(t, s, 32)
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.AdvanceReviewRetryBackoff(oldPR, "sha", now.Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceReviewRetryBackoff(recentPR, "sha", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	removed, err := s.PruneReviewRetryBackoffs(now.Add(-48 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, _, attempts, err := s.CheckReviewRetryBackoff(oldPR, "sha", now); err != nil {
		t.Fatal(err)
	} else if attempts != 0 {
		t.Fatalf("old attempts = %d, want pruned", attempts)
	}
	if _, _, attempts, err := s.CheckReviewRetryBackoff(recentPR, "sha", now); err != nil {
		t.Fatal(err)
	} else if attempts != 1 {
		t.Fatalf("recent attempts = %d, want retained", attempts)
	}
}
