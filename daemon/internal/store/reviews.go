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
	// BaseSHA is the base commit used to construct the reviewed diff. It lets
	// pre/post-submit and deferred-publish checks reject a base-branch race even
	// when the PR's head commit did not move.
	BaseSHA string `json:"base_sha"`
	// Event is the GitHub review event the daemon decided to submit
	// (APPROVE | COMMENT | REQUEST_CHANGES). Persisted so retry/publish paths
	// reproduce the exact decision even if config changes afterwards. Empty on
	// legacy rows — callers fall back to SeverityToEvent(Severity).
	Event string `json:"event"`
	// TimelineCursorID is the stable GitHub timeline event consumed by this
	// review. Zero denotes a legacy row without a remote-event cursor.
	TimelineCursorID int64 `json:"timeline_cursor_id"`
	// SuccessorPending records that publication raced a newer PR HEAD and that
	// the replacement commit still requires a review.
	SuccessorPending bool `json:"successor_pending"`
	// SuccessorEventID records positive review_requested evidence that must not
	// be cleared merely because a later timeline read is temporarily stale.
	SuccessorEventID int64 `json:"successor_event_id"`
	// AuthorizationSource explains why this result may be published:
	// requested, successor, or manual. Empty legacy rows are treated as
	// requested and therefore require live requested-reviewer state.
	AuthorizationSource string `json:"authorization_source"`
}

// reviewColumns is the canonical projection for every query decoded by
// scanReview. Keeping it in one place prevents schema additions from silently
// breaking only one of the Get/List/Latest retry paths.
const reviewColumns = "id, pr_id, cli_used, summary, issues, suggestions, severity, " +
	"created_at, published_at, github_review_id, github_review_state, head_sha, event, " +
	"base_sha, timeline_cursor_id, successor_pending, successor_event_id, authorization_source"

// InsertReview inserts a new review record and returns its row ID.
func (s *Store) InsertReview(r *Review) (int64, error) {
	publishedAt := ""
	if !r.PublishedAt.IsZero() {
		publishedAt = r.PublishedAt.UTC().Format(sqliteTimeFormat)
	}
	res, err := s.db.Exec(`
		INSERT INTO reviews (
			pr_id, cli_used, summary, issues, suggestions, severity, created_at,
			published_at, github_review_id, github_review_state, head_sha, event,
			base_sha, timeline_cursor_id, successor_pending, successor_event_id,
			authorization_source
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.PRID, r.CLIUsed, r.Summary, r.Issues, r.Suggestions, r.Severity,
		r.CreatedAt.UTC().Format(sqliteTimeFormat), publishedAt,
		r.GitHubReviewID, r.GitHubReviewState, r.HeadSHA, r.Event,
		r.BaseSHA, r.TimelineCursorID, boolToInt(r.SuccessorPending),
		r.SuccessorEventID, r.AuthorizationSource,
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert review: %w", err)
	}
	return res.LastInsertId()
}

// ListUnpublishedReviews returns reviews not yet submitted to GitHub (github_review_id == 0).
func (s *Store) ListUnpublishedReviews() ([]*Review, error) {
	rows, err := s.db.Query(
		"SELECT " + reviewColumns + " FROM reviews WHERE github_review_id=0 ORDER BY created_at ASC",
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
		"SELECT "+reviewColumns+" FROM reviews WHERE id = ?",
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

// SetReviewTimelineCursor records the exact GitHub timeline event observed by
// a legacy review row. This one-time backfill lets subsequent re-review checks
// use event identity rather than comparing clocks.
func (s *Store) SetReviewTimelineCursor(reviewID, cursorID int64) error {
	_, err := s.db.Exec(
		"UPDATE reviews SET timeline_cursor_id = ? WHERE id = ?",
		cursorID, reviewID,
	)
	if err != nil {
		return fmt.Errorf("store: set review timeline_cursor_id: %w", err)
	}
	return nil
}

// SetReviewSuccessorPending durably records whether a newer PR HEAD still
// requires review after this commit-addressed review has been submitted.
func (s *Store) SetReviewSuccessorPending(reviewID int64, pending bool) error {
	query := "UPDATE reviews SET successor_pending = ? WHERE id = ?"
	if !pending {
		query = "UPDATE reviews SET successor_pending = ?, successor_event_id = 0 WHERE id = ?"
	}
	_, err := s.db.Exec(query, boolToInt(pending), reviewID)
	if err != nil {
		return fmt.Errorf("store: set review successor_pending: %w", err)
	}
	return nil
}

// SetReviewSuccessorEvidence persists a positive review_requested event as the
// reason a successor remains owed. A later no-change read must not erase it;
// only an observed request removal or a successfully-created successor does.
func (s *Store) SetReviewSuccessorEvidence(reviewID, eventID int64) error {
	if eventID <= 0 {
		return fmt.Errorf("store: successor evidence requires a positive event id")
	}
	_, err := s.db.Exec(
		"UPDATE reviews SET successor_pending = 1, successor_event_id = ? WHERE id = ?",
		eventID, reviewID,
	)
	if err != nil {
		return fmt.Errorf("store: set review successor evidence: %w", err)
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
// Negative ghReviewID values are internal terminal sentinels (for example -1
// for an orphan that can never publish, or -2 for a deferred result superseded
// by a newer HEAD). They are excluded from unpublished retry scans while
// remaining distinguishable from real positive GitHub review IDs; publishedAt
// is still stored as a reference point.
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
		"SELECT "+reviewColumns+" FROM reviews WHERE pr_id = ? ORDER BY created_at DESC",
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
		"SELECT "+reviewColumns+" FROM reviews WHERE pr_id = ? ORDER BY created_at DESC LIMIT 1",
		prID,
	)
	return scanReview(row)
}

// PurgeOldReviews deletes reviews older than maxDays. No-op if maxDays <= 0
// (a negative value is a defensive no-op rather than a future cutoff that would
// wipe every review; config validation already rejects negatives — see #551).
// The cutoff is computed in Go and passed as an RFC3339 string so that the
// comparison is consistent with how the modernc.org/sqlite driver stores values.
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
	var successorPending int
	var err error
	if err = s.Scan(&rev.ID, &rev.PRID, &rev.CLIUsed, &rev.Summary,
		&rev.Issues, &rev.Suggestions, &rev.Severity, &createdAt, &publishedAt,
		&rev.GitHubReviewID, &rev.GitHubReviewState, &rev.HeadSHA, &rev.Event,
		&rev.BaseSHA, &rev.TimelineCursorID, &successorPending,
		&rev.SuccessorEventID, &rev.AuthorizationSource); err != nil {
		return nil, fmt.Errorf("store: scan review: %w", err)
	}
	rev.SuccessorPending = successorPending != 0
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
