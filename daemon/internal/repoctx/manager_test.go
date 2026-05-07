package repoctx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type gitCall struct {
	Dir  string
	Args []string
	Env  []string
}

type fakeGit struct {
	mu      sync.Mutex
	calls   []gitCall
	runErr  error
	onRun   func(call gitCall) error
	blockCh chan struct{}
}

func (f *fakeGit) Run(ctx context.Context, dir string, env []string, args ...string) error {
	call := gitCall{Dir: dir, Args: append([]string(nil), args...), Env: append([]string(nil), env...)}
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
	return m, git, base
}

func TestAcquireUsesExplicitLocalDirWithoutGit(t *testing.T) {
	m, git, _ := newTestManager(t)

	h, err := m.Acquire(context.Background(), Request{
		Repo:               "org/repo",
		ConfiguredLocalDir: "/tmp/user-worktree",
		Token:              "secret",
		Mode:               ModeWrite,
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

func TestAcquireNilManagerIsError(t *testing.T) {
	var m *Manager
	_, err := m.Acquire(context.Background(), Request{Repo: "org/repo", Token: "secret"})
	if err == nil || !strings.Contains(err.Error(), "nil manager") {
		t.Fatalf("Acquire err = %v, want nil manager error", err)
	}
}

func TestAcquireUsesLocalDirBaseOnlyForReadMode(t *testing.T) {
	m, git, cloneBase := newTestManager(t)
	localBase := t.TempDir()
	localRepo := filepath.Join(localBase, "repo")
	if err := os.MkdirAll(localRepo, 0o755); err != nil {
		t.Fatal(err)
	}

	readHandle, err := m.Acquire(context.Background(), Request{
		Repo:          "org/repo",
		LocalDirBases: []string{localBase},
		Token:         "secret",
		Mode:          ModeRead,
	})
	if err != nil {
		t.Fatalf("read Acquire: %v", err)
	}
	if readHandle.Path() != localRepo || readHandle.Managed() {
		t.Fatalf("read handle = (%q, managed=%v), want local_dir_base unmanaged", readHandle.Path(), readHandle.Managed())
	}
	readHandle.Release()

	writeHandle, err := m.Acquire(context.Background(), Request{
		Repo:          "org/repo",
		LocalDirBases: []string{localBase},
		Token:         "secret",
		Mode:          ModeWrite,
	})
	if err != nil {
		t.Fatalf("write Acquire: %v", err)
	}
	defer writeHandle.Release()

	wantManaged := filepath.Join(cloneBase, "heimdallm", "org", "repo")
	if writeHandle.Path() != wantManaged || !writeHandle.Managed() {
		t.Fatalf("write handle = (%q, managed=%v), want managed clone %q", writeHandle.Path(), writeHandle.Managed(), wantManaged)
	}
	calls := git.snapshot()
	if len(calls) != 2 || calls[0].Args[0] != "clone" || strings.Join(calls[1].Args, " ") != "fetch --unshallow --prune origin" {
		t.Fatalf("git calls = %v, want clone then unshallow", calls)
	}
}

func TestAcquireClonesShallowWithOwnershipMarker(t *testing.T) {
	m, git, base := newTestManager(t)

	h, err := m.Acquire(context.Background(), Request{
		Repo:  "org/repo",
		Token: "top-secret-token",
		Mode:  ModeRead,
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
		Repo:  "org/repo",
		Token: "secret",
		Mode:  ModeWrite,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	h.Release()

	calls := git.snapshot()
	got := make([]string, 0, len(calls))
	for _, c := range calls {
		got = append(got, strings.Join(c.Args, " "))
	}
	if got[len(got)-1] != "fetch --unshallow --prune origin" {
		t.Fatalf("last git call = %q, want unshallow; all calls = %v", got[len(got)-1], got)
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
		Repo:  "org/repo",
		Token: "secret",
		Mode:  ModeRead,
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

	_, err := m.Acquire(context.Background(), Request{Repo: "org/repo", Token: "secret"})
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

	h, err := m.Acquire(context.Background(), Request{Repo: "org/repo", Token: "secret"})
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
		"clean -fd -e .heimdallm-managed",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("git calls = %v, want %v", got, want)
	}
	if err := validateMarker(target, "org/repo"); err != nil {
		t.Fatalf("marker should survive update: %v", err)
	}
}

func TestAcquireSerializesByRepoUntilRelease(t *testing.T) {
	m, _, _ := newTestManager(t)
	h, err := m.Acquire(context.Background(), Request{
		Repo:               "org/repo",
		ConfiguredLocalDir: "/tmp/worktree",
	})
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	acquired := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		h2, err := m.Acquire(context.Background(), Request{
			Repo:               "org/repo",
			ConfiguredLocalDir: "/tmp/worktree",
		})
		if err != nil {
			t.Errorf("second Acquire: %v", err)
			return
		}
		close(acquired)
		h2.Release()
	}()

	select {
	case <-acquired:
		t.Fatal("second Acquire completed before first handle was released")
	case <-time.After(50 * time.Millisecond):
	}

	h.Release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second Acquire did not complete after release")
	}
	<-done
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

func TestPurgeNilManagerIsError(t *testing.T) {
	var m *Manager
	err := m.Purge(context.Background(), "org/repo", "")
	if err == nil || !strings.Contains(err.Error(), "nil manager") {
		t.Fatalf("Purge err = %v, want nil manager error", err)
	}
}

func TestEnsureFullHistoryUnshallowsManagedClone(t *testing.T) {
	m, git, base := newTestManager(t)
	target := filepath.Join(base, "heimdallm", "org", "repo")
	if err := os.MkdirAll(filepath.Join(target, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".git", "shallow"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Handle{path: target, managed: true}

	if err := m.EnsureFullHistory(context.Background(), h, "secret"); err != nil {
		t.Fatalf("EnsureFullHistory: %v", err)
	}
	calls := git.snapshot()
	if len(calls) != 1 || strings.Join(calls[0].Args, " ") != "fetch --unshallow --prune origin" {
		t.Fatalf("git calls = %v, want unshallow fetch", calls)
	}
}
