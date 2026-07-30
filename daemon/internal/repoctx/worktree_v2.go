package repoctx

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/config"
)

const (
	externalWorktreeDir = "heimdallm-worktrees-v2"
	externalLockDir     = "heimdallm-locks-v2"
	worktreeLeaseFile   = "lease.lock"
	worktreeMetaFile    = "lease.json"
	worktreeCleanupFile = "cleanup.ready"
	leaseKindWorktree   = "worktree"
	leaseKindSnapshot   = "snapshot"
	// leaseKindSharedCopy is retained only so a new daemon can safely remove
	// metadata written by prerelease builds. New acquisitions never create
	// shared clones because alternates make them depend on the source object
	// store and their origin points back at the operator's repository.
	leaseKindSharedCopy = "shared-clone"
)

var (
	commitSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)
	fetchRefPattern  = regexp.MustCompile(`^refs/(pull/[1-9][0-9]*/head|heads/[A-Za-z0-9][A-Za-z0-9._/-]*)$`)
	sourceIDPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)

	// ErrRepoBusy is returned when a destructive operation would overlap an
	// isolated worktree held by this or another Heimdallm process.
	ErrRepoBusy = errors.New("repoctx: repository has active worktrees")
	// ErrSnapshotChanged means a supposedly read-only review checkout no
	// longer represents the immutable commit materialised by Acquire.
	ErrSnapshotChanged = errors.New("repoctx: immutable review snapshot changed")
)

type worktreeSource struct {
	root       string
	commonDir  string
	key        string
	repo       string
	managed    bool
	opsLockKey string
}

type worktreeLease struct {
	runDir    string
	path      string
	metaPath  string
	lock      *fileLock
	sourceKey string
	kind      string
}

type worktreeLeaseMetadata struct {
	Version int    `json:"version"`
	Repo    string `json:"repo"`
	// SourceRoot and SourceKey are decoded only for cleanup compatibility
	// with version-1 prerelease metadata. Version 2 deliberately omits the
	// operator checkout's absolute path from the run directory: an AI process
	// can walk from checkout to ../lease.json under the same UID.
	SourceRoot string `json:"source_root,omitempty"`
	SourceKey  string `json:"source_key,omitempty"`
	// SourceID is derived from the private coordination-root directory name;
	// it is never serialized into lease.json.
	SourceID  string    `json:"-"`
	Worktree  string    `json:"worktree"`
	CommitSHA string    `json:"commit_sha"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

// prepareWorktreeSource resolves either an operator-owned local repository or
// a Heimdallm-managed clone, then takes the cross-process operations lock for
// that Git object store. The local repository is never reset, cleaned,
// checked out, or used as the AI process CWD.
func (m *Manager) prepareWorktreeSource(
	ctx context.Context,
	req Request,
	owner, name string,
) (*worktreeSource, *fileLock, error) {
	if local := resolveWorktreeSource(req); local != "" {
		source, err := m.inspectLocalSource(ctx, local, req.Repo)
		if err != nil {
			return nil, nil, err
		}
		lock, err := m.acquireOpsLock(ctx, source.opsLockKey)
		if err != nil {
			return nil, nil, fmt.Errorf("repoctx: lock local repository operations: %w", err)
		}
		return source, lock, nil
	}

	target, err := m.cloneTarget(req.CloneDir, owner, name)
	if err != nil {
		return nil, nil, err
	}
	key := canonicalPathForKey(target)
	lock, err := m.acquireOpsLock(ctx, key)
	if err != nil {
		return nil, nil, fmt.Errorf("repoctx: lock managed repository operations: %w", err)
	}
	sourceRoot, err := m.ensureManagedClone(ctx, owner, name, req)
	if err != nil {
		_ = lock.Close()
		return nil, nil, err
	}
	commonDir := filepath.Join(sourceRoot, ".git")
	return &worktreeSource{
		root:       sourceRoot,
		commonDir:  commonDir,
		key:        key,
		repo:       req.Repo,
		managed:    true,
		opsLockKey: key,
	}, lock, nil
}

func resolveWorktreeSource(req Request) string {
	if configured := strings.TrimSpace(req.ConfiguredLocalDir); configured != "" {
		return configured
	}
	// An independent snapshot makes local_dir_base safe for write-mode
	// executions as well: only the ephemeral checkout is mutable, never the
	// mapped source.
	return configResolveLocalDir(req.Repo, req.LocalDirBases)
}

// Kept as a tiny seam so worktree_v2.go does not duplicate config's basename
// resolution rules; identity is validated immediately afterwards.
var configResolveLocalDir = func(repo string, bases []string) string {
	return config.ResolveLocalDir("", repo, bases)
}

func (m *Manager) inspectLocalSource(ctx context.Context, local, repo string) (*worktreeSource, error) {
	abs, err := filepath.Abs(strings.TrimSpace(local))
	if err != nil {
		return nil, fmt.Errorf("repoctx: resolve local repository %q: %w", local, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("repoctx: resolve local repository symlinks %q: %w", abs, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("repoctx: stat local repository %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repoctx: local repository %q is not a directory", resolved)
	}

	safeEnv := gitSafeDirectoryEnv(resolved)
	commonOut, err := m.runner().Output(ctx, resolved, safeEnv, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("repoctx: local repository %q is not a Git checkout: %w", resolved, err)
	}
	commonDir := strings.TrimSpace(string(commonOut))
	if commonDir == "" {
		return nil, fmt.Errorf("repoctx: local repository %q returned an empty git common dir", resolved)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(resolved, commonDir)
	}
	commonDir = canonicalPathForKey(commonDir)

	remoteOut, err := m.runner().Output(ctx, resolved, safeEnv, "remote", "get-url", "--all", "origin")
	if err != nil {
		return nil, fmt.Errorf("repoctx: inspect origin for local repository %q: %w", resolved, err)
	}
	if !remoteMatchesRepo(string(remoteOut), repo) {
		return nil, fmt.Errorf("repoctx: local repository %q origin does not match %s", resolved, repo)
	}
	return &worktreeSource{
		root:       resolved,
		commonDir:  commonDir,
		key:        commonDir,
		repo:       repo,
		managed:    false,
		opsLockKey: commonDir,
	}, nil
}

// gitSafeDirectoryEnv authorises exactly one operator-selected checkout for
// read-only Git inspection. This is required in Docker when the bind mount's
// host UID differs from the daemon UID. A wildcard safe.directory would trust
// unrelated repositories and is deliberately avoided.
func gitSafeDirectoryEnv(path string) []string {
	env := cleanGitEnvironment(os.Environ())
	for i := range env {
		if strings.HasPrefix(env[i], "GIT_CONFIG_COUNT=") {
			env[i] = "GIT_CONFIG_COUNT=2"
			break
		}
	}
	return append(env,
		"GIT_CONFIG_KEY_1=safe.directory",
		"GIT_CONFIG_VALUE_1="+path,
	)
}

func remoteMatchesRepo(raw, repo string) bool {
	want := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(repo), ".git"))
	for _, line := range strings.Fields(raw) {
		if got, ok := githubRepoFromRemote(line); ok && strings.EqualFold(got, want) {
			return true
		}
	}
	return false
}

func githubRepoFromRemote(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	if strings.HasPrefix(value, "git@github.com:") {
		path := strings.TrimPrefix(value, "git@github.com:")
		return trimRepoPath(path)
	}
	u, err := url.Parse(value)
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return "", false
	}
	return trimRepoPath(u.Path)
}

func trimRepoPath(path string) (string, bool) {
	path = strings.Trim(strings.TrimSpace(path), "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return strings.ToLower(parts[0] + "/" + parts[1]), true
}

func canonicalPathForKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	abs = filepath.Clean(abs)

	// Resolve the deepest existing ancestor, then append the missing suffix.
	// Looking at only the immediate parent is insufficient for a new managed
	// clone: both owner and repository directories may not exist yet. On macOS
	// that made `/var/.../owner/repo` acquire one lock before creation and
	// `/private/var/.../owner/repo` acquire a different lock afterwards.
	current := abs
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
	return abs
}

func sourceIDForKey(sourceKey string) string {
	sum := sha256.Sum256([]byte(sourceKey))
	return hex.EncodeToString(sum[:])
}

func (m *Manager) opsLockPath(sourceKey string) string {
	return m.opsLockPathForID(sourceIDForKey(sourceKey))
}

func (m *Manager) opsLockPathForID(sourceID string) string {
	return filepath.Join(m.managerTempDir(), externalLockDir, sourceID+".lock")
}

func defaultCoordinationDir() string {
	if cacheDir, err := os.UserCacheDir(); err == nil && strings.TrimSpace(cacheDir) != "" {
		return filepath.Join(cacheDir, "heimdallm", "repoctx-v2")
	}
	// Last-resort fallback for environments without a discoverable user cache.
	// Normal desktop/service processes use the stable user cache above, so
	// differing TMPDIR values cannot split their lock namespace.
	return filepath.Join(os.TempDir(), "heimdallm-repoctx-v2")
}

func (m *Manager) acquireOpsLock(ctx context.Context, sourceKey string) (*fileLock, error) {
	return m.acquireOpsLockForID(ctx, sourceIDForKey(sourceKey))
}

func (m *Manager) acquireOpsLockForID(ctx context.Context, sourceID string) (*fileLock, error) {
	if !sourceIDPattern.MatchString(sourceID) {
		return nil, fmt.Errorf("repoctx: invalid operations lock source ID")
	}
	path := m.opsLockPathForID(sourceID)
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("repoctx: create operations lock directory: %w", err)
	}
	return acquireFileLock(ctx, path)
}

func (m *Manager) worktreeRoot(sourceKey string) string {
	return filepath.Join(m.managerTempDir(), externalWorktreeDir, sourceIDForKey(sourceKey))
}

func (m *Manager) managerTempDir() string {
	if m != nil && m.coordDir != nil {
		return m.coordDir()
	}
	return defaultCoordinationDir()
}

func (m *Manager) resolveWorktreeCommit(
	ctx context.Context,
	source *worktreeSource,
	req Request,
) (string, error) {
	baseRef := strings.TrimSpace(req.WorktreeBaseRef)
	fetchRef := strings.TrimSpace(req.WorktreeFetchRef)
	if fetchRef != "" {
		if baseRef == "" || !commitSHAPattern.MatchString(baseRef) {
			return "", fmt.Errorf("repoctx: worktree fetch ref requires an immutable commit SHA")
		}
		if !fetchRefPattern.MatchString(fetchRef) || strings.Contains(fetchRef, "..") {
			return "", fmt.Errorf("repoctx: invalid worktree fetch ref %q", fetchRef)
		}
	}

	if baseRef != "" && commitSHAPattern.MatchString(baseRef) {
		if source.managed {
			if err := m.ensureCommitAvailable(ctx, source, req, strings.ToLower(baseRef), fetchRef); err != nil {
				return "", err
			}
		}
		return strings.ToLower(baseRef), nil
	}

	verifyRef := "HEAD"
	if baseRef != "" {
		if strings.HasPrefix(baseRef, "-") || strings.ContainsAny(baseRef, "\x00\n\r") {
			return "", fmt.Errorf("repoctx: invalid worktree base ref %q", baseRef)
		}
		verifyRef = baseRef
	}
	var env []string
	if !source.managed {
		env = gitSafeDirectoryEnv(source.root)
	}
	out, err := m.runner().Output(ctx, source.root, env,
		"rev-parse", "--verify", verifyRef+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("repoctx: resolve worktree base %q: %w", verifyRef, err)
	}
	sha := strings.ToLower(strings.TrimSpace(string(out)))
	if !commitSHAPattern.MatchString(sha) {
		return "", fmt.Errorf("repoctx: resolved worktree base %q to invalid commit %q", verifyRef, sha)
	}
	return sha, nil
}

func (m *Manager) ensureCommitAvailable(
	ctx context.Context,
	source *worktreeSource,
	req Request,
	commitSHA, fetchRef string,
) error {
	object := commitSHA + "^{commit}"
	if err := m.runner().Run(ctx, source.root, nil, "cat-file", "-e", object); err == nil {
		return nil
	}
	return m.fetchCommitFromGitHub(ctx, source, req, commitSHA, fetchRef, nil)
}

func (m *Manager) fetchCommitFromGitHub(
	ctx context.Context,
	source *worktreeSource,
	req Request,
	commitSHA, fetchRef string,
	extraFiles []*os.File,
) error {
	object := commitSHA + "^{commit}"
	if strings.TrimSpace(req.Token) == "" {
		return fmt.Errorf("repoctx: fetch commit %s requires a non-empty token", commitSHA)
	}
	env, cleanup, err := buildAskPassEnv(req.Token)
	if err != nil {
		return fmt.Errorf("repoctx: setup askpass: %w", err)
	}
	defer cleanup()
	remoteURL := fmt.Sprintf("https://x-access-token@github.com/%s.git", req.Repo)
	fetchArgs := []string{"fetch", "--no-tags", "--no-write-fetch-head"}
	if source.managed {
		// Heimdallm owns this clone and deliberately keeps it shallow.
		fetchArgs = append(fetchArgs, "--depth=1")
	}
	// Never pass --depth when this helper is ever used on an operator-owned
	// source: doing so would rewrite its shared .git/shallow file. Operator
	// snapshots are marked managed because only that isolated target is
	// fetched, so their shallow boundary is safe.
	fetchArgs = append(fetchArgs, remoteURL, commitSHA)
	fetchErr := m.runner().RunWithExtraFiles(ctx, source.root, env, extraFiles, fetchArgs...)
	if fetchErr != nil && fetchRef != "" {
		fetchArgs[len(fetchArgs)-1] = fetchRef
		fetchErr = m.runner().RunWithExtraFiles(ctx, source.root, env, extraFiles, fetchArgs...)
	}
	if fetchErr != nil {
		return fmt.Errorf("repoctx: fetch immutable commit %s: %w", commitSHA, fetchErr)
	}
	if err := m.runner().RunWithExtraFiles(ctx, source.root, nil, extraFiles,
		"cat-file", "-e", object); err != nil {
		return fmt.Errorf("repoctx: fetched ref does not contain expected commit %s: %w", commitSHA, err)
	}
	return nil
}

func (m *Manager) createWorktreeLease(
	source *worktreeSource,
	req Request,
	commitSHA string,
) (*worktreeLease, error) {
	root := m.worktreeRoot(source.opsLockKey)
	if err := ensurePrivateDir(root); err != nil {
		return nil, fmt.Errorf("repoctx: create external worktree root: %w", err)
	}

	for attempts := 0; attempts < 8; attempts++ {
		id, err := randomHex(16)
		if err != nil {
			return nil, err
		}
		runDir := filepath.Join(root, req.WorktreeToken+"."+id)
		if err := os.Mkdir(runDir, 0o700); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("repoctx: reserve worktree run directory: %w", err)
		}

		leasePath := filepath.Join(runDir, worktreeLeaseFile)
		lock, err := acquireFileLock(context.Background(), leasePath)
		if err != nil {
			_ = os.RemoveAll(runDir)
			return nil, fmt.Errorf("repoctx: acquire worktree lease: %w", err)
		}
		wtPath := filepath.Join(runDir, "checkout")
		// Every new execution gets its own object database. A linked worktree
		// would keep HEAD/files separate but still expose shared refs, shallow
		// boundaries and object maintenance from the managed source clone.
		// Snapshotting managed and operator-owned sources identically makes the
		// entire Git view immutable for the lifetime of the AI process.
		kind := leaseKindSnapshot
		meta := worktreeLeaseMetadata{
			Version:   2,
			Repo:      req.Repo,
			Worktree:  wtPath,
			CommitSHA: commitSHA,
			Kind:      kind,
			CreatedAt: time.Now().UTC(),
		}
		data, err := json.Marshal(meta)
		if err == nil {
			err = os.WriteFile(filepath.Join(runDir, worktreeMetaFile), append(data, '\n'), 0o600)
		}
		if err != nil {
			_ = lock.Close()
			_ = os.RemoveAll(runDir)
			return nil, fmt.Errorf("repoctx: write worktree lease metadata: %w", err)
		}
		return &worktreeLease{
			runDir:    runDir,
			path:      wtPath,
			metaPath:  filepath.Join(runDir, worktreeMetaFile),
			lock:      lock,
			sourceKey: source.opsLockKey,
			kind:      kind,
		}, nil
	}
	return nil, fmt.Errorf("repoctx: could not allocate a unique worktree run directory")
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("unsafe coordination directory %q", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("coordination directory %q permissions are %03o, want no group/other access",
			path, info.Mode().Perm())
	}
	return nil
}

func randomHex(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("repoctx: generate worktree id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func (m *Manager) materializeIsolatedWorktree(
	ctx context.Context,
	source *worktreeSource,
	req Request,
	commitSHA string,
	lease *worktreeLease,
	opsLock *fileLock,
) (bool, error) {
	switch lease.kind {
	case leaseKindWorktree:
		if err := m.runner().RunWithExtraFiles(ctx, source.root, nil,
			mutationGuardFiles(lease, opsLock),
			"worktree", "add", lease.path, "--detach", commitSHA); err != nil {
			// Git may have created the directory and common-dir registry entry
			// before a filter/hook failed. Tell rollback to inspect and remove
			// that partial materialisation under the lease.
			return true, fmt.Errorf("repoctx: worktree add %s: %w", lease.path, err)
		}
	case leaseKindSnapshot:
		// Sources may be mounted read-only (Docker), and their refs, index,
		// shallow boundary and linked-worktree registry must remain untouched.
		// Initialise an independent repository and fetch only the requested
		// commit closure. This duplicates one snapshot, not the repository's
		// full history, and has neither alternates nor an origin pointing back
		// at the source checkout.
		if err := os.MkdirAll(lease.path, 0o700); err != nil {
			return false, fmt.Errorf("repoctx: create isolated snapshot %s: %w", lease.path, err)
		}
		guards := mutationGuardFiles(lease, opsLock)
		if err := m.runner().RunWithExtraFiles(ctx, lease.path, nil, guards,
			"init", "--quiet", "--template="); err != nil {
			return true, fmt.Errorf("repoctx: initialise isolated snapshot %s: %w", lease.path, err)
		}

		// Prefer the already-present local object store. Fetch copies the
		// commit/tree/blob closure into the snapshot; unlike clone --shared it
		// does not install alternates. If the PR commit is absent locally
		// (common for fork heads), fetch the exact SHA/ref from GitHub into the
		// isolated repository instead.
		localEnv := gitSafeDirectoryEnv(source.root)
		localFetchErr := m.runner().RunWithExtraFiles(ctx, lease.path, localEnv, guards,
			"fetch", "--no-tags", "--no-write-fetch-head", "--depth=1",
			source.root, commitSHA)
		isolated := &worktreeSource{root: lease.path, managed: true}
		if localFetchErr != nil {
			if err := m.fetchCommitFromGitHub(ctx, isolated, req, commitSHA,
				strings.TrimSpace(req.WorktreeFetchRef), guards); err != nil {
				return true, errors.Join(
					fmt.Errorf("repoctx: copy immutable commit from local source: %w", localFetchErr),
					err,
				)
			}
		} else if verifyErr := m.runner().RunWithExtraFiles(ctx, lease.path, nil, guards,
			"cat-file", "-e", commitSHA+"^{commit}"); verifyErr != nil {
			if err := m.fetchCommitFromGitHub(ctx, isolated, req, commitSHA,
				strings.TrimSpace(req.WorktreeFetchRef), guards); err != nil {
				return true, errors.Join(
					fmt.Errorf("repoctx: local snapshot fetch omitted commit %s: %w", commitSHA, verifyErr),
					err,
				)
			}
		}
		if err := m.runner().RunWithExtraFiles(ctx, lease.path, nil, guards,
			"checkout", "--detach", commitSHA); err != nil {
			return true, fmt.Errorf("repoctx: checkout immutable commit %s: %w", commitSHA, err)
		}
		status, statusErr := m.runner().Output(ctx, lease.path, nil,
			"status", "--porcelain=v1", "--untracked-files=all")
		if statusErr != nil {
			return true, fmt.Errorf("repoctx: verify clean isolated snapshot: %w", statusErr)
		}
		if strings.TrimSpace(string(status)) != "" {
			return true, fmt.Errorf("repoctx: isolated snapshot is dirty immediately after checkout")
		}
		if err := verifyIndependentSnapshot(lease.path); err != nil {
			return true, err
		}
		if err := m.runner().RunWithExtraFiles(ctx, lease.path, nil, guards,
			"fsck", "--connectivity-only", "--no-dangling", "HEAD"); err != nil {
			return true, fmt.Errorf("repoctx: verify isolated snapshot object closure: %w", err)
		}
	default:
		return false, fmt.Errorf("repoctx: unknown isolated checkout kind %q", lease.kind)
	}
	out, err := m.runner().Output(ctx, lease.path, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return true, fmt.Errorf("repoctx: verify worktree HEAD: %w", err)
	}
	got := strings.ToLower(strings.TrimSpace(string(out)))
	if got != commitSHA {
		return true, fmt.Errorf("repoctx: worktree HEAD mismatch: got %s, want %s", got, commitSHA)
	}
	return true, nil
}

func (m *Manager) ensureSnapshotFullHistory(
	ctx context.Context,
	h *Handle,
	token string,
	extraFiles []*os.File,
) error {
	shallow, err := m.runner().Output(ctx, h.Path(), nil, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return fmt.Errorf("repoctx: inspect isolated snapshot history: %w", err)
	}
	if strings.TrimSpace(string(shallow)) != "true" {
		return nil
	}

	ref := h.CommitSHA()
	var localErr error
	if source := strings.TrimSpace(h.sourceRoot); source != "" && source != h.Path() {
		localErr = m.runner().RunWithExtraFiles(ctx, h.Path(), gitSafeDirectoryEnv(source), extraFiles,
			"fetch", "--unshallow", "--no-tags", "--no-write-fetch-head", source, ref)
		if localErr == nil {
			stillShallow, inspectErr := m.runner().Output(ctx, h.Path(), nil,
				"rev-parse", "--is-shallow-repository")
			if inspectErr == nil && strings.TrimSpace(string(stillShallow)) == "false" {
				return nil
			}
			if inspectErr != nil {
				localErr = inspectErr
			} else {
				localErr = fmt.Errorf("local source did not contain complete history")
			}
		}
	}

	if strings.TrimSpace(token) == "" {
		return errors.Join(
			fmt.Errorf("repoctx: full-history fetch requires a non-empty token"),
			localErr,
		)
	}
	if strings.TrimSpace(h.repo) == "" {
		return errors.Join(
			fmt.Errorf("repoctx: full-history fetch has no repository identity"),
			localErr,
		)
	}
	env, cleanup, err := buildAskPassEnv(token)
	if err != nil {
		return fmt.Errorf("repoctx: setup askpass: %w", err)
	}
	defer cleanup()

	remoteURL := fmt.Sprintf("https://x-access-token@github.com/%s.git", h.repo)
	fetch := func(fetchRef string) error {
		return m.runner().RunWithExtraFiles(ctx, h.Path(), env, extraFiles,
			"fetch", "--unshallow", "--no-tags", "--no-write-fetch-head", remoteURL, fetchRef)
	}
	remoteErr := fetch(ref)
	if remoteErr != nil && strings.TrimSpace(h.fetchRef) != "" {
		remoteErr = fetch(strings.TrimSpace(h.fetchRef))
	}
	if remoteErr != nil {
		return errors.Join(
			fmt.Errorf("repoctx: unshallow isolated snapshot for %s: %w", h.repo, remoteErr),
			localErr,
		)
	}
	stillShallow, err := m.runner().Output(ctx, h.Path(), nil,
		"rev-parse", "--is-shallow-repository")
	if err != nil {
		return fmt.Errorf("repoctx: verify isolated snapshot history: %w", err)
	}
	if strings.TrimSpace(string(stillShallow)) != "false" {
		return fmt.Errorf("repoctx: isolated snapshot remained shallow after full-history fetch")
	}
	if err := m.runner().RunWithExtraFiles(ctx, h.Path(), nil, extraFiles,
		"cat-file", "-e", ref+"^{commit}"); err != nil {
		return fmt.Errorf("repoctx: full-history snapshot lost commit %s: %w", ref, err)
	}
	return nil
}

func mutationGuardFiles(lease *worktreeLease, opsLock *fileLock) []*os.File {
	files := make([]*os.File, 0, 2)
	if lease != nil && lease.lock != nil && lease.lock.File() != nil {
		files = append(files, lease.lock.File())
	}
	if opsLock != nil && opsLock.File() != nil {
		files = append(files, opsLock.File())
	}
	return files
}

func verifyIndependentSnapshot(path string) error {
	alternates := filepath.Join(path, ".git", "objects", "info", "alternates")
	info, err := os.Lstat(alternates)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("repoctx: inspect isolated snapshot alternates: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("repoctx: unsafe alternates entry in isolated snapshot %q", alternates)
	}
	data, err := os.ReadFile(alternates)
	if err != nil {
		return fmt.Errorf("repoctx: read isolated snapshot alternates: %w", err)
	}
	if strings.TrimSpace(string(data)) != "" {
		return fmt.Errorf("repoctx: isolated snapshot unexpectedly depends on alternate object stores")
	}
	return nil
}

func closeWorktreeLease(lease *worktreeLease, removeRunDir bool) {
	if lease == nil {
		return
	}
	if lease.lock != nil {
		_ = lease.lock.Close()
	}
	if removeRunDir {
		_ = os.RemoveAll(lease.runDir)
	}
}

// rollbackFailedMaterialization is deliberately lease-aware. A failed git
// command may leave a filter, hook, or descendant alive with the inherited
// descriptors. Closing the daemon's descriptor and probing through a new open
// file description proves whether deletion is safe; ambiguity retains the run
// directory fail-closed for operator inspection.
func (m *Manager) rollbackFailedMaterialization(
	source *worktreeSource,
	lease *worktreeLease,
	materialized bool,
) {
	if lease == nil {
		return
	}
	closeWorktreeLease(lease, false)
	probe, acquired, err := tryFileLock(filepath.Join(lease.runDir, worktreeLeaseFile))
	if err != nil {
		slog.Warn("repoctx: failed materialisation lease probe failed; retaining checkout",
			"path", lease.path, "err", err)
		return
	}
	if !acquired {
		slog.Warn("repoctx: failed materialisation left a live child; retaining checkout",
			"path", lease.path)
		return
	}
	defer probe.Close()

	if materialized {
		ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
		removeErr := m.removeMaterializedSnapshot(ctx, source.root, lease.kind, lease.path)
		cancel()
		if removeErr != nil {
			// The lease is proven idle, so a later periodic sweep may safely
			// retry. Never remove runDir when Git's registry cleanup failed.
			if readyErr := markWorktreeCleanupReady(lease.runDir); readyErr != nil {
				removeErr = errors.Join(removeErr, readyErr)
			}
			slog.Warn("repoctx: failed materialisation rollback incomplete; retaining checkout",
				"path", lease.path, "err", removeErr)
			return
		}
	}
	if err := os.RemoveAll(lease.runDir); err != nil {
		slog.Warn("repoctx: remove failed materialisation lease directory",
			"path", lease.runDir, "err", err)
	}
}

func markWorktreeCleanupReady(runDir string) error {
	path := filepath.Join(runDir, worktreeCleanupFile)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("repoctx: unsafe cleanup-ready marker %q", path)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("repoctx: create cleanup-ready marker %q: %w", path, err)
	}
	return file.Close()
}

func worktreeCleanupReady(runDir string) (bool, error) {
	path := filepath.Join(runDir, worktreeCleanupFile)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("repoctx: inspect cleanup-ready marker %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Mode().Perm()&0o077 != 0 {
		return false, fmt.Errorf("repoctx: unsafe cleanup-ready marker %q", path)
	}
	return true, nil
}

func (m *Manager) removeIsolatedWorktree(
	ctx context.Context,
	source *worktreeSource,
	lease *worktreeLease,
) error {
	if source == nil || lease == nil {
		return nil
	}
	lock, err := m.acquireOpsLock(ctx, source.opsLockKey)
	if err != nil {
		return fmt.Errorf("repoctx: lock worktree removal: %w", err)
	}
	defer lock.Close()
	return m.removeMaterializedSnapshot(ctx, source.root, lease.kind, lease.path)
}

func (m *Manager) removeMaterializedSnapshot(
	ctx context.Context,
	sourceRoot, kind, path string,
) error {
	switch kind {
	case "", leaseKindWorktree:
		if err := m.runner().Run(ctx, sourceRoot, nil, "worktree", "remove", "--force", path); err != nil {
			return fmt.Errorf("repoctx: worktree remove %s: %w", path, err)
		}
		return nil
	case leaseKindSnapshot, leaseKindSharedCopy:
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("repoctx: remove isolated snapshot %s: %w", path, err)
		}
		return nil
	default:
		return fmt.Errorf("repoctx: unknown isolated checkout kind %q", kind)
	}
}

// PruneStaleExternalWorktrees removes only v2 worktrees whose normal Release
// wrote cleanup.ready and whose valid lease can be locked non-blockingly. A
// crash before Release (including while git or an AI child still runs) leaves
// no marker and is retained deliberately: lock absence alone cannot prove that
// an arbitrary CLI did not close inherited descriptors while retaining a
// background process that still uses the checkout.
func (m *Manager) PruneStaleExternalWorktrees(ctx context.Context) (int, error) {
	if m == nil {
		return 0, fmt.Errorf("repoctx: nil manager")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	base := filepath.Join(m.managerTempDir(), externalWorktreeDir)
	groups, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("repoctx: list external worktrees: %w", err)
	}
	var total int
	var errs []error
	for _, group := range groups {
		if !group.IsDir() {
			continue
		}
		root := filepath.Join(base, group.Name())
		n, err := m.pruneExternalRoot(ctx, root)
		total += n
		if err != nil {
			errs = append(errs, err)
		}
	}
	return total, errors.Join(errs...)
}

func (m *Manager) pruneExternalRoot(ctx context.Context, root string) (int, error) {
	runs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var pruned int
	var errs []error
	for _, run := range runs {
		if !run.IsDir() {
			continue
		}
		runDir := filepath.Join(root, run.Name())
		meta, err := readValidLeaseMetadata(root, runDir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ready, err := worktreeCleanupReady(runDir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ready {
			continue
		}
		leaseLock, acquired, err := tryFileLock(filepath.Join(runDir, worktreeLeaseFile))
		if err != nil {
			errs = append(errs, fmt.Errorf("repoctx: inspect worktree lease %q: %w", runDir, err))
			continue
		}
		if !acquired {
			continue
		}
		var ops *fileLock
		if meta.Version == 1 {
			ops, err = m.acquireOpsLock(ctx, meta.SourceKey)
		} else {
			ops, err = m.acquireOpsLockForID(ctx, meta.SourceID)
		}
		if err != nil {
			_ = leaseLock.Close()
			errs = append(errs, err)
			continue
		}
		removeErr := m.removeMaterializedSnapshot(ctx, meta.SourceRoot, meta.Kind, meta.Worktree)
		_ = ops.Close()
		_ = leaseLock.Close()
		if removeErr != nil {
			errs = append(errs, fmt.Errorf("repoctx: prune worktree %q: %w", meta.Worktree, removeErr))
			continue
		}
		if err := os.RemoveAll(runDir); err != nil {
			errs = append(errs, fmt.Errorf("repoctx: remove pruned run %q: %w", runDir, err))
			continue
		}
		pruned++
	}
	return pruned, errors.Join(errs...)
}

func readValidLeaseMetadata(root, runDir string) (*worktreeLeaseMetadata, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	runAbs, err := filepath.Abs(runDir)
	if err != nil || !pathWithin(rootAbs, runAbs) || filepath.Dir(runAbs) != rootAbs {
		return nil, fmt.Errorf("repoctx: invalid worktree run path %q", runDir)
	}
	info, err := os.Lstat(runAbs)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("repoctx: unsafe worktree run directory %q", runDir)
	}
	data, err := os.ReadFile(filepath.Join(runAbs, worktreeMetaFile))
	if err != nil {
		return nil, fmt.Errorf("repoctx: read worktree metadata %q: %w", runDir, err)
	}
	var meta worktreeLeaseMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("repoctx: decode worktree metadata %q: %w", runDir, err)
	}
	expectedWT := filepath.Join(runAbs, "checkout")
	if meta.Worktree != expectedWT || !pathWithin(runAbs, meta.Worktree) {
		return nil, fmt.Errorf("repoctx: invalid worktree metadata %q", runDir)
	}
	switch meta.Version {
	case 1:
		if meta.SourceRoot == "" || meta.SourceKey == "" {
			return nil, fmt.Errorf("repoctx: invalid legacy worktree metadata %q", runDir)
		}
	case 2:
		sourceID := filepath.Base(rootAbs)
		if !sourceIDPattern.MatchString(sourceID) ||
			meta.SourceRoot != "" || meta.SourceKey != "" ||
			!commitSHAPattern.MatchString(meta.CommitSHA) {
			return nil, fmt.Errorf("repoctx: invalid private worktree metadata %q", runDir)
		}
		meta.SourceID = sourceID
	default:
		return nil, fmt.Errorf("repoctx: unsupported worktree metadata version in %q", runDir)
	}
	if meta.Kind == "" {
		// Fail-safe compatibility with the first version-1 metadata shape.
		meta.Kind = leaseKindWorktree
	}
	if meta.Kind != leaseKindWorktree && meta.Kind != leaseKindSnapshot &&
		meta.Kind != leaseKindSharedCopy {
		return nil, fmt.Errorf("repoctx: invalid checkout kind in %q", runDir)
	}
	if meta.Version == 2 && meta.Kind != leaseKindSnapshot {
		return nil, fmt.Errorf("repoctx: invalid version-2 checkout kind in %q", runDir)
	}
	if _, _, err := splitRepo(meta.Repo); err != nil {
		return nil, fmt.Errorf("repoctx: invalid worktree repo metadata %q: %w", runDir, err)
	}
	return &meta, nil
}

// cleanupExternalRunsForSourceLocked is called while the repository operations
// lock is held. It never waits for a lease: a live holder may itself be waiting
// for the operations lock in Handle.Release, so waiting here would deadlock.
func (m *Manager) cleanupExternalRunsForSourceLocked(
	ctx context.Context,
	sourceRoot, sourceKey string,
) error {
	root := m.worktreeRoot(sourceKey)
	runs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("repoctx: inspect worktrees before purge: %w", err)
	}
	for _, run := range runs {
		if !run.IsDir() {
			return fmt.Errorf("%w: unknown entry %q", ErrRepoBusy, filepath.Join(root, run.Name()))
		}
		runDir := filepath.Join(root, run.Name())
		meta, err := readValidLeaseMetadata(root, runDir)
		owned := err == nil
		if owned && meta.Version == 1 {
			owned = meta.SourceKey == sourceKey && meta.SourceRoot == sourceRoot
		}
		if owned && meta.Version == 2 {
			owned = meta.SourceID == sourceIDForKey(sourceKey)
		}
		if !owned {
			return fmt.Errorf("%w: cannot prove ownership of %q", ErrRepoBusy, runDir)
		}
		ready, err := worktreeCleanupReady(runDir)
		if err != nil || !ready {
			return fmt.Errorf("%w: checkout %q was not released cleanly", ErrRepoBusy, meta.Worktree)
		}
		lease, acquired, err := tryFileLock(filepath.Join(runDir, worktreeLeaseFile))
		if err != nil {
			return fmt.Errorf("repoctx: inspect worktree before purge: %w", err)
		}
		if !acquired {
			return fmt.Errorf("%w: %s", ErrRepoBusy, meta.Worktree)
		}
		removeErr := m.removeMaterializedSnapshot(ctx, sourceRoot, meta.Kind, meta.Worktree)
		_ = lease.Close()
		if removeErr != nil {
			return fmt.Errorf("repoctx: remove orphan before purge: %w", removeErr)
		}
		if err := os.RemoveAll(runDir); err != nil {
			return fmt.Errorf("repoctx: remove orphan lease before purge: %w", err)
		}
	}
	return nil
}

// refuseUnknownLinkedWorktrees is the final purge guard. Lease discovery uses
// Heimdallm's stable coordination root, but Git's own registry is authoritative:
// if any linked worktree remains (including one created by another tool or an
// older instance), deleting the common repository would invalidate it.
func (m *Manager) refuseUnknownLinkedWorktrees(_ context.Context, sourceRoot string) error {
	registry := filepath.Join(sourceRoot, ".git", "worktrees")
	entries, err := os.ReadDir(registry)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("repoctx: inspect linked-worktree registry: %w", err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: Git registry %s contains %d linked checkout(s)",
			ErrRepoBusy, registry, len(entries))
	}
	return nil
}
