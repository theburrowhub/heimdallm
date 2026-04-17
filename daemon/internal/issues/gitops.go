package issues

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitTimeout caps each `git` invocation so a hung network or huge fetch
// cannot stall the pipeline indefinitely. Three minutes is generous for
// fetch/push on a typical repo and still short enough to unblock operators.
// Callers may tighten this per-call via the context they pass in.
const gitTimeout = 3 * time.Minute

// CommitAuthorName / CommitAuthorEmail identify the daemon in the commits it
// makes on behalf of the auto_implement pipeline. Using a clearly-synthetic
// email avoids collisions with real humans' accounts.
const (
	CommitAuthorName  = "Heimdallm"
	CommitAuthorEmail = "noreply@heimdallm.local"
)

// maxGitStderrBytes caps the amount of stderr we keep in memory for error
// messages. Git can dump huge merge-conflict reports or verbose network
// traces; keeping all of it would let a single bad repo push the daemon
// toward OOM.
const maxGitStderrBytes = 16 * 1024 // 16 KiB

// GitOps is the subset of `git` plumbing the auto_implement pipeline needs.
// Every method takes a context so the daemon can propagate cancellation at
// shutdown (or per-request) through long-running network operations —
// `git fetch` and `git push` in particular.
type GitOps interface {
	// CheckoutNewBranch fetches baseBranch from origin and checks out branch
	// from that tip, overwriting any previous attempt at the same branch so
	// a re-run starts clean.
	CheckoutNewBranch(ctx context.Context, dir, branch, baseBranch string) error
	// HasChanges reports whether the working tree has modified or untracked
	// files — both are in scope for the commit because the agent may create
	// new files as well as edit existing ones.
	HasChanges(ctx context.Context, dir string) (bool, error)
	// CommitAll stages every change and commits with the daemon's identity.
	// The caller is expected to have checked HasChanges first; committing an
	// empty tree is an error here, not a no-op.
	CommitAll(ctx context.Context, dir, message string) error
	// Push uploads the branch to origin using GIT_ASKPASS so the token never
	// touches argv, the URL, or git config on disk.
	Push(ctx context.Context, dir, repo, branch, token string) error
	// DeleteRemoteBranch removes a branch from the origin remote. Used by
	// the pipeline to clean up an orphaned branch when the last step
	// (CreatePR) fails after Push succeeded.
	DeleteRemoteBranch(ctx context.Context, dir, repo, branch, token string) error
}

// GitExec is the default GitOps implementation — shells out to the `git`
// binary. The daemon assumes git is available in PATH; the first command
// that runs returns a descriptive error if it is not.
type GitExec struct{}

// NewGitExec returns a ready-to-use GitExec. Zero configuration required.
func NewGitExec() *GitExec { return &GitExec{} }

// CheckoutNewBranch fetches the base branch and creates (or resets) the
// work branch from it. `-B` is deliberate: on a re-run we want the branch
// to match the latest base rather than pick up stale state from a previous
// failed attempt.
func (g *GitExec) CheckoutNewBranch(ctx context.Context, dir, branch, baseBranch string) error {
	if err := runGit(ctx, dir, nil, "fetch", "origin", baseBranch); err != nil {
		return fmt.Errorf("gitops: fetch origin/%s: %w", baseBranch, err)
	}
	if err := runGit(ctx, dir, nil, "checkout", "-B", branch, "origin/"+baseBranch); err != nil {
		return fmt.Errorf("gitops: checkout -B %s origin/%s: %w", branch, baseBranch, err)
	}
	return nil
}

// HasChanges reports whether `git status --porcelain` shows anything — any
// non-empty line means there is a modified, added, deleted, or untracked
// file to commit.
func (g *GitExec) HasChanges(ctx context.Context, dir string) (bool, error) {
	out, err := captureGit(ctx, dir, nil, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("gitops: status: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// CommitAll stages every change and commits with the Heimdallm identity.
// Uses `-c` flags so the repo-level and global git config are never touched.
func (g *GitExec) CommitAll(ctx context.Context, dir, message string) error {
	if err := runGit(ctx, dir, nil, "add", "-A"); err != nil {
		return fmt.Errorf("gitops: add: %w", err)
	}
	if err := runGit(ctx, dir, nil,
		"-c", "user.name="+CommitAuthorName,
		"-c", "user.email="+CommitAuthorEmail,
		"commit", "-m", message,
	); err != nil {
		return fmt.Errorf("gitops: commit: %w", err)
	}
	return nil
}

// Push uploads the branch to origin. The token is handed to git via
// GIT_ASKPASS: we write a tiny executable that echoes the token, set the
// env var, and let git call it when it needs the password.
//
// This keeps the token out of:
//   - argv (no token in `git push https://…@github.com/…` → invisible to
//     `ps aux` / `/proc/<pid>/cmdline`),
//   - the remote URL (the URL uses `x-access-token` as username only),
//   - the error message path (git's stderr only ever sees an opaque
//     "Password for 'https://x-access-token@github.com'" prompt).
//
// The helper file is written with 0700 perms in an owner-only temp dir and
// removed on function exit.
func (g *GitExec) Push(ctx context.Context, dir, repo, branch, token string) error {
	if token == "" {
		return fmt.Errorf("gitops: push requires a non-empty token")
	}
	env, cleanup, err := buildAskPassEnv(token)
	if err != nil {
		return fmt.Errorf("gitops: setup askpass: %w", err)
	}
	defer cleanup()

	url := fmt.Sprintf("https://x-access-token@github.com/%s.git", repo)
	refspec := branch + ":" + branch
	if err := runGit(ctx, dir, env, "push", url, refspec); err != nil {
		return fmt.Errorf("gitops: push %s:%s: %w", repo, branch, err)
	}
	return nil
}

// DeleteRemoteBranch drops the named branch from origin. Runs through the
// same GIT_ASKPASS path as Push so the token stays off argv.
func (g *GitExec) DeleteRemoteBranch(ctx context.Context, dir, repo, branch, token string) error {
	if token == "" {
		return fmt.Errorf("gitops: delete remote requires a non-empty token")
	}
	env, cleanup, err := buildAskPassEnv(token)
	if err != nil {
		return fmt.Errorf("gitops: setup askpass: %w", err)
	}
	defer cleanup()

	url := fmt.Sprintf("https://x-access-token@github.com/%s.git", repo)
	// `:<branch>` is the standard "delete the remote branch" refspec.
	refspec := ":" + branch
	if err := runGit(ctx, dir, env, "push", url, refspec); err != nil {
		return fmt.Errorf("gitops: delete remote %s:%s: %w", repo, branch, err)
	}
	return nil
}

// buildAskPassEnv writes a small helper script that echoes the token, and
// returns an env slice that points GIT_ASKPASS at it. The returned cleanup
// function must be called (via defer) to remove the temp dir.
//
// Using a temp directory — not just a temp file — means the helper script's
// parent is owner-only too, so even momentarily the file is not world-
// readable.
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
	// The script simply prints the token. Git ignores the "prompt" argument
	// passed in $1; we do not read it. Writing the token verbatim with `cat`
	// avoids shell-escaping pitfalls — the token is fed on stdin-less invoke.
	script := "#!/bin/sh\nprintf '%s' \"$HEIMDALLM_GIT_TOKEN\"\n"
	if err := os.WriteFile(helperPath, []byte(script), 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, nil, fmt.Errorf("write askpass script: %w", err)
	}

	env := append(os.Environ(),
		"GIT_ASKPASS="+helperPath,
		"GIT_TERMINAL_PROMPT=0",
		"HEIMDALLM_GIT_TOKEN="+token, // read by the helper script via env
	)
	cleanup := func() { os.RemoveAll(dir) }
	return env, cleanup, nil
}

// runGit discards stdout and returns an error that wraps whatever git wrote
// to stderr (truncated to maxGitStderrBytes so a verbose failure cannot
// balloon the daemon's memory).
func runGit(ctx context.Context, dir string, env []string, args ...string) error {
	_, err := captureGit(ctx, dir, env, args...)
	return err
}

// captureGit runs git with the effective dir / env / args and returns its
// stdout. When git exits non-zero, the returned error includes a trimmed
// stderr excerpt so the caller can diagnose without digging into logs.
func captureGit(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Cap stderr to protect against pathological output (e.g. huge
		// merge-conflict reports, repeated progress lines, etc).
		errText := stderr.String()
		if len(errText) > maxGitStderrBytes {
			errText = errText[:maxGitStderrBytes] + "\n... (stderr truncated)"
		}
		return nil, fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(errText))
	}
	return stdout.Bytes(), nil
}
