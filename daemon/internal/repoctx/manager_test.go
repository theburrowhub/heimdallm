package repoctx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type gitCall struct {
	Dir        string
	Args       []string
	Env        []string
	ExtraFiles []*os.File
}

type fakeGit struct {
	mu       sync.Mutex
	calls    []gitCall
	heads    map[string]string
	runErr   error
	onRun    func(call gitCall) error
	onOutput func(call gitCall) ([]byte, error)
	blockCh  chan struct{}
}

func (f *fakeGit) Run(ctx context.Context, dir string, env []string, args ...string) error {
	return f.run(ctx, dir, env, nil, args...)
}

func (f *fakeGit) RunWithExtraFiles(
	ctx context.Context,
	dir string,
	env []string,
	extraFiles []*os.File,
	args ...string,
) error {
	return f.run(ctx, dir, env, extraFiles, args...)
}

func (f *fakeGit) run(
	ctx context.Context,
	dir string,
	env []string,
	extraFiles []*os.File,
	args ...string,
) error {
	call := gitCall{
		Dir:        dir,
		Args:       append([]string(nil), args...),
		Env:        append([]string(nil), env...),
		ExtraFiles: append([]*os.File(nil), extraFiles...),
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()

	if f.blockCh != nil {
		select {
		case <-f.blockCh:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.onRun != nil {
		if err := f.onRun(call); err != nil {
			return err
		}
	}
	if len(args) >= 5 && args[0] == "worktree" && args[1] == "add" {
		f.mu.Lock()
		if f.heads == nil {
			f.heads = make(map[string]string)
		}
		f.heads[args[2]] = args[4]
		f.mu.Unlock()
	}
	if len(args) == 3 && args[0] == "checkout" && args[1] == "--detach" {
		f.mu.Lock()
		if f.heads == nil {
			f.heads = make(map[string]string)
		}
		f.heads[dir] = args[2]
		f.mu.Unlock()
	}
	if len(args) > 0 && args[0] == "init" {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			return err
		}
	}
	if len(args) > 0 && args[0] == "fetch" {
		shallowPath := filepath.Join(dir, ".git", "shallow")
		switch {
		case slices.Contains(args, "--depth=1"):
			if err := os.WriteFile(shallowPath, []byte("boundary\n"), 0o644); err != nil {
				return err
			}
		case slices.Contains(args, "--unshallow"):
			if err := os.Remove(shallowPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if len(args) > 0 && args[0] == "clone" {
		target := args[len(args)-1]
		if err := os.MkdirAll(filepath.Join(target, ".git"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(target, ".git", "shallow"), []byte("x"), 0o644); err != nil {
			return err
		}
	}
	return f.runErr
}

func (f *fakeGit) Output(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	call := gitCall{Dir: dir, Args: append([]string(nil), args...), Env: append([]string(nil), env...)}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if f.onOutput != nil {
		return f.onOutput(call)
	}
	if len(args) >= 2 && args[0] == "rev-parse" {
		switch args[1] {
		case "--git-common-dir":
			return []byte(".git\n"), nil
		case "--is-shallow-repository":
			if _, err := os.Stat(filepath.Join(dir, ".git", "shallow")); err == nil {
				return []byte("true\n"), nil
			}
			return []byte("false\n"), nil
		case "--verify":
			f.mu.Lock()
			head := f.heads[dir]
			f.mu.Unlock()
			if head == "" {
				ref := strings.TrimSuffix(args[len(args)-1], "^{commit}")
				if commitSHAPattern.MatchString(ref) {
					head = strings.ToLower(ref)
				} else {
					head = strings.Repeat("a", 40)
				}
			}
			return []byte(head + "\n"), nil
		}
	}
	if len(args) >= 4 && args[0] == "remote" && args[1] == "get-url" {
		return []byte("https://github.com/org/repo.git\n"), nil
	}
	return nil, nil
}

func (f *fakeGit) snapshot() []gitCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gitCall(nil), f.calls...)
}

func newTestManager(t *testing.T) (*Manager, *fakeGit, string) {
	t.Helper()
	base := t.TempDir()
	git := &fakeGit{}
	m := NewManager()
	m.git = git
	m.tempDir = func() string { return base }
	m.coordDir = func() string { return base }
	return m, git, base
}

func TestAcquireUsesExplicitLocalDirWithoutGit(t *testing.T) {
	m, git, _ := newTestManager(t)

	h, err := m.Acquire(context.Background(), Request{
		Repo:               "org/repo",
		ConfiguredLocalDir: "/tmp/user-worktree",
		Token:              "secret",
		Mode:               ModeWrite,
		Inspect:            true,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer h.Release()

	if h.Path() != "/tmp/user-worktree" || h.Managed() {
		t.Fatalf("handle = (%q, managed=%v), want explicit unmanaged", h.Path(), h.Managed())
	}
	if calls := git.snapshot(); len(calls) != 0 {
		t.Fatalf("git calls = %v, want none", calls)
	}
}

// sameCanonicalDir compares two paths through canonicalPathForKey, so a test
// assertion is not sensitive to whether the caller passed a path that traverses
// a symlink (macOS /var → /private/var, or a bind mount alias).
func sameCanonicalDir(a, b string) bool {
	return a == b || canonicalPathForKey(a) == canonicalPathForKey(b)
}

func TestAcquireIsolatesExplicitLocalDirWhenWorktreeRequested(t *testing.T) {
	m, git, base := newTestManagerWithCap(t, 0)
	local := filepath.Join(base, "operator-repo")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}

	h, err := m.Acquire(context.Background(), Request{
		Repo:               "org/repo",
		ConfiguredLocalDir: local,
		Token:              "secret",
		Mode:               ModeWrite,
		WorktreeToken:      "develop-7",
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer h.Release()

	if !h.Managed() || h.Path() == local || pathWithin(local, h.Path()) {
		t.Fatalf("handle = (%q, managed=%v), want external owned worktree", h.Path(), h.Managed())
	}
	if !strings.HasPrefix(filepath.Base(filepath.Dir(h.Path())), "develop-7.") {
		t.Fatalf("worktree path lost token prefix: %q", h.Path())
	}
	metadata, err := os.ReadFile(filepath.Join(filepath.Dir(h.Path()), worktreeMetaFile))
	if err != nil {
		t.Fatalf("read snapshot lease metadata: %v", err)
	}
	metadataText := string(metadata)
	if strings.Contains(metadataText, local) ||
		strings.Contains(metadataText, `"source_root"`) ||
		strings.Contains(metadataText, `"source_key"`) {
		t.Fatalf("AI-visible lease metadata exposes operator source: %s", metadataText)
	}
	sawSafeHeadResolution := false
	for _, call := range git.snapshot() {
		joined := strings.Join(call.Args, " ")
		if strings.Contains(joined, "reset --hard") || strings.HasPrefix(joined, "clean ") ||
			strings.HasPrefix(joined, "remote set-url") {
			t.Fatalf("operator checkout was targeted by mutating clone prep: %v", call)
		}
		// Match on the resolved path: the manager canonicalises the operator dir
		// before running git in it, and on macOS t.TempDir() lives under /var,
		// a symlink to /private/var. Comparing against the raw path meant this
		// branch never ran and the assertion below failed on every macOS run,
		// CI included, without the isolation contract being wrong at all.
		if sameCanonicalDir(call.Dir, local) && joined == "rev-parse --verify HEAD^{commit}" {
			sawSafeHeadResolution = slices.Contains(call.Env, "GIT_CONFIG_COUNT=2") &&
				slices.Contains(call.Env, "GIT_CONFIG_KEY_0=core.hooksPath") &&
				slices.Contains(call.Env, "GIT_CONFIG_KEY_1=safe.directory") &&
				(slices.Contains(call.Env, "GIT_CONFIG_VALUE_1="+local) ||
					slices.Contains(call.Env, "GIT_CONFIG_VALUE_1="+canonicalPathForKey(local)))
		}
	}
	if !sawSafeHeadResolution {
		t.Fatal("operator HEAD resolution did not authorise the exact bind-mounted safe.directory")
	}
}

func TestCleanGitEnvironmentRemovesTraceAndTLSOverrides(t *testing.T) {
	got := strings.Join(cleanGitEnvironment([]string{
		"PATH=/usr/bin",
		"GIT_TRACE=/operator/trace.log",
		"GIT_TRACE_PACKET=/operator/packets.log",
		"GIT_TRACE_CURL=1",
		"GIT_TRACE_REDACT=0",
		"GIT_CURL_VERBOSE=1",
		"GIT_SSL_NO_VERIFY=1",
	}), "\n")
	for _, forbidden := range []string{
		"GIT_TRACE=", "GIT_TRACE_PACKET=", "GIT_TRACE_CURL=",
		"GIT_TRACE_REDACT=", "GIT_CURL_VERBOSE=", "GIT_SSL_NO_VERIFY=",
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("sanitized Git environment still contains %q:\n%s", forbidden, got)
		}
	}
	if !strings.Contains(got, "PATH=/usr/bin") {
		t.Fatalf("sanitized Git environment removed PATH:\n%s", got)
	}
}

func TestAcquireNilManagerIsError(t *testing.T) {
	var m *Manager
	_, err := m.Acquire(context.Background(), Request{Repo: "org/repo", Token: "secret"})
	if err == nil || !strings.Contains(err.Error(), "nil manager") {
		t.Fatalf("Acquire err = %v, want nil manager error", err)
	}
}

func TestAcquireUsesLocalDirBaseAsObjectStoreForBothModes(t *testing.T) {
	m, _, _ := newTestManager(t)
	localBase := t.TempDir()
	localRepo := filepath.Join(localBase, "repo")
	if err := os.MkdirAll(filepath.Join(localRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	readHandle, err := m.Acquire(context.Background(), Request{
		Repo:          "org/repo",
		LocalDirBases: []string{localBase},
		Token:         "secret",
		Mode:          ModeRead,
		WorktreeToken: "read-1",
	})
	if err != nil {
		t.Fatalf("read Acquire: %v", err)
	}
	if readHandle.Path() == localRepo || !readHandle.Managed() || pathWithin(localRepo, readHandle.Path()) {
		t.Fatalf("read handle = (%q, managed=%v), want external isolated worktree", readHandle.Path(), readHandle.Managed())
	}
	readHandle.Release()

	writeHandle, err := m.Acquire(context.Background(), Request{
		Repo:          "org/repo",
		LocalDirBases: []string{localBase},
		Token:         "secret",
		Mode:          ModeWrite,
		WorktreeToken: "write-1",
	})
	if err != nil {
		t.Fatalf("write Acquire: %v", err)
	}
	defer writeHandle.Release()

	if writeHandle.Path() == localRepo || !writeHandle.Managed() || pathWithin(localRepo, writeHandle.Path()) {
		t.Fatalf("write handle = (%q, managed=%v), want external isolated worktree", writeHandle.Path(), writeHandle.Managed())
	}
}

func TestAcquireClonesShallowWithOwnershipMarker(t *testing.T) {
	m, git, base := newTestManager(t)

	h, err := m.Acquire(context.Background(), Request{
		Repo:    "org/repo",
		Token:   "top-secret-token",
		Mode:    ModeRead,
		Inspect: true,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer h.Release()

	wantPath := filepath.Join(base, "heimdallm", "org", "repo")
	if h.Path() != wantPath || !h.Managed() {
		t.Fatalf("handle = (%q, managed=%v), want %q managed", h.Path(), h.Managed(), wantPath)
	}
	if err := validateMarker(wantPath, "org/repo"); err != nil {
		t.Fatalf("marker not valid: %v", err)
	}
	if info, err := os.Stat(filepath.Join(wantPath, MarkerFile)); err != nil {
		t.Fatalf("stat marker: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("marker mode = %o, want 600", info.Mode().Perm())
	}
	calls := git.snapshot()
	if len(calls) != 1 {
		t.Fatalf("git calls = %v, want one clone", calls)
	}
	if !reflect.DeepEqual(calls[0].Args[:2], []string{"clone", "--depth=1"}) {
		t.Fatalf("clone args = %v, want shallow clone", calls[0].Args)
	}
	for _, arg := range calls[0].Args {
		if strings.Contains(arg, "top-secret-token") {
			t.Fatalf("token leaked in git args: %v", calls[0].Args)
		}
	}
}

func TestAcquireModeWriteEnsuresFullHistory(t *testing.T) {
	m, git, base := newTestManager(t)
	target := filepath.Join(base, "heimdallm", "org", "repo")
	if err := os.MkdirAll(filepath.Join(target, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".git", "shallow"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(target, "org/repo"); err != nil {
		t.Fatal(err)
	}

	h, err := m.Acquire(context.Background(), Request{
		Repo:          "org/repo",
		Token:         "secret",
		Mode:          ModeWrite,
		WorktreeToken: "develop-1",
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	snapshot := h.Path()
	h.Release()

	calls := git.snapshot()
	var unshallow *gitCall
	for _, c := range calls {
		if len(c.Args) > 0 && c.Args[0] == "fetch" &&
			slices.Contains(c.Args, "--unshallow") {
			call := c
			unshallow = &call
			break
		}
	}
	if unshallow == nil {
		t.Fatalf("git calls = %v, want isolated-snapshot unshallow", calls)
	}
	if unshallow.Dir != snapshot {
		t.Fatalf("unshallow dir = %q, want isolated snapshot %q", unshallow.Dir, snapshot)
	}
	if !slices.Contains(unshallow.Args, target) {
		t.Fatalf("unshallow args = %v, want managed clone used only as fetch source", unshallow.Args)
	}
	if len(unshallow.ExtraFiles) != 2 {
		t.Fatalf("unshallow inherited %d guards, want lease + operations lock", len(unshallow.ExtraFiles))
	}
}

func TestAcquireCloneFailureRemovesPartialManagedTarget(t *testing.T) {
	m, git, base := newTestManager(t)
	git.onRun = func(call gitCall) error {
		if len(call.Args) > 0 && call.Args[0] == "clone" {
			target := call.Args[len(call.Args)-1]
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			return errors.New("network down")
		}
		return nil
	}

	_, err := m.Acquire(context.Background(), Request{
		Repo:    "org/repo",
		Token:   "secret",
		Mode:    ModeRead,
		Inspect: true,
	})
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("Acquire err = %v, want clone failure", err)
	}
	target := filepath.Join(base, "heimdallm", "org", "repo")
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial clone target still exists or stat failed unexpectedly: %v", statErr)
	}
}

func TestAcquireRefusesExistingUnmanagedTarget(t *testing.T) {
	m, _, base := newTestManager(t)
	target := filepath.Join(base, "heimdallm", "org", "repo")
	if err := os.MkdirAll(filepath.Join(target, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := m.Acquire(context.Background(), Request{Repo: "org/repo", Token: "secret", Inspect: true})
	if err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("Acquire err = %v, want unmanaged-target error", err)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("unmanaged target was removed: %v", statErr)
	}
}

func TestAcquireInvalidRepoDoesNotCreateLock(t *testing.T) {
	m, _, _ := newTestManager(t)
	_, err := m.Acquire(context.Background(), Request{Repo: "bad org/repo", Token: "secret"})
	if err == nil {
		t.Fatal("expected invalid repo error")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.locks) != 0 {
		t.Fatalf("locks = %d, want 0 after pre-lock validation failure", len(m.locks))
	}
}

func TestAcquireUpdatesExistingManagedClone(t *testing.T) {
	m, git, base := newTestManager(t)
	target := filepath.Join(base, "heimdallm", "org", "repo")
	if err := os.MkdirAll(filepath.Join(target, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(target, "org/repo"); err != nil {
		t.Fatal(err)
	}

	h, err := m.Acquire(context.Background(), Request{Repo: "org/repo", Token: "secret", Inspect: true})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	h.Release()

	calls := git.snapshot()
	got := make([]string, 0, len(calls))
	for _, c := range calls {
		got = append(got, strings.Join(c.Args, " "))
	}
	want := []string{
		"remote set-url origin https://x-access-token@github.com/org/repo.git",
		"fetch --depth=1 --prune origin HEAD",
		"reset --hard FETCH_HEAD",
		"clean -fd -e .heimdallm-managed -e .worktrees",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("git calls = %v, want %v", got, want)
	}
	if err := validateMarker(target, "org/repo"); err != nil {
		t.Fatalf("marker should survive update: %v", err)
	}
}

func TestAcquireRejectsSharedCheckoutWithoutWorktreeToken(t *testing.T) {
	m, git, _ := newTestManager(t)
	_, err := m.Acquire(context.Background(), Request{
		Repo:               "org/repo",
		ConfiguredLocalDir: "/tmp/worktree",
	})
	if err == nil || !strings.Contains(err.Error(), "worktree token is required") {
		t.Fatalf("Acquire err = %v, want required worktree token", err)
	}
	if calls := git.snapshot(); len(calls) != 0 {
		t.Fatalf("git calls = %v, want none before fail-closed rejection", calls)
	}
}

func TestPurgeOnlyRemovesManagedClone(t *testing.T) {
	m, _, base := newTestManager(t)
	managed := filepath.Join(base, "heimdallm", "org", "repo")
	if err := os.MkdirAll(filepath.Join(managed, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(managed, "org/repo"); err != nil {
		t.Fatal(err)
	}
	if err := m.Purge(context.Background(), "org/repo", ""); err != nil {
		t.Fatalf("Purge managed: %v", err)
	}
	if _, err := os.Stat(managed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed clone still exists or stat failed unexpectedly: %v", err)
	}

	unmanaged := filepath.Join(base, "heimdallm", "org", "other")
	if err := os.MkdirAll(filepath.Join(unmanaged, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := m.Purge(context.Background(), "org/other", "")
	if err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("Purge unmanaged err = %v, want not-managed error", err)
	}
	if _, statErr := os.Stat(unmanaged); statErr != nil {
		t.Fatalf("unmanaged clone was removed: %v", statErr)
	}
}

func TestPurgeAllRemovesOnlyManagedClones(t *testing.T) {
	m, _, base := newTestManager(t)
	managed := filepath.Join(base, "heimdallm", "org", "repo")
	alsoManaged := filepath.Join(base, "heimdallm", "other", "repo")
	unmanaged := filepath.Join(base, "heimdallm", "org", "unmanaged")
	mismatchedMarker := filepath.Join(base, "heimdallm", "org", "mismatch")
	for _, dir := range []string{managed, alsoManaged, unmanaged, mismatchedMarker} {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeMarker(managed, "org/repo"); err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(alsoManaged, "other/repo"); err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(mismatchedMarker, "other/repo"); err != nil {
		t.Fatal(err)
	}

	report, err := m.PurgeAll(context.Background(), "")
	if err != nil {
		t.Fatalf("PurgeAll: %v", err)
	}
	if report.Removed != 2 {
		t.Fatalf("PurgeAll removed = %d, want 2", report.Removed)
	}
	for _, dir := range []string{managed, alsoManaged} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("managed clone %q still exists or stat failed unexpectedly: %v", dir, err)
		}
	}
	if _, err := os.Stat(unmanaged); err != nil {
		t.Fatalf("unmanaged clone should remain: %v", err)
	}
	if _, err := os.Stat(mismatchedMarker); err != nil {
		t.Fatalf("mismatched marker clone should remain: %v", err)
	}
}

func TestPurgeStaleRemovesOnlyOldUnmonitoredManagedClones(t *testing.T) {
	m, _, base := newTestManager(t)
	oldUnmonitored := filepath.Join(base, "heimdallm", "org", "old")
	oldMonitored := filepath.Join(base, "heimdallm", "org", "keep")
	recentUnmonitored := filepath.Join(base, "heimdallm", "org", "recent")
	unmanaged := filepath.Join(base, "heimdallm", "org", "unmanaged")
	for _, dir := range []string{oldUnmonitored, oldMonitored, recentUnmonitored, unmanaged} {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for repo, dir := range map[string]string{
		"org/old":    oldUnmonitored,
		"org/keep":   oldMonitored,
		"org/recent": recentUnmonitored,
	} {
		if err := writeMarker(dir, repo); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, dir := range []string{oldUnmonitored, oldMonitored} {
		if err := os.Chtimes(filepath.Join(dir, MarkerFile), old, old); err != nil {
			t.Fatal(err)
		}
	}

	report, err := m.PurgeStale(context.Background(), "", map[string]struct{}{"org/keep": {}}, 1)
	if err != nil {
		t.Fatalf("PurgeStale: %v", err)
	}
	if report.Removed != 1 {
		t.Fatalf("PurgeStale removed = %d, want 1", report.Removed)
	}
	if _, err := os.Stat(oldUnmonitored); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old unmonitored clone still exists or stat failed unexpectedly: %v", err)
	}
	for _, dir := range []string{oldMonitored, recentUnmonitored, unmanaged} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("clone %q should remain: %v", dir, err)
		}
	}
}

func TestPurgeStaleDisabledWhenMaxDaysZero(t *testing.T) {
	m, _, base := newTestManager(t)
	managed := filepath.Join(base, "heimdallm", "org", "repo")
	if err := os.MkdirAll(filepath.Join(managed, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(managed, "org/repo"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(managed, MarkerFile), old, old); err != nil {
		t.Fatal(err)
	}

	report, err := m.PurgeStale(context.Background(), "", nil, 0)
	if err != nil {
		t.Fatalf("PurgeStale disabled: %v", err)
	}
	if report.Removed != 0 {
		t.Fatalf("PurgeStale disabled removed = %d, want 0", report.Removed)
	}
	if _, err := os.Stat(managed); err != nil {
		t.Fatalf("managed clone should remain: %v", err)
	}
}

func TestPurgeNilManagerIsError(t *testing.T) {
	var m *Manager
	err := m.Purge(context.Background(), "org/repo", "")
	if err == nil || !strings.Contains(err.Error(), "nil manager") {
		t.Fatalf("Purge err = %v, want nil manager error", err)
	}
}

func newTestManagerWithCap(t *testing.T, cap int) (*Manager, *fakeGit, string) {
	t.Helper()
	base := t.TempDir()
	git := &fakeGit{
		// Materialise worktree dirs so post-conditions hold under real
		// filesystem checks.
		onRun: func(call gitCall) error {
			if len(call.Args) >= 3 && call.Args[0] == "worktree" && call.Args[1] == "add" {
				return os.MkdirAll(call.Args[2], 0o755)
			}
			if len(call.Args) >= 2 && call.Args[0] == "worktree" && call.Args[1] == "remove" {
				// Real `git worktree remove --force <path>` deletes
				// the worktree directory.
				path := call.Args[len(call.Args)-1]
				return os.RemoveAll(path)
			}
			return nil
		},
	}
	m := NewManagerWithOptions(ManagerOptions{MaxWorktreesPerRepo: cap})
	m.git = git
	m.tempDir = func() string { return base }
	m.coordDir = func() string { return base }
	return m, git, base
}

func setupManagedClone(t *testing.T, base string) string {
	t.Helper()
	target := filepath.Join(base, "heimdallm", "org", "repo")
	if err := os.MkdirAll(filepath.Join(target, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(target, "org/repo"); err != nil {
		t.Fatal(err)
	}
	return target
}

func TestPruneStaleWorktreesRetainsUnleasedLegacyEntries(t *testing.T) {
	m, _, base := newTestManagerWithCap(t, 0)
	target := setupManagedClone(t, base)

	// Live worktree the manager tracks as active.
	h, err := m.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", WorktreeToken: "live",
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer h.Release()

	// Orphan worktree left behind by a hypothetical crashed daemon.
	orphan := filepath.Join(target, ".worktrees", "orphan")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}

	n, err := m.PruneStaleWorktrees(context.Background(), target)
	if err != nil {
		t.Fatalf("PruneStaleWorktrees: %v", err)
	}
	if n != 0 {
		t.Fatalf("pruned = %d, want 0 for ownership-unknown legacy entry", n)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("unleased legacy entry must be retained: %v", err)
	}
	if _, err := os.Stat(h.Path()); err != nil {
		t.Fatalf("leased external worktree should remain: %v", err)
	}
}

func TestPruneStaleWorktreesNoWorktreesDir(t *testing.T) {
	m, _, base := newTestManagerWithCap(t, 0)
	target := setupManagedClone(t, base)

	n, err := m.PruneStaleWorktrees(context.Background(), target)
	if err != nil {
		t.Fatalf("PruneStaleWorktrees on missing dir: %v", err)
	}
	if n != 0 {
		t.Fatalf("pruned = %d on missing dir, want 0", n)
	}
}

func TestBootstrapAddsWorktreesToInfoExclude(t *testing.T) {
	m, _, base := newTestManager(t)

	// Fresh clone path — ensureManagedClone takes the bootstrap branch.
	h, err := m.Acquire(context.Background(), Request{
		Repo:    "org/repo",
		Token:   "secret",
		Mode:    ModeRead,
		Inspect: true,
	})
	if err != nil {
		t.Fatalf("Acquire bootstrap: %v", err)
	}
	h.Release()

	target := filepath.Join(base, "heimdallm", "org", "repo")
	excludePath := filepath.Join(target, ".git", "info", "exclude")
	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read info/exclude: %v", err)
	}
	if !strings.Contains(string(data), ".worktrees/") {
		t.Fatalf("info/exclude missing .worktrees/ entry; got %q", string(data))
	}

	// Second Acquire takes the update-existing branch. The entry
	// must not be duplicated.
	h2, err := m.Acquire(context.Background(), Request{
		Repo:    "org/repo",
		Token:   "secret",
		Mode:    ModeRead,
		Inspect: true,
	})
	if err != nil {
		t.Fatalf("Acquire update: %v", err)
	}
	h2.Release()

	data2, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("re-read info/exclude: %v", err)
	}
	if got := strings.Count(string(data2), ".worktrees/"); got != 1 {
		t.Fatalf("info/exclude has %d occurrences of .worktrees/, want 1; content=%q", got, string(data2))
	}

	// Critically, the user's tracked .gitignore must be untouched.
	// info/exclude is the per-clone, never-tracked location.
	if _, err := os.Stat(filepath.Join(target, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf(".gitignore should not be created by manager: err=%v", err)
	}
}

func TestBootstrapPreservesExistingInfoExclude(t *testing.T) {
	// info/exclude may already contain repo-local entries; we must
	// append, not replace.
	m, _, base := newTestManager(t)
	target := filepath.Join(base, "heimdallm", "org", "repo")
	infoDir := filepath.Join(target, ".git", "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(target, "org/repo"); err != nil {
		t.Fatal(err)
	}
	existing := "node_modules/\n*.log\n"
	excludePath := filepath.Join(infoDir, "exclude")
	if err := os.WriteFile(excludePath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	h, err := m.Acquire(context.Background(), Request{
		Repo:    "org/repo",
		Token:   "secret",
		Mode:    ModeRead,
		Inspect: true,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	h.Release()

	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "node_modules/") || !strings.Contains(got, "*.log") {
		t.Fatalf("existing entries lost: %q", got)
	}
	if !strings.Contains(got, ".worktrees/") {
		t.Fatalf(".worktrees/ entry missing after append: %q", got)
	}
}

func TestAcquireSnapshotWithBaseRefDetaches(t *testing.T) {
	m, git, base := newTestManagerWithCap(t, 0)
	target := setupManagedClone(t, base)

	wantSHA := strings.Repeat("a", 40)
	h, err := m.Acquire(context.Background(), Request{
		Repo:            "org/repo",
		Token:           "secret",
		WorktreeToken:   "pr-review-1234",
		WorktreeBaseRef: wantSHA,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer h.Release()

	var (
		initCall     *gitCall
		fetchCall    *gitCall
		checkoutCall *gitCall
	)
	for _, c := range git.snapshot() {
		call := c
		switch {
		case len(c.Args) > 0 && c.Args[0] == "init" && c.Dir == h.Path():
			initCall = &call
		case len(c.Args) > 0 && c.Args[0] == "fetch" && c.Dir == h.Path():
			fetchCall = &call
		case len(c.Args) == 3 && c.Args[0] == "checkout" && c.Args[1] == "--detach":
			checkoutCall = &call
		case len(c.Args) >= 2 && c.Args[0] == "worktree":
			t.Fatalf("independent snapshot must not mutate Git's shared worktree registry: %v", c.Args)
		}
	}
	if initCall == nil || fetchCall == nil || checkoutCall == nil {
		t.Fatalf("snapshot materialisation calls incomplete: init=%v fetch=%v checkout=%v calls=%v",
			initCall, fetchCall, checkoutCall, git.snapshot())
	}
	if !slices.Contains(fetchCall.Args, target) || fetchCall.Args[len(fetchCall.Args)-1] != wantSHA {
		t.Fatalf("snapshot fetch args = %v, want source %q at exact SHA %s",
			fetchCall.Args, target, wantSHA)
	}
	if !reflect.DeepEqual(checkoutCall.Args, []string{"checkout", "--detach", wantSHA}) {
		t.Fatalf("checkout args = %v, want detached exact SHA", checkoutCall.Args)
	}
	if h.CommitSHA() != wantSHA {
		t.Fatalf("CommitSHA = %q, want %q", h.CommitSHA(), wantSHA)
	}
	if pathWithin(target, h.Path()) {
		t.Fatalf("isolated snapshot %q must be outside source clone %q", h.Path(), target)
	}
}

func TestAcquireInspectSkipsWorktreeAndCap(t *testing.T) {
	// Inspect callers (HTTP /config/clones) want the clone path only.
	// They must not take a cap slot — otherwise a single inspection
	// can starve real pipeline executions — and they must not create
	// a worktree.
	m, git, base := newTestManagerWithCap(t, 1)
	target := setupManagedClone(t, base)

	h1, err := m.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", WorktreeToken: "wt-1",
	})
	if err != nil {
		t.Fatalf("Acquire (worktree): %v", err)
	}
	defer h1.Release()

	preInspectCalls := len(git.snapshot())

	h2, err := m.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", Inspect: true,
	})
	if err != nil {
		t.Fatalf("Inspect Acquire: %v", err)
	}
	if h2.Path() != target {
		t.Fatalf("Inspect path = %q, want clone root %q", h2.Path(), target)
	}
	for _, c := range git.snapshot()[preInspectCalls:] {
		if len(c.Args) >= 2 && c.Args[0] == "worktree" {
			t.Fatalf("Inspect triggered worktree op: %v", c.Args)
		}
	}
	h2.Release() // Must not panic or block.
}

func TestReleaseClearsBookkeepingEvenWhenLockTimesOut(t *testing.T) {
	// Reproduces the failure mode flagged in PR review: if the
	// release closure can't reacquire the crit lock (e.g. another
	// caller is stuck), the in-memory active set and the cap
	// semaphore must still clear. Otherwise the path is pinned in
	// m.active forever and PruneStaleWorktrees treats the orphan as
	// live.
	m, _, base := newTestManagerWithCap(t, 1)
	m.releaseTimeout = 50 * time.Millisecond
	target := setupManagedClone(t, base)

	h, err := m.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", WorktreeToken: "stuck",
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Hijack the per-repo crit lock from a separate goroutine so the
	// release closure cannot reacquire it within releaseTimeout.
	holderCtx, holderCancel := context.WithCancel(context.Background())
	defer holderCancel()
	unlock, err := m.acquireRepoLock(holderCtx, "org/repo")
	if err != nil {
		t.Fatalf("hijack lock: %v", err)
	}

	wtPath := h.Path()
	h.Release()

	// Active set must be empty so a follow-up Acquire isn't blocked
	// by the seq-counter or a pinned path.
	m.mu.Lock()
	live := len(m.active)
	m.mu.Unlock()
	if live != 0 {
		t.Fatalf("active set leaks after release-with-failed-lock: %d entries", live)
	}

	// Cap slot must be free — verify a fresh Acquire (with the
	// hijacked lock still held → no crit work expected, but the cap
	// path itself must be unblocked) is not gated by a permanently
	// held slot. To avoid waiting on the hijacked crit lock, this
	// follow-up Acquire happens after we release the hijack.
	holderCancel()
	unlock()

	h2, err := m.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", WorktreeToken: "next",
	})
	if err != nil {
		t.Fatalf("follow-up Acquire: %v", err)
	}
	h2.Release()

	// Fail-closed release leaves the checkout for the lease-aware sweeper.
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree dir %q should remain as a recoverable orphan: %v", wtPath, err)
	}
	if n, err := m.PruneStaleExternalWorktrees(context.Background()); err != nil {
		t.Fatalf("prune released orphan: %v", err)
	} else if n != 1 {
		t.Fatalf("pruned released orphans = %d, want 1", n)
	}
	if _, err := os.Stat(wtPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pruned orphan still exists: %v", err)
	}
	_ = target
}

func TestAcquireSameTokenProducesDistinctWorktrees(t *testing.T) {
	// Two pipeline entry points (e.g. poll review-worker + manual
	// trigger-review) can fire concurrently for the same PR. They
	// pass the same WorktreeToken; the manager must give each its
	// own path so `git worktree add` does not collide.
	m, _, base := newTestManagerWithCap(t, 0)
	setupManagedClone(t, base)

	h1, err := m.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", WorktreeToken: "pr-review-99",
	})
	if err != nil {
		t.Fatalf("Acquire 1: %v", err)
	}
	defer h1.Release()

	h2, err := m.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", WorktreeToken: "pr-review-99",
	})
	if err != nil {
		t.Fatalf("Acquire 2: %v", err)
	}
	defer h2.Release()

	if h1.Path() == h2.Path() {
		t.Fatalf("same-token acquires resolved to same path %q", h1.Path())
	}
	if !strings.HasPrefix(filepath.Base(filepath.Dir(h1.Path())), "pr-review-99.") ||
		!strings.HasPrefix(filepath.Base(filepath.Dir(h2.Path())), "pr-review-99.") {
		t.Fatalf("paths lost the token prefix: %q, %q", h1.Path(), h2.Path())
	}
}

func TestPruneRetainsUnlockedRunWithoutCleanupReadyMarker(t *testing.T) {
	m, _, base := newTestManagerWithCap(t, 0)
	setupManagedClone(t, base)
	h, err := m.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", WorktreeToken: "crash-window",
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Model a killed daemon (or a CLI that closed unknown descriptors): the
	// kernel lease is absent, but normal Release never authorised deletion.
	if err := h.leaseFiles[0].Close(); err != nil {
		t.Fatalf("close simulated crashed lease: %v", err)
	}
	n, err := m.PruneStaleExternalWorktrees(context.Background())
	if err != nil {
		t.Fatalf("PruneStaleExternalWorktrees: %v", err)
	}
	if n != 0 {
		t.Fatalf("pruned unmarked crash run = %d, want 0", n)
	}
	if _, err := os.Stat(h.Path()); err != nil {
		t.Fatalf("unmarked crash run was removed: %v", err)
	}
	h.Release()
}

func TestAcquireBlocksWhenAtMaxWorktreesPerRepo(t *testing.T) {
	m, _, base := newTestManagerWithCap(t, 2)
	setupManagedClone(t, base)

	h1, err := m.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", WorktreeToken: "wt-1",
	})
	if err != nil {
		t.Fatalf("Acquire 1: %v", err)
	}
	h2, err := m.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", WorktreeToken: "wt-2",
	})
	if err != nil {
		t.Fatalf("Acquire 2: %v", err)
	}

	acquired := make(chan *Handle, 1)
	go func() {
		h3, err := m.Acquire(context.Background(), Request{
			Repo: "org/repo", Token: "secret", WorktreeToken: "wt-3",
		})
		if err != nil {
			t.Errorf("Acquire 3: %v", err)
			return
		}
		acquired <- h3
	}()

	select {
	case <-acquired:
		t.Fatal("3rd Acquire returned before any worktree was released; cap not enforced")
	case <-time.After(80 * time.Millisecond):
	}

	h1.Release()

	select {
	case h3 := <-acquired:
		h3.Release()
	case <-time.After(time.Second):
		t.Fatal("3rd Acquire did not proceed after h1.Release")
	}
	h2.Release()
}

func TestAcquireCancelDuringCapQueueReturnsCtxErr(t *testing.T) {
	m, _, base := newTestManagerWithCap(t, 1)
	setupManagedClone(t, base)

	h1, err := m.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", WorktreeToken: "wt-1",
	})
	if err != nil {
		t.Fatalf("Acquire 1: %v", err)
	}
	defer h1.Release()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.Acquire(ctx, Request{
			Repo: "org/repo", Token: "secret", WorktreeToken: "wt-2",
		})
		done <- err
	}()

	// Give the goroutine time to start queuing.
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued Acquire err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled Acquire did not return")
	}

	// Cap slot must be freed — a follow-up Acquire should not deadlock
	// after h1.Release.
	h1.Release()
	h3, err := m.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", WorktreeToken: "wt-3",
	})
	if err != nil {
		t.Fatalf("follow-up Acquire: %v", err)
	}
	h3.Release()
}

func TestAcquireCreatesIndependentSnapshotForManagedClone(t *testing.T) {
	m, git, base := newTestManager(t)
	target := filepath.Join(base, "heimdallm", "org", "repo")
	if err := os.MkdirAll(filepath.Join(target, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(target, "org/repo"); err != nil {
		t.Fatal(err)
	}

	h, err := m.Acquire(context.Background(), Request{
		Repo:          "org/repo",
		Token:         "secret",
		Mode:          ModeRead,
		WorktreeToken: "triage-42",
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// The checkout is outside the source clone and its run directory carries
	// a random cross-process identifier.
	wantWT := h.Path()
	if pathWithin(target, wantWT) {
		t.Fatalf("handle path = %q, must be outside source clone %q", wantWT, target)
	}
	if !strings.HasPrefix(filepath.Base(filepath.Dir(wantWT)), "triage-42.") {
		t.Fatalf("handle path = %q, want token-prefixed run directory", wantWT)
	}
	if info, err := os.Stat(wantWT); err != nil {
		t.Fatalf("snapshot dir missing: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("snapshot path is not a directory")
	}

	var initSeen, exactFetchSeen bool
	for _, c := range git.snapshot() {
		switch {
		case len(c.Args) > 0 && c.Args[0] == "init" && c.Dir == wantWT:
			initSeen = true
		case len(c.Args) > 0 && c.Args[0] == "fetch" && c.Dir == wantWT &&
			slices.Contains(c.Args, target) &&
			c.Args[len(c.Args)-1] == h.CommitSHA():
			exactFetchSeen = true
		case len(c.Args) >= 2 && c.Args[0] == "worktree":
			t.Fatalf("managed source registry was mutated: %v", c.Args)
		}
	}
	if !initSeen || !exactFetchSeen {
		t.Fatalf("independent snapshot calls incomplete: init=%v exact_fetch=%v calls=%v",
			initSeen, exactFetchSeen, git.snapshot())
	}

	h.Release()
	if _, err := os.Stat(wantWT); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released snapshot still exists or stat failed unexpectedly: %v", err)
	}
}

func TestAcquireRejectsInvalidWorktreeToken(t *testing.T) {
	// The WorktreeToken becomes a subdirectory under `<clone>/.worktrees/`,
	// so anything that could escape the clone root or confuse git's
	// porcelain output must be rejected before we touch the filesystem.
	bad := []string{
		"..",
		"../escape",
		"foo/bar",
		"foo\\bar",
		".hidden",
		"with space",
		"semi;colon",
		"a*b",
	}
	for _, tok := range bad {
		t.Run(tok, func(t *testing.T) {
			m, git, _ := newTestManager(t)
			_, err := m.Acquire(context.Background(), Request{
				Repo:          "org/repo",
				Token:         "secret",
				Mode:          ModeRead,
				WorktreeToken: tok,
			})
			if err == nil {
				t.Fatalf("Acquire with WorktreeToken=%q: want error, got nil", tok)
			}
			if !strings.Contains(err.Error(), "worktree token") {
				t.Fatalf("Acquire err = %v, want 'worktree token' error", err)
			}
			if calls := git.snapshot(); len(calls) != 0 {
				t.Fatalf("git ran %d calls for invalid token %q; want none", len(calls), tok)
			}
		})
	}
}

func TestAcquireAcceptsValidWorktreeToken(t *testing.T) {
	// Valid tokens cover the patterns the callsites use:
	// stage-<n> (triage-42), <purpose>-<random> (inspect-deadbeef),
	// dotted decorations (pr-review-1234.retry).
	good := []string{
		"triage-42",
		"pr-review-1234",
		"develop-7",
		"inspect-deadbeef",
		"a",
		"a_b-c.d",
	}
	for _, tok := range good {
		t.Run(tok, func(t *testing.T) {
			if err := validateWorktreeToken(tok); err != nil {
				t.Fatalf("validateWorktreeToken(%q) = %v, want nil", tok, err)
			}
		})
	}
}

func TestCanonicalPathForKeyStableBeforeAndAfterNestedPathExists(t *testing.T) {
	realRoot := t.TempDir()
	aliasRoot := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Fatalf("symlink temp root: %v", err)
	}

	target := filepath.Join(aliasRoot, "missing-owner", "missing-repo")
	before := canonicalPathForKey(target)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("create target: %v", err)
	}
	after := canonicalPathForKey(target)

	if before != after {
		t.Fatalf("canonical key changed after path creation: before=%q after=%q", before, after)
	}
	// Resolve realRoot before comparing: on macOS t.TempDir() itself sits under
	// /var, which is a symlink to /private/var, so canonicalPathForKey resolves
	// one more level than the raw TempDir string. Comparing against the
	// unresolved value made this test fail on every macOS run — including CI,
	// whose Daemon job is macos-14 — for a reason unrelated to what it asserts:
	// that the key is stable across the target coming into existence, and that
	// the alias is resolved to its target.
	resolvedRoot, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatalf("resolve real root: %v", err)
	}
	want := filepath.Join(resolvedRoot, "missing-owner", "missing-repo")
	if before != want {
		t.Fatalf("canonical key = %q, want %q", before, want)
	}
}

func TestImmutableFetchWritesOnlyIndependentSnapshot(t *testing.T) {
	m, git, base := newTestManagerWithCap(t, 0)
	local := filepath.Join(base, "operator-repo")
	if err := os.MkdirAll(filepath.Join(local, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantSHA := strings.Repeat("b", 40)
	catChecks := 0
	git.onRun = func(call gitCall) error {
		if len(call.Args) >= 2 && call.Args[0] == "cat-file" {
			catChecks++
			if catChecks == 1 {
				return errors.New("object missing")
			}
		}
		return nil
	}

	h, err := m.Acquire(context.Background(), Request{
		Repo:               "org/repo",
		ConfiguredLocalDir: local,
		Token:              "secret",
		Mode:               ModeRead,
		WorktreeToken:      "pr-review-55",
		WorktreeBaseRef:    wantSHA,
		WorktreeFetchRef:   "refs/pull/55/head",
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer h.Release()

	var fetch gitCall
	for _, call := range git.snapshot() {
		if len(call.Args) > 0 && call.Args[0] == "fetch" {
			fetch = call
			break
		}
	}
	if len(fetch.Args) == 0 {
		t.Fatal("expected immutable commit fetch")
	}
	if fetch.Dir != h.Path() {
		t.Fatalf("immutable fetch dir = %q, want isolated checkout %q", fetch.Dir, h.Path())
	}
	if !slices.Contains(fetch.Args, "--no-write-fetch-head") {
		t.Fatalf("immutable fetch unexpectedly updates FETCH_HEAD: %v", fetch.Args)
	}
	if len(fetch.ExtraFiles) != 2 {
		t.Fatalf("materialisation fetch inherited %d guards, want lease + operations lock", len(fetch.ExtraFiles))
	}
	if _, err := os.Stat(filepath.Join(local, ".git", "shallow")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("operator repository shallow boundary was created: %v", err)
	}
}

func TestValidateSnapshotRejectsChangedHeadAndDirtyTree(t *testing.T) {
	wantSHA := strings.Repeat("a", 40)
	tests := []struct {
		name   string
		head   string
		status string
	}{
		{name: "changed head", head: strings.Repeat("b", 40)},
		{name: "dirty tracked file", head: wantSHA, status: " M tracked.go\n"},
		{name: "untracked file", head: wantSHA, status: "?? scratch.txt\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, git, _ := newTestManager(t)
			git.onOutput = func(call gitCall) ([]byte, error) {
				if len(call.Args) >= 2 && call.Args[0] == "rev-parse" {
					return []byte(tt.head + "\n"), nil
				}
				if len(call.Args) > 0 && call.Args[0] == "status" {
					return []byte(tt.status), nil
				}
				return nil, nil
			}
			err := m.ValidateSnapshot(context.Background(), &Handle{
				path:      "/tmp/review",
				commitSHA: wantSHA,
			})
			if !errors.Is(err, ErrSnapshotChanged) {
				t.Fatalf("ValidateSnapshot err = %v, want ErrSnapshotChanged", err)
			}
		})
	}
}

func TestEnsureFullHistoryDoesNotMutateOperatorOwnedSource(t *testing.T) {
	m, git, base := newTestManager(t)
	source := filepath.Join(base, "operator-repo")
	if err := os.MkdirAll(filepath.Join(source, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".git", "shallow"), []byte("boundary\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := m.EnsureFullHistory(context.Background(), &Handle{
		path:          filepath.Join(base, "external-checkout"),
		managed:       true,
		sourceRoot:    source,
		sourceManaged: false,
	}, "secret")
	if err != nil {
		t.Fatalf("EnsureFullHistory: %v", err)
	}
	if calls := git.snapshot(); len(calls) != 0 {
		t.Fatalf("operator-owned shallow source was inspected or mutated: %v", calls)
	}
	if got := string(mustReadFile(t, filepath.Join(source, ".git", "shallow"))); got != "boundary\n" {
		t.Fatalf("operator shallow boundary changed: %q", got)
	}
}

func TestAcquireRemovesIndependentSnapshotWhenVerificationFails(t *testing.T) {
	m, git, base := newTestManagerWithCap(t, 0)
	target := setupManagedClone(t, base)
	var snapshot string
	git.onRun = func(call gitCall) error {
		if len(call.Args) > 0 && call.Args[0] == "init" {
			snapshot = call.Dir
		}
		return nil
	}
	git.onOutput = func(call gitCall) ([]byte, error) {
		if len(call.Args) >= 2 && call.Args[0] == "rev-parse" && call.Args[1] == "--verify" {
			if call.Dir != target {
				return nil, errors.New("verification failed")
			}
			return []byte(strings.Repeat("a", 40) + "\n"), nil
		}
		return nil, nil
	}

	_, err := m.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", WorktreeToken: "verify-failure",
	})
	if err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("Acquire err = %v, want verification failure", err)
	}
	if snapshot == "" {
		t.Fatalf("snapshot path was not captured; calls=%v", git.snapshot())
	}
	if _, statErr := os.Stat(snapshot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed snapshot still exists or stat failed unexpectedly: %v", statErr)
	}
	for _, call := range git.snapshot() {
		if len(call.Args) >= 2 && call.Args[0] == "worktree" {
			t.Fatalf("independent snapshot rollback mutated shared worktree registry: %v", call.Args)
		}
	}
}

func TestPurgeRefusesGitRegisteredWorktreeOutsideLeaseRoot(t *testing.T) {
	m, _, base := newTestManager(t)
	target := setupManagedClone(t, base)
	registryEntry := filepath.Join(target, ".git", "worktrees", "foreign")
	if err := os.MkdirAll(registryEntry, 0o755); err != nil {
		t.Fatal(err)
	}

	err := m.Purge(context.Background(), "org/repo", "")
	if !errors.Is(err, ErrRepoBusy) {
		t.Fatalf("Purge err = %v, want ErrRepoBusy", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("busy clone was removed: %v", err)
	}
}

func TestEnsureFullHistoryUnshallowsIndependentSnapshot(t *testing.T) {
	m, git, base := newTestManager(t)
	source := filepath.Join(base, "heimdallm", "org", "repo")
	snapshot := filepath.Join(base, "snapshot")
	for _, dir := range []string{source, snapshot} {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, ".git", "shallow"), []byte("source-boundary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, ".git", "shallow"), []byte("snapshot-boundary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantSHA := strings.Repeat("a", 40)
	h := &Handle{
		path:          snapshot,
		managed:       true,
		repo:          "org/repo",
		commitSHA:     wantSHA,
		sourceRoot:    source,
		sourceManaged: true,
	}

	if err := m.EnsureFullHistory(context.Background(), h, "secret"); err != nil {
		t.Fatalf("EnsureFullHistory: %v", err)
	}
	calls := git.snapshot()
	var unshallow *gitCall
	for _, c := range calls {
		if len(c.Args) > 0 && c.Args[0] == "fetch" &&
			slices.Contains(c.Args, "--unshallow") {
			call := c
			unshallow = &call
		}
		if c.Dir == source {
			t.Fatalf("full-history upgrade executed inside source clone: %v", c)
		}
	}
	if unshallow == nil {
		t.Fatalf("git calls = %v, want snapshot-local unshallow fetch", calls)
	}
	if unshallow.Dir != snapshot ||
		!reflect.DeepEqual(unshallow.Args, []string{
			"fetch", "--unshallow", "--no-tags", "--no-write-fetch-head", source, wantSHA,
		}) {
		t.Fatalf("unshallow call = %+v, want isolated snapshot fetching %s from source", unshallow, wantSHA)
	}
	if got := string(mustReadFile(t, filepath.Join(source, ".git", "shallow"))); got != "source-boundary\n" {
		t.Fatalf("source shallow boundary changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(snapshot, ".git", "shallow")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot remained shallow: %v", err)
	}
}
