package executor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

// TestExecuteRawGivesCodexAnIsolatedWorkspaceWithoutWorkDir covers the review
// path that has no local checkout (see theburrowhub/heimdallm#655). Without a
// WorkDir the child used to inherit the daemon's cwd — which under launchd is
// `/` — and `codex exec` aborted with "Not inside a trusted directory and
// --skip-git-repo-check was not specified." The daemon must hand codex an empty
// per-execution workspace instead of the filesystem root, and tell codex not to
// insist on a git repo, because a review needs no checkout at all: the diff is
// already in the prompt.
func TestExecuteRawGivesCodexAnIsolatedWorkspaceWithoutWorkDir(t *testing.T) {
	defer executor.ResetLoginPathCacheForTest()()

	binDir := t.TempDir()
	captureArgs := filepath.Join(t.TempDir(), "args.txt")
	captureCWD := filepath.Join(t.TempDir(), "cwd.txt")
	script := fakeCLIScript("Usage: codex\n  -C, --cd <DIR>\n", captureArgs, captureCWD)
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir)

	e := executor.New()
	if _, err := e.ExecuteRaw("codex", "prompt", executor.ExecOptions{}); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}

	argsBytes, err := os.ReadFile(captureArgs)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	args := strings.Fields(string(argsBytes))
	if !contains(args, "--skip-git-repo-check") {
		t.Errorf("args = %v, want --skip-git-repo-check when codex runs without a WorkDir", args)
	}

	cwdBytes, err := os.ReadFile(captureCWD)
	if err != nil {
		t.Fatalf("read captured cwd: %v", err)
	}
	cwd := strings.TrimSpace(string(cwdBytes))
	if cwd == "/" {
		t.Fatal("codex ran with cwd=/, want a dedicated empty workspace")
	}
	if cwd == "" {
		t.Fatal("fake CLI captured an empty cwd")
	}

	entries, err := os.ReadDir(cwd)
	if err == nil && len(entries) > 0 {
		t.Errorf("workspace %q was not empty: %d entries", cwd, len(entries))
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("workspace %q still exists after the run (stat err = %v), want it removed", cwd, err)
	}
}

// TestExecuteRawKeepsCodexTrustFlagOffWithWorkDir pins the behaviour that must
// NOT change: when a checkout is available the daemon passes it via --cd and
// adds no trust-policy flag, so codex keeps its own guard for real workspaces.
func TestExecuteRawKeepsCodexTrustFlagOffWithWorkDir(t *testing.T) {
	defer executor.ResetLoginPathCacheForTest()()

	binDir := t.TempDir()
	captureArgs := filepath.Join(t.TempDir(), "args.txt")
	captureCWD := filepath.Join(t.TempDir(), "cwd.txt")
	script := fakeCLIScript("Usage: codex\n  -C, --cd <DIR>\n", captureArgs, captureCWD)
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir)

	workDir := t.TempDir()
	e := executor.New()
	if _, err := e.ExecuteRaw("codex", "prompt", executor.ExecOptions{WorkDir: workDir}); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}

	argsBytes, err := os.ReadFile(captureArgs)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	args := strings.Fields(string(argsBytes))
	if contains(args, "--skip-git-repo-check") {
		t.Errorf("args = %v, want no trust-policy flag when a WorkDir is provided", args)
	}
	if !containsInOrder(args, "--cd", workDir) {
		t.Errorf("args = %v, want --cd %s", args, workDir)
	}

	cwdBytes, err := os.ReadFile(captureCWD)
	if err != nil {
		t.Fatalf("read captured cwd: %v", err)
	}
	requireSameDir(t, strings.TrimSpace(string(cwdBytes)), workDir)
}

// TestExecuteRawLeavesOtherCLIsAloneWithoutWorkDir keeps the workspace
// substitution scoped to codex: claude and gemini have no such requirement and
// their argument lists must stay untouched.
func TestExecuteRawLeavesOtherCLIsAloneWithoutWorkDir(t *testing.T) {
	for _, cli := range []string{"claude", "gemini"} {
		t.Run(cli, func(t *testing.T) {
			defer executor.ResetLoginPathCacheForTest()()

			binDir := t.TempDir()
			captureArgs := filepath.Join(t.TempDir(), "args.txt")
			captureCWD := filepath.Join(t.TempDir(), "cwd.txt")
			script := fakeCLIScript("Usage: "+cli+"\n", captureArgs, captureCWD)
			if err := os.WriteFile(filepath.Join(binDir, cli), []byte(script), 0o755); err != nil {
				t.Fatalf("write fake CLI: %v", err)
			}
			t.Setenv("PATH", binDir)

			e := executor.New()
			if _, err := e.ExecuteRaw(cli, "prompt", executor.ExecOptions{}); err != nil {
				t.Fatalf("ExecuteRaw: %v", err)
			}

			argsBytes, err := os.ReadFile(captureArgs)
			if err != nil {
				t.Fatalf("read captured args: %v", err)
			}
			args := strings.Fields(string(argsBytes))
			if contains(args, "--skip-git-repo-check") {
				t.Errorf("args = %v, want no codex-specific flag for %s", args, cli)
			}
			if contains(args, "--cd") {
				t.Errorf("args = %v, want no --cd for %s without a WorkDir", args, cli)
			}
		})
	}
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
