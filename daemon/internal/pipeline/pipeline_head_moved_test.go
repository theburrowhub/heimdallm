package pipeline_test

import (
	"errors"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/store"
)

// headMoveGH lets a test change the SHA reported by GetPRHeadSHA between the
// hydration call at the top of Run and the freshness re-check in step 8, which
// is how a force-push mid-review is simulated. Every write to the PR is
// counted so a test can assert that NOTHING was posted, not merely that the
// summary review was withheld.
type headMoveGH struct {
	diff string
	// shaSequence is returned one entry per GetPRHeadSHA call; the last entry
	// is repeated once exhausted. Encodes "HEAD was X, now it is Y".
	shaSequence []string
	shaErr      error

	shaCalls int
	submits  int
	comments int
	// lastEvent records the event of the last submitted review so a test can
	// prove a REQUEST_CHANGES never reached GitHub.
	lastEvent string
}

func (f *headMoveGH) FetchDiff(repo string, number int) (string, error) { return f.diff, nil }

func (f *headMoveGH) SubmitReview(repo string, number int, body, event string) (int64, string, error) {
	f.submits++
	f.lastEvent = event
	return 4862165122, "CHANGES_REQUESTED", nil
}

// Commit-anchored pair required by the client interface after the isolation
// refactor. This double deliberately does NOT implement PRSnapshotResolver, so
// these tests keep covering the retireIfPRMoved fallback — the HEAD-only
// freshness path for clients without a snapshot resolver.
func (f *headMoveGH) FetchDiffForCommit(repo string, number int, _ string) (string, error) {
	return f.FetchDiff(repo, number)
}

func (f *headMoveGH) SubmitReviewForCommit(repo string, number int, body, event, _ string) (int64, string, error) {
	return f.SubmitReview(repo, number, body, event)
}

func (f *headMoveGH) PostComment(repo string, number int, body string) (time.Time, error) {
	f.comments++
	return time.Now().UTC(), nil
}

func (f *headMoveGH) FetchComments(repo string, number int) ([]github.Comment, error) {
	return nil, nil
}

func (f *headMoveGH) GetPRHeadSHA(repo string, number int) (string, error) {
	f.shaCalls++
	if f.shaErr != nil {
		return "", f.shaErr
	}
	if len(f.shaSequence) == 0 {
		return "", nil
	}
	i := f.shaCalls - 1
	if i >= len(f.shaSequence) {
		i = len(f.shaSequence) - 1
	}
	return f.shaSequence[i], nil
}

// issuesExec returns a finding so ReviewEvent resolves to REQUEST_CHANGES and
// multi mode has something to post per issue.
type issuesExec struct{}

func (issuesExec) Detect(primary, fallback string) (string, error) { return "fake_claude", nil }

func (issuesExec) Execute(cli, prompt string, _ executor.ExecOptions) (*executor.ReviewResult, error) {
	return &executor.ReviewResult{
		Summary: "Needs work",
		Issues: []executor.Issue{
			{File: "main.go", Line: 1, Description: "unsafe", Severity: "high"},
			{File: "other.go", Line: 9, Description: "also unsafe", Severity: "high"},
		},
		Severity: "high",
	}, nil
}

func headMovePR(sha string) *github.PullRequest {
	pr := &github.PullRequest{
		ID: 1802, Number: 1802, Title: "force-pushed a lot", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/1802",
	}
	pr.Head.SHA = sha
	return pr
}

// TestPipeline_Run_RetiresReviewWhenHeadMovedDuringRun is the regression test
// for theburrowhub/heimdallm#664. The run analyses 306bfcc7; by the time it is
// ready to publish, HEAD is 1b284050. The verdict must not be published, and
// the stored row must be retired with the Superseded sentinel so the
// publish-worker's ListUnpublishedReviews (github_review_id == 0) does not pick
// it up and post it later anyway.
func TestPipeline_Run_RetiresReviewWhenHeadMovedDuringRun(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	gh := &headMoveGH{diff: "+line", shaSequence: []string{"1b284050"}}
	p := pipeline.New(s, gh, issuesExec{}, &fakeNotify{})

	pr := headMovePR("306bfcc7")
	rev, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini", Force: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Same contract as the other skip paths: a nil review keeps main.go from
	// emitting review_completed / the activity row for work never published.
	if rev != nil {
		t.Errorf("expected nil review for a superseded run, got %+v", rev)
	}
	if gh.submits != 0 {
		t.Errorf("SubmitReview called %d times on a superseded commit; want 0 (event=%q)",
			gh.submits, gh.lastEvent)
	}

	// The reviews row is keyed on the internal PR id, not the GitHub id.
	storedPR, err := s.GetPRByGithubID(pr.ID)
	if err != nil {
		t.Fatalf("get pr: %v", err)
	}
	stored, err := s.LatestReviewForPR(storedPR.ID)
	if err != nil {
		t.Fatalf("latest review: %v", err)
	}
	if stored == nil {
		t.Fatal("expected the analysed review to remain stored for auditing")
	}
	if stored.GitHubReviewID != pipeline.SupersededReviewID {
		t.Errorf("stored github_review_id = %d, want SupersededReviewID (%d) so the "+
			"publish-worker stops retrying it",
			stored.GitHubReviewID, pipeline.SupersededReviewID)
	}
	if stored.HeadSHA != "306bfcc7" {
		t.Errorf("stored head_sha = %q, want the analysed SHA %q", stored.HeadSHA, "306bfcc7")
	}
}

// TestPipeline_Run_SkipsPerIssueCommentsWhenHeadMoved pins the placement of the
// guard. In multi mode the per-issue comment loop runs BEFORE SubmitReview, so a
// check sitting immediately before SubmitReview would still leave one comment
// per finding attached to superseded code — with no summary review to explain
// them. The guard must run ahead of the whole publish block.
func TestPipeline_Run_SkipsPerIssueCommentsWhenHeadMoved(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	gh := &headMoveGH{diff: "+line", shaSequence: []string{"1b284050"}}
	p := pipeline.New(s, gh, issuesExec{}, &fakeNotify{})

	if _, err := p.Run(headMovePR("306bfcc7"), pipeline.RunOptions{
		Primary: "claude", Fallback: "gemini", ReviewMode: "multi", Force: true,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gh.comments != 0 {
		t.Errorf("PostComment called %d times on a superseded commit; want 0 — the "+
			"guard must precede the multi-mode comment loop, not just SubmitReview",
			gh.comments)
	}
	if gh.submits != 0 {
		t.Errorf("SubmitReview called %d times; want 0", gh.submits)
	}
}

// TestPipeline_Run_PublishesWhenHeadUnchanged is the control: the common case
// must be untouched, and the re-check must cost exactly one extra API call.
func TestPipeline_Run_PublishesWhenHeadUnchanged(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	gh := &headMoveGH{diff: "+line", shaSequence: []string{"306bfcc7"}}
	p := pipeline.New(s, gh, issuesExec{}, &fakeNotify{})

	rev, err := p.Run(headMovePR("306bfcc7"), pipeline.RunOptions{Primary: "claude", Fallback: "gemini", Force: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev == nil {
		t.Fatal("expected a published review when HEAD did not move")
	}
	if gh.submits != 1 {
		t.Errorf("SubmitReview calls = %d, want 1", gh.submits)
	}
	// The PR arrives with Head.SHA already set, so hydration is skipped. Three
	// calls remain, and pinning the number keeps the API cost of the isolation
	// refactor visible for a client WITHOUT a snapshot resolver, which is the
	// worst case:
	//   1. the pre-publish HEAD re-check;
	//   2. the post-execute revalidation this branch adds before storing;
	//   3. retireIfPRMoved's freshness lookup, reached only because this double
	//      is not a PRSnapshotResolver — real clients are, so production pays
	//      one GetPRSnapshot here instead of calls 1 and 3.
	// If this number grows, a guard was added without accounting for its cost.
	if gh.shaCalls != 3 {
		t.Errorf("GetPRHeadSHA calls = %d, want 3 (pre-publish + post-execute + retireIfPRMoved fallback)", gh.shaCalls)
	}
}

// TestPipeline_Run_DefersWhenRevisionRecheckFails records the posture flip this
// branch makes deliberately.
//
// Before the isolation refactor this path was fail-OPEN: an unresolvable HEAD
// published anyway, on the reasoning that deferring would delay every publish
// during an API blip and that a lifecycle state for "analysed but unpublished"
// did not exist. Both halves of that reasoning changed here. The revision
// re-check is no longer advisory — it is what makes a commit-anchored review
// honest — and the deferred publish path this branch hardens IS that lifecycle
// state: the row persists with GitHubReviewID == 0, so the publish-worker picks
// it up and re-validates under its own claim.
//
// So an unresolvable revision now returns an error rather than publishing. The
// review is not lost; it is retried by a path that will re-check before
// submitting. Publishing findings we cannot prove still describe HEAD is the
// one outcome this PR exists to prevent.
func TestPipeline_Run_DefersWhenRevisionRecheckFails(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	gh := &headMoveGH{diff: "+line", shaErr: errors.New("502 bad gateway")}
	p := pipeline.New(s, gh, issuesExec{}, &fakeNotify{})

	_, err = p.Run(headMovePR("306bfcc7"), pipeline.RunOptions{Primary: "claude", Fallback: "gemini", Force: true})
	if err == nil {
		t.Fatal("expected an error when the revision re-check could not resolve, so the " +
			"caller requeues instead of publishing an unverifiable review")
	}
	if gh.submits != 0 {
		t.Errorf("SubmitReview calls = %d, want 0 — nothing may reach GitHub while the "+
			"analysed revision is unverifiable", gh.submits)
	}
}

// TestPipeline_Run_NoRecheckWhenRunSkipsEarly guards the API budget: a run that
// short-circuits before the CLI executes must not pay for the publish-time
// re-check, because it never reaches the publish block.
func TestPipeline_Run_NoRecheckWhenRunSkipsEarly(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	gh := &headMoveGH{diff: "+line", shaSequence: []string{"306bfcc7"}}
	p := pipeline.New(s, gh, issuesExec{}, &fakeNotify{})
	pr := headMovePR("306bfcc7")

	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini", Force: true}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	callsAfterPublish := gh.shaCalls

	// Second run on the same SHA with no re-request: the dedup gate skips it
	// before the CLI runs, so no further HEAD resolution should happen.
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini", Force: true}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if gh.shaCalls != callsAfterPublish {
		t.Errorf("GetPRHeadSHA calls grew from %d to %d on a run that skipped before "+
			"executing; the re-check must not run outside the publish block",
			callsAfterPublish, gh.shaCalls)
	}
	if gh.submits != 1 {
		t.Errorf("SubmitReview calls = %d, want 1", gh.submits)
	}
}

// snapshotGH implements the optional PRSnapshotFetcher and CommitAnchoredReviewer
// capabilities, so it exercises the production path rather than the degraded
// fallback the other fakes in this file use.
type snapshotGH struct {
	diff  string
	state string
	sha   string
	err   error

	// baseSHA is required by the publish guard on this branch: it compares BOTH
	// HeadSHA and BaseSHA against the reviewed revision and refuses to publish
	// on an empty one, so a snapshot with only HeadSHA set is not a valid
	// production response any more.
	baseSHA string

	snapshotCalls int
	submits       int
	comments      int
	// anchoredCommit records the commit_id the review was pinned to, which is
	// the whole point of preferring SubmitReviewForCommit.
	anchoredCommit string
	usedUnanchored bool
}

func (f *snapshotGH) FetchDiff(string, int) (string, error) { return f.diff, nil }

func (f *snapshotGH) FetchComments(string, int) ([]github.Comment, error) { return nil, nil }

func (f *snapshotGH) PostComment(string, int, string) (time.Time, error) {
	f.comments++
	return time.Now().UTC(), nil
}

func (f *snapshotGH) GetPRHeadSHA(string, int) (string, error) { return f.sha, nil }

func (f *snapshotGH) GetPRSnapshot(string, int) (*github.PRSnapshot, error) {
	f.snapshotCalls++
	if f.err != nil {
		return nil, f.err
	}
	// Author matters: the publish guard re-evaluates the self-authored
	// policy against the snapshot, and an empty author collides with an
	// unset bot login. Mirror headMovePR's user.
	return &github.PRSnapshot{
		State: f.state, HeadSHA: f.sha, BaseSHA: f.baseSHA, Author: "alice",
	}, nil
}

func (f *snapshotGH) SubmitReview(_ string, _ int, _, _ string) (int64, string, error) {
	f.submits++
	f.usedUnanchored = true
	return 1, "COMMENTED", nil
}

func (f *snapshotGH) SubmitReviewForCommit(_ string, _ int, _, _, commitID string) (int64, string, error) {
	f.submits++
	f.anchoredCommit = commitID
	return 1, "COMMENTED", nil
}

func (f *snapshotGH) FetchDiffForCommit(repo string, number int, _ string) (string, error) {
	return f.FetchDiff(repo, number)
}

// GetPRRevision makes this double a RevisionResolver. Without it the branch
// resolves the review's revision through the degraded path, stores the row with
// an empty BaseSHA, and the publish guard then retires it as revision-stale
// against a snapshot that DOES carry a base — a silent generate/retire loop.
// The same values back both resolvers on purpose: a client that reports one
// base at analysis time and another at publish time is a moved PR, not a fake.
func (f *snapshotGH) GetPRRevision(string, int) (github.PRRevision, error) {
	if f.err != nil {
		return github.PRRevision{}, f.err
	}
	return github.PRRevision{HeadSHA: f.sha, BaseSHA: f.baseSHA}, nil
}

// TestPipeline_Run_RetiresReviewWhenPRClosedDuringRun covers the parity gap
// raised in review: pendingReviewInvalidReason retires a pending review on TWO
// conditions, and only the HEAD one was implemented. A PR merged or closed during
// a run — a window documented as reaching past 3000s — still got its verdict
// posted.
func TestPipeline_Run_RetiresReviewWhenPRClosedDuringRun(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// HEAD is unchanged; only the state moved. The old SHA-only guard published.
	gh := &snapshotGH{diff: "+line", state: "closed", sha: "306bfcc7", baseSHA: "ba5e0000"}
	p := pipeline.New(s, gh, issuesExec{}, &fakeNotify{})

	pr := headMovePR("306bfcc7")
	rev, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini", Force: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev != nil {
		t.Errorf("expected nil review for a closed PR, got %+v", rev)
	}
	if gh.submits != 0 {
		t.Errorf("SubmitReview* called %d times on a closed PR; want 0", gh.submits)
	}

	storedPR, err := s.GetPRByGithubID(pr.ID)
	if err != nil {
		t.Fatalf("get pr: %v", err)
	}
	stored, err := s.LatestReviewForPR(storedPR.ID)
	if err != nil {
		t.Fatalf("latest review: %v", err)
	}
	// A closed PR is terminal, so it takes the orphan sentinel rather than
	// Superseded — Superseded means "re-review the new commit", which is wrong
	// here. -1 is the orphan value the deferred path uses for the same reason.
	if stored.GitHubReviewID != -1 {
		t.Errorf("stored github_review_id = %d, want -1 (terminal orphan) for a closed PR",
			stored.GitHubReviewID)
	}
}

// TestPipeline_Run_AnchorsPublishToAnalysedCommit pins the other half of the
// review finding: the re-check is check-then-act, so the submit itself must pin
// commit_id or a push racing it would have GitHub attach the findings to newer
// code.
func TestPipeline_Run_AnchorsPublishToAnalysedCommit(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	gh := &snapshotGH{diff: "+line", state: "open", sha: "306bfcc7", baseSHA: "ba5e0000"}
	p := pipeline.New(s, gh, issuesExec{}, &fakeNotify{})

	if _, err := p.Run(headMovePR("306bfcc7"), pipeline.RunOptions{
		Primary: "claude", Fallback: "gemini", Force: true,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gh.submits != 1 {
		t.Fatalf("submits = %d, want 1", gh.submits)
	}
	if gh.usedUnanchored {
		t.Error("fell back to the unanchored SubmitReview even though the anchored " +
			"variant was available")
	}
	if gh.anchoredCommit != "306bfcc7" {
		t.Errorf("anchored commit = %q, want the analysed SHA %q",
			gh.anchoredCommit, "306bfcc7")
	}
	// One snapshot call, replacing the previous GetPRHeadSHA call: covering the
	// extra state condition costs no additional API request.
	if gh.snapshotCalls != 1 {
		t.Errorf("GetPRSnapshot calls = %d, want exactly 1", gh.snapshotCalls)
	}
}

// TestPipeline_Run_FallsBackToUnanchoredSubmit keeps the optional-capability
// design honest: a gh dependency without the anchored method must still publish
// rather than fail.
func TestPipeline_Run_FallsBackToUnanchoredSubmit(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// headMoveGH implements neither optional capability.
	gh := &headMoveGH{diff: "+line", shaSequence: []string{"306bfcc7"}}
	p := pipeline.New(s, gh, issuesExec{}, &fakeNotify{})

	rev, err := p.Run(headMovePR("306bfcc7"), pipeline.RunOptions{
		Primary: "claude", Fallback: "gemini",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev == nil {
		t.Fatal("expected the review to publish through the fallback path")
	}
	if gh.submits != 1 {
		t.Errorf("submits = %d, want 1", gh.submits)
	}
}

// TestPipeline_Run_HeadOnlyFallbackDoesNotTreatUnknownStateAsClosed guards the
// stateKnown flag. Without it the fallback would report an empty state, the
// guard would read that as "not open", and every review on a gh dependency
// lacking GetPRSnapshot would be retired instead of published.
func TestPipeline_Run_HeadOnlyFallbackDoesNotTreatUnknownStateAsClosed(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	gh := &headMoveGH{diff: "+line", shaSequence: []string{"abc"}}
	p := pipeline.New(s, gh, issuesExec{}, &fakeNotify{})

	rev, err := p.Run(headMovePR("abc"), pipeline.RunOptions{Primary: "claude", Fallback: "gemini", Force: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev == nil || gh.submits != 1 {
		t.Errorf("review was withheld on an unknown state (rev nil: %v, submits: %d)",
			rev == nil, gh.submits)
	}
}
