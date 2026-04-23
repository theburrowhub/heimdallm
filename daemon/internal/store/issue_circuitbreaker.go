package store

import (
	"fmt"
	"time"
)

// CountIssueReviewsForIssue returns the number of reviews for the given
// issue whose created_at is at or after `since`. Mirrors
// CountReviewsForPR from the PR side; used by the issue-triage circuit
// breaker to cap runaway re-triage loops (see theburrowhub/heimdallm#292).
func (s *Store) CountIssueReviewsForIssue(issueID int64, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM issue_reviews WHERE issue_id = ? AND created_at >= ?",
		issueID, since.UTC().Format(sqliteTimeFormat),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count issue reviews for issue: %w", err)
	}
	return n, nil
}

// CountIssueTriagesForRepo returns the number of triage reviews on ANY
// issue in the given repo whose created_at is at or after `since`.
// Powers the per-repo axis of CheckIssueCircuitBreaker.
func (s *Store) CountIssueTriagesForRepo(repo string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM issue_reviews r
		JOIN issues i ON r.issue_id = i.id
		WHERE i.repo = ? AND r.created_at >= ?`,
		repo, since.UTC().Format(sqliteTimeFormat),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count issue triages for repo: %w", err)
	}
	return n, nil
}

// IssueCircuitBreakerLimits is the configured set of caps for issue
// triage. Zero values mean "unlimited" for that axis, same contract as
// CircuitBreakerLimits on the PR side.
type IssueCircuitBreakerLimits struct {
	PerIssue24h int // max triages per issue in any 24h window
	PerRepoHr   int // max triages per repo in any 1h window
}

// CheckIssueCircuitBreaker returns (tripped, reason, err). When tripped
// is true, the caller MUST NOT proceed to spend Claude credits for this
// issue. reason is a human-readable explanation suitable for logs and
// SSE events. Mirrors CheckCircuitBreaker on the PR side.
func (s *Store) CheckIssueCircuitBreaker(issueID int64, repo string, cfg IssueCircuitBreakerLimits) (bool, string, error) {
	if cfg.PerIssue24h > 0 {
		n, err := s.CountIssueReviewsForIssue(issueID, time.Now().Add(-24*time.Hour))
		if err != nil {
			return false, "", err
		}
		if n >= cfg.PerIssue24h {
			return true, fmt.Sprintf("per-issue cap reached: %d triages in last 24h (cap %d)", n, cfg.PerIssue24h), nil
		}
	}
	if cfg.PerRepoHr > 0 && repo != "" {
		n, err := s.CountIssueTriagesForRepo(repo, time.Now().Add(-1*time.Hour))
		if err != nil {
			return false, "", err
		}
		if n >= cfg.PerRepoHr {
			return true, fmt.Sprintf("per-repo cap reached: %d issue triages on %s in last 1h (cap %d)", n, repo, cfg.PerRepoHr), nil
		}
	}
	return false, "", nil
}
