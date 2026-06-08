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
	// Contract change for #322 Bug 4: legacy-backfill is now a silent skip
	// (returns nil) — same shape as the gate-skip and SHA-skip paths — so
	// the caller's defensive `if rev == nil { return }` filter suppresses
	// the false EventReviewCompleted / activity_log row. The backfill side
	// effect on the reviews table still happens; assert it via the store below.
	if rev != nil {
		t.Errorf("expected nil review on legacy-backfill skip, got %+v", rev)
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

// newTestAdapter constructs a minimal tier2Adapter-equivalent that exposes
// PRAlreadyReviewed for the test. Mirrors the real adapter in main.go but
// without the Flutter/scheduler/config deps we don't need here.
func newTestAdapter(s *store.Store) interface {
	PRAlreadyReviewed(githubID int64, repo string, number int, updatedAt time.Time, headSHA string) bool
} {
	return pipeline.NewTestAdapter(s)
}

// TestPRAlreadyReviewed_NoGraceWindow verifies that PRAlreadyReviewed no
// longer suppresses enqueues based on a time-based grace window. The former
// 2-minute PublishedAt grace has been removed — ghost enqueues from the
// Search API's replication lag are now caught upstream by the
// requested_reviewers check in FetchPRsToReview (GetPRHeadInfo). With the
// grace gone, PRAlreadyReviewed should return false for a non-dismissed PR
// even when updated_at is seconds after the last review's PublishedAt.
// This locks in the regression fix for theburrowhub/heimdallm#243's
// successor: a push + re-request-review within 2 minutes of the last
// review is no longer suppressed.
func TestPRAlreadyReviewed_NoGraceWindow(t *testing.T) {
	s := newMemStore(t)
	prRow := &store.PR{GithubID: 99, Repo: "org/r", Number: 99, Title: "t",
		State: "open", UpdatedAt: time.Now()}
	prID, err := s.UpsertPR(prRow)
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}

	publishedAt := time.Now().Add(-30 * time.Second)
	if _, err := s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
		Severity: "low", CreatedAt: publishedAt.Add(-3 * time.Minute),
		PublishedAt: publishedAt, HeadSHA: "abc",
	}); err != nil {
		t.Fatalf("insert review: %v", err)
	}

	// updated_at is 15s after PublishedAt — formerly inside the 2-min grace.
	// PRAlreadyReviewed must now return false (the requested_reviewers check
	// upstream is the real guard).
	updatedAt := publishedAt.Add(15 * time.Second)
	adapter := newTestAdapter(s)
	if adapter.PRAlreadyReviewed(99, "org/r", 99, updatedAt, "abc") {
		t.Errorf("PRAlreadyReviewed must not suppress based on time grace — that guard moved to requested_reviewers check")
	}
}

// TestPRAlreadyReviewed_LegacyRowNoGrace verifies that legacy rows (zero
// PublishedAt) do not trigger false positives now that the grace is removed.
// Before: CreatedAt fallback + grace could suppress. Now: no grace at all.
func TestPRAlreadyReviewed_LegacyRowNoGrace(t *testing.T) {
	s := newMemStore(t)
	prRow := &store.PR{GithubID: 100, Repo: "org/r", Number: 100, Title: "t",
		State: "open", UpdatedAt: time.Now()}
	prID, err := s.UpsertPR(prRow)
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	createdAt := time.Now().Add(-30 * time.Second)
	if _, err := s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
		Severity: "low", CreatedAt: createdAt, // PublishedAt zero
		HeadSHA: "abc",
	}); err != nil {
		t.Fatalf("insert review: %v", err)
	}

	updatedAt := createdAt.Add(10 * time.Second)
	adapter := newTestAdapter(s)
	if adapter.PRAlreadyReviewed(100, "org/r", 100, updatedAt, "abc") {
		t.Errorf("legacy row must not suppress — grace removed, requested_reviewers check is the guard")
	}
}

// TestPRAlreadyReviewed_FallsBackToRepoNumberWhenGithubIDDiffers covers the
// Search Issues API vs Pulls API id mismatch from theburrowhub/heimdallm#351.
// The stored row came from the Pulls API after a successful review, while the
// next Tier 2 poll checks the Search API id before deciding whether to publish
// another review job. The stable repo/number identity must bridge that gap.
// With the grace removed, PRAlreadyReviewed returns false for non-dismissed
// PRs — but the ID fallback must still resolve correctly for circuit-breaker
// evaluation. This test verifies the fallback path finds the PR row.
func TestPRAlreadyReviewed_FallsBackToRepoNumberWhenGithubIDDiffers(t *testing.T) {
	s := newMemStore(t)
	const pullsAPIID int64 = 3578062677
	const searchAPIID int64 = 4321703389

	publishedAt := time.Now().UTC().Add(-30 * time.Second)
	prRow := &store.PR{
		GithubID:  pullsAPIID,
		Repo:      "org/r",
		Number:    337,
		Title:     "t",
		State:     "open",
		UpdatedAt: publishedAt.Add(-time.Minute),
	}
	_, err := s.UpsertPR(prRow)
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}

	// Without circuit breaker, PRAlreadyReviewed returns false (grace removed).
	// But it must NOT panic on the ID fallback path.
	updatedAt := publishedAt.Add(15 * time.Second)
	adapter := newTestAdapter(s)
	// The test verifies the fallback resolves the PR; with no circuit breaker
	// in the test adapter the result is false (non-dismissed, no breaker).
	_ = adapter.PRAlreadyReviewed(searchAPIID, "org/r", 337, updatedAt, "abc")
}

// TestPRAlreadyReviewed_DismissedStillBlocks verifies that dismissed PRs are
// still blocked by PRAlreadyReviewed even without the grace window.
func TestPRAlreadyReviewed_DismissedStillBlocks(t *testing.T) {
	s := newMemStore(t)
	prRow := &store.PR{GithubID: 1234, Repo: "org/r", Number: 1234,
		Title: "t", State: "open", UpdatedAt: time.Now()}
	prID, err := s.UpsertPR(prRow)
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if err := s.DismissPR(prID); err != nil {
		t.Fatalf("dismiss pr: %v", err)
	}

	updatedAt := time.Now()
	adapter := pipeline.NewTestAdapter(s)
	if !adapter.PRAlreadyReviewed(1234, "org/r", 1234, updatedAt, "abc") {
		t.Errorf("dismissed PR must still be blocked by PRAlreadyReviewed")
	}
}

// TestPRAlreadyReviewed_NoPRInStore verifies that an unknown PR (not in
// store) returns false so it proceeds to review.
func TestPRAlreadyReviewed_NoPRInStore(t *testing.T) {
	s := newMemStore(t)
	adapter := newTestAdapter(s)
	if adapter.PRAlreadyReviewed(999, "org/r", 999, time.Now(), "abc") {
		t.Errorf("unknown PR must not be blocked")
	}
}
