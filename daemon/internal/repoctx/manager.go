package repoctx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/heimdallm/daemon/internal/config"
)

const (
	// MarkerFile identifies clones that Heimdallm is allowed to mutate.
	MarkerFile = ".heimdallm-managed"

	gitTimeout        = 3 * time.Minute
	maxGitStderrBytes = 16 * 1024
)

var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// worktreeTokenPattern restricts WorktreeToken to characters that are safe
// as a directory name on every supported platform and cannot collide with
// git porcelain field separators. The leading character must be
// alphanumeric so we never produce hidden directories under `.worktrees/`.
var worktreeTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Mode describes how the caller will use the resolved checkout.
type Mode int

const (
	// ModeRead gives the AI CLI repository context. For isolated executions,
	// local_dir and local_dir_base are read-only sources only; the AI receives
	// a fresh detached checkout with an independent object store.
	ModeRead Mode = iota
	// ModeWrite is for auto_implement. It uses the same isolated-worktree
	// boundary, so agent writes never land in the operator's checkout.
	ModeWrite
)

// Request is the repo-context acquisition contract.
type Request struct {
	Repo               string
	ConfiguredLocalDir string
	LocalDirBases      []string
	CloneDir           string
	Token              string
	Mode               Mode

	// WorktreeToken identifies the execution and becomes the readable prefix
	// of an external, randomly-unique checkout directory. Callers derive a
	// deterministic stage label (e.g. `triage-42`, `pr-review-1234`), while
	// the manager adds a cryptographic execution ID so retries and processes
	// never reuse a mutable checkout.
	WorktreeToken string

	// WorktreeBaseRef, when set, becomes the ref the worktree is
	// created at (`git worktree add <path> --detach <ref>`). For PR
	// reviews this MUST be the immutable commit SHA that the diff and
	// published review are anchored to. Empty snapshots the source
	// repository's current HEAD.
	WorktreeBaseRef string

	// WorktreeFetchRef is an optional remote ref used only to make
	// WorktreeBaseRef available locally (for example
	// refs/pull/123/head). The worktree is still created from, and
	// verified against, WorktreeBaseRef -- never this movable ref.
	WorktreeFetchRef string

	// Inspect skips worktree creation and returns a handle pointing at
	// the clone root. Used by the read-only `/config/clones` endpoint
	// where worktree overhead would be pure waste.
	Inspect bool
}

// Handle owns a repo-context lock until Release is called.
type Handle struct {
	path          string
	managed       bool
	repo          string
	commitSHA     string
	fetchRef      string
	sourceRoot    string
	sourceManaged bool
	leaseFiles    []*os.File
	once          sync.Once
	release       func()
}

// Path returns the working directory to pass to the executor.
func (h *Handle) Path() string {
	if h == nil {
		return ""
	}
	return h.path
}

// Managed reports whether Heimdallm created and may mutate this checkout.
func (h *Handle) Managed() bool {
	if h == nil {
		return false
	}
	return h.managed
}

// CommitSHA returns the immutable commit checked out for this handle. It is
// empty only for legacy/inspection handles that do not own an isolated
// worktree.
func (h *Handle) CommitSHA() string {
	if h == nil {
		return ""
	}
	return h.commitSHA
}

// LeaseFiles returns descriptors that callers should inherit into
// any child process using this worktree. Keeping the lease open in the child
// prevents another Heimdallm process from reaping the checkout if the daemon
// itself is killed while the child is still running. The caller must not close
// these descriptors; Handle.Release owns their lifecycle.
func (h *Handle) LeaseFiles() []*os.File {
	if h == nil || len(h.leaseFiles) == 0 {
		return nil
	}
	return append([]*os.File(nil), h.leaseFiles...)
}

// Release frees the per-repo lock. It is safe to call more than once.
func (h *Handle) Release() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		if h.release != nil {
			h.release()
		}
	})
}

type repoLock struct {
	ch   chan struct{}
	refs int
}

// repoCap is a long-lived counting semaphore that bounds the number of
// concurrent worktrees per repo. Refcount tracks waiters so the map
// entry can be GC'd once the last release runs.
type repoCap struct {
	ch   chan struct{}
	refs int
}

type gitRunner interface {
	Run(ctx context.Context, dir string, env []string, args ...string) error
	RunWithExtraFiles(ctx context.Context, dir string, env []string, extraFiles []*os.File, args ...string) error
	Output(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)
}

// PurgeReport summarizes managed clone cleanup without exposing local paths to
// HTTP callers.
type PurgeReport struct {
	Scanned int
	Removed int
}

type managedClone struct {
	repo          string
	path          string
	markerModTime time.Time
}

// Manager resolves and prepares repository working directories for AI
// runs. Its coordination model has three parts:
//   - A binary critical-section lock per repo (`locks`) protects source
//     preparation and isolated-checkout creation/removal. A kernel-backed
//     operations lock supplies the same exclusion across daemon processes.
//   - A counting semaphore per repo (`caps`) bounds concurrent isolated
//     checkouts to MaxWorktreesPerRepo. It is held across the AI run.
//   - A per-checkout file lease is held across the AI run and inherited by
//     the child process. Cleanup additionally requires cleanup.ready, so an
//     ambiguous crash leftover is retained fail-closed.
type Manager struct {
	mu       sync.Mutex
	locks    map[string]*repoLock
	caps     map[string]*repoCap
	active   map[string]struct{} // absolute worktree paths currently held
	git      gitRunner
	tempDir  func() string
	coordDir func() string

	maxWorktrees int

	// releaseTimeout caps how long a Handle.Release will wait for the
	// critical-section lock when running `git worktree remove`. Beyond
	// this, release leaves a lease-aware orphan for a later safe sweep so
	// it never deletes a checkout that an inherited child may still use.
	// Tests override this; production keeps the default at gitTimeout.
	releaseTimeout time.Duration
}

// ManagerOptions configures a Manager at construction.
type ManagerOptions struct {
	// MaxWorktreesPerRepo bounds the number of concurrent worktrees
	// per repo. Zero (or negative) disables the cap entirely — useful
	// in tests and legacy deployments. The daemon resolves the
	// effective default from configuration.
	MaxWorktreesPerRepo int
}

// NewManager returns a Manager backed by the local git binary with
// worktree capping disabled.
func NewManager() *Manager {
	return NewManagerWithOptions(ManagerOptions{})
}

// NewManagerWithOptions returns a Manager configured per opts.
func NewManagerWithOptions(opts ManagerOptions) *Manager {
	return &Manager{
		locks:          make(map[string]*repoLock),
		caps:           make(map[string]*repoCap),
		active:         make(map[string]struct{}),
		git:            execGit{},
		tempDir:        os.TempDir,
		coordDir:       defaultCoordinationDir,
		maxWorktrees:   opts.MaxWorktreesPerRepo,
		releaseTimeout: gitTimeout,
	}
}

// DefaultCloneDir is the base used when clone_dir is not configured.
func DefaultCloneDir() string {
	return filepath.Join(os.TempDir(), "heimdallm")
}

// Acquire returns a locked handle to the best available repo context.
func (m *Manager) Acquire(ctx context.Context, req Request) (*Handle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil {
		return nil, fmt.Errorf("repoctx: nil manager")
	}
	owner, name, err := splitRepo(req.Repo)
	if err != nil {
		return nil, err
	}
	if req.WorktreeToken != "" {
		// Validate eagerly so callers see a deterministic error before
		// any locks or filesystem work happens. An empty token still
		// works during the rollout — callsites are migrated stepwise.
		if err := validateWorktreeToken(req.WorktreeToken); err != nil {
			return nil, err
		}
	}
	if req.Inspect {
		return m.acquireInspect(ctx, req, owner, name)
	}
	if req.WorktreeToken == "" {
		return nil, fmt.Errorf("repoctx: worktree token is required for executable repository context")
	}
	return m.acquireWorktree(ctx, req, owner, name)
}

// acquireInspect returns the clone root path without taking a cap
// slot or creating a worktree. Used by the HTTP /config/clones
// inspection endpoint where forcing a worktree allocation would
// inflate disk usage and serialise reads behind real pipeline runs.
func (m *Manager) acquireInspect(ctx context.Context, req Request, owner, name string) (*Handle, error) {
	unlock, err := m.acquireRepoLock(ctx, req.Repo)
	if err != nil {
		return nil, err
	}
	defer unlock()

	if local := resolveLocal(req); local != "" {
		return &Handle{path: local, sourceRoot: local, managed: false, release: func() {}}, nil
	}
	target, err := m.cloneTarget(req.CloneDir, owner, name)
	if err != nil {
		return nil, err
	}
	opsLock, err := m.acquireOpsLock(ctx, canonicalPathForKey(target))
	if err != nil {
		return nil, fmt.Errorf("repoctx: lock managed repository inspection: %w", err)
	}
	defer opsLock.Close()
	path, err := m.ensureManagedClone(ctx, owner, name, req)
	if err != nil {
		return nil, err
	}
	return &Handle{path: path, sourceRoot: path, sourceManaged: true, managed: true, release: func() {}}, nil
}

// acquireWorktree creates a per-execution worktree under the managed
// clone. The cap semaphore is held across the entire AI run; the
// critical-section lock is only held while mutating shared state
// (clone prep + `git worktree add` / `worktree remove`).
//
// Named returns make the deferred rollback explicit: the cleanup
// closures read the outer `err` directly, so a future refactor that
// introduces a shadowing `err` cannot silently disable the rollback.
func (m *Manager) acquireWorktree(ctx context.Context, req Request, owner, name string) (h *Handle, err error) {
	capRel, err := m.acquireWorktreeCap(ctx, req.Repo)
	if err != nil {
		return nil, err
	}
	capReleased := false
	releaseCap := func() {
		if !capReleased {
			capReleased = true
			capRel()
		}
	}
	defer func() {
		if !capReleased && err != nil {
			releaseCap()
		}
	}()

	var unlock func()
	unlock, err = m.acquireRepoLock(ctx, req.Repo)
	if err != nil {
		return nil, err
	}
	critReleased := false
	releaseCrit := func() {
		if !critReleased {
			critReleased = true
			unlock()
		}
	}
	defer func() {
		if !critReleased && err != nil {
			releaseCrit()
		}
	}()

	var source *worktreeSource
	var opsLock *fileLock
	source, opsLock, err = m.prepareWorktreeSource(ctx, req, owner, name)
	if err != nil {
		return nil, err
	}
	opsReleased := false
	releaseOps := func() {
		if !opsReleased {
			opsReleased = true
			_ = opsLock.Close()
		}
	}
	defer func() {
		if !opsReleased {
			releaseOps()
		}
	}()

	var commitSHA string
	commitSHA, err = m.resolveWorktreeCommit(ctx, source, req)
	if err != nil {
		return nil, err
	}

	var lease *worktreeLease
	lease, err = m.createWorktreeLease(source, req, commitSHA)
	if err != nil {
		return nil, err
	}
	materialized := false
	defer func() {
		if err != nil {
			m.rollbackFailedMaterialization(source, lease, materialized)
		}
	}()
	if materialized, err = m.materializeIsolatedWorktree(ctx, source, req, commitSHA, lease, opsLock); err != nil {
		return nil, err
	}
	if req.Mode == ModeWrite {
		provisional := &Handle{
			path:          lease.path,
			managed:       true,
			repo:          req.Repo,
			commitSHA:     commitSHA,
			fetchRef:      strings.TrimSpace(req.WorktreeFetchRef),
			sourceRoot:    source.root,
			sourceManaged: source.managed,
			leaseFiles:    []*os.File{lease.lock.File()},
		}
		if err = m.ensureSnapshotFullHistory(ctx, provisional, req.Token, mutationGuardFiles(lease, opsLock)); err != nil {
			return nil, fmt.Errorf("%w; retry after fixing Git access or purge the managed clone via DELETE /config/clones/{repo}", err)
		}
	}
	m.markActive(lease.path)

	releaseOps()
	releaseCrit()

	release := func() {
		// Drop only the daemon's descriptor, then probe the lease using a new
		// open-file description. If a child inherited that descriptor, cleanup
		// waits until the kernel reports that the final copy has closed.
		closeWorktreeLease(lease, false)
		cleanup := func(probe *fileLock) {
			// Only a normal Release that has observed every inherited lease
			// descriptor closed may authorise crash recovery.
			if readyErr := markWorktreeCleanupReady(lease.runDir); readyErr != nil {
				slog.Warn("repoctx: could not mark worktree cleanup ready; direct cleanup only",
					"repo", req.Repo, "path", lease.path, "err", readyErr)
			}

			// Re-acquire the critical-section lock briefly to serialise the
			// worktree-registry mutation. Use a fresh background ctx so a
			// cancelled caller never prevents lease-aware cleanup.
			bgCtx, cancel := context.WithTimeout(context.Background(), m.releaseTimeout)
			defer cancel()
			defer probe.Close()
			defer releaseCap()
			defer m.unmarkActive(lease.path)
			unlockRm, lockErr := m.acquireRepoLock(bgCtx, req.Repo)
			if lockErr != nil {
				slog.Warn("repoctx: worktree release lock unavailable; leaving leased orphan",
					"repo", req.Repo, "path", lease.path, "err", lockErr)
				return
			}
			defer unlockRm()
			if rmErr := m.removeIsolatedWorktree(bgCtx, source, lease); rmErr != nil {
				slog.Warn("repoctx: worktree remove failed; leaving leased orphan",
					"path", lease.path, "err", rmErr)
				return
			}
			// Canonicalise while the path still exists. On macOS /var resolves
			// to /private/var; after RemoveAll the spelling cannot be recovered.
			m.unmarkActive(lease.path)
			if rmErr := os.RemoveAll(lease.runDir); rmErr != nil {
				slog.Warn("repoctx: remove released worktree lease directory failed",
					"path", lease.runDir, "err", rmErr)
			}
		}

		probe, acquired, probeErr := tryFileLock(filepath.Join(lease.runDir, worktreeLeaseFile))
		if probeErr != nil {
			slog.Warn("repoctx: worktree lease probe failed; leaving orphan",
				"repo", req.Repo, "path", lease.path, "err", probeErr)
			releaseCap()
			m.unmarkActive(lease.path)
			return
		}
		if !acquired {
			slog.Info("repoctx: inherited child still holds worktree lease; deferring cleanup",
				"repo", req.Repo, "path", lease.path)
			// Keep the per-repo capacity reservation while the child lives so
			// detached descendants cannot cause unbounded snapshot growth.
			go func() {
				waiter, waitErr := acquireFileLock(
					context.Background(),
					filepath.Join(lease.runDir, worktreeLeaseFile),
				)
				if waitErr != nil {
					slog.Warn("repoctx: deferred worktree lease wait failed; leaving orphan",
						"repo", req.Repo, "path", lease.path, "err", waitErr)
					releaseCap()
					m.unmarkActive(lease.path)
					return
				}
				cleanup(waiter)
			}()
			return
		}
		cleanup(probe)
	}
	slog.Info("repoctx: worktree acquired",
		"repo", req.Repo, "token", req.WorktreeToken,
		"commit_sha", commitSHA, "path", lease.path)
	return &Handle{
		path:          lease.path,
		managed:       true,
		repo:          req.Repo,
		commitSHA:     commitSHA,
		fetchRef:      strings.TrimSpace(req.WorktreeFetchRef),
		sourceRoot:    source.root,
		sourceManaged: source.managed,
		leaseFiles:    []*os.File{lease.lock.File()},
		release:       release,
	}, nil
}

// EnsureFullHistory upgrades the isolated snapshot itself. The operator-owned
// source and Heimdallm's managed source clone remain read-only inputs, so a
// concurrent branch switch, fetch, maintenance task, or second daemon cannot
// change the object graph visible to the running AI process.
func (m *Manager) EnsureFullHistory(ctx context.Context, h *Handle, token string) error {
	if m == nil {
		return fmt.Errorf("repoctx: nil manager")
	}
	if h == nil || h.Path() == "" || !h.Managed() || h.CommitSHA() == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return m.ensureSnapshotFullHistory(ctx, h, token, h.LeaseFiles())
}

// ValidateSnapshot verifies that a read-only execution neither switched HEAD
// nor changed files after the immutable worktree was created. Review callers
// run this immediately after the AI process exits and discard its result on
// failure.
func (m *Manager) ValidateSnapshot(ctx context.Context, h *Handle) error {
	if m == nil {
		return fmt.Errorf("repoctx: nil manager")
	}
	if h == nil || strings.TrimSpace(h.CommitSHA()) == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	headOut, err := m.runner().Output(ctx, h.Path(), nil,
		"rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("%w: inspect HEAD: %v", ErrSnapshotChanged, err)
	}
	head := strings.ToLower(strings.TrimSpace(string(headOut)))
	if head != strings.ToLower(h.CommitSHA()) {
		return fmt.Errorf("%w: HEAD is %s, want %s", ErrSnapshotChanged, head, h.CommitSHA())
	}
	statusOut, err := m.runner().Output(ctx, h.Path(), nil,
		"status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return fmt.Errorf("%w: inspect worktree status: %v", ErrSnapshotChanged, err)
	}
	if status := strings.TrimSpace(string(statusOut)); status != "" {
		return fmt.Errorf("%w: worktree is dirty", ErrSnapshotChanged)
	}
	return nil
}

// Purge removes one managed clone. It refuses to delete directories without a
// valid ownership marker.
func (m *Manager) Purge(ctx context.Context, repo, cloneDir string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil {
		return fmt.Errorf("repoctx: nil manager")
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	unlock, err := m.acquireRepoLock(ctx, repo)
	if err != nil {
		return err
	}
	defer unlock()

	target, err := m.cloneTarget(cloneDir, owner, name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("repoctx: stat clone target %q: %w", target, err)
	}
	if err := validateMarker(target, repo); err != nil {
		return err
	}
	sourceKey := canonicalPathForKey(target)
	opsLock, err := m.acquireOpsLock(ctx, sourceKey)
	if err != nil {
		return fmt.Errorf("repoctx: lock clone purge: %w", err)
	}
	defer opsLock.Close()
	if err := m.cleanupExternalRunsForSourceLocked(ctx, target, sourceKey); err != nil {
		return err
	}
	if err := m.refuseUnknownLinkedWorktrees(ctx, target); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("repoctx: purge %q: %w", target, err)
	}
	return nil
}

// PurgeAll removes every valid Heimdallm-managed clone under cloneDir. It
// ignores unmanaged directories and invalid markers so operator-owned paths are
// never removed by a broad cleanup.
func (m *Manager) PurgeAll(ctx context.Context, cloneDir string) (PurgeReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil {
		return PurgeReport{}, fmt.Errorf("repoctx: nil manager")
	}
	clones, err := m.managedClones(cloneDir)
	if err != nil {
		return PurgeReport{}, err
	}
	var report PurgeReport
	var errs []error
	for _, clone := range clones {
		report.Scanned++
		if err := m.purgeTarget(ctx, clone.repo, clone.path); err != nil {
			errs = append(errs, err)
			continue
		}
		report.Removed++
	}
	return report, errors.Join(errs...)
}

// PurgeStale removes managed clones for repos that are no longer monitored and
// have not been prepared within maxDays. maxDays <= 0 disables cleanup.
func (m *Manager) PurgeStale(ctx context.Context, cloneDir string, monitored map[string]struct{}, maxDays int) (PurgeReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil {
		return PurgeReport{}, fmt.Errorf("repoctx: nil manager")
	}
	if maxDays <= 0 {
		return PurgeReport{}, nil
	}
	clones, err := m.managedClones(cloneDir)
	if err != nil {
		return PurgeReport{}, err
	}
	cutoff := time.Now().AddDate(0, 0, -maxDays)
	var report PurgeReport
	var errs []error
	for _, clone := range clones {
		report.Scanned++
		if _, ok := monitored[clone.repo]; ok {
			continue
		}
		if clone.markerModTime.After(cutoff) {
			continue
		}
		if err := m.purgeTarget(ctx, clone.repo, clone.path); err != nil {
			errs = append(errs, err)
			continue
		}
		report.Removed++
	}
	return report, errors.Join(errs...)
}

func resolveLocal(req Request) string {
	configured := strings.TrimSpace(req.ConfiguredLocalDir)
	if configured != "" {
		return configured
	}
	if req.Mode == ModeWrite {
		return ""
	}
	return config.ResolveLocalDir("", req.Repo, req.LocalDirBases)
}

func (m *Manager) ensureManagedClone(ctx context.Context, owner, name string, req Request) (string, error) {
	if strings.TrimSpace(req.Token) == "" {
		return "", fmt.Errorf("repoctx: clone %s requires a non-empty token", req.Repo)
	}
	target, err := m.cloneTarget(req.CloneDir, owner, name)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	switch {
	case err == nil:
		if !info.IsDir() {
			return "", fmt.Errorf("repoctx: clone target %q exists but is not a directory", target)
		}
		if err := validateMarker(target, req.Repo); err != nil {
			return "", err
		}
		if err := requireGitDir(target); err != nil {
			return "", err
		}
		if err := m.updateManagedClone(ctx, target, req.Repo, req.Token); err != nil {
			return "", err
		}
		if err := writeMarker(target, req.Repo); err != nil {
			return "", err
		}
		if err := ensureWorktreeExclude(target); err != nil {
			return "", err
		}
		return target, nil
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", fmt.Errorf("repoctx: create clone parent: %w", err)
		}
		if err := m.clone(ctx, target, req.Repo, req.Token); err != nil {
			_ = os.RemoveAll(target)
			return "", err
		}
		if err := writeMarker(target, req.Repo); err != nil {
			_ = os.RemoveAll(target)
			return "", err
		}
		if err := ensureWorktreeExclude(target); err != nil {
			_ = os.RemoveAll(target)
			return "", err
		}
		return target, nil
	default:
		return "", fmt.Errorf("repoctx: stat clone target %q: %w", target, err)
	}
}

func (m *Manager) clone(ctx context.Context, target, repo, token string) error {
	env, cleanup, err := buildAskPassEnv(token)
	if err != nil {
		return fmt.Errorf("repoctx: setup askpass: %w", err)
	}
	defer cleanup()
	url := fmt.Sprintf("https://x-access-token@github.com/%s.git", repo)
	if err := m.runner().Run(ctx, "", env, "clone", "--depth=1", url, target); err != nil {
		return fmt.Errorf("repoctx: clone %s: %w", repo, err)
	}
	if err := requireGitDir(target); err != nil {
		return err
	}
	return nil
}

func (m *Manager) updateManagedClone(ctx context.Context, target, repo, token string) error {
	env, cleanup, err := buildAskPassEnv(token)
	if err != nil {
		return fmt.Errorf("repoctx: setup askpass: %w", err)
	}
	defer cleanup()
	url := fmt.Sprintf("https://x-access-token@github.com/%s.git", repo)
	// set-url writes an opaque username-only URL and does not need
	// credentials; keep the askpass env scoped to network operations.
	if err := m.runner().Run(ctx, target, nil, "remote", "set-url", "origin", url); err != nil {
		return fmt.Errorf("repoctx: set remote url: %w", err)
	}
	if err := m.runner().Run(ctx, target, env, "fetch", "--depth=1", "--prune", "origin", "HEAD"); err != nil {
		return fmt.Errorf("repoctx: fetch %s: %w", repo, err)
	}
	if err := m.runner().Run(ctx, target, nil, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("repoctx: reset %s: %w", repo, err)
	}
	if err := m.runner().Run(ctx, target, nil, "clean", "-fd", "-e", MarkerFile, "-e", worktreesDir); err != nil {
		return fmt.Errorf("repoctx: clean %s: %w", repo, err)
	}
	return nil
}

// worktreesDir is the relative path under each clone where Heimdallm
// materialises per-execution worktrees. Excluded from `git clean` so
// in-flight executions aren't nuked by a concurrent repo update.
const worktreesDir = ".worktrees"

// ensureWorktreeExclude makes sure `<dir>/.git/info/exclude` lists
// the worktrees subdirectory. info/exclude is the per-clone analogue
// of .gitignore: it is never tracked by upstream, so `git reset
// --hard FETCH_HEAD` cannot revert our entry. Idempotent — an
// existing entry is left untouched.
func ensureWorktreeExclude(dir string) error {
	const entry = ".worktrees/"
	info := filepath.Join(dir, ".git", "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		return fmt.Errorf("repoctx: create %q: %w", info, err)
	}
	path := filepath.Join(info, "exclude")
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return os.WriteFile(path, []byte(entry+"\n"), 0o644)
	case err != nil:
		return fmt.Errorf("repoctx: read exclude %q: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == entry || trimmed == ".worktrees" || trimmed == "/.worktrees/" || trimmed == "/.worktrees" {
			return nil
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, []byte(entry+"\n")...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("repoctx: write exclude %q: %w", path, err)
	}
	return nil
}

func (m *Manager) cloneBase(cloneDir string) (string, error) {
	base := strings.TrimSpace(cloneDir)
	if base == "" {
		if m != nil && m.tempDir != nil {
			base = filepath.Join(m.tempDir(), "heimdallm")
		} else {
			base = DefaultCloneDir()
		}
	}
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("repoctx: resolve clone_dir %q: %w", base, err)
	}
	return baseAbs, nil
}

func (m *Manager) cloneTarget(cloneDir, owner, name string) (string, error) {
	baseAbs, err := m.cloneBase(cloneDir)
	if err != nil {
		return "", err
	}
	target := filepath.Join(baseAbs, owner, name)
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("repoctx: resolve clone target %q: %w", target, err)
	}
	if !pathWithin(baseAbs, targetAbs) {
		return "", fmt.Errorf("repoctx: clone target %q escapes clone_dir %q", targetAbs, baseAbs)
	}
	return targetAbs, nil
}

func (m *Manager) managedClones(cloneDir string) ([]managedClone, error) {
	baseAbs, err := m.cloneBase(cloneDir)
	if err != nil {
		return nil, err
	}
	orgEntries, err := os.ReadDir(baseAbs)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("repoctx: list clone_dir %q: %w", baseAbs, err)
	}

	var clones []managedClone
	for _, orgEntry := range orgEntries {
		if !orgEntry.IsDir() {
			continue
		}
		orgPath := filepath.Join(baseAbs, orgEntry.Name())
		repoEntries, err := os.ReadDir(orgPath)
		if err != nil {
			return nil, fmt.Errorf("repoctx: list clone org %q: %w", orgPath, err)
		}
		for _, repoEntry := range repoEntries {
			if !repoEntry.IsDir() {
				continue
			}
			target := filepath.Join(orgPath, repoEntry.Name())
			targetAbs, err := filepath.Abs(target)
			if err != nil || !pathWithin(baseAbs, targetAbs) {
				continue
			}
			marker, info, err := readMarkerInfo(targetAbs)
			if err != nil || marker.Version != 1 || marker.ManagedBy != "heimdallm" {
				continue
			}
			owner, name, err := splitRepo(marker.Repo)
			if err != nil {
				continue
			}
			expected, err := m.cloneTarget(cloneDir, owner, name)
			if err != nil || expected != targetAbs {
				continue
			}
			clones = append(clones, managedClone{
				repo:          marker.Repo,
				path:          targetAbs,
				markerModTime: info.ModTime(),
			})
		}
	}
	return clones, nil
}

func (m *Manager) purgeTarget(ctx context.Context, repo, target string) error {
	unlock, err := m.acquireRepoLock(ctx, repo)
	if err != nil {
		return err
	}
	defer unlock()

	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("repoctx: stat clone target %q: %w", target, err)
	}
	if err := validateMarker(target, repo); err != nil {
		return err
	}
	sourceKey := canonicalPathForKey(target)
	opsLock, err := m.acquireOpsLock(ctx, sourceKey)
	if err != nil {
		return fmt.Errorf("repoctx: lock clone purge: %w", err)
	}
	defer opsLock.Close()
	if err := m.cleanupExternalRunsForSourceLocked(ctx, target, sourceKey); err != nil {
		return err
	}
	if err := m.refuseUnknownLinkedWorktrees(ctx, target); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("repoctx: purge %q: %w", target, err)
	}
	return nil
}

func (m *Manager) runner() gitRunner {
	if m == nil || m.git == nil {
		return execGit{}
	}
	return m.git
}

func (m *Manager) acquireRepoLock(ctx context.Context, repo string) (func(), error) {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = make(map[string]*repoLock)
	}
	l := m.locks[repo]
	if l == nil {
		l = &repoLock{ch: make(chan struct{}, 1)}
		m.locks[repo] = l
	}
	l.refs++
	m.mu.Unlock()

	select {
	case l.ch <- struct{}{}:
	case <-ctx.Done():
		m.releaseRepoRef(repo, l)
		return nil, ctx.Err()
	}

	return func() {
		<-l.ch
		m.releaseRepoRef(repo, l)
	}, nil
}

func (m *Manager) releaseRepoRef(repo string, l *repoLock) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l.refs--
	if l.refs == 0 && m.locks[repo] == l {
		delete(m.locks, repo)
	}
}

// acquireWorktreeCap takes one slot of the per-repo worktree
// semaphore. When MaxWorktreesPerRepo is non-positive the cap is
// effectively disabled and the returned release is a no-op.
func (m *Manager) acquireWorktreeCap(ctx context.Context, repo string) (func(), error) {
	if m.maxWorktrees <= 0 {
		return func() {}, nil
	}
	m.mu.Lock()
	if m.caps == nil {
		m.caps = make(map[string]*repoCap)
	}
	c := m.caps[repo]
	if c == nil {
		c = &repoCap{ch: make(chan struct{}, m.maxWorktrees)}
		m.caps[repo] = c
	}
	c.refs++
	m.mu.Unlock()

	select {
	case c.ch <- struct{}{}:
	case <-ctx.Done():
		m.releaseCapRef(repo, c)
		return nil, ctx.Err()
	}

	return func() {
		<-c.ch
		m.releaseCapRef(repo, c)
	}, nil
}

// canonicalWorktreePath returns the absolute, symlink-resolved form
// of path. Symlink resolution targets the parent directory rather
// than the path itself so the function returns the same key for a
// path that does not yet exist (or has just been removed) and for
// the same path while the worktree directory is on disk. The parent
// (`<clone>/.worktrees/`) is created by Acquire before any mark /
// unmark, so resolving it is always possible.
//
// Without this, on platforms where the temp/clone root sits under a
// symlink (e.g. macOS `/var` → `/private/var`), markActive would
// store the resolved form while unmarkActive (after `os.RemoveAll`)
// fell back to the literal form, leaking the entry in m.active and
// pinning the worktree as "live" forever from PruneStaleWorktrees'
// perspective.
func canonicalWorktreePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	parent := filepath.Dir(abs)
	leaf := filepath.Base(abs)
	if resolvedParent, err := filepath.EvalSymlinks(parent); err == nil {
		return filepath.Join(resolvedParent, leaf)
	}
	return abs
}

func (m *Manager) markActive(path string) {
	canon := canonicalWorktreePath(path)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		m.active = make(map[string]struct{})
	}
	m.active[canon] = struct{}{}
}

func (m *Manager) unmarkActive(path string) {
	canon := canonicalWorktreePath(path)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.active, canon)
}

// PruneStaleWorktreesUnder discovers every managed clone beneath
// cloneBase and prunes stale worktrees for each. Used at startup so a
// single call covers all configured clone roots.
func (m *Manager) PruneStaleWorktreesUnder(ctx context.Context, cloneBase string) (int, error) {
	if m == nil {
		return 0, fmt.Errorf("repoctx: nil manager")
	}
	clones, err := m.managedClones(cloneBase)
	if err != nil {
		return 0, err
	}
	var total int
	var errs []error
	for _, c := range clones {
		n, err := m.PruneStaleWorktrees(ctx, c.path)
		total += n
		if err != nil {
			errs = append(errs, err)
		}
	}
	return total, errors.Join(errs...)
}

// PruneStaleWorktrees deliberately leaves legacy `.worktrees` directories
// untouched. Older releases did not create cross-process leases, so absence
// from this Manager's in-memory active set cannot prove that another daemon
// or inherited AI child is not using one. New worktrees live in the external
// v2 root and are cleaned by PruneStaleExternalWorktrees using kernel locks.
func (m *Manager) PruneStaleWorktrees(ctx context.Context, cloneDir string) (int, error) {
	if m == nil {
		return 0, fmt.Errorf("repoctx: nil manager")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	root := filepath.Join(cloneDir, worktreesDir)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("repoctx: list worktrees %q: %w", root, err)
	}
	unknown := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		canon := canonicalWorktreePath(path)
		m.mu.Lock()
		_, live := m.active[canon]
		m.mu.Unlock()
		if live {
			continue
		}
		unknown++
		slog.Warn("repoctx: retaining unleased legacy worktree (ownership cannot be proven)",
			"path", path)
	}
	if unknown > 0 {
		slog.Warn("repoctx: legacy worktrees require manual cleanup after confirming no process uses them",
			"dir", cloneDir, "count", unknown)
	}
	return 0, nil
}

func (m *Manager) releaseCapRef(repo string, c *repoCap) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.refs--
	if c.refs == 0 && m.caps[repo] == c {
		delete(m.caps, repo)
	}
}

// buildWorktreeAddArgs assembles `git worktree add` args. When branch
// is non-empty a fresh local branch is created with `-b`; otherwise
// the worktree is created detached. A non-empty baseRef is appended
// as the final positional argument so git resolves it as the start
// point. Empty baseRef leaves git to default to HEAD.
func buildWorktreeAddArgs(path, branch, baseRef string) []string {
	args := []string{"worktree", "add", path}
	if branch != "" {
		args = append(args, "-b", branch)
	} else {
		args = append(args, "--detach")
	}
	if baseRef != "" {
		args = append(args, baseRef)
	}
	return args
}

// validateWorktreeToken rejects any token that could escape the
// `.worktrees/` namespace or collide with git's reserved names. Path
// separators, parent-dir hops, and leading dots are forbidden.
func validateWorktreeToken(token string) error {
	if token == "" {
		return fmt.Errorf("repoctx: worktree token is required")
	}
	if token == "." || token == ".." {
		return fmt.Errorf("repoctx: worktree token %q is reserved", token)
	}
	if !worktreeTokenPattern.MatchString(token) {
		return fmt.Errorf("repoctx: worktree token %q must match [A-Za-z0-9][A-Za-z0-9._-]*", token)
	}
	return nil
}

func splitRepo(repo string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(repo), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repoctx: repo %q must be in owner/name form", repo)
	}
	if err := config.ValidateOrgSlug(parts[0]); err != nil {
		return "", "", err
	}
	name := parts[1]
	if name == "." || name == ".." || !repoNamePattern.MatchString(name) {
		return "", "", fmt.Errorf("repoctx: repo name %q is invalid", name)
	}
	return parts[0], name, nil
}

func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

type marker struct {
	Version   int    `json:"version"`
	Repo      string `json:"repo"`
	ManagedBy string `json:"managed_by"`
}

func writeMarker(dir, repo string) error {
	data, err := json.Marshal(marker{Version: 1, Repo: repo, ManagedBy: "heimdallm"})
	if err != nil {
		return fmt.Errorf("repoctx: marshal marker: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, MarkerFile), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("repoctx: write marker: %w", err)
	}
	return nil
}

func validateMarker(dir, repo string) error {
	marker, _, err := readMarkerInfo(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("repoctx: clone target %q exists but is not managed by Heimdallm", dir)
		}
		return err
	}
	if marker.Version != 1 || marker.ManagedBy != "heimdallm" || marker.Repo != repo {
		return fmt.Errorf("repoctx: marker in %q does not match repo %s", dir, repo)
	}
	return nil
}

func readMarkerInfo(dir string) (marker, os.FileInfo, error) {
	path := filepath.Join(dir, MarkerFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return marker{}, nil, err
	}
	var m marker
	if err := json.Unmarshal(data, &m); err != nil {
		return marker{}, nil, fmt.Errorf("repoctx: invalid marker in %q: %w", dir, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return marker{}, nil, fmt.Errorf("repoctx: stat marker in %q: %w", dir, err)
	}
	return m, info, nil
}

func touchMarker(dir string) error {
	marker, _, err := readMarkerInfo(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return writeMarker(dir, marker.Repo)
}

func requireGitDir(dir string) error {
	if info, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return fmt.Errorf("repoctx: clone target %q has no .git directory: %w", dir, err)
	} else if !info.IsDir() {
		return fmt.Errorf("repoctx: clone target %q has non-directory .git", dir)
	}
	return nil
}

func buildAskPassEnv(token string) ([]string, func(), error) {
	dir, err := os.MkdirTemp("", "heimdallm-askpass-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create askpass dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, nil, fmt.Errorf("chmod askpass dir: %w", err)
	}
	helperPath := filepath.Join(dir, "askpass.sh")
	script := "#!/bin/sh\nprintf '%s' \"$HEIMDALLM_GIT_TOKEN\"\n"
	if err := os.WriteFile(helperPath, []byte(script), 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, nil, fmt.Errorf("write askpass script: %w", err)
	}
	env := append(cleanGitEnvironment(os.Environ()),
		"GIT_ASKPASS="+helperPath,
		"GIT_TERMINAL_PROMPT=0",
		"HEIMDALLM_GIT_TOKEN="+token,
	)
	cleanup := func() { os.RemoveAll(dir) }
	return env, cleanup, nil
}

type execGit struct{}

func (execGit) Run(ctx context.Context, dir string, env []string, args ...string) error {
	_, err := (execGit{}).run(ctx, dir, env, nil, false, args...)
	return err
}

func (execGit) RunWithExtraFiles(
	ctx context.Context,
	dir string,
	env []string,
	extraFiles []*os.File,
	args ...string,
) error {
	_, err := (execGit{}).run(ctx, dir, env, extraFiles, false, args...)
	return err
}

func (execGit) Output(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	return (execGit{}).run(ctx, dir, env, nil, true, args...)
}

func (execGit) run(
	ctx context.Context,
	dir string,
	env []string,
	extraFiles []*os.File,
	captureStdout bool,
	args ...string,
) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", args...)
	cmd.Dir = dir
	cmd.Env = cleanGitEnvironment(os.Environ())
	if env != nil {
		// Callers construct explicit environments through buildAskPassEnv or
		// gitSafeDirectoryEnv; both start from cleanGitEnvironment and then add
		// the narrowly-scoped variables their operation needs.
		cmd.Env = env
	}
	cmd.ExtraFiles = append([]*os.File(nil), extraFiles...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if captureStdout {
		cmd.Stdout = &stdout
	}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errText := stderr.String()
		if len(errText) > maxGitStderrBytes {
			errText = errText[:maxGitStderrBytes] + "\n... (stderr truncated)"
		}
		return nil, fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(errText))
	}
	return stdout.Bytes(), nil
}

// cleanGitEnvironment prevents ambient repository-routing and configuration
// variables from redirecting a command or activating operator-owned hooks.
// Repository-local config is still readable where Git needs it, but the
// command-scope hooksPath override prevents checkout/commit hooks from running.
func cleanGitEnvironment(env []string) []string {
	out := make([]string, 0, len(env)+7)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch {
		case key == "GIT_DIR",
			key == "GIT_COMMON_DIR",
			key == "GIT_WORK_TREE",
			key == "GIT_INDEX_FILE",
			key == "GIT_OBJECT_DIRECTORY",
			key == "GIT_ALTERNATE_OBJECT_DIRECTORIES",
			key == "GIT_NAMESPACE",
			key == "GIT_CONFIG",
			key == "GIT_CONFIG_GLOBAL",
			key == "GIT_CONFIG_SYSTEM",
			key == "GIT_CONFIG_NOSYSTEM",
			key == "GIT_CONFIG_PARAMETERS",
			key == "GIT_IMPLICIT_WORK_TREE",
			key == "GIT_GRAFT_FILE",
			key == "GIT_REPLACE_REF_BASE",
			key == "GIT_NO_REPLACE_OBJECTS",
			key == "GIT_PREFIX",
			key == "GIT_INTERNAL_SUPER_PREFIX",
			key == "GIT_SHALLOW_FILE",
			key == "GIT_CEILING_DIRECTORIES",
			key == "GIT_DISCOVERY_ACROSS_FILESYSTEM",
			key == "GIT_ATTR_NOSYSTEM",
			key == "GIT_EXEC_PATH",
			key == "GIT_EXTERNAL_DIFF",
			key == "GIT_TEMPLATE_DIR",
			strings.HasPrefix(key, "GIT_TRACE"),
			key == "GIT_CURL_VERBOSE",
			key == "GIT_SSL_NO_VERIFY",
			key == "GIT_SSH",
			key == "GIT_SSH_COMMAND",
			key == "GIT_SSH_VARIANT",
			key == "GIT_CONFIG_COUNT",
			strings.HasPrefix(key, "GIT_CONFIG_KEY_"),
			strings.HasPrefix(key, "GIT_CONFIG_VALUE_"):
			continue
		default:
			out = append(out, entry)
		}
	}
	return append(out,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0="+os.DevNull,
	)
}
