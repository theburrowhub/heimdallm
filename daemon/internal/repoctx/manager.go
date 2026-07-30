package repoctx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/gitproc"
)

const (
	// MarkerFile identifies clones that Heimdallm is allowed to mutate.
	MarkerFile = ".heimdallm-managed"

	gitTimeout = 3 * time.Minute
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
	// ModeRead gives the AI CLI repository context. Existing local_dir and
	// local_dir_base checkouts are accepted because the manager never mutates
	// them.
	ModeRead Mode = iota
	// ModeWrite is for auto_implement. Only an explicit local_dir or a
	// Heimdallm-managed clone is returned, because local_dir_base mounts are
	// treated as read-only shared context.
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

	// WorktreeToken identifies the execution and becomes the subdirectory
	// name under `<clone>/.worktrees/<WorktreeToken>/`. Callers are
	// expected to derive a deterministic value (e.g. `triage-42`,
	// `pr-review-1234`) so that retries land on the same path and
	// concurrent operations on different keys never collide. Sanitised
	// against path traversal and unsafe characters.
	WorktreeToken string

	// WorktreeBaseRef, when set, becomes the ref the worktree is
	// created at (`git worktree add <path> --detach <ref>`). Empty
	// means HEAD of the clone.
	WorktreeBaseRef string

	// Branch, when non-empty, creates the worktree with a fresh local
	// branch (`git worktree add <path> -b <Branch> <BaseRef>`). Only
	// meaningful for ModeWrite executions that need to push to GitHub.
	Branch string

	// Inspect skips worktree creation and returns a handle pointing at
	// the clone root. Used by the read-only `/config/clones` endpoint
	// where worktree overhead would be pure waste.
	Inspect bool
}

// Handle owns a repo-context lock until Release is called.
type Handle struct {
	path    string
	managed bool
	once    sync.Once
	release func()
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
}

// secureGitRunner is implemented by the production runner. Keeping gitRunner
// as the smaller seam preserves deterministic fakes while network operations
// gain the isolated bare-transport path.
type secureGitRunner interface {
	gitRunner
	CloneRemote(ctx context.Context, target, repo, token string) error
	FetchRemote(ctx context.Context, target, repo, ref, token string, opts gitproc.FetchOptions) error
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
// runs. Concurrency uses a two-tier lock model:
//   - A binary critical-section lock per repo (`locks`) is held while
//     mutating the shared clone — fetch/reset/clean and worktree
//     add/remove. It is short-lived for worktree callers (released
//     once the worktree exists) and long-lived for legacy callers
//     that operate directly on the clone.
//   - A counting semaphore per repo (`caps`) bounds the number of
//     concurrent worktrees per repo to MaxWorktreesPerRepo. It is held
//     across the entire AI run and released after `worktree remove`.
type Manager struct {
	mu      sync.Mutex
	locks   map[string]*repoLock
	caps    map[string]*repoCap
	active  map[string]struct{} // absolute worktree paths currently held
	git     gitRunner
	tempDir func() string

	maxWorktrees int

	// wtSeq disambiguates concurrent worktrees that share the same
	// caller-supplied WorktreeToken (e.g. when poll review-worker and
	// manual trigger-review fire for the same PR). It is monotonic
	// across the manager's lifetime so paths stay stable for the
	// duration of a single execution.
	wtSeq atomic.Uint64

	// releaseTimeout caps how long a Handle.Release will wait for the
	// critical-section lock when running `git worktree remove`. Beyond
	// this, release falls back to a filesystem-only cleanup so the cap
	// semaphore is never held hostage by a stuck lock. Tests override
	// this; production keeps the default at gitTimeout.
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
		git:            execGit{runner: gitproc.New()},
		tempDir:        os.TempDir,
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
		return m.acquireClone(ctx, req, owner, name)
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
		return &Handle{path: local, managed: false, release: func() {}}, nil
	}
	path, err := m.ensureManagedClone(ctx, owner, name, req)
	if err != nil {
		return nil, err
	}
	return &Handle{path: path, managed: true, release: func() {}}, nil
}

// acquireClone is the legacy single-lock path used by callers that did
// not opt into worktrees yet. It mirrors the pre-#461 behaviour: hold
// the per-repo critical-section lock for the duration of the handle.
func (m *Manager) acquireClone(ctx context.Context, req Request, owner, name string) (*Handle, error) {
	unlock, err := m.acquireRepoLock(ctx, req.Repo)
	if err != nil {
		return nil, err
	}
	released := false
	release := func() {
		if !released {
			released = true
			unlock()
		}
	}
	defer func() {
		if !released && err != nil {
			release()
		}
	}()

	if local := resolveLocal(req); local != "" {
		return &Handle{path: local, managed: false, release: release}, nil
	}

	path, err := m.ensureManagedClone(ctx, owner, name, req)
	if err != nil {
		return nil, err
	}
	if req.Mode == ModeWrite {
		cloneHandle := &Handle{path: path, managed: true}
		if err = m.EnsureFullHistory(ctx, cloneHandle, req.Token); err != nil {
			return nil, fmt.Errorf("%w; retry after fixing Git access or purge the managed clone via DELETE /config/clones/{repo}", err)
		}
	}
	return &Handle{path: path, managed: true, release: release}, nil
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

	if local := resolveLocal(req); local != "" {
		// User-mapped repos sidestep worktree creation until the
		// gitignore bootstrap step lands; release the crit lock now
		// and keep the cap so concurrency is still bounded.
		releaseCrit()
		return &Handle{path: local, managed: false, release: releaseCap}, nil
	}

	var cloneRoot string
	cloneRoot, err = m.ensureManagedClone(ctx, owner, name, req)
	if err != nil {
		return nil, err
	}
	if req.Mode == ModeWrite {
		cloneHandle := &Handle{path: cloneRoot, managed: true}
		if err = m.EnsureFullHistory(ctx, cloneHandle, req.Token); err != nil {
			return nil, fmt.Errorf("%w; retry after fixing Git access or purge the managed clone via DELETE /config/clones/{repo}", err)
		}
	}

	seq := m.wtSeq.Add(1)
	wtName := fmt.Sprintf("%s.%d", req.WorktreeToken, seq)
	wtPath := filepath.Join(cloneRoot, ".worktrees", wtName)
	if err = os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return nil, fmt.Errorf("repoctx: create worktrees root: %w", err)
	}
	addArgs := buildWorktreeAddArgs(wtPath, req.Branch, req.WorktreeBaseRef)
	if err = m.runner().Run(ctx, cloneRoot, nil, addArgs...); err != nil {
		return nil, fmt.Errorf("repoctx: worktree add %s: %w", wtPath, err)
	}
	m.markActive(wtPath)

	releaseCrit()

	release := func() {
		// Re-acquire the critical-section lock briefly to serialise
		// the worktree-registry mutation. Use a fresh background ctx
		// so a cancelled caller ctx never leaves a worktree on disk.
		bgCtx, cancel := context.WithTimeout(context.Background(), m.releaseTimeout)
		defer cancel()
		// In-memory bookkeeping must clear regardless of whether the
		// git/filesystem cleanup succeeded — otherwise a single
		// lock-acquire timeout would pin an orphan in m.active
		// forever and PruneStaleWorktrees would never remove it.
		defer releaseCap()
		defer m.unmarkActive(wtPath)
		unlockRm, lockErr := m.acquireRepoLock(bgCtx, req.Repo)
		if lockErr != nil {
			slog.Warn("repoctx: worktree release lock unavailable; falling back to filesystem cleanup",
				"repo", req.Repo, "path", wtPath, "err", lockErr)
			// Git's worktree registry entry survives in `.git/worktrees/`,
			// but the next `git worktree prune` (issued by
			// PruneStaleWorktrees on startup) reaps it. Removing the
			// on-disk directory here is enough to keep `.worktrees/`
			// tidy and prevent collisions with a re-issued seq.
			if rmErr := os.RemoveAll(wtPath); rmErr != nil {
				slog.Warn("repoctx: worktree filesystem remove failed",
					"path", wtPath, "err", rmErr)
			}
			return
		}
		defer unlockRm()
		if rmErr := m.runner().Run(bgCtx, cloneRoot, nil, "worktree", "remove", "--force", wtPath); rmErr != nil {
			slog.Warn("repoctx: worktree remove failed", "path", wtPath, "err", rmErr)
		}
	}
	slog.Info("repoctx: worktree acquired",
		"repo", req.Repo, "token", req.WorktreeToken, "seq", seq, "path", wtPath)
	return &Handle{path: wtPath, managed: true, release: release}, nil
}

// EnsureFullHistory upgrades a managed shallow clone in-place. It assumes the
// caller still owns the handle lock returned by Acquire.
func (m *Manager) EnsureFullHistory(ctx context.Context, h *Handle, token string) error {
	if m == nil {
		return fmt.Errorf("repoctx: nil manager")
	}
	if h == nil || h.Path() == "" || !h.Managed() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if token == "" {
		return fmt.Errorf("repoctx: full-history fetch requires a non-empty token")
	}
	shallow := filepath.Join(h.Path(), ".git", "shallow")
	if _, err := os.Stat(shallow); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("repoctx: inspect shallow marker: %w", err)
	}
	runner := m.runner()
	if secure, ok := runner.(secureGitRunner); ok {
		metadata, _, err := readMarkerInfo(h.Path())
		if err != nil {
			return fmt.Errorf("repoctx: read managed clone marker for full history: %w", err)
		}
		if err := secure.FetchRemote(
			ctx,
			h.Path(),
			metadata.Repo,
			"HEAD",
			token,
			gitproc.FetchOptions{Unshallow: true},
		); err != nil {
			return fmt.Errorf("repoctx: unshallow %s: %w", h.Path(), err)
		}
	} else if err := runner.Run(ctx, h.Path(), nil, "fetch", "--unshallow", "--prune", "origin"); err != nil {
		return fmt.Errorf("repoctx: unshallow %s: %w", h.Path(), err)
	}
	if err := touchMarker(h.Path()); err != nil {
		return err
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
	runner := m.runner()
	if secure, ok := runner.(secureGitRunner); ok {
		if err := secure.CloneRemote(ctx, target, repo, token); err != nil {
			return fmt.Errorf("repoctx: clone %s: %w", repo, err)
		}
	} else {
		url, err := gitproc.GitHubRemote(repo)
		if err != nil {
			return fmt.Errorf("repoctx: remote: %w", err)
		}
		if err := runner.Run(ctx, "", nil, "clone", "--depth=1", url, target); err != nil {
			return fmt.Errorf("repoctx: clone %s: %w", repo, err)
		}
	}
	if err := requireGitDir(target); err != nil {
		return err
	}
	return nil
}

func (m *Manager) updateManagedClone(ctx context.Context, target, repo, token string) error {
	url, err := gitproc.GitHubRemote(repo)
	if err != nil {
		return fmt.Errorf("repoctx: remote: %w", err)
	}
	runner := m.runner()
	// set-url writes an opaque username-only URL and does not need
	// credentials.
	if err := runner.Run(ctx, target, nil, "remote", "set-url", "origin", url); err != nil {
		return fmt.Errorf("repoctx: set remote url: %w", err)
	}
	if secure, ok := runner.(secureGitRunner); ok {
		if err := secure.FetchRemote(
			ctx,
			target,
			repo,
			"HEAD",
			token,
			gitproc.FetchOptions{Depth: 1},
		); err != nil {
			return fmt.Errorf("repoctx: fetch %s: %w", repo, err)
		}
	} else if err := runner.Run(ctx, target, nil, "fetch", "--depth=1", "--prune", "origin", "HEAD"); err != nil {
		return fmt.Errorf("repoctx: fetch %s: %w", repo, err)
	}
	if err := runner.Run(ctx, target, nil, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("repoctx: reset %s: %w", repo, err)
	}
	if err := runner.Run(ctx, target, nil, "clean", "-fd", "-e", MarkerFile, "-e", worktreesDir); err != nil {
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
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("repoctx: purge %q: %w", target, err)
	}
	return nil
}

func (m *Manager) runner() gitRunner {
	if m == nil || m.git == nil {
		return execGit{runner: gitproc.New()}
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

// PruneStaleWorktrees removes any subdirectory under
// `<cloneDir>/.worktrees/` that the manager does not currently track
// as active and then runs `git worktree prune` so git's own registry
// matches the on-disk state. Intended for daemon startup (where any
// leftover worktree is by definition stale) and for periodic sweeps
// that catch leaks from crashed releases.
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
	pruned := 0
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
		// Try git's tooling first so the registry stays consistent;
		// fall back to filesystem removal if git refuses (e.g., the
		// registry entry already vanished).
		if err := m.runner().Run(ctx, cloneDir, nil, "worktree", "remove", "--force", path); err != nil {
			slog.Warn("repoctx: prune worktree via git failed, falling back to rm", "path", path, "err", err)
			if rmErr := os.RemoveAll(path); rmErr != nil {
				return pruned, fmt.Errorf("repoctx: prune worktree %q: %w", path, rmErr)
			}
		}
		pruned++
	}
	if err := m.runner().Run(ctx, cloneDir, nil, "worktree", "prune"); err != nil {
		slog.Warn("repoctx: git worktree prune", "dir", cloneDir, "err", err)
	}
	return pruned, nil
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
	path := filepath.Join(dir, ".git")
	if info, err := os.Lstat(path); err != nil {
		return fmt.Errorf("repoctx: clone target %q has no .git directory: %w", dir, err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("repoctx: clone target %q has symlinked .git", dir)
	} else if !info.IsDir() {
		return fmt.Errorf("repoctx: clone target %q has non-directory .git", dir)
	}
	return nil
}

type execGit struct {
	runner *gitproc.Runner
}

func (g execGit) proc() *gitproc.Runner {
	if g.runner == nil {
		return gitproc.New()
	}
	return g.runner
}

func (g execGit) Run(ctx context.Context, dir string, env []string, args ...string) error {
	if len(env) != 0 {
		return errors.New("repoctx: custom Git environments are disabled")
	}
	return g.proc().Run(ctx, dir, args...)
}

func (g execGit) CloneRemote(ctx context.Context, target, repo, token string) error {
	remote, err := gitproc.GitHubRemote(repo)
	if err != nil {
		return err
	}
	return g.proc().CloneNoCheckout(ctx, remote, target, token, 1)
}

func (g execGit) FetchRemote(
	ctx context.Context,
	target, repo, ref, token string,
	opts gitproc.FetchOptions,
) error {
	remote, err := gitproc.GitHubRemote(repo)
	if err != nil {
		return err
	}
	return g.proc().Fetch(ctx, target, remote, ref, token, opts)
}
