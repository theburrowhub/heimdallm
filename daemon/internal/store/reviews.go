package store

import (
	"fmt"
	"time"
)

// Review represents a code review result stored locally and (when published) on GitHub.
//
// GitHubReviewID and GitHubReviewState are populated together after a successful
// SubmitReview call. GitHubReviewState is one of the GitHub pull-request review
// states (APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED, PENDING) — empty
// string on legacy rows and before publish. The web UI renders a
// review-decision badge from this field rather than deriving it from severity,
// so the displayed state reflects exactly what the daemon told GitHub.
type Review struct {
	ID          int64     `json:"id"`
	PRID        int64     `json:"pr_id"`
	CLIUsed     string    `json:"cli_used"`
	Summary     string    `json:"summary"`
	Issues      string    `json:"issues"`      // JSON array
	Suggestions string    `json:"suggestions"` // JSON array
	Severity    string    `json:"severity"`
	CreatedAt   time.Time `json:"created_at"`
	// PublishedAt is the local clock time immediately after SubmitReview
	// returned a success. Anchor for the updated_at dedup — using CreatedAt
	// made the 30s grace useless because CreatedAt is stamped BEFORE the
	// Claude call, so for any review taking longer than 30s the grace had
	// already expired when the review actually hit GitHub. See
	// theburrowhub/heimdallm#243.
	PublishedAt       time.Time `json:"published_at"`
	GitHubReviewID    int64     `json:"github_review_id"`    // 0 = not yet published
	GitHubReviewState string    `json:"github_review_state"` // '' until published
	// HeadSHA is the PR's HEAD commit at review time. Used as the authoritative
	// dedup key: if we've already reviewed this SHA, peer-bot review submissions
	// bumping the PR's updated_at must not trigger a re-review.
	HeadSHA string `json:"head_sha"`
}

// InsertReview inserts a new review record and returns its row ID.
func (s *Store) InsertReview(r *Review) (int64, error) {
	publishedAt := ""
	if !r.PublishedAt.IsZero() {
		publishedAt = r.PublishedAt.UTC().Format(sqliteTimeFormat)
	}
	res, err := s.db.Exec(`
		INSERT INTO reviews (pr_id, cli_used, summary, issues, suggestions, severity, created_at, published_at, github_review_id, github_review_state, head_sha)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.PRID, r.CLIUsed, r.Summary, r.Issues, r.Suggestions, r.Severity,
		r.CreatedAt.UTC().Format(sqliteTimeFormat), publishedAt,
		r.GitHubReviewID, r.GitHubReviewState, r.HeadSHA,
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert review: %w", err)
	}
	return res.LastInsertId()
}

// ListUnpublishedReviews returns reviews not yet submitted to GitHub (github_review_id == 0).
func (s *Store) ListUnpublishedReviews() ([]*Review, error) {
	rows, err := s.db.Query(
		"SELECT id, pr_id, cli_used, summary, issues, suggestions, severity, created_at, published_at, github_review_id, github_review_state, head_sha FROM reviews WHERE github_review_id=0 ORDER BY created_at ASC",
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

// GetReview returns a single review by its local row ID.
func (s *Store) GetReview(id int64) (*Review, error) {
	row := s.db.QueryRow(
		"SELECT id, pr_id, cli_used, summary, issues, suggestions, severity, created_at, published_at, github_review_id, github_review_state, head_sha FROM reviews WHERE id = ?",
		id,
	)
	return scanReview(row)
}

// UpdateReviewHeadSHA backfills the head_sha column on a legacy review row.
// Used by the pipeline's fail-closed dedup: if a previous review had no SHA
// (from before the column was populated), we populate it from the current
// snapshot instead of proceeding to a full re-review.
func (s *Store) UpdateReviewHeadSHA(reviewID int64, headSHA string) error {
	_, err := s.db.Exec("UPDATE reviews SET head_sha = ? WHERE id = ?", headSHA, reviewID)
	if err != nil {
		return fmt.Errorf("store: update review head_sha: %w", err)
	}
	return nil
}

// MarkReviewPublished records the GitHub review ID, state, and local post
// timestamp after a successful SubmitReview. publishedAt is stamped by the
// caller immediately after the API returned; anchoring the dedup window on
// this value (not on the row's CreatedAt, which precedes the Claude call)
// is what closes the 2026-04-22 cost-runaway regression. See
// theburrowhub/heimdallm#243.
//
// Pass the sentinel pair (-1, "") for ghReviewID / ghReviewState to mark an
// orphan review that can never publish (e.g. the source PR's repo was
// unset); publishedAt is still stored as a reference point.
func (s *Store) MarkReviewPublished(reviewID, ghReviewID int64, ghReviewState string, publishedAt time.Time) error {
	_, err := s.db.Exec(
		"UPDATE reviews SET github_review_id=?, github_review_state=?, published_at=? WHERE id=?",
		ghReviewID, ghReviewState,
		publishedAt.UTC().Format(sqliteTimeFormat), reviewID,
	)
	return err
}

// ListReviewsForPR returns all reviews for a given PR, ordered by created_at descending.
func (s *Store) ListReviewsForPR(prID int64) ([]*Review, error) {
	rows, err := s.db.Query(
		"SELECT id, pr_id, cli_used, summary, issues, suggestions, severity, created_at, published_at, github_review_id, github_review_state, head_sha FROM reviews WHERE pr_id = ? ORDER BY created_at DESC",
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
		"SELECT id, pr_id, cli_used, summary, issues, suggestions, severity, created_at, published_at, github_review_id, github_review_state, head_sha FROM reviews WHERE pr_id = ? ORDER BY created_at DESC LIMIT 1",
		prID,
	)
	return scanReview(row)
}

// PurgeOldReviews deletes reviews older than maxDays. No-op if maxDays <= 0.
// The cutoff is computed in Go and passed as an RFC3339 string so that the
// comparison is consistent with how the modernc.org/sqlite driver stores values.
//
// A negative maxDays is treated as a no-op rather than computing a future
// cutoff (which would delete every review). Config validation already rejects
// negative retention, so this is a defensive last barrier before a destructive
// DELETE.
func (s *Store) PurgeOldReviews(maxDays int) error {
	if maxDays <= 0 {
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
	var createdAt, publishedAt string
	var err error
	if err = s.Scan(&rev.ID, &rev.PRID, &rev.CLIUsed, &rev.Summary,
		&rev.Issues, &rev.Suggestions, &rev.Severity, &createdAt, &publishedAt,
		&rev.GitHubReviewID, &rev.GitHubReviewState, &rev.HeadSHA); err != nil {
		return nil, fmt.Errorf("store: scan review: %w", err)
	}
	if rev.CreatedAt, err = time.Parse(sqliteTimeFormat, createdAt); err != nil {
		return nil, fmt.Errorf("store: parse created_at %q: %w", createdAt, err)
	}
	// Legacy rows (pre-migration) store an empty string for published_at;
	// leave rev.PublishedAt at its zero value so callers can detect the
	// "unknown, fall back to CreatedAt" case explicitly.
	if publishedAt != "" {
		if rev.PublishedAt, err = time.Parse(sqliteTimeFormat, publishedAt); err != nil {
			return nil, fmt.Errorf("store: parse published_at %q: %w", publishedAt, err)
		}
	}
	return &rev, nil
}
