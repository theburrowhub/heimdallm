package store

import (
	"fmt"
	"time"
)

// Review represents a code review result stored locally and (when published) on GitHub.
type Review struct {
	ID             int64     `json:"id"`
	PRID           int64     `json:"pr_id"`
	CLIUsed        string    `json:"cli_used"`
	Summary        string    `json:"summary"`
	Issues         string    `json:"issues"`      // JSON array
	Suggestions    string    `json:"suggestions"` // JSON array
	Severity       string    `json:"severity"`
	CreatedAt      time.Time `json:"created_at"`
	GitHubReviewID int64     `json:"github_review_id"` // 0 = not yet published
}

// InsertReview inserts a new review record and returns its row ID.
func (s *Store) InsertReview(r *Review) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO reviews (pr_id, cli_used, summary, issues, suggestions, severity, created_at, github_review_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, r.PRID, r.CLIUsed, r.Summary, r.Issues, r.Suggestions, r.Severity,
		r.CreatedAt.UTC().Format(sqliteTimeFormat), r.GitHubReviewID,
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert review: %w", err)
	}
	return res.LastInsertId()
}

// ListUnpublishedReviews returns reviews not yet submitted to GitHub (github_review_id == 0).
func (s *Store) ListUnpublishedReviews() ([]*Review, error) {
	rows, err := s.db.Query(
		"SELECT id, pr_id, cli_used, summary, issues, suggestions, severity, created_at, github_review_id FROM reviews WHERE github_review_id=0 ORDER BY created_at ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("store: list unpublished: %w", err)
	}
	defer rows.Close()
	var reviews []*Review
	for rows.Next() {
		rev, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		reviews = append(reviews, rev)
	}
	return reviews, rows.Err()
}

// UpdateGitHubReviewID stores the GitHub review ID after successful submission.
func (s *Store) UpdateGitHubReviewID(reviewID, ghReviewID int64) error {
	_, err := s.db.Exec("UPDATE reviews SET github_review_id=? WHERE id=?", ghReviewID, reviewID)
	return err
}

// ListReviewsForPR returns all reviews for a given PR, ordered by created_at descending.
func (s *Store) ListReviewsForPR(prID int64) ([]*Review, error) {
	rows, err := s.db.Query(
		"SELECT id, pr_id, cli_used, summary, issues, suggestions, severity, created_at, github_review_id FROM reviews WHERE pr_id = ? ORDER BY created_at DESC",
		prID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list reviews: %w", err)
	}
	defer rows.Close()
	var reviews []*Review
	for rows.Next() {
		rev, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		reviews = append(reviews, rev)
	}
	return reviews, rows.Err()
}

// LatestReviewForPR returns the most recent review for a PR. Returns sql.ErrNoRows if none.
func (s *Store) LatestReviewForPR(prID int64) (*Review, error) {
	row := s.db.QueryRow(
		"SELECT id, pr_id, cli_used, summary, issues, suggestions, severity, created_at, github_review_id FROM reviews WHERE pr_id = ? ORDER BY created_at DESC LIMIT 1",
		prID,
	)
	return scanReview(row)
}

// PurgeOldReviews deletes reviews older than maxDays. No-op if maxDays == 0.
// The cutoff is computed in Go and passed as an RFC3339 string so that the
// comparison is consistent with how the modernc.org/sqlite driver stores values.
func (s *Store) PurgeOldReviews(maxDays int) error {
	if maxDays == 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-time.Duration(maxDays) * 24 * time.Hour).Format(sqliteTimeFormat)
	_, err := s.db.Exec("DELETE FROM reviews WHERE created_at < ?", cutoff)
	if err != nil {
		return fmt.Errorf("store: purge old reviews: %w", err)
	}
	return nil
}

func scanReview(s scanner) (*Review, error) {
	var rev Review
	var createdAt string
	var err error
	if err = s.Scan(&rev.ID, &rev.PRID, &rev.CLIUsed, &rev.Summary,
		&rev.Issues, &rev.Suggestions, &rev.Severity, &createdAt, &rev.GitHubReviewID); err != nil {
		return nil, fmt.Errorf("store: scan review: %w", err)
	}
	if rev.CreatedAt, err = time.Parse(sqliteTimeFormat, createdAt); err != nil {
		return nil, fmt.Errorf("store: parse created_at %q: %w", createdAt, err)
	}
	return &rev, nil
}
