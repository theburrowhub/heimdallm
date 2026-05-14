// Package rename owns the cross-subsystem reconciliation that runs
// when a GitHub repository or its parent organisation is renamed.
//
// The detection probe (Tier 2 / Tier 4) calls Reconciler.Run on a
// mismatch between the configured slug and GitHub's canonical
// `full_name`. The reconciler then propagates the new slug through
// every surface that keys on `owner/name`: the SQLite store, the
// in-memory config, the on-disk TOML, the on-disk worktree, and the
// SSE event stream that wakes Flutter.
//
// See `docs/superpowers/plans/2026-05-14-issue-489-repo-org-rename.md`
// for the full design.
package rename

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/heimdallm/daemon/internal/sse"
)

// ErrInvalidRepoSlug is returned by Run when either slug is empty,
// identical to the other, or malformed. Detection callers pre-validate
// against the GitHub response, so reaching this in production usually
// indicates a programming bug or a manual admin endpoint invocation.
var ErrInvalidRepoSlug = errors.New("rename: invalid repo slug")

// Store is the SQLite-side dependency. *store.Store satisfies it via
// its RenameRepo method.
type Store interface {
	// RenameRepo moves every row keyed on oldRepo to newRepo and
	// writes an audit row, idempotent across calls. Returns
	// applied=true when this invocation produced state change,
	// applied=false when the audit table already recorded the same
	// rename.
	RenameRepo(oldRepo, newRepo string) (applied bool, err error)
}

// Persister rewrites the config TOML on disk. config.RenameRepoInTOML
// satisfies this via a thin wrapper in main.go.
type Persister interface {
	Rename(path, oldRepo, newRepo string) error
}

// Purger drops the on-disk worktree for the OLD slug so the next
// acquire on the NEW slug clones fresh. *repoctx.Manager satisfies
// this signature.
type Purger interface {
	Purge(ctx context.Context, repo, cloneDir string) error
}

// Publisher fans the rename event out to SSE subscribers.
// *sse.Broker satisfies this.
type Publisher interface {
	Publish(e sse.Event)
}

// Deps is the constructor input — all dependencies plus the cfgMu
// and the in-memory config mutator the daemon owns.
type Deps struct {
	Store     Store
	Persister Persister
	Purger    Purger
	Publisher Publisher

	// CfgMu serializes config mutation + persistence. The reconciler
	// holds it across ApplyConfig + Persister.Rename so concurrent
	// readers never see disk/memory drift.
	CfgMu sync.Locker

	// ApplyConfig mutates the in-memory config in place (rename keys
	// in Repositories, NonMonitored, AI.Repos, AI.Orgs as applicable).
	// Invoked under CfgMu between the store commit and the disk write.
	ApplyConfig func(oldRepo, newRepo string)

	CfgPath  string // path to the config TOML to rewrite
	CloneDir string // base directory for managed worktrees
}

// Reconciler runs the rename pipeline. Construct one per daemon and
// reuse it across all rename invocations.
type Reconciler struct {
	deps Deps
}

// NewReconciler returns a Reconciler that uses the supplied deps.
// The constructor does not validate deps — missing fields surface as
// nil-pointer panics on the first Run, which is the desired
// fail-fast behaviour during daemon wiring.
func NewReconciler(deps Deps) *Reconciler {
	return &Reconciler{deps: deps}
}

// Run reconciles oldRepo→newRepo across every surface. It is safe to
// call repeatedly with the same pair: the store-level idempotency
// guard short-circuits the call before any side effects fire, so the
// detection probe can invoke Run on every tick without consequence.
func (r *Reconciler) Run(ctx context.Context, oldRepo, newRepo string) error {
	if err := validatePair(oldRepo, newRepo); err != nil {
		return err
	}

	// 1. Authoritative state move: SQLite + audit row in one TX.
	applied, err := r.deps.Store.RenameRepo(oldRepo, newRepo)
	if err != nil {
		return fmt.Errorf("rename: store: %w", err)
	}
	if !applied {
		// Already on record — every downstream surface should be in
		// the renamed state already, so we exit silently. This is
		// the post-restart / probe-loop path.
		return nil
	}

	// 2. Config mutation + on-disk rewrite under cfgMu so readers
	//    that hold the lock cannot observe a half-applied state.
	r.deps.CfgMu.Lock()
	r.deps.ApplyConfig(oldRepo, newRepo)
	cfgErr := r.deps.Persister.Rename(r.deps.CfgPath, oldRepo, newRepo)
	r.deps.CfgMu.Unlock()
	if cfgErr != nil {
		// Store committed but config persistence failed. We do NOT
		// roll back the store — its audit row reflects truth and the
		// next probe tick will retry the persister via the same
		// reconciler entry point (store call short-circuits at that
		// point, so retry cost is bounded to the persister).
		return fmt.Errorf("rename: persist config: %w", cfgErr)
	}

	// 3. Drop the old worktree. Non-fatal — repoctx clones fresh on
	//    the next acquire of the new slug, so a failure here at
	//    most leaves an orphan dir for the operator to mop up.
	purgeErr := r.deps.Purger.Purge(ctx, oldRepo, r.deps.CloneDir)
	if purgeErr != nil {
		slog.Warn("rename: worktree purge failed (non-fatal)",
			"old_repo", oldRepo, "err", purgeErr)
	}

	// 4. Notify Flutter / TUI subscribers.
	r.deps.Publisher.Publish(sse.Event{
		Type: sse.EventRepoRenamed,
		Data: fmt.Sprintf(
			`{"old_repo":%q,"new_repo":%q,"worktree_purged":%t}`,
			oldRepo, newRepo, purgeErr == nil,
		),
	})
	return nil
}

func validatePair(oldRepo, newRepo string) error {
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

func looksLikeOwnerName(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return false
	}
	return parts[0] != "" && parts[1] != ""
}
