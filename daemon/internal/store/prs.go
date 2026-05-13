package store

import (
	"fmt"
	"strings"
	"time"
)

// PR represents a GitHub pull request stored locally.
type PR struct {
	ID        int64     `json:"id"`
	GithubID  int64     `json:"github_id"`
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Author    string    `json:"author"`
	URL       string    `json:"url"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
	FetchedAt time.Time `json:"fetched_at"`
	Dismissed bool      `json:"dismissed"`

	// Review-state vigilance for auto_implement-created PRs (#482). All
	// fields are zero-valued on standard PRs (not created by
	// auto_implement) and stay that way — only Tier 3's review-state
	// path and the Responder/FixRunner modules write here.
	ExternalReviewState  string    `json:"external_review_state,omitempty"`
	ExternalReviewer     string    `json:"external_reviewer,omitempty"`
	ExternalReviewAt     time.Time `json:"external_review_at,omitempty"`
	AutoImplementIssueID int64     `json:"auto_implement_issue_id,omitempty"`
	ReviewResponseCount  int       `json:"review_response_count,omitempty"`
	ReviewFixCount       int       `json:"review_fix_count,omitempty"`
	LastRespondedAt      time.Time `json:"last_responded_at,omitempty"`
}

// UpsertPR inserts or updates a PR record, keyed on github_id. Returns the row ID.
// Note: dismissed is intentionally excluded from the UPDATE clause so a user's
// dismiss choice is preserved even when the poll loop re-fetches the same PR.
func (s *Store) UpsertPR(pr *PR) (int64, error) {
	_, err := s.db.Exec(`
		INSERT INTO prs (github_id, repo, number, title, author, url, state, updated_at, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(github_id) DO UPDATE SET
			repo=excluded.repo, number=excluded.number, title=excluded.title,
			author=excluded.author, url=excluded.url, state=excluded.state,
			updated_at=excluded.updated_at, fetched_at=excluded.fetched_at
	`, pr.GithubID, pr.Repo, pr.Number, pr.Title, pr.Author, pr.URL, pr.State,
		pr.UpdatedAt.UTC().Format(sqliteTimeFormat),
		pr.FetchedAt.UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return 0, fmt.Errorf("store: upsert pr: %w", err)
	}
	// Always look up the row by github_id rather than trusting LastInsertId:
	// modernc.org/sqlite returns the database-wide last inserted rowid, which
	// on the UPDATE path can be the id of a row in a different table (e.g.
	// reviews) — returning that value corrupted the circuit-breaker counts in
	// issue #243. The unique index on github_id makes this SELECT cheap.
	var id int64
	row := s.db.QueryRow("SELECT id FROM prs WHERE github_id = ?", pr.GithubID)
	if err := row.Scan(&id); err != nil {
		return 0, fmt.Errorf("store: upsert pr select: %w", err)
	}
	return id, nil
}

// prColumns is the canonical column list for PR SELECTs, kept on a single
// line so adding a column only touches the schema + this constant + scanPR.
const prColumns = "id, github_id, repo, number, title, author, url, state, " +
	"updated_at, fetched_at, dismissed, " +
	"external_review_state, external_reviewer, external_review_at, " +
	"auto_implement_issue_id, review_response_count, review_fix_count, " +
	"last_responded_at"

// GetPR retrieves a PR by its local row ID.
func (s *Store) GetPR(id int64) (*PR, error) {
	row := s.db.QueryRow("SELECT "+prColumns+" FROM prs WHERE id = ?", id)
	return scanPR(row)
}

// GetPRByGithubID retrieves a PR by its GitHub PR ID.
func (s *Store) GetPRByGithubID(githubID int64) (*PR, error) {
	row := s.db.QueryRow("SELECT "+prColumns+" FROM prs WHERE github_id = ?", githubID)
	return scanPR(row)
}

// GetPRByRepoNumber retrieves a PR by its stable repository-local identity.
// GitHub's Search Issues API and Pulls API can return different global IDs for
// the same PR, so scheduler-side dedup needs this fallback after github_id
// misses. If duplicate rows exist from an older bug, prefer the row that
// already has review history so we dedup against the real prior work.
func (s *Store) GetPRByRepoNumber(repo string, number int) (*PR, error) {
	row := s.db.QueryRow(`
		SELECT `+prColumns+`
		FROM prs p
		WHERE p.repo = ? AND p.number = ?
		ORDER BY (
			SELECT COALESCE(MAX(r.created_at), '')
			FROM reviews r
			WHERE r.pr_id = p.id
		) DESC, p.fetched_at DESC, p.id DESC
		LIMIT 1
	`, repo, number)
	return scanPR(row)
}

// ListPRs returns all non-dismissed PRs ordered by updated_at descending.
// An optional list of states (e.g. "open", "closed") filters the result;
// when no states are provided all non-dismissed PRs are returned.
func (s *Store) ListPRs(states ...string) ([]*PR, error) {
	query := "SELECT " + prColumns + " FROM prs WHERE dismissed = 0"
	var args []any
	if len(states) > 0 {
		placeholders := strings.Repeat("?,", len(states))
		placeholders = placeholders[:len(placeholders)-1] // trim trailing comma
		query += " AND state IN (" + placeholders + ")"
		for _, st := range states {
			args = append(args, st)
		}
	}
	query += " ORDER BY updated_at DESC"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list prs: %w", err)
	}
	defer rows.Close()
	var prs []*PR
	for rows.Next() {
		pr, err := scanPR(rows)
		if err != nil {
			return nil, err
		}
		prs = append(prs, pr)
	}
	return prs, rows.Err()
}

// ListOpenPRs is a convenience wrapper that returns only non-dismissed PRs
// with state "open".
func (s *Store) ListOpenPRs() ([]*PR, error) {
	return s.ListPRs("open")
}

// UpdatePRState updates the state of a PR by its local row ID.
func (s *Store) UpdatePRState(id int64, state string) error {
	_, err := s.db.Exec("UPDATE prs SET state = ? WHERE id = ?", state, id)
	if err != nil {
		return fmt.Errorf("store: update pr state %d: %w", id, err)
	}
	return nil
}

// UpdatePRStateByGithubID updates the state of a PR by its GitHub PR ID.
// This is used by Tier 3 which supplies a github_id rather than the local id.
func (s *Store) UpdatePRStateByGithubID(githubID int64, state string) error {
	_, err := s.db.Exec("UPDATE prs SET state = ? WHERE github_id = ?", state, githubID)
	if err != nil {
		return fmt.Errorf("store: update pr state by github_id %d: %w", githubID, err)
	}
	return nil
}

// DismissPR marks a PR as dismissed so it no longer appears in the dashboard
// or triggers auto-reviews.
func (s *Store) DismissPR(id int64) error {
	_, err := s.db.Exec("UPDATE prs SET dismissed = 1 WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: dismiss pr %d: %w", id, err)
	}
	return nil
}

// UndismissPR restores a previously dismissed PR.
func (s *Store) UndismissPR(id int64) error {
	_, err := s.db.Exec("UPDATE prs SET dismissed = 0 WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: undismiss pr %d: %w", id, err)
	}
	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanPR(s scanner) (*PR, error) {
	var pr PR
	var updatedAt, fetchedAt string
	var externalReviewAt, lastRespondedAt string
	var dismissed int
	var err error
	if err = s.Scan(&pr.ID, &pr.GithubID, &pr.Repo, &pr.Number, &pr.Title,
		&pr.Author, &pr.URL, &pr.State, &updatedAt, &fetchedAt, &dismissed,
		&pr.ExternalReviewState, &pr.ExternalReviewer, &externalReviewAt,
		&pr.AutoImplementIssueID, &pr.ReviewResponseCount, &pr.ReviewFixCount,
		&lastRespondedAt,
	); err != nil {
		return nil, fmt.Errorf("store: scan pr: %w", err)
	}
	if pr.UpdatedAt, err = time.Parse(sqliteTimeFormat, updatedAt); err != nil {
		return nil, fmt.Errorf("store: parse updated_at %q: %w", updatedAt, err)
	}
	if pr.FetchedAt, err = time.Parse(sqliteTimeFormat, fetchedAt); err != nil {
		return nil, fmt.Errorf("store: parse fetched_at %q: %w", fetchedAt, err)
	}
	// The external_review_at / last_responded_at columns are stored as
	// TEXT with empty-string defaults so legacy rows (and PRs that never
	// received an external review) round-trip to a zero Time without an
	// extra IFNULL on every read.
	if externalReviewAt != "" {
		if pr.ExternalReviewAt, err = time.Parse(sqliteTimeFormat, externalReviewAt); err != nil {
			return nil, fmt.Errorf("store: parse external_review_at %q: %w", externalReviewAt, err)
		}
	}
	if lastRespondedAt != "" {
		if pr.LastRespondedAt, err = time.Parse(sqliteTimeFormat, lastRespondedAt); err != nil {
			return nil, fmt.Errorf("store: parse last_responded_at %q: %w", lastRespondedAt, err)
		}
	}
	pr.Dismissed = dismissed != 0
	return &pr, nil
}

// UpdatePRReviewState records the external review-state observed by
// Tier 3 for an auto_implement-created PR (#482). Always replaces the
// previous value — Tier 3 only calls this when the aggregate state has
// actually changed (see main.go CheckItem flow).
func (s *Store) UpdatePRReviewState(prID int64, state, reviewer string, at time.Time) error {
	_, err := s.db.Exec(
		`UPDATE prs SET external_review_state = ?, external_reviewer = ?, external_review_at = ? WHERE id = ?`,
		state, reviewer, at.UTC().Format(sqliteTimeFormat), prID,
	)
	if err != nil {
		return fmt.Errorf("store: update pr review state %d: %w", prID, err)
	}
	return nil
}

// MarkPRAutoImplementOrigin writes the back-link from a PR to the issue
// that produced it via auto_implement (#482). Tier 3 keys its review-
// state vigilance on a non-zero value here; standard PRs (created by
// humans or by other automation) leave this at 0 and stay on the
// existing PR-review codepath.
func (s *Store) MarkPRAutoImplementOrigin(prID, issueID int64) error {
	_, err := s.db.Exec(
		`UPDATE prs SET auto_implement_issue_id = ? WHERE id = ?`,
		issueID, prID,
	)
	if err != nil {
		return fmt.Errorf("store: mark pr %d origin issue %d: %w", prID, issueID, err)
	}
	return nil
}

// IncrementPRReviewResponseCount atomically bumps the per-PR response
// counter and returns the post-increment value. Callers compare the
// return value against the per_pr_24h cap (#482 phase 2).
//
// Atomicity note: the Store uses a single-writer sqlite connection
// (SetMaxOpenConns(1) in Open), so the UPDATE + SELECT pair runs
// sequentially against the same writer. A future move to a higher
// concurrency limit would require wrapping these in a transaction.
func (s *Store) IncrementPRReviewResponseCount(prID int64) (int, error) {
	if _, err := s.db.Exec(
		`UPDATE prs SET review_response_count = review_response_count + 1 WHERE id = ?`, prID,
	); err != nil {
		return 0, fmt.Errorf("store: increment pr review_response_count %d: %w", prID, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT review_response_count FROM prs WHERE id = ?`, prID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: read pr review_response_count %d: %w", prID, err)
	}
	return n, nil
}

// IncrementPRReviewFixCount mirrors IncrementPRReviewResponseCount for
// the per-PR-lifetime fix counter (#482 phase 3).
func (s *Store) IncrementPRReviewFixCount(prID int64) (int, error) {
	if _, err := s.db.Exec(
		`UPDATE prs SET review_fix_count = review_fix_count + 1 WHERE id = ?`, prID,
	); err != nil {
		return 0, fmt.Errorf("store: increment pr review_fix_count %d: %w", prID, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT review_fix_count FROM prs WHERE id = ?`, prID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: read pr review_fix_count %d: %w", prID, err)
	}
	return n, nil
}

// SetPRLastRespondedAt records the timestamp of the most recent
// Responder-posted comment on a PR. Used by the Responder's cooldown
// check (#482 phase 2).
func (s *Store) SetPRLastRespondedAt(prID int64, at time.Time) error {
	_, err := s.db.Exec(
		`UPDATE prs SET last_responded_at = ? WHERE id = ?`,
		at.UTC().Format(sqliteTimeFormat), prID,
	)
	if err != nil {
		return fmt.Errorf("store: set pr last_responded_at %d: %w", prID, err)
	}
	return nil
}
