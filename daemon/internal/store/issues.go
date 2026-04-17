package store

import (
	"fmt"
	"time"
)

// Issue mirrors a GitHub issue stored locally for the issue-tracking pipeline.
// `Assignees` and `Labels` are the raw JSON strings (`[]` when empty) kept
// alongside the row — the pipeline (#26/#27) unmarshals them on demand, so
// we do not round-trip through a slice in the store layer.
type Issue struct {
	ID        int64     `json:"id"`
	GithubID  int64     `json:"github_id"`
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Author    string    `json:"author"`
	Assignees string    `json:"assignees"` // JSON array
	Labels    string    `json:"labels"`    // JSON array
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	FetchedAt time.Time `json:"fetched_at"`
	Dismissed bool      `json:"dismissed"`
}

// IssueReview is the record of one run of the issue pipeline against an issue.
// `ActionTaken` is "review_only" or "auto_implement" (the pipeline falls back
// to review_only when auto_implement is configured but local_dir is unset —
// see #26). `PRCreated` stores the GitHub PR number when auto_implement opened
// one, or zero otherwise.
type IssueReview struct {
	ID          int64     `json:"id"`
	IssueID     int64     `json:"issue_id"`
	CLIUsed     string    `json:"cli_used"`
	Summary     string    `json:"summary"`
	Triage      string    `json:"triage"`      // JSON object {severity, category, ...}
	Suggestions string    `json:"suggestions"` // JSON array
	ActionTaken string    `json:"action_taken"`
	PRCreated   int       `json:"pr_created"`
	CreatedAt   time.Time `json:"created_at"`
}

// UpsertIssue inserts or updates an issue keyed on github_id. The dismissed
// flag is intentionally not part of the UPDATE clause so a user's dismiss
// choice survives the next poll — same pattern as UpsertPR.
func (s *Store) UpsertIssue(i *Issue) (int64, error) {
	if i.Assignees == "" {
		i.Assignees = "[]"
	}
	if i.Labels == "" {
		i.Labels = "[]"
	}
	res, err := s.db.Exec(`
		INSERT INTO issues (github_id, repo, number, title, body, author, assignees, labels, state, created_at, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(github_id) DO UPDATE SET
			repo=excluded.repo, number=excluded.number, title=excluded.title,
			body=excluded.body, author=excluded.author, assignees=excluded.assignees,
			labels=excluded.labels, state=excluded.state,
			created_at=excluded.created_at, fetched_at=excluded.fetched_at
	`, i.GithubID, i.Repo, i.Number, i.Title, i.Body, i.Author, i.Assignees, i.Labels, i.State,
		i.CreatedAt.UTC().Format(sqliteTimeFormat),
		i.FetchedAt.UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return 0, fmt.Errorf("store: upsert issue: %w", err)
	}
	// LastInsertId returns 0 on the UPDATE path with modernc.org/sqlite (the
	// driver this project uses). Other SQLite drivers may report the existing
	// row id instead — the fallback SELECT below handles either case so this
	// code is portable if the driver ever changes.
	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		row := s.db.QueryRow("SELECT id FROM issues WHERE github_id = ?", i.GithubID)
		if scanErr := row.Scan(&id); scanErr != nil {
			return 0, fmt.Errorf("store: upsert issue fallback select: %w", scanErr)
		}
	}
	return id, nil
}

// GetIssue retrieves an issue by its local row ID.
func (s *Store) GetIssue(id int64) (*Issue, error) {
	row := s.db.QueryRow(
		`SELECT id, github_id, repo, number, title, body, author, assignees, labels,
		        state, created_at, fetched_at, dismissed FROM issues WHERE id = ?`, id,
	)
	return scanIssue(row)
}

// GetIssueByGithubID retrieves an issue by its GitHub ID (the natural key).
func (s *Store) GetIssueByGithubID(githubID int64) (*Issue, error) {
	row := s.db.QueryRow(
		`SELECT id, github_id, repo, number, title, body, author, assignees, labels,
		        state, created_at, fetched_at, dismissed FROM issues WHERE github_id = ?`, githubID,
	)
	return scanIssue(row)
}

// ListIssues returns all non-dismissed issues ordered by fetched_at descending.
func (s *Store) ListIssues() ([]*Issue, error) {
	rows, err := s.db.Query(
		`SELECT id, github_id, repo, number, title, body, author, assignees, labels,
		        state, created_at, fetched_at, dismissed FROM issues
		 WHERE dismissed = 0 ORDER BY fetched_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list issues: %w", err)
	}
	defer rows.Close()
	var issues []*Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		issues = append(issues, i)
	}
	return issues, rows.Err()
}

// DismissIssue hides an issue from the dashboard and opts it out of future
// pipeline runs until the user undismisses it.
func (s *Store) DismissIssue(id int64) error {
	_, err := s.db.Exec("UPDATE issues SET dismissed = 1 WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: dismiss issue %d: %w", id, err)
	}
	return nil
}

// UndismissIssue restores a previously dismissed issue.
func (s *Store) UndismissIssue(id int64) error {
	_, err := s.db.Exec("UPDATE issues SET dismissed = 0 WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: undismiss issue %d: %w", id, err)
	}
	return nil
}

// InsertIssueReview stores a single pipeline run's result.
// Empty Triage / Suggestions are normalised to valid JSON (`{}` / `[]`) so
// downstream consumers can `json.Unmarshal` them without guarding against
// the empty-string case.
func (s *Store) InsertIssueReview(r *IssueReview) (int64, error) {
	if r.Triage == "" {
		r.Triage = "{}"
	}
	if r.Suggestions == "" {
		r.Suggestions = "[]"
	}
	if r.ActionTaken == "" {
		r.ActionTaken = "review_only"
	}
	res, err := s.db.Exec(`
		INSERT INTO issue_reviews (issue_id, cli_used, summary, triage, suggestions, action_taken, pr_created, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, r.IssueID, r.CLIUsed, r.Summary, r.Triage, r.Suggestions, r.ActionTaken, r.PRCreated,
		r.CreatedAt.UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert issue review: %w", err)
	}
	return res.LastInsertId()
}

// ListIssueReviews returns every review for an issue, newest first.
func (s *Store) ListIssueReviews(issueID int64) ([]*IssueReview, error) {
	rows, err := s.db.Query(
		`SELECT id, issue_id, cli_used, summary, triage, suggestions, action_taken, pr_created, created_at
		 FROM issue_reviews WHERE issue_id = ? ORDER BY created_at DESC`,
		issueID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list issue reviews: %w", err)
	}
	defer rows.Close()
	var reviews []*IssueReview
	for rows.Next() {
		r, err := scanIssueReview(rows)
		if err != nil {
			return nil, err
		}
		reviews = append(reviews, r)
	}
	return reviews, rows.Err()
}

// LatestIssueReview returns the most recent review for an issue, or
// sql.ErrNoRows if none exists yet.
func (s *Store) LatestIssueReview(issueID int64) (*IssueReview, error) {
	row := s.db.QueryRow(
		`SELECT id, issue_id, cli_used, summary, triage, suggestions, action_taken, pr_created, created_at
		 FROM issue_reviews WHERE issue_id = ? ORDER BY created_at DESC LIMIT 1`,
		issueID,
	)
	return scanIssueReview(row)
}

func scanIssue(s scanner) (*Issue, error) {
	var i Issue
	var createdAt, fetchedAt string
	var dismissed int
	if err := s.Scan(&i.ID, &i.GithubID, &i.Repo, &i.Number, &i.Title, &i.Body,
		&i.Author, &i.Assignees, &i.Labels, &i.State, &createdAt, &fetchedAt, &dismissed); err != nil {
		return nil, fmt.Errorf("store: scan issue: %w", err)
	}
	var err error
	if i.CreatedAt, err = time.Parse(sqliteTimeFormat, createdAt); err != nil {
		return nil, fmt.Errorf("store: parse issue created_at %q: %w", createdAt, err)
	}
	if i.FetchedAt, err = time.Parse(sqliteTimeFormat, fetchedAt); err != nil {
		return nil, fmt.Errorf("store: parse issue fetched_at %q: %w", fetchedAt, err)
	}
	i.Dismissed = dismissed != 0
	return &i, nil
}

func scanIssueReview(s scanner) (*IssueReview, error) {
	var r IssueReview
	var createdAt string
	if err := s.Scan(&r.ID, &r.IssueID, &r.CLIUsed, &r.Summary, &r.Triage,
		&r.Suggestions, &r.ActionTaken, &r.PRCreated, &createdAt); err != nil {
		return nil, fmt.Errorf("store: scan issue review: %w", err)
	}
	var err error
	if r.CreatedAt, err = time.Parse(sqliteTimeFormat, createdAt); err != nil {
		return nil, fmt.Errorf("store: parse issue review created_at %q: %w", createdAt, err)
	}
	return &r, nil
}
