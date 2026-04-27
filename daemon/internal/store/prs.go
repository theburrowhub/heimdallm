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

// GetPR retrieves a PR by its local row ID.
func (s *Store) GetPR(id int64) (*PR, error) {
	row := s.db.QueryRow(
		"SELECT id, github_id, repo, number, title, author, url, state, updated_at, fetched_at, dismissed FROM prs WHERE id = ?", id,
	)
	return scanPR(row)
}

// GetPRByGithubID retrieves a PR by its GitHub PR ID.
func (s *Store) GetPRByGithubID(githubID int64) (*PR, error) {
	row := s.db.QueryRow(
		"SELECT id, github_id, repo, number, title, author, url, state, updated_at, fetched_at, dismissed FROM prs WHERE github_id = ?", githubID,
	)
	return scanPR(row)
}

// GetPRByRepoNumber retrieves a PR by its stable repository-local identity.
// GitHub's Search Issues API and Pulls API can return different global IDs for
// the same PR, so scheduler-side dedup needs this fallback after github_id
// misses. If duplicate rows exist from an older bug, prefer the row that
// already has review history so we dedup against the real prior work.
func (s *Store) GetPRByRepoNumber(repo string, number int) (*PR, error) {
	row := s.db.QueryRow(`
		SELECT p.id, p.github_id, p.repo, p.number, p.title, p.author, p.url,
		       p.state, p.updated_at, p.fetched_at, p.dismissed
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
	query := "SELECT id, github_id, repo, number, title, author, url, state, updated_at, fetched_at, dismissed FROM prs WHERE dismissed = 0"
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
	var dismissed int
	var err error
	if err = s.Scan(&pr.ID, &pr.GithubID, &pr.Repo, &pr.Number, &pr.Title,
		&pr.Author, &pr.URL, &pr.State, &updatedAt, &fetchedAt, &dismissed); err != nil {
		return nil, fmt.Errorf("store: scan pr: %w", err)
	}
	if pr.UpdatedAt, err = time.Parse(sqliteTimeFormat, updatedAt); err != nil {
		return nil, fmt.Errorf("store: parse updated_at %q: %w", updatedAt, err)
	}
	if pr.FetchedAt, err = time.Parse(sqliteTimeFormat, fetchedAt); err != nil {
		return nil, fmt.Errorf("store: parse fetched_at %q: %w", fetchedAt, err)
	}
	pr.Dismissed = dismissed != 0
	return &pr, nil
}
