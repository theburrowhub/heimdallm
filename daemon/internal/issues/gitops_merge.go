package issues

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RebaseOutcome reports how a rebase ended.
type RebaseOutcome struct {
	// Clean means the rebase completed with no conflicts.
	Clean bool
	// Conflicts lists the unmerged paths when the rebase stopped. Non-empty
	// implies !Clean.
	Conflicts []string
	// Stderr is git's (truncated) complaint, for diagnostics.
	Stderr string
}

// conflictMarkers are the textual markers git leaves in a conflicted file. A
// resolution that leaves any of them behind is not a resolution.
var conflictMarkers = []string{"<<<<<<< ", "=======\n", ">>>>>>> "}

// FetchRef fetches a single ref from the repo's HTTPS remote and returns the
// SHA it points at. Uses the same GIT_ASKPASS path as the rest of GitExec, so
// the token never reaches argv, the URL or git config on disk.
func (g *GitExec) FetchRef(ctx context.Context, dir, repo, ref, token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("gitops: fetch ref requires a non-empty token")
	}
	env, cleanup, err := buildAskPassEnv(token)
	if err != nil {
		return "", fmt.Errorf("gitops: setup askpass for fetch ref: %w", err)
	}
	defer cleanup()

	url := fmt.Sprintf("https://x-access-token@github.com/%s.git", repo)
	if err := runGit(ctx, dir, env, "fetch", "--no-tags", url, ref); err != nil {
		return "", fmt.Errorf("gitops: fetch %s/%s: %w", repo, ref, err)
	}
	out, err := captureGit(ctx, dir, nil, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", fmt.Errorf("gitops: rev-parse FETCH_HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CheckoutRemoteBranch fetches branch and checks out a local branch of the same
// name at its tip, discarding any local state from a previous run. Returns the
// SHA that was checked out — the caller needs it as the lease for a later
// force-push, so it can prove nobody else pushed in the meantime.
func (g *GitExec) CheckoutRemoteBranch(ctx context.Context, dir, repo, branch, token string) (string, error) {
	sha, err := g.FetchRef(ctx, dir, repo, branch, token)
	if err != nil {
		return "", err
	}
	// -B rather than -b: a retry must land on the freshly fetched tip instead
	// of inheriting a stale local branch.
	if err := runGit(ctx, dir, nil, "checkout", "-B", branch, "FETCH_HEAD"); err != nil {
		return "", fmt.Errorf("gitops: checkout -B %s: %w", branch, err)
	}
	return sha, nil
}

// HeadSHA returns the current HEAD commit.
func (g *GitExec) HeadSHA(ctx context.Context, dir string) (string, error) {
	out, err := captureGit(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("gitops: rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// rebaseIdentityArgs pins the committer identity and disables anything
// interactive. Without the editor overrides a rebase that wants to open an
// editor would hang until the 3-minute timeout kills it.
func rebaseIdentityArgs() []string {
	return []string{
		"-c", "user.name=" + CommitAuthorName,
		"-c", "user.email=" + CommitAuthorEmail,
		"-c", "core.editor=true",
		"-c", "rebase.autoStash=false",
	}
}

// rebaseEnv disables the sequence editor for the same reason as core.editor.
func rebaseEnv() []string {
	return append(os.Environ(),
		"GIT_EDITOR=true",
		"GIT_SEQUENCE_EDITOR=true",
		"GIT_TERMINAL_PROMPT=0",
	)
}

// RebaseOnto replays the current branch on top of ontoSHA.
//
// A non-zero exit is not automatically an error: the expected outcome of a
// rebase with conflicts is exit 1 plus unmerged paths, which the caller wants
// reported, not raised. A non-zero exit with no unmerged paths is a real
// failure, and the rebase is aborted before returning so the working tree is
// never left mid-rebase for the next caller to trip over.
func (g *GitExec) RebaseOnto(ctx context.Context, dir, ontoSHA string) (RebaseOutcome, error) {
	args := append(rebaseIdentityArgs(), "rebase", ontoSHA)
	_, err := captureGit(ctx, dir, rebaseEnv(), args...)
	if err == nil {
		return RebaseOutcome{Clean: true}, nil
	}

	conflicts, listErr := g.ConflictedFiles(ctx, dir)
	if listErr == nil && len(conflicts) > 0 {
		return RebaseOutcome{Conflicts: conflicts, Stderr: err.Error()}, nil
	}
	// Not a conflict: leave no half-finished rebase behind.
	if abortErr := g.AbortRebase(ctx, dir); abortErr != nil {
		return RebaseOutcome{Stderr: err.Error()},
			fmt.Errorf("gitops: rebase onto %s failed (%v) and abort also failed: %w", ontoSHA, err, abortErr)
	}
	return RebaseOutcome{Stderr: err.Error()}, fmt.Errorf("gitops: rebase onto %s: %w", ontoSHA, err)
}

// MergeRef merges ontoSHA into the current branch, reporting conflicts the same
// way RebaseOnto does.
func (g *GitExec) MergeRef(ctx context.Context, dir, ontoSHA, message string) (RebaseOutcome, error) {
	args := append(rebaseIdentityArgs(), "merge", "--no-ff", "-m", message, ontoSHA)
	_, err := captureGit(ctx, dir, rebaseEnv(), args...)
	if err == nil {
		return RebaseOutcome{Clean: true}, nil
	}
	conflicts, listErr := g.ConflictedFiles(ctx, dir)
	if listErr == nil && len(conflicts) > 0 {
		return RebaseOutcome{Conflicts: conflicts, Stderr: err.Error()}, nil
	}
	if abortErr := g.AbortMerge(ctx, dir); abortErr != nil {
		return RebaseOutcome{Stderr: err.Error()},
			fmt.Errorf("gitops: merge %s failed (%v) and abort also failed: %w", ontoSHA, err, abortErr)
	}
	return RebaseOutcome{Stderr: err.Error()}, fmt.Errorf("gitops: merge %s: %w", ontoSHA, err)
}

// splitNUL splits git's -z output, which is NUL-separated with no trailing
// record.
//
// Every listing whose paths are fed back to the filesystem must use -z rather
// than the line-oriented default. git C-quotes any name containing a double
// quote, a backslash, a control character or a byte above ASCII, and
// core.quotePath=false only suppresses the last of those four: `café.txt` comes
// back verbatim under it, but `quo"te.txt` still lists as `"quo\"te.txt"`. A
// quoted name is a name no file has, so os.ReadFile reports it missing — and in
// the conflict-resolution scope guard that means the path hashes to the empty
// string on BOTH sides of the agent run, so changedBetween concludes nothing
// changed. That is a fail-open on exactly the file the agent touched, in the
// guard whose job is to contain a possibly prompt-injected agent.
func splitNUL(s string) []string {
	var out []string
	for _, path := range strings.Split(s, "\x00") {
		if path != "" {
			out = append(out, path)
		}
	}
	return out
}

// ConflictedFiles lists the paths git considers unmerged.
func (g *GitExec) ConflictedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := captureGit(ctx, dir, nil, "diff", "--name-only", "-z", "--diff-filter=U")
	if err != nil {
		return nil, fmt.Errorf("gitops: list conflicted files: %w", err)
	}
	return splitNUL(string(out)), nil
}

// HasUnmergedPaths reports whether any conflict remains. Cheaper and more
// direct than ConflictedFiles when only the boolean matters, and it is the
// guard that decides whether an agent's resolution is complete.
func (g *GitExec) HasUnmergedPaths(ctx context.Context, dir string) (bool, error) {
	files, err := g.ConflictedFiles(ctx, dir)
	if err != nil {
		return false, err
	}
	return len(files) > 0, nil
}

// ChangedFiles lists the paths that differ between sinceSHA and the working
// tree, including untracked files.
//
// This backs the scope guard on the conflict-resolution agent: anything the
// agent touched outside the conflicted set is out of scope, and the run is
// abandoned rather than pushed.
func (g *GitExec) ChangedFiles(ctx context.Context, dir, sinceSHA string) ([]string, error) {
	tracked, err := captureGit(ctx, dir, nil, "diff", "--name-only", "-z", sinceSHA)
	if err != nil {
		return nil, fmt.Errorf("gitops: diff --name-only %s: %w", sinceSHA, err)
	}
	untracked, err := captureGit(ctx, dir, nil,
		"ls-files", "--others", "--exclude-standard", "-z",
		"--", ".", ":(exclude)"+managedCloneMarkerFile)
	if err != nil {
		return nil, fmt.Errorf("gitops: list untracked files: %w", err)
	}
	seen := make(map[string]struct{})
	var out []string
	for _, name := range append(splitNUL(string(tracked)), splitNUL(string(untracked))...) {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, nil
}

// WorktreeDigest fingerprints every path in the worktree that differs from
// HEAD — modified, unmerged, deleted and untracked alike — as a map from path
// to a content hash. A path that is not on disk maps to the empty string, so a
// deletion is a value rather than an absence.
//
// It exists because the scope guard on the conflict-resolution agent cannot
// diff against the base commit: mid-rebase, HEAD already carries the PR's
// earlier replayed commits, so a diff against the base lists every file the PR
// touches and the guard would fire on every genuine resolution. Two digests
// taken either side of the agent run answer the question that actually
// matters — what did the agent change?
func (g *GitExec) WorktreeDigest(ctx context.Context, dir string) (map[string]string, error) {
	paths, err := g.ChangedFiles(ctx, dir, "HEAD")
	if err != nil {
		return nil, err
	}
	digest := make(map[string]string, len(paths))
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			if os.IsNotExist(err) {
				digest[rel] = ""
				continue
			}
			return nil, fmt.Errorf("gitops: read %s: %w", rel, err)
		}
		sum := sha256.Sum256(data)
		digest[rel] = hex.EncodeToString(sum[:])
	}
	return digest, nil
}

// FilesWithConflictMarkers returns the subset of paths that still contain git
// conflict markers.
//
// An agent can "resolve" a conflict by deleting one side and leaving the
// markers in place, or by editing around them. git happily stages that, and the
// result compiles in some languages, so the unmerged-path check alone is not
// enough — the file contents have to be inspected too.
func (g *GitExec) FilesWithConflictMarkers(ctx context.Context, dir string, paths []string) ([]string, error) {
	var out []string
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		full := dir + string(os.PathSeparator) + rel
		data, err := os.ReadFile(full)
		if err != nil {
			// A deleted file is a legitimate resolution of a delete/modify
			// conflict, so a missing path is not a failure here.
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("gitops: read %s: %w", rel, err)
		}
		text := string(data)
		for _, marker := range conflictMarkers {
			if strings.Contains(text, marker) {
				out = append(out, rel)
				break
			}
		}
	}
	return out, nil
}

// StageAll stages every change and enforces the sensitive-path denylist, so a
// prompt-injected agent cannot smuggle credentials into a commit. Shared with
// CommitAll, which is StageAll plus the commit itself.
func (g *GitExec) StageAll(ctx context.Context, dir string) error {
	if err := runGit(ctx, dir, nil, "add", "-A", "--",
		".", ":(exclude)"+managedCloneMarkerFile); err != nil {
		return fmt.Errorf("gitops: stage all: %w", err)
	}
	return enforceSensitivePathDenylist(ctx, dir)
}

// ContinueRebase finishes a conflicted rebase after the conflicts were staged.
func (g *GitExec) ContinueRebase(ctx context.Context, dir string) error {
	args := append(rebaseIdentityArgs(), "rebase", "--continue")
	if err := runGit(ctx, dir, rebaseEnv(), args...); err != nil {
		return fmt.Errorf("gitops: rebase --continue: %w", err)
	}
	return nil
}

// CommitMerge finishes a conflicted merge after the conflicts were staged.
func (g *GitExec) CommitMerge(ctx context.Context, dir, message string) error {
	args := append(rebaseIdentityArgs(), "commit", "--no-verify", "-m", message)
	if err := runGit(ctx, dir, rebaseEnv(), args...); err != nil {
		return fmt.Errorf("gitops: commit merge: %w", err)
	}
	return nil
}

// AbortRebase returns the tree to its pre-rebase state. Safe to call when no
// rebase is in progress: git's failure is reported but callers treat it as
// advisory.
func (g *GitExec) AbortRebase(ctx context.Context, dir string) error {
	if err := runGit(ctx, dir, rebaseEnv(), "rebase", "--abort"); err != nil {
		return fmt.Errorf("gitops: rebase --abort: %w", err)
	}
	return nil
}

// AbortMerge returns the tree to its pre-merge state.
func (g *GitExec) AbortMerge(ctx context.Context, dir string) error {
	if err := runGit(ctx, dir, rebaseEnv(), "merge", "--abort"); err != nil {
		return fmt.Errorf("gitops: merge --abort: %w", err)
	}
	return nil
}

// PushForceWithLease force-pushes branch, but only if the remote still points
// at expectedRemoteSHA.
//
// The lease is spelled out explicitly (`--force-with-lease=<branch>:<sha>`)
// rather than relying on the bare form. The bare form compares against the
// local remote-tracking ref, which a stale or missing fetch can make wrong —
// and being wrong here means overwriting somebody's commits. With the SHA
// named, git refuses unless the remote is exactly where we last saw it.
func (g *GitExec) PushForceWithLease(ctx context.Context, dir, repo, branch, expectedRemoteSHA, token string) error {
	if token == "" {
		return fmt.Errorf("gitops: force-push requires a non-empty token")
	}
	if strings.TrimSpace(expectedRemoteSHA) == "" {
		return fmt.Errorf("gitops: force-push requires an expected remote sha")
	}
	env, cleanup, err := buildAskPassEnv(token)
	if err != nil {
		return fmt.Errorf("gitops: setup askpass for force-push: %w", err)
	}
	defer cleanup()

	url := fmt.Sprintf("https://x-access-token@github.com/%s.git", repo)
	lease := fmt.Sprintf("--force-with-lease=%s:%s", branch, expectedRemoteSHA)
	if err := runGit(ctx, dir, env, "push", lease, url, branch+":"+branch); err != nil {
		return fmt.Errorf("gitops: force-push %s:%s: %w", repo, branch, err)
	}
	return nil
}

func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
