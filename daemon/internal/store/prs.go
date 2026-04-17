package store

import (
	"fmt"
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
	res, err := s.db.Exec(`
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
	// LastInsertId returns 0 on the UPDATE path with modernc.org/sqlite (the
	// driver this project uses). Other SQLite drivers may report the existing
	// row id instead — the fallback SELECT below handles either case so this
	// code is portable if the driver ever changes.
	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		row := s.db.QueryRow("SELECT id FROM prs WHERE github_id = ?", pr.GithubID)
		if scanErr := row.Scan(&id); scanErr != nil {
			return 0, fmt.Errorf("store: upsert pr fallback select: %w", scanErr)
		}
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

// ListPRs returns all non-dismissed PRs ordered by updated_at descending.
func (s *Store) ListPRs() ([]*PR, error) {
	rows, err := s.db.Query(
		"SELECT id, github_id, repo, number, title, author, url, state, updated_at, fetched_at, dismissed FROM prs WHERE dismissed = 0 ORDER BY updated_at DESC",
	)
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
