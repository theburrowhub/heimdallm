package store

import (
	"database/sql"
	"fmt"
	"time"
)

// RepoInstruction is a persistent, project-scoped review instruction captured
// from an authorized PR comment directive. It is injected into every future
// review of the repo. See issue #383.
type RepoInstruction struct {
	ID          int64
	Repo        string
	Instruction string
	Author      string
	CommentID   int64
	CreatedAt   time.Time
}

// ListRepoInstructions returns all standing instructions for a repo, oldest first.
func (s *Store) ListRepoInstructions(repo string) ([]RepoInstruction, error) {
	rows, err := s.db.Query(
		"SELECT id, repo, instruction, author, comment_id, created_at FROM repo_instructions WHERE repo = ? ORDER BY id ASC",
		repo,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list repo instructions: %w", err)
	}
	defer rows.Close()

	var out []RepoInstruction
	for rows.Next() {
		var ri RepoInstruction
		var createdAt string
		if err := rows.Scan(&ri.ID, &ri.Repo, &ri.Instruction, &ri.Author, &ri.CommentID, &createdAt); err != nil {
			return nil, fmt.Errorf("store: scan repo instruction: %w", err)
		}
		ri.CreatedAt, _ = time.Parse(sqliteTimeFormat, createdAt)
		out = append(out, ri)
	}
	return out, rows.Err()
}

// AddRepoInstruction inserts a new standing instruction and returns its id.
func (s *Store) AddRepoInstruction(repo, instruction, author string, commentID int64) (int64, error) {
	res, err := s.db.Exec(
		"INSERT INTO repo_instructions (repo, instruction, author, comment_id, created_at) VALUES (?, ?, ?, ?, ?)",
		repo, instruction, author, commentID, time.Now().UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return 0, fmt.Errorf("store: add repo instruction: %w", err)
	}
	return res.LastInsertId()
}

// DeleteRepoInstruction removes a standing instruction by id, scoped to repo so
// a directive on one repo cannot delete another repo's instruction. Returns
// (false, nil) when no row matched.
func (s *Store) DeleteRepoInstruction(repo string, id int64) (bool, error) {
	res, err := s.db.Exec("DELETE FROM repo_instructions WHERE repo = ? AND id = ?", repo, id)
	if err != nil {
		return false, fmt.Errorf("store: delete repo instruction: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DirectiveProcessed reports whether a directive comment has already been handled.
func (s *Store) DirectiveProcessed(commentID int64) (bool, error) {
	var one int
	err := s.db.QueryRow("SELECT 1 FROM directive_marks WHERE comment_id = ?", commentID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: directive processed: %w", err)
	}
	return true, nil
}

// MarkDirectiveProcessed records that a directive comment has been handled so it
// is never re-applied or re-acked across poll cycles. Idempotent.
func (s *Store) MarkDirectiveProcessed(commentID int64, verb string) error {
	if _, err := s.db.Exec(
		"INSERT INTO directive_marks (comment_id, verb, processed_at) VALUES (?, ?, ?) ON CONFLICT(comment_id) DO NOTHING",
		commentID, verb, time.Now().UTC().Format(sqliteTimeFormat),
	); err != nil {
		return fmt.Errorf("store: mark directive processed: %w", err)
	}
	return nil
}
