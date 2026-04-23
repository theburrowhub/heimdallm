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

// newMemStore opens an in-memory SQLite store for tests. Callers get a
// fully-migrated *store.Store; cleanup is registered via t.Cleanup.
func newMemStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fakeGHReloop implements the subset of the pipeline's github dependency
// needed to exercise the HEAD-SHA fail-closed path. It records whether the
// diff/submit steps ran so tests can assert the pipeline short-circuits
// before executing a review when the SHA resolver fails.
type fakeGHReloop struct {
	headSHAErr   error
	headSHAValue string
	headSHACalls int
	submitted    bool
	diffCalled   bool
}

func (f *fakeGHReloop) GetPRHeadSHA(_ string, _ int) (string, error) {
	f.headSHACalls++
	if f.headSHAErr != nil {
		return "", f.headSHAErr
	}
	return f.headSHAValue, nil
}

func (f *fakeGHReloop) FetchDiff(_ string, _ int) (string, error) {
	f.diffCalled = true
	return "+line", nil
}

func (f *fakeGHReloop) SubmitReview(_ string, _ int, _, _ string) (int64, string, error) {
	f.submitted = true
	return 0, "", nil
}

func (f *fakeGHReloop) PostComment(_ string, _ int, _ string) (time.Time, error) {
	return time.Now().UTC(), nil
}

func (f *fakeGHReloop) FetchComments(_ string, _ int) ([]gh.Comment, error) {
	return nil, nil
}

// fakeExecReloop tracks whether the CLI executor was invoked — the fail-closed
// guard must short-circuit before this runs so Claude credits are not spent.
type fakeExecReloop struct {
	calls int
}

func (f *fakeExecReloop) Detect(_, _ string) (string, error) { return "fake_claude", nil }
func (f *fakeExecReloop) Execute(_, _ string, _ executor.ExecOptions) (*executor.ReviewResult, error) {
	f.calls++
	return &executor.ReviewResult{Summary: "ok", Severity: "low"}, nil
}

// TestRun_FailClosedWhenHeadSHALookupFails is the regression guard for the
// 2026-04-22 cost-runaway (theburrowhub/heimdallm#243). When GetPRHeadSHA
// returns a persistent error, the pipeline must NOT fall through to the
// executor — doing so would let a transient API outage bypass the
// cross-instance dedup and have every peer daemon spend Claude credits on
// the same commit.
func TestRun_FailClosedWhenHeadSHALookupFails(t *testing.T) {
	s := newMemStore(t)
	fgh := &fakeGHReloop{headSHAErr: errors.New("github: 503 service unavailable")}
	fexec := &fakeExecReloop{}
	p := pipeline.New(s, fgh, fexec, &fakeNotify{})

	pr := &gh.PullRequest{
		ID: 1, Number: 1, Title: "t", Repo: "org/repo",
		User: gh.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/1",
		Head: gh.Branch{SHA: ""}, // empty forces resolver path
	}
	_, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	if err == nil {
		t.Fatalf("expected fail-closed error, got nil")
	}
	// Cost-boundary contract: FetchDiff IS allowed to run (it happens before
	// the SHA check at pipeline.go:192) — it's a cheap GitHub API call. What
	// must NOT run is the Claude executor or the review submission, because
	// those are the expensive steps that burned €1,300 in #243.
	if !fgh.diffCalled {
		t.Errorf("expected FetchDiff to run before the SHA check (documents cost boundary)")
	}
	if fexec.calls != 0 {
		t.Errorf("executor must not be called when HEAD SHA resolver fails (calls=%d)", fexec.calls)
	}
	if fgh.submitted {
		t.Errorf("SubmitReview must not be called when HEAD SHA resolver fails")
	}
	// A single short retry is allowed to absorb rate-limit blips.
	if fgh.headSHACalls > 2 {
		t.Errorf("GetPRHeadSHA called more than twice (retries should stop at 1): %d", fgh.headSHACalls)
	}
}

// TestRun_LegacyRowWithEmptyHeadSHAIsBackfilledAndSkipped covers the second
// half of the fail-closed fix: rows stored before HeadSHA was populated carry
// HeadSHA = "", which the old "prev.HeadSHA == pr.Head.SHA" check could never
// match — so every legacy row would have bypassed the guard and re-run
// Claude. Instead we backfill the column from the current snapshot and skip
// the re-review; a user who wants a fresh review can trigger one manually.
func TestRun_LegacyRowWithEmptyHeadSHAIsBackfilledAndSkipped(t *testing.T) {
	s := newMemStore(t)

	// Seed a "legacy" review row with head_sha = "".
	prRow := &store.PR{
		GithubID:  100,
		Repo:      "org/repo",
		Number:    2,
		Title:     "t",
		Author:    "alice",
		State:     "open",
		UpdatedAt: time.Now(),
		FetchedAt: time.Now(),
	}
	prID, err := s.UpsertPR(prRow)
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	_, err = s.InsertReview(&store.Review{
		PRID:        prID,
		CLIUsed:     "claude",
		Summary:     "",
		Issues:      "[]",
		Suggestions: "[]",
		Severity:    "low",
		CreatedAt:   time.Now().Add(-1 * time.Hour),
		HeadSHA:     "",
	})
	if err != nil {
		t.Fatalf("insert legacy review: %v", err)
	}

	fgh := &fakeGHReloop{headSHAValue: "abc123"}
	fexec := &fakeExecReloop{}
	p := pipeline.New(s, fgh, fexec, &fakeNotify{})

	pr := &gh.PullRequest{
		ID: 100, Number: 2, Title: "t", Repo: "org/repo",
		User: gh.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/2",
		Head: gh.Branch{SHA: ""},
	}
	rev, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev == nil {
		t.Fatalf("expected returned review, got nil")
	}
	if rev.HeadSHA != "abc123" {
		t.Errorf("expected legacy row backfilled to abc123, got %q", rev.HeadSHA)
	}
	if fexec.calls != 0 {
		t.Errorf("executor must not be called when backfilling legacy row (calls=%d)", fexec.calls)
	}
	if fgh.submitted {
		t.Errorf("SubmitReview must not be called when backfilling legacy row")
	}

	// Verify the row was actually persisted with the backfilled SHA so a
	// subsequent Run hits the standard same-SHA skip branch.
	reviews, err := s.ListReviewsForPR(prID)
	if err != nil {
		t.Fatalf("list reviews: %v", err)
	}
	if len(reviews) != 1 {
		t.Fatalf("expected 1 review after backfill, got %d", len(reviews))
	}
	if reviews[0].HeadSHA != "abc123" {
		t.Errorf("stored HeadSHA = %q, want %q", reviews[0].HeadSHA, "abc123")
	}
}
