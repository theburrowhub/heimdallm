package store

import (
	"fmt"
	"strings"
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
	ID             int64     `json:"id"`
	IssueID        int64     `json:"issue_id"`
	CLIUsed        string    `json:"cli_used"`
	Summary        string    `json:"summary"`
	Triage         string    `json:"triage"`                    // JSON object {severity, category, ...}
	RefinementData string    `json:"refinement_data,omitempty"` // JSON object for refinement runs
	NextSteps      string    `json:"next_steps"`                // JSON array
	ActionTaken    string    `json:"action_taken"`
	PRCreated      int       `json:"pr_created"`
	CreatedAt      time.Time `json:"created_at"`
	CommentedAt    time.Time `json:"commented_at"`
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
// An optional list of states (e.g. "open", "closed") filters the result;
// when no states are provided all non-dismissed issues are returned.
func (s *Store) ListIssues(states ...string) ([]*Issue, error) {
	query := `SELECT id, github_id, repo, number, title, body, author, assignees, labels,
		        state, created_at, fetched_at, dismissed FROM issues WHERE dismissed = 0`
	var args []any
	if len(states) > 0 {
		placeholders := strings.Repeat("?,", len(states))
		placeholders = placeholders[:len(placeholders)-1] // trim trailing comma
		query += " AND state IN (" + placeholders + ")"
		for _, st := range states {
			args = append(args, st)
		}
	}
	query += " ORDER BY fetched_at DESC"
	rows, err := s.db.Query(query, args...)
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

// ListOpenIssues is a convenience wrapper that returns only non-dismissed
// issues with state "open".
func (s *Store) ListOpenIssues() ([]*Issue, error) {
	return s.ListIssues("open")
}

// UpdateIssueState updates the state of an issue by its local row ID.
func (s *Store) UpdateIssueState(id int64, state string) error {
	_, err := s.db.Exec("UPDATE issues SET state = ? WHERE id = ?", state, id)
	if err != nil {
		return fmt.Errorf("store: update issue state %d: %w", id, err)
	}
	return nil
}

// UpdateIssueStateByGithubID updates the state of an issue by its GitHub ID.
// This is used by Tier 3 which supplies a github_id rather than the local id.
func (s *Store) UpdateIssueStateByGithubID(githubID int64, state string) error {
	_, err := s.db.Exec("UPDATE issues SET state = ? WHERE github_id = ?", state, githubID)
	if err != nil {
		return fmt.Errorf("store: update issue state by github_id %d: %w", githubID, err)
	}
	return nil
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
// Empty Triage / NextSteps are normalised to valid JSON (`{}` / `[]`) so
// downstream consumers can `json.Unmarshal` them without guarding against
// the empty-string case.
func (s *Store) InsertIssueReview(r *IssueReview) (int64, error) {
	if r.Triage == "" {
		r.Triage = "{}"
	}
	if r.NextSteps == "" {
		r.NextSteps = "[]"
	}
	if r.ActionTaken == "" {
		r.ActionTaken = "review_only"
	}
	commentedAt := ""
	if !r.CommentedAt.IsZero() {
		commentedAt = r.CommentedAt.UTC().Format(sqliteTimeFormat)
	}
	res, err := s.db.Exec(`
		INSERT INTO issue_reviews (issue_id, cli_used, summary, triage, refinement_data, next_steps, action_taken, pr_created, created_at, commented_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.IssueID, r.CLIUsed, r.Summary, r.Triage, r.RefinementData, r.NextSteps, r.ActionTaken, r.PRCreated,
		r.CreatedAt.UTC().Format(sqliteTimeFormat),
		commentedAt,
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert issue review: %w", err)
	}
	return res.LastInsertId()
}

// ListIssueReviews returns every review for an issue, newest first.
func (s *Store) ListIssueReviews(issueID int64) ([]*IssueReview, error) {
	rows, err := s.db.Query(
		`SELECT id, issue_id, cli_used, summary, triage, COALESCE(refinement_data,''), next_steps, action_taken, pr_created, created_at, COALESCE(commented_at,'')
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
		`SELECT id, issue_id, cli_used, summary, triage, COALESCE(refinement_data,''), next_steps, action_taken, pr_created, created_at, COALESCE(commented_at,'')
		 FROM issue_reviews WHERE issue_id = ? ORDER BY created_at DESC LIMIT 1`,
		issueID,
	)
	return scanIssueReview(row)
}

// LatestIssueReviewByAction returns the newest review row for a given action,
// or sql.ErrNoRows when that action has not run for the issue yet.
func (s *Store) LatestIssueReviewByAction(issueID int64, action string) (*IssueReview, error) {
	row := s.db.QueryRow(
		`SELECT id, issue_id, cli_used, summary, triage, COALESCE(refinement_data,''), next_steps, action_taken, pr_created, created_at, COALESCE(commented_at,'')
		 FROM issue_reviews WHERE issue_id = ? AND action_taken = ? ORDER BY created_at DESC LIMIT 1`,
		issueID, action,
	)
	return scanIssueReview(row)
}

// CountFailedAutoImplement returns the number of consecutive failed
// auto_implement attempts for an issue (action_taken =
// "auto_implement_failed"). The count resets conceptually when a successful
// review lands (the dedup logic in the fetcher stops retrying once the cap is
// hit, so the counter never actually needs a reset in practice). Used by the
// fetcher to enforce the max-retry cap (#223).
func (s *Store) CountFailedAutoImplement(issueID int64) (int, error) {
	row := s.db.QueryRow(
		`SELECT COUNT(*) FROM issue_reviews WHERE issue_id = ? AND action_taken = 'auto_implement_failed'`,
		issueID,
	)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count failed auto_implement for issue %d: %w", issueID, err)
	}
	return n, nil
}

// SetIssueClaimedByAutonomous flags (or clears) an issue as claimed by the
// autonomous end-to-end pipeline. Used for auditing and to keep the selector
// from re-picking an in-flight task.
func (s *Store) SetIssueClaimedByAutonomous(issueID int64, claimed bool) error {
	v := 0
	if claimed {
		v = 1
	}
	if _, err := s.db.Exec(`UPDATE issues SET claimed_by_autonomous = ? WHERE id = ?`, v, issueID); err != nil {
		return fmt.Errorf("store: set issue claimed_by_autonomous: %w", err)
	}
	return nil
}

// IsIssueClaimedByAutonomous reports whether the issue is flagged claimed.
func (s *Store) IsIssueClaimedByAutonomous(issueID int64) (bool, error) {
	var v int
	if err := s.db.QueryRow(`SELECT claimed_by_autonomous FROM issues WHERE id = ?`, issueID).Scan(&v); err != nil {
		return false, fmt.Errorf("store: get issue claimed_by_autonomous: %w", err)
	}
	return v != 0, nil
}

// SetAutonomousClaimUntil records a time-based claim lease on the issue. The
// lease doubles as the failure/no-progress cooldown for the autonomous
// selector and, because it has an explicit expiry, recovers automatically from
// a crash mid-Drive (no permanent stuck claim, no manual operator step). A zero
// `until` clears the lease (stored as ”). Times are persisted RFC3339 UTC.
func (s *Store) SetAutonomousClaimUntil(issueID int64, until time.Time) error {
	v := ""
	if !until.IsZero() {
		v = until.UTC().Format(sqliteTimeFormat)
	}
	if _, err := s.db.Exec(`UPDATE issues SET autonomous_claim_until = ? WHERE id = ?`, v, issueID); err != nil {
		return fmt.Errorf("store: set issue autonomous_claim_until: %w", err)
	}
	return nil
}

// IsAutonomousClaimActive reports whether the issue currently holds an active
// (un-expired) autonomous claim lease relative to `now`. An empty or
// unparseable stored value is treated leniently as "no active lease" (false) so
// a malformed row never permanently blocks selection.
func (s *Store) IsAutonomousClaimActive(issueID int64, now time.Time) (bool, error) {
	var v string
	if err := s.db.QueryRow(`SELECT autonomous_claim_until FROM issues WHERE id = ?`, issueID).Scan(&v); err != nil {
		return false, fmt.Errorf("store: get issue autonomous_claim_until: %w", err)
	}
	if strings.TrimSpace(v) == "" {
		return false, nil
	}
	until, err := time.Parse(sqliteTimeFormat, v)
	if err != nil {
		return false, nil // lenient: malformed lease never blocks
	}
	return until.After(now), nil
}

// HasOpenAutoImplementPR reports whether an open PR is linked to the issue's
// GitHub id via auto_implement_issue_id. Used by the autonomous selector's
// "not started" predicate.
//
// Production stores prs.auto_implement_issue_id as the issue STORE ROW ID (the
// return of UpsertIssue; see issues pipeline MarkPRAutoImplementOrigin), not
// the GitHub id. Callers, however, hold the GitHub id, so we map github_id →
// store id with a subquery. An unknown github_id yields NULL from the subquery,
// which matches no row (COUNT = 0), so the function safely returns false.
func (s *Store) HasOpenAutoImplementPR(issueGithubID int64) (bool, error) {
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM prs
		 WHERE auto_implement_issue_id = (SELECT id FROM issues WHERE github_id = ?)
		   AND state = 'open'`,
		issueGithubID,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("store: has open auto_implement PR: %w", err)
	}
	return n > 0, nil
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
	var createdAt, commentedAt string
	if err := s.Scan(&r.ID, &r.IssueID, &r.CLIUsed, &r.Summary, &r.Triage,
		&r.RefinementData, &r.NextSteps, &r.ActionTaken, &r.PRCreated, &createdAt, &commentedAt); err != nil {
		return nil, fmt.Errorf("store: scan issue review: %w", err)
	}
	var err error
	if r.CreatedAt, err = time.Parse(sqliteTimeFormat, createdAt); err != nil {
		return nil, fmt.Errorf("store: parse issue review created_at %q: %w", createdAt, err)
	}
	if commentedAt != "" {
		if r.CommentedAt, err = time.Parse(sqliteTimeFormat, commentedAt); err != nil {
			return nil, fmt.Errorf("store: parse issue review commented_at %q: %w", commentedAt, err)
		}
	}
	return &r, nil
}
