package issues

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// gitTimeout caps each `git` invocation so a hung network or huge fetch
// cannot stall the pipeline indefinitely. Three minutes is generous for
// fetch/push on a typical repo and still short enough to unblock operators.
const gitTimeout = 3 * time.Minute

// CommitAuthorName / CommitAuthorEmail identify the daemon in the commits it
// makes on behalf of the auto_implement pipeline. Using a clearly-synthetic
// email avoids collisions with real humans' accounts.
const (
	CommitAuthorName  = "Heimdallm"
	CommitAuthorEmail = "noreply@heimdallm.local"
)

// GitOps is the subset of `git` plumbing the auto_implement pipeline needs.
// Kept as an interface so tests inject a fake without a real checkout on
// disk.
type GitOps interface {
	// CheckoutNewBranch fetches baseBranch from origin and checks out branch
	// from that tip, overwriting any previous attempt at the same branch so
	// a re-run starts clean.
	CheckoutNewBranch(dir, branch, baseBranch string) error
	// HasChanges reports whether the working tree has modified or untracked
	// files — both are in scope for the commit because the agent may create
	// new files as well as edit existing ones.
	HasChanges(dir string) (bool, error)
	// CommitAll stages every change and commits with the daemon's identity.
	// The caller is expected to have checked HasChanges first; committing an
	// empty tree is an error here, not a no-op.
	CommitAll(dir, message string) error
	// Push uploads the branch to origin using a token in the ephemeral URL.
	// The token is never written to git config or to stdout.
	Push(dir, repo, branch, token string) error
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
func (g *GitExec) CheckoutNewBranch(dir, branch, baseBranch string) error {
	if err := runGit(dir, "fetch", "origin", baseBranch); err != nil {
		return fmt.Errorf("gitops: fetch origin/%s: %w", baseBranch, err)
	}
	if err := runGit(dir, "checkout", "-B", branch, "origin/"+baseBranch); err != nil {
		return fmt.Errorf("gitops: checkout -B %s origin/%s: %w", branch, baseBranch, err)
	}
	return nil
}

// HasChanges reports whether `git status --porcelain` shows anything — any
// non-empty line means there is a modified, added, deleted, or untracked
// file to commit.
func (g *GitExec) HasChanges(dir string) (bool, error) {
	out, err := captureGit(dir, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("gitops: status: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// CommitAll stages every change and commits with the Heimdallm identity.
// Uses `-c` flags so the repo-level and global git config are never touched.
func (g *GitExec) CommitAll(dir, message string) error {
	if err := runGit(dir, "add", "-A"); err != nil {
		return fmt.Errorf("gitops: add: %w", err)
	}
	if err := runGit(dir,
		"-c", "user.name="+CommitAuthorName,
		"-c", "user.email="+CommitAuthorEmail,
		"commit", "-m", message,
	); err != nil {
		return fmt.Errorf("gitops: commit: %w", err)
	}
	return nil
}

// Push uploads the branch to origin via HTTPS with the token in the URL's
// userinfo. The token lives only inside the git process arguments and the
// pipe buffers — it is never persisted to a config file, stderr is scrubbed
// of any echoes before an error bubbles up.
func (g *GitExec) Push(dir, repo, branch, token string) error {
	if token == "" {
		return fmt.Errorf("gitops: push requires a non-empty token")
	}
	url := fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", token, repo)
	refspec := branch + ":" + branch
	if err := runGit(dir, "push", url, refspec); err != nil {
		// Scrub the token out of any surface containing it before returning —
		// git will occasionally echo the remote URL in error messages.
		msg := strings.ReplaceAll(err.Error(), token, "***")
		return fmt.Errorf("gitops: push %s:%s: %s", repo, branch, msg)
	}
	return nil
}

func runGit(dir string, args ...string) error {
	_, err := captureGit(dir, args...)
	return err
}

func captureGit(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
