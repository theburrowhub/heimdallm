package issues

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/heimdallm/daemon/internal/gitproc"
)

// CommitAuthorName / CommitAuthorEmail identify the daemon in the commits it
// makes on behalf of the auto_implement pipeline. Using a clearly-synthetic
// email avoids collisions with real humans' accounts.
const (
	CommitAuthorName  = "Heimdallm"
	CommitAuthorEmail = "noreply@heimdallm.local"
)

// managedCloneMarkerFile is written by repoctx into Heimdallm-managed clones.
// It is operational metadata, not implementation output, so auto_implement
// must ignore it when deciding whether the agent changed code and when
// staging commits.
const managedCloneMarkerFile = ".heimdallm-managed"

// GitOps is the subset of `git` plumbing the auto_implement pipeline needs.
// Every method takes a context so the daemon can propagate cancellation at
// shutdown (or per-request) through long-running network operations —
// `git fetch` and `git push` in particular.
type GitOps interface {
	// CheckoutNewBranch fetches baseBranch and checks out branch from that
	// tip, overwriting any previous attempt so a re-run starts clean.
	// Uses HTTPS with token for fetch (avoids SSH dependency in Docker).
	CheckoutNewBranch(ctx context.Context, dir, repo, branch, baseBranch, token string) error
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
	// Diff returns the unified diff between `base` ref and HEAD.
	// Used by the pipeline to capture what the agent implemented for
	// LLM-generated PR descriptions (#158).
	Diff(ctx context.Context, dir, base string) (string, error)
}

// GitExec is the default GitOps implementation. All subprocesses pass through
// gitproc so repository config, hooks and inherited environment cannot affect
// daemon-owned Git operations.
type GitExec struct {
	runner *gitproc.Runner
}

// NewGitExec returns a ready-to-use GitExec. Zero configuration required.
func NewGitExec() *GitExec { return &GitExec{runner: gitproc.New()} }

func (g *GitExec) proc() *gitproc.Runner {
	if g == nil || g.runner == nil {
		return gitproc.New()
	}
	return g.runner
}

// CheckoutNewBranch fetches the base branch via HTTPS (using the same
// GIT_ASKPASS mechanism as Push) and creates (or resets) the work branch
// from it. `-B` is deliberate: on a re-run we want the branch to match
// the latest base rather than pick up stale state from a previous attempt.
//
// Using an explicit HTTPS URL instead of `git fetch origin` avoids relying
// on the clone's remote configuration, which may point at an SSH URL that
// requires keys/agent not available inside the Docker container.
func (g *GitExec) CheckoutNewBranch(ctx context.Context, dir, repo, branch, baseBranch, token string) error {
	if token == "" {
		return fmt.Errorf("gitops: checkout requires a non-empty token")
	}
	if err := gitproc.ValidateBranch(branch); err != nil {
		return fmt.Errorf("gitops: checkout branch: %w", err)
	}
	remote, err := gitproc.GitHubRemote(repo)
	if err != nil {
		return fmt.Errorf("gitops: remote: %w", err)
	}
	if err := g.proc().Fetch(ctx, dir, remote, baseBranch, token, gitproc.FetchOptions{}); err != nil {
		return fmt.Errorf("gitops: fetch %s/%s: %w", repo, baseBranch, err)
	}
	// FETCH_HEAD points to the tip of what we just fetched.
	if err := g.proc().Run(ctx, dir, "checkout", "-B", branch, "FETCH_HEAD"); err != nil {
		return fmt.Errorf("gitops: checkout -B %s: %w", branch, err)
	}
	return nil
}

// HasChanges reports whether `git status --porcelain` shows anything — any
// non-empty line means there is a modified, added, deleted, or untracked
// file to commit.
func (g *GitExec) HasChanges(ctx context.Context, dir string) (bool, error) {
	out, err := g.proc().Capture(
		ctx,
		dir,
		"status",
		"--porcelain",
		"--untracked-files=all",
		"--",
		".",
		":(exclude)"+managedCloneMarkerFile,
	)
	if err != nil {
		return false, fmt.Errorf("gitops: status: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// sensitivePathPatterns lists basename globs that the auto_implement
// pipeline refuses to commit. Prompt-injection on the issue body
// could otherwise coerce the AI into writing exfiltration files
// (credentials, private keys) which would then be pushed to GitHub
// via the PR. The patterns target common secret shapes; legitimate
// repository content rarely matches.
//
// Match is performed against the lowercased basename of the staged
// path with filepath.Match, so e.g. `secret.pem`, `Secret.PEM`, and
// `subdir/secret.pem` all match `*.pem`. Lowercasing closes a
// case-insensitive-filesystem bypass (macOS / Windows default).
//
// Notes on intentional exclusions:
//   - `.heimdallm-managed` is already excluded from staging by the
//     `:(exclude)` pathspec in CommitAll, so it does not need to
//     appear here.
//   - SSH public keys (id_*.pub) are not secrets — projects
//     legitimately ship example/deploy public keys, so they stay
//     allow-listed.
//   - `config.toml` is handled separately (rootOnlySensitiveNames)
//     because many Go/Rust/Hugo projects use the name for harmless
//     subdirectory configuration; we only refuse it at the repo
//     root, where it would collide with Heimdallm's own operator
//     config.
var sensitivePathPatterns = []string{
	".env", ".env.*",
	"*.pem", "*.key", "*.crt", "*.cer", "*.p12", "*.pfx",
	"*.gpg", "*.asc",
	"*.jks", "*.keystore", "*.kdbx",
	"*.ovpn",
	"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
	"credentials", "credentials.*",
	".git-credentials",
	"kubeconfig", ".kubeconfig",
	".npmrc", ".netrc", ".pypirc",
	".bash_history", ".zsh_history",
	"service-account*.json",
	"terraform.tfvars", "terraform.tfvars.*",
	"wallet.dat",
}

// rootOnlySensitiveNames lists basenames that are refused only when
// they appear at the repository root. Same-named files nested under
// a subdirectory (e.g. `docs/config.toml` as an example fixture) are
// allowed because the legitimate-use rate is high.
var rootOnlySensitiveNames = map[string]bool{
	"config.toml": true,
}

// matchesSensitivePattern reports whether the staged path is
// considered sensitive. Returns the matched pattern and true on hit.
// The basename is lowercased before matching to defeat case-variant
// bypasses on case-insensitive filesystems. Match errors are treated
// as non-matches: a bad pattern is a programmer bug, not a reason to
// silently let through every commit.
func matchesSensitivePattern(path string) (string, bool) {
	clean := filepath.Clean(path)
	base := strings.ToLower(filepath.Base(clean))
	// Root-only allowlist: refuse `config.toml` at depth 0 but allow
	// `anywhere/else/config.toml`.
	if !strings.Contains(clean, string(filepath.Separator)) {
		if rootOnlySensitiveNames[base] {
			return base, true
		}
	}
	for _, pat := range sensitivePathPatterns {
		ok, err := filepath.Match(pat, base)
		if err == nil && ok {
			return pat, true
		}
	}
	return "", false
}

// CommitAll stages every change and commits with the Heimdallm identity.
// Uses `-c` flags so the repo-level and global git config are never touched.
//
// Before committing, the staged file list is scanned against
// sensitivePathPatterns: if a prompt-injected AI run tried to write
// secrets (private keys, .env, the daemon's config.toml) into the
// worktree to exfiltrate them via the PR, the commit is refused and
// the index is reset so a retry from scratch is not poisoned.
func (g *GitExec) CommitAll(ctx context.Context, dir, message string) error {
	if err := g.proc().Run(ctx, dir, "add", "-A", "--", ".", ":(exclude)"+managedCloneMarkerFile); err != nil {
		return fmt.Errorf("gitops: add: %w", err)
	}
	// `-z` + NUL split: defeats core.quotepath=on (the git default)
	// which would escape non-ASCII paths like `weird\303\251.pem` and
	// make filepath.Match miss them. `-c core.quotepath=off` is
	// redundant when -z is used but kept as belt-and-suspenders.
	staged, err := g.proc().Capture(
		ctx,
		dir,
		"-c", "core.quotepath=off",
		"diff",
		"--cached",
		"--no-ext-diff",
		"--no-textconv",
		"--name-only",
		"-z",
		"--",
	)
	if err != nil {
		return fmt.Errorf("gitops: list staged: %w", err)
	}
	var refused []string
	for _, path := range strings.Split(strings.TrimRight(string(staged), "\x00"), "\x00") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if pat, hit := matchesSensitivePattern(path); hit {
			slog.Warn("gitops: sensitive-path denylist hit (prompt-injection defense)",
				"path", path, "pattern", pat)
			refused = append(refused, path)
			continue
		}
		// Defense-in-depth: even if the basename looks innocuous, a
		// symlink can carry intent (target path embedded in the blob)
		// and signals an AI run trying to reach outside the worktree.
		// Refuse outright. The cleanup path is the same so we batch
		// the refusal list together.
		if info, statErr := os.Lstat(filepath.Join(dir, path)); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			slog.Warn("gitops: symlink in staged tree refused (prompt-injection defense)",
				"path", path)
			refused = append(refused, path)
		}
	}
	if len(refused) > 0 {
		// Reset the index AND remove the offending paths from disk —
		// otherwise the worktree state would still trip the next
		// `git add -A` and the pipeline would loop. We only remove
		// the matched files; the rest of the worktree (which contains
		// legitimate edits) stays intact for triage. Cleanup errors
		// are surfaced via slog so a stuck worktree (read-only FS,
		// missing perms) is diagnosable.
		if resetErr := g.proc().Run(ctx, dir, "reset", "--", "."); resetErr != nil {
			slog.Error("gitops: failed to reset index after denylist hit", "err", resetErr)
		}
		for _, p := range refused {
			if rmErr := os.Remove(filepath.Join(dir, p)); rmErr != nil && !os.IsNotExist(rmErr) {
				slog.Error("gitops: failed to remove refused path; retry may loop",
					"path", p, "err", rmErr)
			}
		}
		return fmt.Errorf("gitops: refusing commit — staged %d file(s) matched sensitive-path denylist (e.g. %q); prompt-injection defense aborted the auto-implement run",
			len(refused), refused[0])
	}
	if err := g.proc().Run(ctx, dir,
		"-c", "user.name="+CommitAuthorName,
		"-c", "user.email="+CommitAuthorEmail,
		"commit", "--no-verify", "-m", message,
	); err != nil {
		return fmt.Errorf("gitops: commit: %w", err)
	}
	return nil
}

// Push uploads the branch through gitproc's temporary bare transport. The
// token is handed only to that transport's Git process via GIT_ASKPASS.
//
// This keeps the token out of:
//   - argv (no token in `git push https://…@github.com/…` → invisible to
//     `ps aux` / `/proc/<pid>/cmdline`),
//   - the remote URL (the URL uses `x-access-token` as username only),
//   - the error message path (git's stderr only ever sees an opaque
//     "Password for 'https://x-access-token@github.com'" prompt).
//
// The helper and transport live in owner-only temporary directories and are
// removed when the operation returns.
func (g *GitExec) Push(ctx context.Context, dir, repo, branch, token string) error {
	if token == "" {
		return fmt.Errorf("gitops: push requires a non-empty token")
	}
	remote, err := gitproc.GitHubRemote(repo)
	if err != nil {
		return fmt.Errorf("gitops: remote: %w", err)
	}
	if _, err := g.proc().PushBranch(ctx, dir, remote, branch, token); err != nil {
		return fmt.Errorf("gitops: push %s:%s: %w", repo, branch, err)
	}
	return nil
}

// DeleteRemoteBranch drops the named branch only if it still points at the
// local object that Push uploaded. It uses the same isolated transport as Push.
func (g *GitExec) DeleteRemoteBranch(ctx context.Context, dir, repo, branch, token string) error {
	if token == "" {
		return fmt.Errorf("gitops: delete remote requires a non-empty token")
	}
	if err := gitproc.ValidateBranch(branch); err != nil {
		return fmt.Errorf("gitops: delete remote branch: %w", err)
	}
	remote, err := gitproc.GitHubRemote(repo)
	if err != nil {
		return fmt.Errorf("gitops: remote: %w", err)
	}
	expected, err := g.proc().Revision(ctx, dir, "refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("gitops: resolve pushed branch %s: %w", branch, err)
	}
	if err := g.proc().DeleteBranch(ctx, remote, branch, expected, token); err != nil {
		return fmt.Errorf("gitops: delete remote %s:%s: %w", repo, branch, err)
	}
	return nil
}

// Diff returns the unified diff between base and HEAD.
func (g *GitExec) Diff(ctx context.Context, dir, base string) (string, error) {
	baseOID, err := g.proc().Revision(ctx, dir, base)
	if err != nil {
		return "", fmt.Errorf("gitops: resolve diff base %s: %w", base, err)
	}
	out, err := g.proc().Capture(ctx, dir, "diff", "--no-ext-diff", "--no-textconv", baseOID+"..HEAD", "--")
	if err != nil {
		return "", fmt.Errorf("gitops: diff %s..HEAD: %w", base, err)
	}
	return string(out), nil
}

// captureGit remains the package-level history-reader seam used by triage.
// Environment injection is deliberately unsupported: all callers now share
// gitproc's clean subprocess boundary.
func captureGit(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	if len(env) != 0 {
		return nil, fmt.Errorf("gitops: custom Git environments are disabled")
	}
	return gitproc.New().Capture(ctx, dir, args...)
}
