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
	rev, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
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
		Primary: "claude", Fallback: "gemini", ReviewMode: "multi",
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

	rev, err := p.Run(headMovePR("306bfcc7"), pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev == nil {
		t.Fatal("expected a published review when HEAD did not move")
	}
	if gh.submits != 1 {
		t.Errorf("SubmitReview calls = %d, want 1", gh.submits)
	}
	// The PR arrives with Head.SHA already set, so hydration is skipped and the
	// only GetPRHeadSHA call is the publish-time re-check. Pinning this keeps
	// the added API cost visible: one call per published review, no more.
	if gh.shaCalls != 1 {
		t.Errorf("GetPRHeadSHA calls = %d, want exactly 1 (the publish-time re-check)", gh.shaCalls)
	}
}

// TestPipeline_Run_PublishesWhenHeadRecheckFails documents the deliberate
// fail-open choice. Deferring on a transient API error would delay every
// publish whenever GitHub blips, and publishing stale would additionally
// require HEAD to have moved in the same run. The deferred publish path stays
// hardened either way because it re-validates the SHA under its own claim.
func TestPipeline_Run_PublishesWhenHeadRecheckFails(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	gh := &headMoveGH{diff: "+line", shaErr: errors.New("502 bad gateway")}
	p := pipeline.New(s, gh, issuesExec{}, &fakeNotify{})

	rev, err := p.Run(headMovePR("306bfcc7"), pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev == nil {
		t.Fatal("expected the review to publish when the re-check could not resolve HEAD")
	}
	if gh.submits != 1 {
		t.Errorf("SubmitReview calls = %d, want 1 (fail-open)", gh.submits)
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

	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	callsAfterPublish := gh.shaCalls

	// Second run on the same SHA with no re-request: the dedup gate skips it
	// before the CLI runs, so no further HEAD resolution should happen.
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"}); err != nil {
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
