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

func TestCommitAll_RefusesSensitivePaths(t *testing.T) {
	// Prompt-injection defense: a compromised AI run could write
	// secrets (.env, *.pem, config.toml) into the worktree to
	// exfiltrate via the PR diff. CommitAll must scan staged files
	// against a denylist and refuse the commit before push.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	for _, filename := range []string{".env", "secret.pem", "id_rsa", "config.toml", "credentials.json"} {
		t.Run(filename, func(t *testing.T) {
			dir := t.TempDir()
			runGitForTest(t, dir, "init")
			runGitForTest(t, dir, "config", "user.name", "Test User")
			runGitForTest(t, dir, "config", "user.email", "test@example.com")
			if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# init\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGitForTest(t, dir, "add", "README.md")
			runGitForTest(t, dir, "commit", "-m", "initial")

			// Attacker payload: legitimate edit alongside a sensitive file.
			if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# fixed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, filename), []byte("EXFILTRATED=secret\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			git := issues.NewGitExec()
			err := git.CommitAll(context.Background(), dir, "fix: legit")
			if err == nil {
				t.Fatalf("CommitAll accepted sensitive path %q (M-? bypass)", filename)
			}
			if !strings.Contains(err.Error(), "sensitive") {
				t.Errorf("error should mention sensitive denylist, got: %v", err)
			}
			// Nothing should have been committed.
			out := runGitForTest(t, dir, "log", "--oneline")
			if strings.Count(out, "\n") > 1 {
				t.Fatalf("commit landed despite sensitive-path veto:\n%s", out)
			}
			// The index must be left clean (unstaged) so a retry from
			// scratch isn't poisoned by the previous stage.
			status := runGitForTest(t, dir, "diff", "--cached", "--name-only")
			if strings.TrimSpace(status) != "" {
				t.Errorf("index not reset after veto; cached files:\n%s", status)
			}
		})
	}
}

func TestCommitAll_AllowsLegitimateChanges(t *testing.T) {
	// Sanity: denylist must not block normal repo edits.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()
	runGitForTest(t, dir, "init")
	runGitForTest(t, dir, "config", "user.name", "Test User")
	runGitForTest(t, dir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# init\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, dir, "add", "README.md")
	runGitForTest(t, dir, "commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	git := issues.NewGitExec()
	if err := git.CommitAll(context.Background(), dir, "feat: add main"); err != nil {
		t.Fatalf("CommitAll rejected a legitimate edit: %v", err)
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
