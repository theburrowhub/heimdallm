package repoctx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfiguredLocalDirIsObjectStoreForImmutableWorktree(t *testing.T) {
	source, shaA, shaB := initRealRepository(t)
	tempRoot := t.TempDir()

	// Simulate Codex/Claude working in the operator checkout while Heimdallm
	// starts a review of the older, explicitly requested commit.
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("user dirty edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "untracked.txt"), []byte("agent scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager()
	m.tempDir = func() string { return tempRoot }
	m.coordDir = func() string { return tempRoot }
	h, err := m.Acquire(context.Background(), Request{
		Repo:               "org/repo",
		ConfiguredLocalDir: source,
		Token:              "unused-because-object-exists",
		Mode:               ModeRead,
		WorktreeToken:      "pr-review-7",
		WorktreeBaseRef:    shaA,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer h.Release()

	if !h.Managed() {
		t.Fatal("isolated checkout must be owned by Heimdallm")
	}
	if h.CommitSHA() != shaA {
		t.Fatalf("CommitSHA = %q, want %q", h.CommitSHA(), shaA)
	}
	if pathWithin(source, h.Path()) {
		t.Fatalf("worktree %q must be external to operator checkout %q", h.Path(), source)
	}
	if got := strings.TrimSpace(runGitTest(t, h.Path(), "rev-parse", "HEAD")); got != shaA {
		t.Fatalf("worktree HEAD = %q, want %q", got, shaA)
	}
	if got := string(mustReadFile(t, filepath.Join(h.Path(), "tracked.txt"))); got != "commit A\n" {
		t.Fatalf("worktree content = %q, want commit A", got)
	}

	if got := strings.TrimSpace(runGitTest(t, source, "rev-parse", "HEAD")); got != shaB {
		t.Fatalf("operator checkout HEAD changed: got %q, want %q", got, shaB)
	}
	if got := string(mustReadFile(t, filepath.Join(source, "tracked.txt"))); got != "user dirty edit\n" {
		t.Fatalf("operator tracked edit changed: %q", got)
	}
	if got := string(mustReadFile(t, filepath.Join(source, "untracked.txt"))); got != "agent scratch\n" {
		t.Fatalf("operator untracked file changed: %q", got)
	}
	if got := strings.TrimSpace(runGitTest(t, h.Path(), "remote")); got != "" {
		t.Fatalf("isolated snapshot inherited a remote that could target the operator repository: %q", got)
	}
	alternates := filepath.Join(h.Path(), ".git", "objects", "info", "alternates")
	if data, err := os.ReadFile(alternates); err == nil && strings.TrimSpace(string(data)) != "" {
		t.Fatalf("isolated snapshot depends on alternate object stores: %q", data)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect isolated snapshot alternates: %v", err)
	}

	// Removing the entire source after acquisition is a stronger regression
	// guard than running gc: HEAD, trees and blobs must remain usable from the
	// snapshot's own object store.
	movedSource := source + ".removed"
	if err := os.Rename(source, movedSource); err != nil {
		t.Fatalf("make operator source unavailable: %v", err)
	}
	runGitTest(t, h.Path(), "fsck", "--connectivity-only", "--no-dangling", "HEAD")
	if got := string(mustReadFile(t, filepath.Join(h.Path(), "tracked.txt"))); got != "commit A\n" {
		t.Fatalf("snapshot stopped working after source removal: %q", got)
	}
	if len(h.LeaseFiles()) != 1 || h.LeaseFiles()[0] == nil {
		t.Fatal("worktree handle must expose its inherited lease descriptor")
	}
}

func TestTwoManagersKeepLiveLeasedWorktreesAndUseDistinctPaths(t *testing.T) {
	source, shaA, shaB := initRealRepository(t)
	tempRoot := t.TempDir()
	newManager := func() *Manager {
		m := NewManager()
		m.tempDir = func() string { return tempRoot }
		m.coordDir = func() string { return tempRoot }
		return m
	}
	m1, m2 := newManager(), newManager()

	acquire := func(m *Manager, sha string) *Handle {
		t.Helper()
		h, err := m.Acquire(context.Background(), Request{
			Repo:               "org/repo",
			ConfiguredLocalDir: source,
			Token:              "unused",
			Mode:               ModeRead,
			WorktreeToken:      "pr-review-8",
			WorktreeBaseRef:    sha,
		})
		if err != nil {
			t.Fatalf("Acquire(%s): %v", sha, err)
		}
		return h
	}

	h1 := acquire(m1, shaA)
	defer h1.Release()
	h2 := acquire(m2, shaB)
	defer h2.Release()
	if h1.Path() == h2.Path() {
		t.Fatalf("cross-manager worktrees collided at %q", h1.Path())
	}

	n, err := m2.PruneStaleExternalWorktrees(context.Background())
	if err != nil {
		t.Fatalf("PruneStaleExternalWorktrees: %v", err)
	}
	if n != 0 {
		t.Fatalf("pruned live worktrees = %d, want 0", n)
	}
	for _, path := range []string{h1.Path(), h2.Path()} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("live worktree %q was removed: %v", path, err)
		}
	}
}

func TestConfiguredLocalDirRemoteMismatchFailsClosed(t *testing.T) {
	source, shaA, _ := initRealRepository(t)
	runGitTest(t, source, "remote", "set-url", "origin", "git@github.com:other/repo.git")

	m := NewManager()
	tempRoot := t.TempDir()
	m.tempDir = func() string { return tempRoot }
	m.coordDir = func() string { return tempRoot }
	_, err := m.Acquire(context.Background(), Request{
		Repo:               "org/repo",
		ConfiguredLocalDir: source,
		Token:              "unused",
		WorktreeToken:      "pr-review-9",
		WorktreeBaseRef:    shaA,
	})
	if err == nil || !strings.Contains(err.Error(), "origin does not match") {
		t.Fatalf("Acquire err = %v, want origin identity mismatch", err)
	}
}

func TestPurgeRefusesManagedCloneWithForeignLiveLease(t *testing.T) {
	m1, _, base := newTestManagerWithCap(t, 0)
	target := setupManagedClone(t, base)
	m2, git2, _ := newTestManagerWithCap(t, 0)
	m2.tempDir = m1.tempDir
	m2.coordDir = m1.coordDir
	m2.git = git2

	h, err := m1.Acquire(context.Background(), Request{
		Repo: "org/repo", Token: "secret", WorktreeToken: "develop-12",
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	err = m2.Purge(context.Background(), "org/repo", "")
	if !errors.Is(err, ErrRepoBusy) {
		h.Release()
		t.Fatalf("Purge err = %v, want ErrRepoBusy", err)
	}
	if _, err := os.Stat(target); err != nil {
		h.Release()
		t.Fatalf("busy managed clone was removed: %v", err)
	}

	h.Release()
	if err := m2.Purge(context.Background(), "org/repo", ""); err != nil {
		t.Fatalf("Purge after release: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed clone still exists after safe purge: %v", err)
	}
}

func initRealRepository(t *testing.T) (source, shaA, shaB string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		// The repository's pinned Alpine test image intentionally contains only
		// the Go toolchain. CI/dev environments with git exercise this integration
		// layer; fake-runner tests still enforce the command protocol everywhere.
		t.Skipf("git is unavailable in this test environment: %v", err)
	}
	source = filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, source, "init", "-b", "main")
	runGitTest(t, source, "config", "user.name", "Heimdallm Test")
	runGitTest(t, source, "config", "user.email", "heimdallm@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("commit A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, source, "add", "tracked.txt")
	runGitTest(t, source, "commit", "-m", "commit A")
	shaA = strings.TrimSpace(runGitTest(t, source, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("commit B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, source, "commit", "-am", "commit B")
	shaB = strings.TrimSpace(runGitTest(t, source, "rev-parse", "HEAD"))
	runGitTest(t, source, "remote", "add", "origin", "https://github.com/org/repo.git")
	return source, shaA, shaB
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
