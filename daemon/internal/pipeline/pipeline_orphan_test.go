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

func (f *fakeGHLockedSubmit) FetchDiff(_ string, _ int) (string, error) { return "+line", nil }
func (f *fakeGHLockedSubmit) SubmitReview(_ string, _ int, _, _ string) (int64, string, error) {
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
		GitHubReviewID: 0, // marks it as unpublished
	}); err != nil {
		t.Fatalf("insert review: %v", err)
	}

	fgh := &fakeGHLockedSubmit{}
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
		HeadSHA: "def", GitHubReviewID: 0,
	}); err != nil {
		t.Fatalf("insert review: %v", err)
	}

	fgh := &fakeGHTransientSubmit{}
	p := pipeline.New(s, fgh, &fakeExecOrphan{}, &fakeNotify{})

	// Two ticks — both should attempt SubmitReview, both should leave
	// the row in the queue (no orphan-marking on transient errors).
	p.PublishPending()
	p.PublishPending()
	if fgh.submitCalls != 2 {
		t.Errorf("transient SubmitReview attempts = %d, want 2 (must keep retrying)", fgh.submitCalls)
	}
	unpub, _ := s.ListUnpublishedReviews()
	if len(unpub) != 1 {
		t.Errorf("expected 1 unpublished review (transient must NOT mark orphan), got %d", len(unpub))
	}
}

// fakeGHTransientSubmit returns a generic non-permanent error so the
// transient-keeps-retrying test can drive the negative path.
type fakeGHTransientSubmit struct{ submitCalls int }

func (f *fakeGHTransientSubmit) FetchDiff(_ string, _ int) (string, error) { return "+line", nil }
func (f *fakeGHTransientSubmit) SubmitReview(_ string, _ int, _, _ string) (int64, string, error) {
	f.submitCalls++
	return 0, "", errors.New("github: submit review: status 503: upstream")
}
func (f *fakeGHTransientSubmit) PostComment(_ string, _ int, _ string) (time.Time, error) {
	return time.Now().UTC(), nil
}
func (f *fakeGHTransientSubmit) FetchComments(_ string, _ int) ([]gh.Comment, error) {
	return nil, nil
}
func (f *fakeGHTransientSubmit) GetPRHeadSHA(_ string, _ int) (string, error) { return "", nil }
