package store

import (
	"fmt"
	"time"
)

// PR represents a GitHub pull request stored locally.
type PR struct {
	ID        int64
	GithubID  int64
	Repo      string
	Number    int
	Title     string
	Author    string
	URL       string
	State     string
	UpdatedAt time.Time
	FetchedAt time.Time
}

// UpsertPR inserts or updates a PR record, keyed on github_id. Returns the row ID.
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
	id, err := res.LastInsertId()
	// LastInsertId returns 0 on UPDATE (conflict path) — fall back to a SELECT.
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
		"SELECT id, github_id, repo, number, title, author, url, state, updated_at, fetched_at FROM prs WHERE id = ?", id,
	)
	return scanPR(row)
}

// GetPRByGithubID retrieves a PR by its GitHub PR ID.
func (s *Store) GetPRByGithubID(githubID int64) (*PR, error) {
	row := s.db.QueryRow(
		"SELECT id, github_id, repo, number, title, author, url, state, updated_at, fetched_at FROM prs WHERE github_id = ?", githubID,
	)
	return scanPR(row)
}

// ListPRs returns all PRs ordered by updated_at descending.
func (s *Store) ListPRs() ([]*PR, error) {
	rows, err := s.db.Query(
		"SELECT id, github_id, repo, number, title, author, url, state, updated_at, fetched_at FROM prs ORDER BY updated_at DESC",
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

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanPR(s scanner) (*PR, error) {
	var pr PR
	var updatedAt, fetchedAt string
	var err error
	if err = s.Scan(&pr.ID, &pr.GithubID, &pr.Repo, &pr.Number, &pr.Title,
		&pr.Author, &pr.URL, &pr.State, &updatedAt, &fetchedAt); err != nil {
		return nil, fmt.Errorf("store: scan pr: %w", err)
	}
	if pr.UpdatedAt, err = time.Parse(sqliteTimeFormat, updatedAt); err != nil {
		return nil, fmt.Errorf("store: parse updated_at %q: %w", updatedAt, err)
	}
	if pr.FetchedAt, err = time.Parse(sqliteTimeFormat, fetchedAt); err != nil {
		return nil, fmt.Errorf("store: parse fetched_at %q: %w", fetchedAt, err)
	}
	return &pr, nil
}
