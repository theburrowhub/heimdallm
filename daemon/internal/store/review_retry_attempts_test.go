package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func seedRetryPRInRepo(t *testing.T, s *store.Store, githubID int64, repo string) int64 {
	t.Helper()
	prID, err := s.UpsertPR(&store.PR{
		GithubID: githubID, Repo: repo, Number: int(githubID),
		Title: "t", State: "open", UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("upsert PR: %v", err)
	}
	return prID
}

func TestReviewRetryAttempts_RepoLimitCountsOnlyUnclearedRecentReservations(t *testing.T) {
	s := newTestStore(t)
	first := seedRetryPRInRepo(t, s, 101, "org/repo")
	second := seedRetryPRInRepo(t, s, 102, "org/repo")
	third := seedRetryPRInRepo(t, s, 103, "org/repo")
	other := seedRetryPRInRepo(t, s, 104, "org/other")
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	old, err := s.ReserveReviewRetryAttempt(first, "org/repo", "old", now.Add(-2*time.Hour), 2)
	if err != nil || old.Blocked || old.ID == 0 {
		t.Fatalf("old reservation = %#v, error %v", old, err)
	}
	one, err := s.ReserveReviewRetryAttempt(first, "org/repo", "sha-1", now, 2)
	if err != nil || one.Blocked || one.ID == 0 {
		t.Fatalf("first reservation = %#v, error %v", one, err)
	}
	two, err := s.ReserveReviewRetryAttempt(second, "org/repo", "sha-2", now, 2)
	if err != nil || two.Blocked || two.ID == 0 {
		t.Fatalf("second reservation = %#v, error %v", two, err)
	}

	blocked, err := s.ReserveReviewRetryAttempt(third, "org/repo", "sha-3", now, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked.Blocked || blocked.ID != 0 || blocked.FailureCount != 2 ||
		!blocked.RetryAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("blocked reservation = %#v", blocked)
	}

	// The limit is repository-scoped, and a manual/disabled-limit admission
	// still records a reservation so future automatic checks can see its cost.
	otherReservation, err := s.ReserveReviewRetryAttempt(other, "org/other", "sha", now, 1)
	if err != nil || otherReservation.Blocked || otherReservation.ID == 0 {
		t.Fatalf("other repo reservation = %#v, error %v", otherReservation, err)
	}
	forced, err := s.ReserveReviewRetryAttempt(third, "org/repo", "forced", now, 0)
	if err != nil || forced.Blocked || forced.ID == 0 {
		t.Fatalf("unlimited reservation = %#v, error %v", forced, err)
	}

	// A successful execution clears only its own provisional row. Old failures
	// remain charged, but freeing one recent reservation permits one admission.
	if err := s.ClearReviewRetryAttempt(one.ID); err != nil {
		t.Fatal(err)
	}
	stillBlocked, err := s.ReserveReviewRetryAttempt(third, "org/repo", "sha-3", now, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !stillBlocked.Blocked || stillBlocked.FailureCount != 2 {
		t.Fatalf("forced failure disappeared after clearing another row: %#v", stillBlocked)
	}
	if err := s.ClearReviewRetryAttempt(forced.ID); err != nil {
		t.Fatal(err)
	}
	allowed, err := s.ReserveReviewRetryAttempt(third, "org/repo", "sha-3", now, 2)
	if err != nil || allowed.Blocked || allowed.ID == 0 {
		t.Fatalf("reservation after successful clear = %#v, error %v", allowed, err)
	}
}

func TestReviewRetryAttempts_PruneAndErrors(t *testing.T) {
	t.Run("prune", func(t *testing.T) {
		s := newTestStore(t)
		prID := seedRetryPRInRepo(t, s, 201, "org/repo")
		now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
		if _, err := s.ReserveReviewRetryAttempt(prID, "org/repo", "old", now.Add(-72*time.Hour), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReserveReviewRetryAttempt(prID, "org/repo", "recent", now, 0); err != nil {
			t.Fatal(err)
		}
		removed, err := s.PruneReviewRetryAttempts(now.Add(-48 * time.Hour))
		if err != nil || removed != 1 {
			t.Fatalf("prune = %d, error %v", removed, err)
		}
		blocked, err := s.ReserveReviewRetryAttempt(prID, "org/repo", "next", now, 1)
		if err != nil || !blocked.Blocked || blocked.FailureCount != 1 {
			t.Fatalf("recent row after prune = %#v, error %v", blocked, err)
		}
	})

	t.Run("malformed oldest timestamp", func(t *testing.T) {
		s := newTestStore(t)
		prID := seedRetryPRInRepo(t, s, 202, "org/repo")
		if _, err := s.DB().Exec(`
			INSERT INTO review_retry_attempts (pr_id, head_sha, started_at)
			VALUES (?, ?, ?)`, prID, "sha", "not-a-time"); err != nil {
			t.Fatal(err)
		}
		_, err := s.ReserveReviewRetryAttempt(prID, "org/repo", "next", time.Now(), 1)
		if err == nil || !strings.Contains(err.Error(), "parse oldest review retry attempt") {
			t.Fatalf("malformed timestamp error = %v", err)
		}
	})

	t.Run("closed database", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReserveReviewRetryAttempt(1, "org/repo", "sha", time.Now(), 1); err == nil {
			t.Fatal("reservation on closed database returned nil error")
		}
		if err := s.ClearReviewRetryAttempt(1); err == nil {
			t.Fatal("clear on closed database returned nil error")
		}
		if _, err := s.PruneReviewRetryAttempts(time.Now()); err == nil {
			t.Fatal("prune on closed database returned nil error")
		}
	})

	t.Run("invalid foreign key", func(t *testing.T) {
		s := newTestStore(t)
		if _, err := s.DB().Exec("PRAGMA foreign_keys = ON"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReserveReviewRetryAttempt(999, "", "sha", time.Now(), 0); err == nil ||
			!strings.Contains(err.Error(), "reserve review retry attempt") {
			t.Fatalf("invalid foreign-key reservation error = %v", err)
		}
		if err := s.ClearReviewRetryAttempt(0); err != nil {
			t.Fatalf("zero reservation clear = %v", err)
		}
	})
}
