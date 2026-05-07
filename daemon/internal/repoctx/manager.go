package repoctx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type gitRunner interface {
	Run(ctx context.Context, dir string, env []string, args ...string) error
}

// Manager resolves and prepares repository working directories for AI runs.
type Manager struct {
	mu      sync.Mutex
	locks   map[string]*repoLock
	git     gitRunner
	tempDir func() string
}

// NewManager returns a Manager backed by the local git binary.
func NewManager() *Manager {
	return &Manager{
		locks:   make(map[string]*repoLock),
		git:     execGit{},
		tempDir: os.TempDir,
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
		m = NewManager()
	}
	owner, name, err := splitRepo(req.Repo)
	if err != nil {
		return nil, err
	}
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
	return &Handle{path: path, managed: true, release: release}, nil
}

// EnsureFullHistory upgrades a managed shallow clone in-place. It assumes the
// caller still owns the handle lock returned by Acquire.
func (m *Manager) EnsureFullHistory(ctx context.Context, h *Handle, token string) error {
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
	env, cleanup, err := buildAskPassEnv(token)
	if err != nil {
		return fmt.Errorf("repoctx: setup askpass: %w", err)
	}
	defer cleanup()
	if err := m.runner().Run(ctx, h.Path(), env, "fetch", "--unshallow", "--prune", "origin"); err != nil {
		return fmt.Errorf("repoctx: unshallow %s: %w", h.Path(), err)
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
		m = NewManager()
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
	if err := m.runner().Run(ctx, target, nil, "remote", "set-url", "origin", url); err != nil {
		return fmt.Errorf("repoctx: set remote url: %w", err)
	}
	if err := m.runner().Run(ctx, target, env, "fetch", "--depth=1", "--prune", "origin", "HEAD"); err != nil {
		return fmt.Errorf("repoctx: fetch %s: %w", repo, err)
	}
	if err := m.runner().Run(ctx, target, nil, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("repoctx: reset %s: %w", repo, err)
	}
	if err := m.runner().Run(ctx, target, nil, "clean", "-fd", "-e", MarkerFile); err != nil {
		return fmt.Errorf("repoctx: clean %s: %w", repo, err)
	}
	return nil
}

func (m *Manager) cloneTarget(cloneDir, owner, name string) (string, error) {
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
	if err := os.WriteFile(filepath.Join(dir, MarkerFile), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("repoctx: write marker: %w", err)
	}
	return nil
}

func validateMarker(dir, repo string) error {
	data, err := os.ReadFile(filepath.Join(dir, MarkerFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("repoctx: clone target %q exists but is not managed by Heimdallm", dir)
		}
		return fmt.Errorf("repoctx: read marker: %w", err)
	}
	var m marker
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("repoctx: invalid marker in %q: %w", dir, err)
	}
	if m.Version != 1 || m.ManagedBy != "heimdallm" || m.Repo != repo {
		return fmt.Errorf("repoctx: marker in %q does not match repo %s", dir, repo)
	}
	return nil
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
	env := append(os.Environ(),
		"GIT_ASKPASS="+helperPath,
		"GIT_TERMINAL_PROMPT=0",
		"HEIMDALLM_GIT_TOKEN="+token,
	)
	cleanup := func() { os.RemoveAll(dir) }
	return env, cleanup, nil
}

type execGit struct{}

func (execGit) Run(ctx context.Context, dir string, env []string, args ...string) error {
	runCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errText := stderr.String()
		if len(errText) > maxGitStderrBytes {
			errText = errText[:maxGitStderrBytes] + "\n... (stderr truncated)"
		}
		return fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(errText))
	}
	return nil
}
