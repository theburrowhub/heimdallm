package issues_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/issues"
)

func TestGitExecIgnoresManagedCloneMarker(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	dir := t.TempDir()
	runGitForTest(t, dir, "init")
	runGitForTest(t, dir, "config", "user.name", "Test User")
	runGitForTest(t, dir, "config", "user.email", "test@example.com")

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, dir, "add", "README.md")
	runGitForTest(t, dir, "commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(dir, ".heimdallm-managed"), []byte(`{"version":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	git := issues.NewGitExec()
	hasChanges, err := git.HasChanges(context.Background(), dir)
	if err != nil {
		t.Fatalf("HasChanges marker-only: %v", err)
	}
	if hasChanges {
		t.Fatal("marker-only worktree must not count as auto_implement changes")
	}

	if err := os.WriteFile(filepath.Join(dir, "fix.txt"), []byte("real change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hasChanges, err = git.HasChanges(context.Background(), dir)
	if err != nil {
		t.Fatalf("HasChanges real file: %v", err)
	}
	if !hasChanges {
		t.Fatal("real worktree changes must still be detected")
	}
	if err := git.CommitAll(context.Background(), dir, "fix: add real change"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}

	out := runGitForTest(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
	files := strings.Split(strings.TrimSpace(out), "\n")
	for _, f := range files {
		if f == ".heimdallm-managed" {
			t.Fatalf("managed marker was committed: %v", files)
		}
	}
	if !strings.Contains(out, "fix.txt") {
		t.Fatalf("real change was not committed; tree:\n%s", out)
	}
}

func runGitForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}
