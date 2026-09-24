// Package gitops is the git plumbing Heimdallm runs against its own managed
// checkouts: fetching and checking out PR branches, rebasing them, detecting
// and staging conflict resolutions, and force-pushing with an explicit lease.
//
// Every network operation authenticates through GIT_ASKPASS so the GitHub
// token never reaches argv, the remote URL or git config on disk.
package gitops

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/procgroup"
)

// gitTimeout caps each `git` invocation so a hung network or huge fetch
// cannot stall the pipeline indefinitely. Three minutes is generous for
// fetch/push on a typical repo and still short enough to unblock operators.
// Callers may tighten this per-call via the context they pass in.
const gitTimeout = 3 * time.Minute

// CommitAuthorName / CommitAuthorEmail identify the daemon in the commits it
// makes on its own behalf (conflict-resolution rebases). Using a clearly-synthetic
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

// managedCloneMarkerFile is written by repoctx into Heimdallm-managed clones.
// It is operational metadata, not repository content, so it is never staged
// and never counted as a change.
const managedCloneMarkerFile = ".heimdallm-managed"

// GitExec shells out to the `git` binary. The daemon assumes git is available
// in PATH; the first command that runs returns a descriptive error if it is
// not.
type GitExec struct{}

// NewGitExec returns a ready-to-use GitExec. Zero configuration required.
func NewGitExec() *GitExec { return &GitExec{} }

// sensitivePathPatterns lists basename globs that StageAll refuses to
// stage. Prompt-injection through repository content (conflict hunks,
// file names) could otherwise coerce the agent into writing exfiltration
// files (credentials, private keys) which would then be force-pushed to
// GitHub. The patterns target common secret shapes; legitimate
// repository content rarely matches.
//
// Match is performed against the lowercased basename of the staged
// path with filepath.Match, so e.g. `secret.pem`, `Secret.PEM`, and
// `subdir/secret.pem` all match `*.pem`. Lowercasing closes a
// case-insensitive-filesystem bypass (macOS / Windows default).
//
// Notes on intentional exclusions:
//   - `.heimdallm-managed` is already excluded from staging by the
//     `:(exclude)` pathspec in StageAll, so it does not need to
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

// enforceSensitivePathDenylist scans the already-staged file list and refuses
// the whole operation when any path looks like a secret or is a symlink.
//
// Every path that stages files goes through this one scan, so there is no
// second, subtly different copy of the prompt-injection defense to drift.
func enforceSensitivePathDenylist(ctx context.Context, dir string) error {
	// `-z` + NUL split: defeats core.quotepath=on (the git default)
	// which would escape non-ASCII paths like `weird\303\251.pem` and
	// make filepath.Match miss them. `-c core.quotepath=off` is
	// redundant when -z is used but kept as belt-and-suspenders.
	staged, err := captureGit(ctx, dir, nil, "-c", "core.quotepath=off", "diff", "--cached", "--name-only", "-z")
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
		if resetErr := runGit(ctx, dir, nil, "reset", "--", "."); resetErr != nil {
			slog.Error("gitops: failed to reset index after denylist hit", "err", resetErr)
		}
		for _, p := range refused {
			if rmErr := os.Remove(filepath.Join(dir, p)); rmErr != nil && !os.IsNotExist(rmErr) {
				slog.Error("gitops: failed to remove refused path; retry may loop",
					"path", p, "err", rmErr)
			}
		}
		return fmt.Errorf("gitops: refusing commit — staged %d file(s) matched sensitive-path denylist (e.g. %q); prompt-injection defense aborted the run",
			len(refused), refused[0])
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
	// procgroup.Run rather than cmd.Run: this helper backs every git call in
	// GitExec, including FetchRef / PushForceWithLease, which
	// fork `ssh` for SSH remotes. exec.CommandContext's cancellation reaches
	// only git, leaving that ssh child orphaned onto PID 1 as a zombie
	// (theburrowhub/heimdallm#665).
	if err := procgroup.Run(cmd); err != nil {
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
