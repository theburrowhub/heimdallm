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

func TestCommitAll_RefusesSensitivePathsCaseInsensitive(t *testing.T) {
	// macOS/Windows default to case-insensitive filesystems, where
	// `.ENV` resolves to the same file as `.env`. Lowercasing the
	// basename before matching closes that bypass.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	for _, filename := range []string{".ENV", "Secret.PEM", "ID_RSA", "Config.TOML"} {
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

			if err := os.WriteFile(filepath.Join(dir, filename), []byte("EXFIL=\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			err := issues.NewGitExec().CommitAll(context.Background(), dir, "fix")
			if err == nil {
				t.Fatalf("CommitAll accepted case-variant %q (case-insensitive bypass)", filename)
			}
		})
	}
}

func TestCommitAll_RefusesSensitivePathsNested(t *testing.T) {
	// Nested paths under arbitrary subdirectories must still be
	// caught — match is on basename so directory depth is irrelevant.
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

	subdir := filepath.Join(dir, "deep", "nested", "dir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "secret.pem"), []byte("-----BEGIN-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := issues.NewGitExec().CommitAll(context.Background(), dir, "fix")
	if err == nil {
		t.Fatalf("CommitAll accepted nested sensitive path")
	}
	if !strings.Contains(err.Error(), "sensitive-path") {
		t.Errorf("error should mention denylist, got: %v", err)
	}
}

func TestCommitAll_RetryAfterDenylistDoesNotLoop(t *testing.T) {
	// On a denylist hit we reset the index AND remove the offending
	// file from disk so a subsequent `git add -A` does not re-stage
	// the same secret. Without disk cleanup, the auto-implement
	// pipeline would loop forever on the same content.
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

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("X=y\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	git := issues.NewGitExec()
	if err := git.CommitAll(context.Background(), dir, "fix"); err == nil {
		t.Fatal("first CommitAll should refuse")
	}
	// The sensitive file must be gone from disk so the next
	// add-stage cycle starts clean.
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env still on disk after denylist hit: %v", err)
	}
	// README.md must survive — legitimate edits stay in the worktree.
	data, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil || string(data) != "# fixed\n" {
		t.Fatalf("legitimate edit was wiped or not preserved: data=%q err=%v", string(data), err)
	}
	// And a retry without the sensitive file must succeed.
	if err := git.CommitAll(context.Background(), dir, "fix"); err != nil {
		t.Fatalf("retry CommitAll: %v", err)
	}
}

func TestCommitAll_RefusesSymlinkEvenWithInnocentBasename(t *testing.T) {
	// Defense-in-depth: a symlink with an innocent basename can still
	// signal an AI run trying to reach outside the worktree. Reject
	// it so the intent never leaves the daemon.
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

	// Symlink with an innocent basename, target points outside the worktree.
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "helpers.txt")); err != nil {
		t.Fatal(err)
	}

	err := issues.NewGitExec().CommitAll(context.Background(), dir, "fix")
	if err == nil {
		t.Fatal("CommitAll accepted symlink with innocent basename")
	}
	if !strings.Contains(err.Error(), "denylist") {
		t.Errorf("error should mention denylist, got: %v", err)
	}
}

func TestCommitAll_AllowsConfigTomlInSubdir(t *testing.T) {
	// config.toml is denied at the repo root (Heimdallm's operator
	// config) but allowed in subdirectories where projects often
	// keep example fixtures.
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

	subdir := filepath.Join(dir, "docs", "examples")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "config.toml"), []byte("[example]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := issues.NewGitExec().CommitAll(context.Background(), dir, "docs: add example"); err != nil {
		t.Fatalf("CommitAll rejected docs/examples/config.toml: %v", err)
	}
}

func TestCommitAll_AllowsPublicSSHKeys(t *testing.T) {
	// Public SSH keys are not secrets and projects legitimately
	// ship them (deploy-key docs, ssh tutorials, etc.).
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

	if err := os.WriteFile(filepath.Join(dir, "id_rsa.pub"), []byte("ssh-rsa AAAA...\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := issues.NewGitExec().CommitAll(context.Background(), dir, "docs: deploy key"); err != nil {
		t.Fatalf("CommitAll rejected public key: %v", err)
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
