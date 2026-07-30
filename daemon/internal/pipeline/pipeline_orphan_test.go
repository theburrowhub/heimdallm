package pipeline_test

import (
	"errors"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/store"
)

// fakeGHLockedSubmit is a github dependency stub whose SubmitReview
// returns *gh.PermanentSubmitError on every call, simulating a PR
// whose conversation has been locked by an operator. Used by the
// #325 regression tests to assert the pipeline marks the row orphan
// instead of looping the retry forever.
type fakeGHLockedSubmit struct {
	submitCalls int
	headSHA     string
}

func (f *fakeGHLockedSubmit) FetchDiffForCommit(_ string, _ int, headSHA string) (string, error) {
	f.headSHA = headSHA
	return "+line", nil
}
func (f *fakeGHLockedSubmit) SubmitReviewForCommit(_ string, _ int, _, _, _ string) (int64, string, error) {
	f.submitCalls++
	return 0, "", &gh.PermanentSubmitError{
		StatusCode: 422,
		Reason:     "pr_locked",
		Body:       "lock prevents review",
	}
}
func (f *fakeGHLockedSubmit) PostComment(_ string, _ int, _ string) (time.Time, error) {
	return time.Now().UTC(), nil
}
func (f *fakeGHLockedSubmit) FetchComments(_ string, _ int) ([]gh.Comment, error) {
	return nil, nil
}
func (f *fakeGHLockedSubmit) GetPRHeadSHA(_ string, _ int) (string, error) { return f.headSHA, nil }
func (f *fakeGHLockedSubmit) GetPRRevision(_ string, _ int) (gh.PRRevision, error) {
	return gh.PRRevision{BaseSHA: "base", HeadSHA: f.headSHA}, nil
}

// fakeExecOrphan mirrors fakeExecCounter but lives in this file so the
// orphan tests are self-contained.
type fakeExecOrphan struct{ calls int }

func (f *fakeExecOrphan) Detect(_, _ string) (string, error) { return "fake_claude", nil }
func (f *fakeExecOrphan) Execute(_, _ string, _ executor.ExecOptions) (*executor.ReviewResult, error) {
	f.calls++
	return &executor.ReviewResult{Summary: "ok", Severity: "low"}, nil
}

// TestRun_LockedPRMarksReviewOrphanImmediately covers the initial
// publish path: a 422 lock during the first SubmitReview call must
// mark the freshly inserted row as orphaned in-place, so it never
// enters the PublishPending retry loop. Without this, every locked
// PR burns one GitHub API call per poll cycle indefinitely. See
// theburrowhub/heimdallm#325.
func TestRun_LockedPRMarksReviewOrphanImmediately(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	fgh := &fakeGHLockedSubmit{headSHA: "deadbeef"}
	p := pipeline.New(s, fgh, &fakeExecOrphan{}, &fakeNotify{})

	pr := &gh.PullRequest{
		ID: 1, Number: 1, Title: "t", Repo: "org/repo",
		User: gh.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/1",
		Head: gh.Branch{SHA: "deadbeef"},
	}
	rev, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev == nil {
		t.Fatalf("expected stored review, got nil")
	}

	// The row must be present in the unpublished-reviews query before
	// MarkReviewPublished runs — but afterwards it MUST NOT be.
	unpub, err := s.ListUnpublishedReviews()
	if err != nil {
		t.Fatalf("list unpublished: %v", err)
	}
	if len(unpub) != 0 {
		t.Errorf("expected 0 unpublished reviews after lock-orphan marking, got %d (the retry loop would burn API calls): %+v", len(unpub), unpub)
	}

	// Sanity: only one SubmitReview attempt — the orphan-marker stops the loop.
	if fgh.submitCalls != 1 {
		t.Errorf("SubmitReview attempts = %d, want 1 (orphan marker should stop the cycle)", fgh.submitCalls)
	}

	// PublishPending should now be a no-op for this row.
	p.PublishPending()
	if fgh.submitCalls != 1 {
		t.Errorf("PublishPending re-attempted SubmitReview on orphaned row: calls=%d", fgh.submitCalls)
	}
}

// TestPublishPending_LockedPRStopsRetrying covers the retry-loop
// path: a row already in the unpublished queue (e.g. inserted by an
// older daemon version, or by a transient publish failure that has
// since transitioned to a permanent lock) must be marked orphan on
// the very first PublishPending tick that observes the
// PermanentSubmitError. The next tick must not re-attempt.
func TestPublishPending_LockedPRStopsRetrying(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// Seed a PR + an unpublished review row directly, mirroring the
	// state PublishPending iterates over on every tick.
	prID, err := s.UpsertPR(&store.PR{
		GithubID: 100, Repo: "org/repo", Number: 7, Title: "t", Author: "alice",
		State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if _, err := s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
		Severity: "low", CreatedAt: time.Now(),
		HeadSHA:        "abc",
		BaseSHA:        "base",
		GitHubReviewID: 0, // marks it as unpublished
	}); err != nil {
		t.Fatalf("insert review: %v", err)
	}

	fgh := &fakeGHLockedSubmit{headSHA: "abc"}
	p := pipeline.New(s, fgh, &fakeExecOrphan{}, &fakeNotify{})

	// First PublishPending tick: SubmitReview returns PermanentSubmitError,
	// pipeline marks the row orphan.
	p.PublishPending()
	if fgh.submitCalls != 1 {
		t.Fatalf("first tick SubmitReview calls = %d, want 1", fgh.submitCalls)
	}
	unpub, _ := s.ListUnpublishedReviews()
	if len(unpub) != 0 {
		t.Errorf("expected 0 unpublished reviews after orphan marking, got %d", len(unpub))
	}

	// Subsequent ticks must NOT re-attempt SubmitReview — that's the
	// regression we're guarding against.
	for i := 0; i < 3; i++ {
		p.PublishPending()
	}
	if fgh.submitCalls != 1 {
		t.Errorf("PublishPending re-attempted on orphaned row across %d extra ticks: total calls=%d, want 1", 3, fgh.submitCalls)
	}
}

// TestPublishPending_TransientErrorStillRetries makes sure we did NOT
// over-rotate: a transient (non-permanent) error must still leave the
// row in the unpublished queue so the next tick retries. Mirrors the
// fail-closed posture of #245 — a 5xx outage cannot wipe legitimate
// reviews.
func TestPublishPending_TransientErrorStillRetries(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	prID, _ := s.UpsertPR(&store.PR{
		GithubID: 200, Repo: "org/repo", Number: 8, Title: "t", Author: "alice",
		State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now(),
	})
	if _, err := s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
		Severity: "low", CreatedAt: time.Now(),
		HeadSHA: "def", BaseSHA: "base", GitHubReviewID: 0,
	}); err != nil {
		t.Fatalf("insert review: %v", err)
	}

	fgh := &fakeGHTransientSubmit{headSHA: "def"}
	p := pipeline.New(s, fgh, &fakeExecOrphan{}, &fakeNotify{})

	// Two ticks — both should attempt SubmitReview, both should leave
	// the row in the queue (no orphan-marking on transient errors).
	p.PublishPending()
	p.PublishPending()
	if fgh.submitCalls != 2 {
		t.Errorf("transient SubmitReview attempts = %d, want 2 (must keep retrying)", fgh.submitCalls)
	}
	if len(fgh.commitIDs) != 2 || fgh.commitIDs[0] != "def" || fgh.commitIDs[1] != "def" {
		t.Errorf("pending commit IDs = %v, want [def def]", fgh.commitIDs)
	}
	unpub, _ := s.ListUnpublishedReviews()
	if len(unpub) != 1 {
		t.Errorf("expected 1 unpublished review (transient must NOT mark orphan), got %d", len(unpub))
	}
}

func TestPublishPending_ForcedReviewRetainsManualAuthorization(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	fgh := &fakeGHTransientSubmit{succeedOnCall: 2}
	exec := &fakeExecOrphan{}
	p := pipeline.New(s, fgh, exec, &fakeNotify{})
	p.SetBotLogin("heimdallm-bot")
	p.SetTimelineFetcher(&fakeTimeline{events: []gh.TimelineEvent{
		{ID: 501, Event: "review_request_removed", CreatedAt: time.Now().UTC()},
	}})
	p.SetReviewerFetcher(&fakeReviewerFetcher{info: gh.PRHeadInfo{HeadSHA: "head"}})
	pr := &gh.PullRequest{
		ID: 205, Number: 85, Title: "manual retry", Repo: "org/repo",
		User: gh.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), Head: gh.Branch{SHA: "head"},
	}

	rev, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Force: true})
	if err != nil {
		t.Fatalf("forced run: %v", err)
	}
	if rev == nil || rev.AuthorizationSource != pipeline.ReviewAuthorizationManual {
		t.Fatalf("review = %+v, want durable manual authorization", rev)
	}
	if rev.GitHubReviewID != 0 || fgh.submitCalls != 1 {
		t.Fatalf("initial publish id/calls = %d/%d, want 0/1 after transient failure",
			rev.GitHubReviewID, fgh.submitCalls)
	}

	// requested_reviewers remains empty and the timeline says the automatic
	// request was removed. Neither may cancel an explicitly forced retry.
	p.PublishPending()
	if fgh.submitCalls != 2 {
		t.Fatalf("retry submit calls = %d, want 2", fgh.submitCalls)
	}
	stored, err := s.GetReview(rev.ID)
	if err != nil {
		t.Fatalf("get stored review: %v", err)
	}
	if stored.GitHubReviewID <= 0 || stored.AuthorizationSource != pipeline.ReviewAuthorizationManual {
		t.Fatalf("stored review = %+v, want published manual review", stored)
	}
}

func TestPublishPending_ChangedHeadRetiresWithoutPublishing(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	prID, err := s.UpsertPR(&store.PR{
		GithubID: 201, Repo: "org/repo", Number: 81, Title: "t", Author: "alice",
		State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	reviewID, err := s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
		Severity: "low", CreatedAt: time.Now(),
		HeadSHA: "old-sha", GitHubReviewID: 0,
	})
	if err != nil {
		t.Fatalf("insert review: %v", err)
	}

	fgh := &fakeGHTransientSubmit{headSHA: "new-sha"}
	pipeline.New(s, fgh, &fakeExecOrphan{}, &fakeNotify{}).PublishPending()

	if fgh.submitCalls != 0 {
		t.Fatalf("SubmitReviewForCommit calls = %d, want 0", fgh.submitCalls)
	}
	got, err := s.GetReview(reviewID)
	if err != nil {
		t.Fatalf("get review: %v", err)
	}
	if got.GitHubReviewID != pipeline.SupersededReviewID {
		t.Fatalf("GitHubReviewID = %d, want superseded sentinel %d",
			got.GitHubReviewID, pipeline.SupersededReviewID)
	}
}

func TestPublishPending_EmptyHeadSHANeverPublishes(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	prID, err := s.UpsertPR(&store.PR{
		GithubID: 300, Repo: "org/repo", Number: 9, Title: "legacy", Author: "alice",
		State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	reviewID, err := s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Summary: "unknown snapshot",
		Issues: "[]", Suggestions: "[]", Severity: "low",
		CreatedAt: time.Now(), HeadSHA: "", GitHubReviewID: 0,
	})
	if err != nil {
		t.Fatalf("insert review: %v", err)
	}

	fgh := &fakeGHTransientSubmit{}
	p := pipeline.New(s, fgh, &fakeExecOrphan{}, &fakeNotify{})
	p.PublishPending()

	if fgh.submitCalls != 0 {
		t.Fatalf("SubmitReviewForCommit calls = %d, want 0 for unknown commit", fgh.submitCalls)
	}
	if unpub, err := s.ListUnpublishedReviews(); err != nil {
		t.Fatalf("ListUnpublishedReviews: %v", err)
	} else if len(unpub) != 0 {
		t.Fatalf("unpublished reviews = %d, want legacy row retired", len(unpub))
	}
	got, err := s.GetReview(reviewID)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if got.GitHubReviewID != -1 {
		t.Fatalf("GitHubReviewID = %d, want orphan sentinel -1", got.GitHubReviewID)
	}
}

// fakeGHTransientSubmit returns a generic non-permanent error so the
// transient-keeps-retrying test can drive the negative path.
type fakeGHTransientSubmit struct {
	submitCalls   int
	commitIDs     []string
	headSHA       string
	succeedOnCall int
}

func (f *fakeGHTransientSubmit) FetchDiffForCommit(_ string, _ int, headSHA string) (string, error) {
	f.headSHA = headSHA
	return "+line", nil
}
func (f *fakeGHTransientSubmit) FetchDiffSnapshot(_ string, _ int, headSHA string) (gh.PRDiffSnapshot, error) {
	f.headSHA = headSHA
	return gh.PRDiffSnapshot{Diff: "+line", BaseSHA: "base", HeadSHA: headSHA}, nil
}
func (f *fakeGHTransientSubmit) SubmitReviewForCommit(_ string, _ int, _, _, commitID string) (int64, string, error) {
	f.submitCalls++
	f.commitIDs = append(f.commitIDs, commitID)
	if f.succeedOnCall > 0 && f.submitCalls >= f.succeedOnCall {
		return int64(9000 + f.submitCalls), "COMMENTED", nil
	}
	return 0, "", errors.New("github: submit review: status 503: upstream")
}
func (f *fakeGHTransientSubmit) PostComment(_ string, _ int, _ string) (time.Time, error) {
	return time.Now().UTC(), nil
}
func (f *fakeGHTransientSubmit) FetchComments(_ string, _ int) ([]gh.Comment, error) {
	return nil, nil
}
func (f *fakeGHTransientSubmit) GetPRHeadSHA(_ string, _ int) (string, error) {
	return f.headSHA, nil
}
func (f *fakeGHTransientSubmit) GetPRRevision(_ string, _ int) (gh.PRRevision, error) {
	return gh.PRRevision{BaseSHA: "base", HeadSHA: f.headSHA}, nil
}

// TestPublishPending_CorruptIssuesJSONOrphansWithoutPublishing covers #549: a
// stored review whose issues JSON is corrupt must NOT be posted to GitHub with
// its findings silently dropped, and must be orphaned so the deterministic
// decode failure doesn't loop the retry forever.
func TestPublishPending_CorruptIssuesJSONRetiresForRegeneration(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	prID, err := s.UpsertPR(&store.PR{
		GithubID: 300, Repo: "org/repo", Number: 9, Title: "t", Author: "alice",
		State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	// Seed an unpublished review whose Issues column is corrupt JSON.
	prevReviewID, err := s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Issues: "{not valid json", Suggestions: "[]",
		Severity: "high", CreatedAt: time.Now(), HeadSHA: "abc", GitHubReviewID: 0,
	})
	if err != nil {
		t.Fatalf("insert review: %v", err)
	}

	fgh := &fakeGHTransientSubmit{} // would be called if the bug were present; we assert it isn't
	p := pipeline.New(s, fgh, &fakeExecOrphan{}, &fakeNotify{})

	p.PublishPending()

	// Must NOT post a misleading (issue-less) review to GitHub.
	if fgh.submitCalls != 0 {
		t.Errorf("SubmitReview called on corrupt-JSON review: calls=%d, want 0", fgh.submitCalls)
	}
	// Must be retired so it doesn't retry forever.
	unpub, err := s.ListUnpublishedReviews()
	if err != nil {
		t.Fatalf("list unpublished: %v", err)
	}
	if len(unpub) != 0 {
		t.Errorf("expected 0 unpublished reviews after corrupt-JSON retirement, got %d", len(unpub))
	}

	// Corruption happened before GitHub received anything, so use the
	// regenerable sentinel rather than permanently blocking this PR/SHA.
	got, err := s.GetReview(prevReviewID)
	if err != nil {
		t.Fatalf("get review: %v", err)
	}
	if got.GitHubReviewID != pipeline.SupersededReviewID {
		t.Errorf("retired review GitHubReviewID = %d, want %d (regenerable sentinel)",
			got.GitHubReviewID, pipeline.SupersededReviewID)
	}

	// Subsequent ticks must remain a no-op.
	p.PublishPending()
	if fgh.submitCalls != 0 {
		t.Errorf("PublishPending re-attempted on retired corrupt row: calls=%d, want 0", fgh.submitCalls)
	}
}
