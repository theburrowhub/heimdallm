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

// requireGit skips when the git binary is absent, matching the convention in
// gitops_test.go. The pinned alpine test image has no git; CI and developer
// machines do, so these run there.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

// These tests drive real git repositories in t.TempDir(). Rebase and
// conflict-marker behaviour is exactly the kind of thing a fake gets subtly
// wrong, and getting it wrong here means force-pushing a broken tree.

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// diverged builds a repo where `feature` and `main` both changed the same file,
// which is what git needs to produce a conflict.
func diverged(t *testing.T) (dir, mainSHA string) {
	t.Helper()
	requireGit(t)
	dir = t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "shared.txt", "base\n")
	write(t, dir, "other.txt", "untouched\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	git(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "shared.txt", "feature change\n")
	git(t, dir, "commit", "-qam", "feature")

	git(t, dir, "checkout", "-q", "main")
	write(t, dir, "shared.txt", "main change\n")
	git(t, dir, "commit", "-qam", "main")
	mainSHA = git(t, dir, "rev-parse", "HEAD")

	git(t, dir, "checkout", "-q", "feature")
	return dir, mainSHA
}

func TestRebaseOnto_CleanRebaseReportsClean(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "base\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	git(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "feature.txt", "new\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "feature")

	git(t, dir, "checkout", "-q", "main")
	write(t, dir, "main.txt", "new\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "main")
	mainSHA := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "feature")

	g := issues.NewGitExec()
	out, err := g.RebaseOnto(context.Background(), dir, mainSHA)
	if err != nil {
		t.Fatalf("RebaseOnto: %v", err)
	}
	if !out.Clean {
		t.Fatalf("expected a clean rebase, got %+v", out)
	}
	// The feature commit should now sit on top of main.
	if parent := git(t, dir, "rev-parse", "HEAD~1"); parent != mainSHA {
		t.Errorf("HEAD~1 = %s, want main %s", parent, mainSHA)
	}
}

func TestRebaseOnto_ConflictIsReportedNotRaised(t *testing.T) {
	requireGit(t)
	dir, mainSHA := diverged(t)
	g := issues.NewGitExec()

	out, err := g.RebaseOnto(context.Background(), dir, mainSHA)
	// A conflict is the expected outcome, not a failure: the caller wants the
	// file list so the agent can resolve it.
	if err != nil {
		t.Fatalf("a conflicting rebase must not return an error: %v", err)
	}
	if out.Clean {
		t.Fatal("expected a conflict")
	}
	if len(out.Conflicts) != 1 || out.Conflicts[0] != "shared.txt" {
		t.Fatalf("conflicts = %v, want [shared.txt]", out.Conflicts)
	}

	unmerged, err := g.HasUnmergedPaths(context.Background(), dir)
	if err != nil {
		t.Fatalf("HasUnmergedPaths: %v", err)
	}
	if !unmerged {
		t.Error("the tree should have unmerged paths mid-rebase")
	}
}

// The markers git writes into the file are what the agent has to remove; a
// resolution that leaves them behind must be detectable.
func TestFilesWithConflictMarkers_DetectsAndClearsCorrectly(t *testing.T) {
	requireGit(t)
	dir, mainSHA := diverged(t)
	g := issues.NewGitExec()
	out, err := g.RebaseOnto(context.Background(), dir, mainSHA)
	if err != nil || out.Clean {
		t.Fatalf("expected a conflict: %+v %v", out, err)
	}

	withMarkers, err := g.FilesWithConflictMarkers(context.Background(), dir, out.Conflicts)
	if err != nil {
		t.Fatalf("FilesWithConflictMarkers: %v", err)
	}
	if len(withMarkers) != 1 || withMarkers[0] != "shared.txt" {
		t.Fatalf("with markers = %v, want [shared.txt]", withMarkers)
	}

	// Resolve properly and the detector must go quiet.
	write(t, dir, "shared.txt", "main change\nfeature change\n")
	withMarkers, err = g.FilesWithConflictMarkers(context.Background(), dir, out.Conflicts)
	if err != nil {
		t.Fatalf("FilesWithConflictMarkers: %v", err)
	}
	if len(withMarkers) != 0 {
		t.Errorf("with markers = %v, want none after a real resolution", withMarkers)
	}
}

// The full happy path: conflict, resolve, stage, continue.
func TestStageAllAndContinueRebase_FinishesTheRebase(t *testing.T) {
	requireGit(t)
	dir, mainSHA := diverged(t)
	g := issues.NewGitExec()
	ctx := context.Background()

	out, err := g.RebaseOnto(ctx, dir, mainSHA)
	if err != nil || out.Clean {
		t.Fatalf("expected a conflict: %+v %v", out, err)
	}
	write(t, dir, "shared.txt", "main change\nfeature change\n")

	if err := g.StageAll(ctx, dir); err != nil {
		t.Fatalf("StageAll: %v", err)
	}
	if err := g.ContinueRebase(ctx, dir); err != nil {
		t.Fatalf("ContinueRebase: %v", err)
	}

	unmerged, err := g.HasUnmergedPaths(ctx, dir)
	if err != nil {
		t.Fatalf("HasUnmergedPaths: %v", err)
	}
	if unmerged {
		t.Error("no unmerged paths should remain after continuing")
	}
	if parent := git(t, dir, "rev-parse", "HEAD~1"); parent != mainSHA {
		t.Errorf("HEAD~1 = %s, want main %s", parent, mainSHA)
	}
	// A rebase preserves the original author and re-commits under the
	// committer identity, so it is the committer that must be the daemon.
	if committer := git(t, dir, "log", "-1", "--format=%cn"); committer != issues.CommitAuthorName {
		t.Errorf("committer = %q, want %q", committer, issues.CommitAuthorName)
	}
}

func TestAbortRebase_RestoresTheBranch(t *testing.T) {
	requireGit(t)
	dir, mainSHA := diverged(t)
	g := issues.NewGitExec()
	ctx := context.Background()

	before := git(t, dir, "rev-parse", "HEAD")
	if _, err := g.RebaseOnto(ctx, dir, mainSHA); err != nil {
		t.Fatalf("RebaseOnto: %v", err)
	}
	if err := g.AbortRebase(ctx, dir); err != nil {
		t.Fatalf("AbortRebase: %v", err)
	}
	if after := git(t, dir, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD = %s after abort, want the original %s", after, before)
	}
	unmerged, err := g.HasUnmergedPaths(ctx, dir)
	if err != nil {
		t.Fatalf("HasUnmergedPaths: %v", err)
	}
	if unmerged {
		t.Error("abort must leave a clean tree")
	}
}

// The scope guard depends on this: anything outside the conflicted set means
// the agent did something it was not asked to.
func TestChangedFiles_IncludesEditsAndUntrackedFiles(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	baseSHA := git(t, dir, "rev-parse", "HEAD")

	write(t, dir, "a.txt", "changed\n")
	write(t, dir, "sneaky.txt", "new file\n")

	g := issues.NewGitExec()
	changed, err := g.ChangedFiles(context.Background(), dir, baseSHA)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	got := map[string]bool{}
	for _, f := range changed {
		got[f] = true
	}
	if !got["a.txt"] {
		t.Error("an edited tracked file must be reported")
	}
	if !got["sneaky.txt"] {
		t.Error("an untracked file must be reported — otherwise the scope guard misses new files")
	}
}

func TestChangedFiles_IgnoresTheManagedCloneMarker(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	baseSHA := git(t, dir, "rev-parse", "HEAD")

	write(t, dir, ".heimdallm-managed", "repoctx metadata\n")

	g := issues.NewGitExec()
	changed, err := g.ChangedFiles(context.Background(), dir, baseSHA)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	for _, f := range changed {
		if f == ".heimdallm-managed" {
			t.Error("the repoctx marker is our own metadata and must not count as an agent change")
		}
	}
}

// leaseRepos builds a bare remote plus a local clone, and rewrites the GitHub
// URL that PushForceWithLease constructs so it resolves to the local bare repo.
//
// Without the rewrite the push fails on DNS, and a test asserting "the push was
// refused" would pass without ever exercising the lease — which is the entire
// safety property being claimed here.
func leaseRepos(t *testing.T) (remote, local, repoSlug string) {
	t.Helper()
	requireGit(t)
	remote = t.TempDir()
	git(t, remote, "init", "-q", "--bare", "-b", "main")

	repoSlug = "acme/widgets"
	local = t.TempDir()
	git(t, local, "init", "-q", "-b", "main")
	git(t, local, "config",
		"url."+remote+".insteadOf",
		"https://x-access-token@github.com/"+repoSlug+".git")

	write(t, local, "a.txt", "one\n")
	git(t, local, "add", ".")
	git(t, local, "commit", "-q", "-m", "base")
	git(t, local, "push", "-q", remote, "main:main")
	return remote, local, repoSlug
}

// The lease is the whole safety property of the force-push: if the remote moved
// since we looked, git must refuse rather than overwrite someone's commits.
func TestPushForceWithLease_RefusesWhenTheRemoteMoved(t *testing.T) {
	requireGit(t)
	remote, local, slug := leaseRepos(t)
	observed := git(t, local, "rev-parse", "HEAD")

	// Someone else pushes to the remote after we observed it.
	other := t.TempDir()
	git(t, other, "clone", "-q", remote, ".")
	write(t, other, "b.txt", "theirs\n")
	git(t, other, "add", ".")
	git(t, other, "commit", "-q", "-m", "theirs")
	git(t, other, "push", "-q", "origin", "main")
	theirs := git(t, other, "rev-parse", "HEAD")

	// We rewrite history on top of the stale base.
	write(t, local, "a.txt", "ours\n")
	git(t, local, "commit", "-qam", "ours")

	g := issues.NewGitExec()
	err := g.PushForceWithLease(context.Background(), local, slug, "main", observed, "token")
	if err == nil {
		t.Fatal("the force-push must be refused: the remote moved since we looked")
	}
	if !strings.Contains(err.Error(), "stale info") && !strings.Contains(err.Error(), "rejected") {
		t.Errorf("err = %v, want a lease rejection rather than an unrelated failure", err)
	}
	if head := git(t, remote, "rev-parse", "main"); head != theirs {
		t.Errorf("remote main = %s, want the other person's commit %s left intact", head, theirs)
	}
}

func TestPushForceWithLease_SucceedsWhenTheRemoteIsWhereWeLeftIt(t *testing.T) {
	requireGit(t)
	remote, local, slug := leaseRepos(t)
	observed := git(t, local, "rev-parse", "HEAD")

	// Rewrite history locally, as a rebase would.
	write(t, local, "a.txt", "rewritten\n")
	git(t, local, "commit", "-qam", "rewritten", "--amend")

	g := issues.NewGitExec()
	if err := g.PushForceWithLease(context.Background(), local, slug, "main", observed, "token"); err != nil {
		t.Fatalf("PushForceWithLease: %v", err)
	}
	if got, want := git(t, remote, "rev-parse", "main"), git(t, local, "rev-parse", "HEAD"); got != want {
		t.Errorf("remote main = %s, want %s", got, want)
	}
}

func TestPushForceWithLease_RequiresTokenAndLease(t *testing.T) {
	requireGit(t)
	g := issues.NewGitExec()
	ctx := context.Background()
	if err := g.PushForceWithLease(ctx, t.TempDir(), "acme/widgets", "main", "sha", ""); err == nil {
		t.Error("an empty token must be rejected")
	}
	if err := g.PushForceWithLease(ctx, t.TempDir(), "acme/widgets", "main", "", "token"); err == nil {
		t.Error("an empty lease must be rejected — it would degrade to a plain force push")
	}
}

func TestHeadSHA_ReturnsTheCurrentCommit(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	g := issues.NewGitExec()
	got, err := g.HeadSHA(context.Background(), dir)
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if want := git(t, dir, "rev-parse", "HEAD"); got != want {
		t.Errorf("HeadSHA = %q, want %q", got, want)
	}
}

// StageAll shares CommitAll's denylist, so a prompt-injected agent cannot
// smuggle a secret in through the conflict-resolution path either.
func TestStageAll_RefusesSensitivePaths(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	write(t, dir, "id_rsa", "-----BEGIN PRIVATE KEY-----\n")

	g := issues.NewGitExec()
	err := g.StageAll(context.Background(), dir)
	if err == nil {
		t.Fatal("staging a private key must be refused")
	}
	if !strings.Contains(err.Error(), "denylist") {
		t.Errorf("err = %v, want the denylist refusal", err)
	}
}

func TestFetchRefAndCheckoutRemoteBranch(t *testing.T) {
	requireGit(t)
	remote, local, slug := leaseRepos(t)
	// Add a second branch on the remote for the checkout to land on.
	other := t.TempDir()
	git(t, other, "clone", "-q", remote, ".")
	git(t, other, "checkout", "-q", "-b", "feature")
	write(t, other, "feature.txt", "theirs\n")
	git(t, other, "add", ".")
	git(t, other, "commit", "-q", "-m", "feature")
	git(t, other, "push", "-q", "origin", "feature")
	want := git(t, other, "rev-parse", "HEAD")

	g := issues.NewGitExec()
	ctx := context.Background()

	sha, err := g.FetchRef(ctx, local, slug, "feature", "token")
	if err != nil {
		t.Fatalf("FetchRef: %v", err)
	}
	if sha != want {
		t.Errorf("FetchRef = %q, want %q", sha, want)
	}

	got, err := g.CheckoutRemoteBranch(ctx, local, slug, "feature", "token")
	if err != nil {
		t.Fatalf("CheckoutRemoteBranch: %v", err)
	}
	if got != want {
		t.Errorf("CheckoutRemoteBranch = %q, want %q", got, want)
	}
	if branch := git(t, local, "rev-parse", "--abbrev-ref", "HEAD"); branch != "feature" {
		t.Errorf("checked out %q, want feature", branch)
	}
	// -B, not -b: a retry must land on the freshly fetched tip rather than
	// inheriting stale local state.
	if head := git(t, local, "rev-parse", "HEAD"); head != want {
		t.Errorf("HEAD = %q, want the fetched tip %q", head, want)
	}
}

func TestFetchRefAndCheckoutRemoteBranch_RequireAToken(t *testing.T) {
	requireGit(t)
	g := issues.NewGitExec()
	ctx := context.Background()
	if _, err := g.FetchRef(ctx, t.TempDir(), "acme/widgets", "main", ""); err == nil {
		t.Error("an empty token must be rejected")
	}
	if _, err := g.CheckoutRemoteBranch(ctx, t.TempDir(), "acme/widgets", "main", ""); err == nil {
		t.Error("an empty token must be rejected")
	}
}

func TestFetchRef_UnknownRefIsReported(t *testing.T) {
	requireGit(t)
	_, local, slug := leaseRepos(t)
	g := issues.NewGitExec()
	if _, err := g.FetchRef(context.Background(), local, slug, "no-such-branch", "token"); err == nil {
		t.Fatal("fetching an unknown ref must be reported")
	}
}

func TestMergeRef_CleanAndConflicting(t *testing.T) {
	requireGit(t)
	g := issues.NewGitExec()
	ctx := context.Background()

	t.Run("clean", func(t *testing.T) {
		dir := t.TempDir()
		git(t, dir, "init", "-q", "-b", "main")
		write(t, dir, "a.txt", "base\n")
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-q", "-m", "base")
		git(t, dir, "checkout", "-q", "-b", "feature")
		write(t, dir, "feature.txt", "new\n")
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-q", "-m", "feature")
		git(t, dir, "checkout", "-q", "main")
		write(t, dir, "main.txt", "new\n")
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-q", "-m", "main")
		mainSHA := git(t, dir, "rev-parse", "HEAD")
		git(t, dir, "checkout", "-q", "feature")

		out, err := g.MergeRef(ctx, dir, mainSHA, "merge main")
		if err != nil {
			t.Fatalf("MergeRef: %v", err)
		}
		if !out.Clean {
			t.Fatalf("expected a clean merge, got %+v", out)
		}
	})

	t.Run("conflicting", func(t *testing.T) {
		dir, mainSHA := diverged(t)
		out, err := g.MergeRef(ctx, dir, mainSHA, "merge main")
		if err != nil {
			t.Fatalf("a conflicting merge must not return an error: %v", err)
		}
		if out.Clean || len(out.Conflicts) != 1 || out.Conflicts[0] != "shared.txt" {
			t.Fatalf("outcome = %+v, want a conflict on shared.txt", out)
		}

		// Resolve and finish, the path the resolver takes for a merge strategy.
		write(t, dir, "shared.txt", "both\n")
		if err := g.StageAll(ctx, dir); err != nil {
			t.Fatalf("StageAll: %v", err)
		}
		if err := g.CommitMerge(ctx, dir, "resolve"); err != nil {
			t.Fatalf("CommitMerge: %v", err)
		}
		if unmerged, err := g.HasUnmergedPaths(ctx, dir); err != nil || unmerged {
			t.Errorf("unmerged=%v err=%v after committing the merge", unmerged, err)
		}
		if committer := git(t, dir, "log", "-1", "--format=%cn"); committer != issues.CommitAuthorName {
			t.Errorf("committer = %q, want %q", committer, issues.CommitAuthorName)
		}
	})
}

func TestAbortMerge_RestoresTheBranch(t *testing.T) {
	requireGit(t)
	dir, mainSHA := diverged(t)
	g := issues.NewGitExec()
	ctx := context.Background()

	before := git(t, dir, "rev-parse", "HEAD")
	if _, err := g.MergeRef(ctx, dir, mainSHA, "merge main"); err != nil {
		t.Fatalf("MergeRef: %v", err)
	}
	if err := g.AbortMerge(ctx, dir); err != nil {
		t.Fatalf("AbortMerge: %v", err)
	}
	if after := git(t, dir, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD = %s after abort, want %s", after, before)
	}
}

// A deleted file is a legitimate resolution of a delete/modify conflict, so a
// missing path must not fail the scan.
func TestFilesWithConflictMarkers_ToleratesMissingFiles(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	g := issues.NewGitExec()
	got, err := g.FilesWithConflictMarkers(context.Background(), dir, []string{"gone.txt", "a.txt"})
	if err != nil {
		t.Fatalf("FilesWithConflictMarkers: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("with markers = %v, want none", got)
	}
}

func TestConflictedFiles_EmptyOnACleanTree(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	g := issues.NewGitExec()
	files, err := g.ConflictedFiles(context.Background(), dir)
	if err != nil {
		t.Fatalf("ConflictedFiles: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("conflicted files = %v, want none", files)
	}
}

// A non-conflict rebase failure must leave no half-finished rebase for the next
// caller to trip over.
func TestRebaseOnto_UnknownBaseFailsAndLeavesNoRebaseInProgress(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	g := issues.NewGitExec()
	out, err := g.RebaseOnto(context.Background(), dir, "0000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("rebasing onto a nonexistent commit must fail")
	}
	if out.Clean {
		t.Error("a failed rebase is not clean")
	}
	if unmerged, uErr := g.HasUnmergedPaths(context.Background(), dir); uErr != nil || unmerged {
		t.Errorf("tree left dirty: unmerged=%v err=%v", unmerged, uErr)
	}
}

// The scope guard on the conflict-resolution agent used to diff the mid-rebase
// working tree against the BASE commit. Mid-rebase, HEAD already carries the
// PR's earlier replayed commits, so that diff lists every file the PR touches
// and the guard fired on every genuine resolution: the agent's one legitimate
// job — edit the conflicted file — was reported as "changed files outside the
// conflicted set" before it had touched anything.
//
// WorktreeDigest answers the question that actually matters, so a snapshot
// either side of the agent run isolates what the agent did.
func TestWorktreeDigest_ExcludesCommitsAlreadyReplayedByTheRebase(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "shared.txt", "base\n")
	write(t, dir, "carried.txt", "base\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	// A two-commit PR: the first commit is replayed cleanly, the second
	// conflicts. This is the ordinary shape of a PR, not a corner case.
	git(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "carried.txt", "feature\n")
	git(t, dir, "commit", "-qam", "carry")
	write(t, dir, "shared.txt", "feature\n")
	git(t, dir, "commit", "-qam", "conflict")

	git(t, dir, "checkout", "-q", "main")
	write(t, dir, "shared.txt", "main\n")
	git(t, dir, "commit", "-qam", "main moves")
	mainSHA := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "feature")

	ctx := context.Background()
	g := issues.NewGitExec()
	out, err := g.RebaseOnto(ctx, dir, mainSHA)
	if err != nil {
		t.Fatalf("RebaseOnto: %v", err)
	}
	if out.Clean {
		t.Fatal("expected the second commit to conflict")
	}

	// The old guard's view: carried.txt is in here purely because the rebase
	// replayed it, and the agent has not run yet.
	changed, err := g.ChangedFiles(ctx, dir, mainSHA)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if !contains(changed, "carried.txt") {
		t.Fatalf("precondition: a base diff should list the replayed file, got %v", changed)
	}

	before, err := g.WorktreeDigest(ctx, dir)
	if err != nil {
		t.Fatalf("WorktreeDigest before: %v", err)
	}
	if _, ok := before["carried.txt"]; ok {
		t.Errorf("a commit the rebase already replayed is not a pending change: %v", before)
	}
	if _, ok := before["shared.txt"]; !ok {
		t.Errorf("the conflicted file must be in the snapshot: %v", before)
	}

	// The agent resolves the conflict and touches nothing else.
	write(t, dir, "shared.txt", "resolved\n")
	after, err := g.WorktreeDigest(ctx, dir)
	if err != nil {
		t.Fatalf("WorktreeDigest after: %v", err)
	}
	if after["shared.txt"] == before["shared.txt"] {
		t.Error("the resolved file must hash differently")
	}
	for path, sum := range after {
		if path != "shared.txt" && before[path] != sum {
			t.Errorf("%s changed but the agent never touched it", path)
		}
	}
}

// A file the agent edits behind the guard's back has to show up even when the
// set of changed paths is unchanged — hence content hashes rather than names.
func TestWorktreeDigest_NoticesAnEditThatChangesNoPathNames(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "base\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	write(t, dir, "a.txt", "edited\n")

	ctx := context.Background()
	g := issues.NewGitExec()
	before, err := g.WorktreeDigest(ctx, dir)
	if err != nil {
		t.Fatalf("WorktreeDigest: %v", err)
	}
	write(t, dir, "a.txt", "edited again\n")
	after, err := g.WorktreeDigest(ctx, dir)
	if err != nil {
		t.Fatalf("WorktreeDigest: %v", err)
	}
	if before["a.txt"] == after["a.txt"] {
		t.Error("a content edit must change the digest even though the path set did not")
	}
}

// A deletion is a change, and a path that is simply gone from the map would
// read as "nothing happened here".
func TestWorktreeDigest_RecordsDeletionsAsAValue(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "base\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	digest, err := issues.NewGitExec().WorktreeDigest(context.Background(), dir)
	if err != nil {
		t.Fatalf("WorktreeDigest: %v", err)
	}
	sum, ok := digest["a.txt"]
	if !ok {
		t.Fatalf("a deleted path must be present in the digest, got %v", digest)
	}
	if sum != "" {
		t.Errorf("a deleted path hashes to the empty string, got %q", sum)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// git C-quotes anything outside ASCII by default, so `café.txt` comes back from
// a listing as `"caf\303\251.txt"` — a name no file has. WorktreeDigest then
// read it as missing, mapped it to the empty string in BOTH snapshots, and
// changedBetween concluded nothing had changed: an agent edit to any
// non-ASCII-named file sailed past the scope guard and got pushed.
func TestWorktreeDigest_SeesNonASCIIPaths(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "café.txt", "base\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	write(t, dir, "café.txt", "edited\n")

	ctx := context.Background()
	g := issues.NewGitExec()
	before, err := g.WorktreeDigest(ctx, dir)
	if err != nil {
		t.Fatalf("WorktreeDigest: %v", err)
	}
	sum, ok := before["café.txt"]
	if !ok {
		t.Fatalf("the path must be reported verbatim, got %v", before)
	}
	if sum == "" {
		t.Fatal("an existing file must hash to something: the digest read a quoted name")
	}

	// A newly created one, from the untracked listing.
	write(t, dir, "niño.txt", "new\n")
	after, err := g.WorktreeDigest(ctx, dir)
	if err != nil {
		t.Fatalf("WorktreeDigest: %v", err)
	}
	if got, ok := after["niño.txt"]; !ok || got == "" {
		t.Errorf("an untracked non-ASCII file must be seen, got %v", after)
	}
}

// The conflicted-file list is handed to the agent and to the marker scan, so a
// quoted name there turns a resolvable conflict into a read failure.
func TestConflictedFiles_ReportsNonASCIIPathsVerbatim(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "café.txt", "base\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	git(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "café.txt", "feature\n")
	git(t, dir, "commit", "-qam", "feature")
	git(t, dir, "checkout", "-q", "main")
	write(t, dir, "café.txt", "main\n")
	git(t, dir, "commit", "-qam", "main")
	mainSHA := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "feature")

	ctx := context.Background()
	g := issues.NewGitExec()
	if _, err := g.RebaseOnto(ctx, dir, mainSHA); err != nil {
		t.Fatalf("RebaseOnto: %v", err)
	}
	conflicts, err := g.ConflictedFiles(ctx, dir)
	if err != nil {
		t.Fatalf("ConflictedFiles: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0] != "café.txt" {
		t.Fatalf("conflicts = %v, want [café.txt]", conflicts)
	}
	// The marker scan reads the paths straight off the list.
	withMarkers, err := g.FilesWithConflictMarkers(ctx, dir, conflicts)
	if err != nil {
		t.Fatalf("FilesWithConflictMarkers: %v", err)
	}
	if len(withMarkers) != 1 {
		t.Errorf("markers = %v, want the conflicted file", withMarkers)
	}
}
