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

	// TOMLMu serializes read-modify-write operations against the
	// config TOML file on disk. The HTTP server's PATCH handlers
	// hold the same mutex (Server.TOMLMu) so the rename pipeline
	// and operator-driven config edits do not clobber each other.
	// Lock order is TOMLMu → CfgMu, consistent with the PATCH path
	// which holds TOMLMu while triggering reloadFn (which takes
	// CfgMu internally).
	TOMLMu sync.Locker

	// ApplyConfig mutates the in-memory config in place (rename keys
	// in Repositories, NonMonitored, AI.Repos, AI.Orgs as applicable).
	// Invoked under CfgMu only AFTER Persister.Rename has successfully
	// rewritten the disk TOML — so a persister failure leaves the
	// in-memory config untouched and the next probe tick re-detects
	// the mismatch (because Repos() still reads the old slug).
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
// call repeatedly with the same pair: each downstream step
// (ApplyConfig, Persister.Rename, Purger.Purge) is itself idempotent
// in the absence of the old slug, so a retry after a partial failure
// converges.
//
// The store's `applied` return value is informational only — it
// tracks whether THIS invocation moved rows or whether a prior
// invocation already wrote the audit row. It is NOT a signal to skip
// downstream work: the downstream surfaces are not audited, so a
// previous Run could have committed the store and then failed at
// config persistence or worktree purge, leaving disk and DB out of
// sync. Re-running the downstream on every successful audit covers
// the recovery path.
func (r *Reconciler) Run(ctx context.Context, oldRepo, newRepo string) error {
	if err := validatePair(oldRepo, newRepo); err != nil {
		return err
	}

	// 1. Authoritative state move: SQLite + audit row in one TX.
	applied, err := r.deps.Store.RenameRepo(oldRepo, newRepo)
	if err != nil {
		return fmt.Errorf("rename: store: %w", err)
	}
	if applied {
		slog.Info("rename: applying", "old_repo", oldRepo, "new_repo", newRepo)
	} else {
		// Audit row already present — could be a duplicate probe
		// tick after a fully-successful prior Run, or a recovery
		// attempt after a prior Run committed the store and failed
		// downstream. Either way we run the downstream; if state is
		// already settled the steps are cheap no-ops.
		slog.Info("rename: recovery path (audit row present)",
			"old_repo", oldRepo, "new_repo", newRepo)
	}

	// 2. Config rewrite. Lock order: TOMLMu first (shared with the
	//    HTTP PATCH handlers' read-modify-write), then CfgMu. Disk
	//    persister runs FIRST: if it fails, in-memory config stays
	//    untouched and the probe's next tick will still see the old
	//    slug in Repos() and re-dispatch this reconciler — which is
	//    the recovery path. Mutating in-memory first would mask the
	//    mismatch from the probe and the rename would silently stick
	//    in a half-state until daemon restart.
	r.deps.TOMLMu.Lock()
	r.deps.CfgMu.Lock()
	cfgErr := r.deps.Persister.Rename(r.deps.CfgPath, oldRepo, newRepo)
	if cfgErr == nil {
		r.deps.ApplyConfig(oldRepo, newRepo)
	}
	r.deps.CfgMu.Unlock()
	r.deps.TOMLMu.Unlock()
	if cfgErr != nil {
		// Store has the audit row but config persistence failed. We
		// do NOT roll back the store — the next probe tick (or a
		// manual /admin/repo-rename call) re-enters Run, the store
		// returns applied=false, and the flow above re-runs the
		// downstream until the persister succeeds.
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
