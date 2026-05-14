package store

import (
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
// `repo_renames`. Tables touched:
//
//   - prs.repo, issues.repo, watch_state.repo: straight UPDATE.
//   - activity_log: UPDATEs both repo AND org (when the org
//     component differs between oldRepo and newRepo) so the
//     ListActivity org-filter keeps surfacing historical entries
//     under the new org name after an org-rename. The invariant
//     org == repoOrg(repo) is preserved.
//
// In-flight tables (`reviews_in_flight`, `issue_triage_in_flight`)
// are keyed on numeric IDs and do not need renaming.
//
// Idempotency is derived from the data, not the audit table: each
// UPDATE matches `WHERE repo = oldRepo`, so on a second call after
// a successful rename the WHERE matches zero rows and the UPDATE is
// a natural no-op. This correctly handles rename chains like
// A→B→A→B: every step inspects the current state of the tables and
// moves whatever is there. The audit row is inserted ONLY when at
// least one row actually moved, so the audit log reflects real
// state transitions instead of accumulating attempts.
//
// IMPORTANT: `applied` is INFORMATIONAL telemetry only — callers MUST
// NOT use it to skip downstream reconciliation work (config rewrite,
// worktree purge, SSE emission). The downstream surfaces are not
// audited at the store level, so a prior invocation could have
// committed the store work and then failed at the persister or
// purger; the recovery retry sees applied=false and must still
// complete those steps. See rename.Reconciler.Run for the full
// rationale.
func (s *Store) RenameRepo(oldRepo, newRepo string) (applied bool, err error) {
	if err := validateRenamePair(oldRepo, newRepo); err != nil {
		return false, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("store: rename begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var totalMoved int64
	for _, table := range []string{"prs", "issues", "watch_state"} {
		res, err := tx.Exec(
			`UPDATE `+table+` SET repo = ? WHERE repo = ?`,
			newRepo, oldRepo,
		)
		if err != nil {
			return false, fmt.Errorf("store: rename %s: %w", table, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("store: rename %s: rows affected: %w", table, err)
		}
		totalMoved += n
	}

	// activity_log has both `repo` and `org` columns. The org column
	// drives the ListActivity Orgs filter and is rendered to the UI,
	// so we keep it in sync with the new repo's org component. For a
	// same-org rename newOrg == oldOrg and this is a no-op for that
	// column; for an org rename it's the load-bearing UPDATE that
	// keeps activity history visible under the new org filter.
	newOrg := orgOfSlug(newRepo)
	res, err := tx.Exec(
		`UPDATE activity_log SET repo = ?, org = ? WHERE repo = ?`,
		newRepo, newOrg, oldRepo,
	)
	if err != nil {
		return false, fmt.Errorf("store: rename activity_log: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: rename activity_log: rows affected: %w", err)
	}
	totalMoved += n

	// Audit row only when something actually moved. Reflecting "no
	// rows for oldRepo" as a no-op in the audit table keeps the log
	// usable as a real-state-transitions history rather than a
	// retry/probe-tick log.
	if totalMoved > 0 {
		if _, err := tx.Exec(
			`INSERT INTO repo_renames (old_repo, new_repo, renamed_at) VALUES (?, ?, ?)`,
			oldRepo, newRepo, time.Now().UTC().Format(sqliteTimeFormat),
		); err != nil {
			return false, fmt.Errorf("store: rename insert audit: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: rename commit: %w", err)
	}
	committed = true
	return totalMoved > 0, nil
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

// orgOfSlug returns the owner segment of a `owner/name` slug, or "" when
// the input is malformed. validateRenamePair has already enforced the
// shape by the time this is called from RenameRepo, but we keep the
// defensive empty return for any future direct caller.
func orgOfSlug(repo string) string {
	if i := strings.Index(repo, "/"); i > 0 {
		return repo[:i]
	}
	return ""
}
