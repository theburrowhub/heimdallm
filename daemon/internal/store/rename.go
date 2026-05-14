package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidRepoSlug is returned by RenameRepo when either slug is
// empty, equal to the other, or not shaped as "owner/name". Detection
// callers pre-validate against the GitHub response, so reaching this
// error in production usually indicates a programming bug or a manual
// admin endpoint invocation with malformed input.
var ErrInvalidRepoSlug = errors.New("store: invalid repo slug")

// RenameRepo moves every row keyed on `oldRepo` over to `newRepo`
// in a single SQLite transaction, and writes an audit row to
// `repo_renames`. The four tables touched are prs, issues,
// activity_log, watch_state — these are the surfaces that store the
// `owner/name` string directly. In-flight tables (`reviews_in_flight`,
// `issue_triage_in_flight`) are keyed on numeric IDs and do not need
// renaming. activity_log's `org` column is left untouched because the
// repoOrg helper deriving it is recomputed from `repo` at read time
// elsewhere; backfilling it here would silently obscure the org
// rename in audit history.
//
// Idempotency: if a `repo_renames` row already maps `oldRepo`→`newRepo`,
// the method returns (false, nil) without writing further state. This
// keeps daemon restarts from re-applying a successful rename, and lets
// the detection probe call RenameRepo on every probe tick without
// double-writing the audit trail.
//
// IMPORTANT: `applied` is INFORMATIONAL telemetry only — callers MUST
// NOT use it to skip downstream reconciliation work (config rewrite,
// worktree purge, SSE emission). The downstream surfaces are not
// audited at the store level, so a prior invocation could have
// committed this audit row and then failed at the persister or
// purger; the recovery retry sees applied=false and must still
// complete those steps. See rename.Reconciler.Run for the full
// rationale.
func (s *Store) RenameRepo(oldRepo, newRepo string) (applied bool, err error) {
	if err := validateRenamePair(oldRepo, newRepo); err != nil {
		return false, err
	}

	// Idempotency guard: if the most recent audit row for oldRepo
	// already points at newRepo, the rename was applied previously
	// and the call is a no-op. A different newRepo would indicate a
	// rename chain (old→intermediate→new); the caller is expected to
	// reconcile in order, so we treat that as a fresh rename.
	var lastNew sql.NullString
	if err := s.db.QueryRow(
		`SELECT new_repo FROM repo_renames WHERE old_repo = ? ORDER BY id DESC LIMIT 1`,
		oldRepo,
	).Scan(&lastNew); err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("store: rename idempotency check: %w", err)
	}
	if lastNew.Valid && lastNew.String == newRepo {
		return false, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("store: rename begin tx: %w", err)
	}
	// Roll back unconditionally; the Commit path nils err so the deferred
	// rollback becomes a no-op (sql.ErrTxDone is expected and ignored).
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, table := range []string{"prs", "issues", "activity_log", "watch_state"} {
		if _, err := tx.Exec(
			`UPDATE `+table+` SET repo = ? WHERE repo = ?`,
			newRepo, oldRepo,
		); err != nil {
			return false, fmt.Errorf("store: rename %s: %w", table, err)
		}
	}

	if _, err := tx.Exec(
		`INSERT INTO repo_renames (old_repo, new_repo, renamed_at) VALUES (?, ?, ?)`,
		oldRepo, newRepo, time.Now().UTC().Format(sqliteTimeFormat),
	); err != nil {
		return false, fmt.Errorf("store: rename insert audit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: rename commit: %w", err)
	}
	committed = true
	return true, nil
}

func validateRenamePair(oldRepo, newRepo string) error {
	if oldRepo == "" || newRepo == "" {
		return fmt.Errorf("%w: empty slug", ErrInvalidRepoSlug)
	}
	if oldRepo == newRepo {
		return fmt.Errorf("%w: old and new are identical", ErrInvalidRepoSlug)
	}
	if !looksLikeOwnerName(oldRepo) || !looksLikeOwnerName(newRepo) {
		return fmt.Errorf("%w: expected owner/name shape", ErrInvalidRepoSlug)
	}
	return nil
}

// looksLikeOwnerName checks the minimum well-formedness of a GitHub
// slug: exactly one '/' separator with non-empty owner and name. GitHub
// itself enforces stricter character rules; we do not duplicate the
// regex here because the detection probe only ever passes us values it
// just read from the GitHub API.
func looksLikeOwnerName(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return false
	}
	return parts[0] != "" && parts[1] != ""
}
