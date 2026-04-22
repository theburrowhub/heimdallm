package pipeline_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/store"
)

type fakeGH struct {
	diff     string
	comments []github.Comment
}

func (f *fakeGH) FetchDiff(repo string, number int) (string, error) {
	return f.diff, nil
}

func (f *fakeGH) SubmitReview(repo string, number int, body, event string) (int64, string, error) {
	// Mirror GitHub's actual mapping from the submitted event to the
	// returned state so pipeline tests that inspect GitHubReviewState see
	// something realistic rather than a hardcoded constant.
	state := "COMMENTED"
	switch event {
	case "APPROVE":
		state = "APPROVED"
	case "REQUEST_CHANGES":
		state = "CHANGES_REQUESTED"
	}
	return 12345, state, nil
}

func (f *fakeGH) PostComment(repo string, number int, body string) (time.Time, error) {
	return time.Now().UTC(), nil
}

func (f *fakeGH) FetchComments(repo string, number int) ([]github.Comment, error) {
	return f.comments, nil
}

func (f *fakeGH) GetPRHeadSHA(repo string, number int) (string, error) { return "", nil }

type fakeExec struct{}

func (f *fakeExec) Detect(primary, fallback string) (string, error) {
	return "fake_claude", nil
}

func (f *fakeExec) Execute(cli, prompt string, _ executor.ExecOptions) (*executor.ReviewResult, error) {
	return &executor.ReviewResult{
		Summary:     "Looks good",
		Issues:      []executor.Issue{{File: "main.go", Line: 1, Description: "test", Severity: "low"}},
		Suggestions: []string{"add tests"},
		Severity:    "low",
	}, nil
}

type fakeNotify struct {
	events []string
}

func (f *fakeNotify) Notify(title, message string) {
	f.events = append(f.events, title)
}

func TestPipeline_Run(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	notify := &fakeNotify{}
	p := pipeline.New(s, &fakeGH{diff: "+new line"}, &fakeExec{}, notify)

	pr := &github.PullRequest{
		ID: 1, Number: 1, Title: "Fix bug", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/1",
	}

	rev, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini", ReviewMode: "single"})
	if err != nil {
		t.Fatalf("pipeline run: %v", err)
	}
	if rev.Summary != "Looks good" {
		t.Errorf("summary: %q", rev.Summary)
	}
	// Verify stored in DB
	prs, _ := s.ListPRs()
	if len(prs) != 1 {
		t.Errorf("expected 1 PR in store, got %d", len(prs))
	}
	var issues []map[string]any
	json.Unmarshal([]byte(rev.Issues), &issues)
	if len(issues) != 1 {
		t.Errorf("expected 1 issue, got %d", len(issues))
	}
	if len(notify.events) < 2 {
		t.Errorf("expected at least 2 notifications, got %d", len(notify.events))
	}
}

// fakeExecCapture captures the prompt passed to Execute for assertion in tests.
type fakeExecCapture struct {
	capturePrompt *string
}

func (f *fakeExecCapture) Detect(primary, fallback string) (string, error) {
	return "fake_claude", nil
}

func (f *fakeExecCapture) Execute(cli, prompt string, _ executor.ExecOptions) (*executor.ReviewResult, error) {
	if f.capturePrompt != nil {
		*f.capturePrompt = prompt
	}
	return &executor.ReviewResult{Summary: "ok", Severity: "low"}, nil
}

// fakeGHCommentsError simulates a GitHub client where FetchComments fails.
type fakeGHCommentsError struct {
	diff string
}

func (f *fakeGHCommentsError) FetchDiff(repo string, number int) (string, error) {
	return f.diff, nil
}

func (f *fakeGHCommentsError) SubmitReview(repo string, number int, body, event string) (int64, string, error) {
	return 1, "COMMENTED", nil
}

func (f *fakeGHCommentsError) PostComment(repo string, number int, body string) (time.Time, error) {
	return time.Now().UTC(), nil
}

func (f *fakeGHCommentsError) FetchComments(repo string, number int) ([]github.Comment, error) {
	return nil, fmt.Errorf("network error")
}

func (f *fakeGHCommentsError) GetPRHeadSHA(repo string, number int) (string, error) { return "", nil }

func TestPipeline_Run_CommentsInjectedIntoPrompt(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	var capturedPrompt string
	exec := &fakeExecCapture{capturePrompt: &capturedPrompt}

	comments := []github.Comment{
		{Author: "reviewer1", Body: "Please add error handling here"},
	}
	p := pipeline.New(s, &fakeGH{diff: "+new line", comments: comments}, exec, &fakeNotify{})

	pr := &github.PullRequest{
		ID: 2, Number: 2, Title: "Add feature", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/2",
	}
	_, err = p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	if err != nil {
		t.Fatalf("pipeline run: %v", err)
	}
	if !strings.Contains(capturedPrompt, "reviewer1") {
		t.Errorf("expected comments in prompt, got: %s", capturedPrompt)
	}
	if !strings.Contains(capturedPrompt, "Please add error handling here") {
		t.Errorf("expected comment body in prompt, got: %s", capturedPrompt)
	}
}

func TestPipeline_Run_CommentsFetchErrorIsNonFatal(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	p := pipeline.New(s, &fakeGHCommentsError{diff: "+line"}, &fakeExec{}, &fakeNotify{})

	pr := &github.PullRequest{
		ID: 3, Number: 3, Title: "Fix", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/3",
	}
	_, err = p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	if err != nil {
		t.Fatalf("expected pipeline to succeed despite comments fetch error, got: %v", err)
	}
}

// fakeGHWithHeadSHA resolves a HEAD SHA via GetPRHeadSHA, simulating the
// real GitHub client — the Search Issues API used by Tier 2 does not return
// head.sha, so the pipeline must hydrate it before the dedup guard can fire.
type fakeGHWithHeadSHA struct {
	diff   string
	sha    string
	shaErr error
	// calls tracks GetPRHeadSHA invocations so tests can assert hydration
	// only happens when pr.Head.SHA is empty.
	shaCalls int
	submits  int
}

func (f *fakeGHWithHeadSHA) FetchDiff(repo string, number int) (string, error) {
	return f.diff, nil
}
func (f *fakeGHWithHeadSHA) SubmitReview(repo string, number int, body, event string) (int64, string, error) {
	f.submits++
	return 1, "COMMENTED", nil
}
func (f *fakeGHWithHeadSHA) PostComment(repo string, number int, body string) (time.Time, error) { return time.Now().UTC(), nil }
func (f *fakeGHWithHeadSHA) FetchComments(repo string, number int) ([]github.Comment, error) {
	return nil, nil
}
func (f *fakeGHWithHeadSHA) GetPRHeadSHA(repo string, number int) (string, error) {
	f.shaCalls++
	return f.sha, f.shaErr
}

// TestPipeline_Run_HydratesHeadSHAWhenMissing covers the production path: the
// Search Issues API doesn't populate head.sha, so Tier 2 hands the pipeline a
// PR with Head.SHA == "". The pipeline must fetch it so the dedup guard and
// the stored review row both record the correct SHA.
func TestPipeline_Run_HydratesHeadSHAWhenMissing(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	gh := &fakeGHWithHeadSHA{diff: "+line", sha: "abc123"}
	p := pipeline.New(s, gh, &fakeExec{}, &fakeNotify{})

	pr := &github.PullRequest{
		ID: 7, Number: 7, Title: "t", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/7",
		// Head.SHA intentionally empty — mirrors Search API payload.
	}
	rev, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gh.shaCalls != 1 {
		t.Errorf("expected 1 GetPRHeadSHA call, got %d", gh.shaCalls)
	}
	if rev.HeadSHA != "abc123" {
		t.Errorf("stored HeadSHA = %q, want %q", rev.HeadSHA, "abc123")
	}

	// Second run: the PR now has the SHA inline (as if hydrated upstream).
	// Pipeline must NOT call GetPRHeadSHA again, and must skip on SHA match.
	pr.Head.SHA = "abc123"
	_, err = p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if gh.shaCalls != 1 {
		t.Errorf("GetPRHeadSHA called redundantly: %d", gh.shaCalls)
	}
	if gh.submits != 1 {
		t.Errorf("SubmitReview called on same SHA: %d", gh.submits)
	}
}

// fakeExecCounter records how many times Execute was called so tests can
// assert whether the pipeline short-circuited before invoking the CLI.
type fakeExecCounter struct {
	calls int
}

func (f *fakeExecCounter) Detect(primary, fallback string) (string, error) {
	return "fake_claude", nil
}

func (f *fakeExecCounter) Execute(cli, prompt string, _ executor.ExecOptions) (*executor.ReviewResult, error) {
	f.calls++
	return &executor.ReviewResult{Summary: "ok", Severity: "low"}, nil
}

// fakeGHCounter records SubmitReview calls so tests can verify no publish
// happens on a skipped re-review.
type fakeGHCounter struct {
	diff     string
	submits  int
}

func (f *fakeGHCounter) FetchDiff(repo string, number int) (string, error) { return f.diff, nil }
func (f *fakeGHCounter) SubmitReview(repo string, number int, body, event string) (int64, string, error) {
	f.submits++
	return 1, "COMMENTED", nil
}
func (f *fakeGHCounter) PostComment(repo string, number int, body string) (time.Time, error) { return time.Now().UTC(), nil }
func (f *fakeGHCounter) FetchComments(repo string, number int) ([]github.Comment, error) { return nil, nil }
func (f *fakeGHCounter) GetPRHeadSHA(repo string, number int) (string, error)            { return "", nil }

// TestPipeline_Run_SkipsReviewOnSameHeadSHA is the regression guard for the
// bot-feedback loop bug seen on theburrowhub/heimdallm#139: any review
// submission bumps the PR's updated_at, so the timestamp-based dedup let
// multiple bots re-review the same commit over and over. The authoritative
// guard must be the HEAD commit SHA — if we've already reviewed this exact
// commit, the pipeline must not run the CLI or publish a new review.
func TestPipeline_Run_SkipsReviewOnSameHeadSHA(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	exec := &fakeExecCounter{}
	gh := &fakeGHCounter{diff: "+line"}
	p := pipeline.New(s, gh, exec, &fakeNotify{})

	pr := &github.PullRequest{
		ID: 42, Number: 42, Title: "Feature", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/42",
		Head: github.Branch{SHA: "deadbeef"},
	}

	// First run — produces the initial review on commit deadbeef.
	rev1, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if rev1 == nil || rev1.HeadSHA != "deadbeef" {
		t.Fatalf("first review HeadSHA = %q, want %q", func() string { if rev1 == nil { return "<nil>" }; return rev1.HeadSHA }(), "deadbeef")
	}
	if exec.calls != 1 || gh.submits != 1 {
		t.Fatalf("first run: exec=%d submits=%d, want 1/1", exec.calls, gh.submits)
	}

	// Simulate another bot posting a review, bumping updated_at. HEAD SHA unchanged.
	pr.UpdatedAt = time.Now().Add(5 * time.Minute)

	// Second run on the same HEAD SHA — must short-circuit. No CLI call, no
	// publish, no new review row.
	rev2, err := p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if exec.calls != 1 {
		t.Errorf("executor called again on same SHA: calls=%d", exec.calls)
	}
	if gh.submits != 1 {
		t.Errorf("SubmitReview called again on same SHA: submits=%d", gh.submits)
	}
	reviews, _ := s.ListReviewsForPR(rev1.PRID)
	if len(reviews) != 1 {
		t.Errorf("duplicate review row inserted on same SHA: got %d reviews", len(reviews))
	}
	if rev2 == nil || rev2.ID != rev1.ID {
		t.Errorf("expected Run to return the existing review on same SHA; got rev2=%+v", rev2)
	}

	// Third run with a new HEAD SHA — must proceed normally.
	pr.Head.SHA = "cafef00d"
	_, err = p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if exec.calls != 2 {
		t.Errorf("executor not invoked on new SHA: calls=%d", exec.calls)
	}
	if gh.submits != 2 {
		t.Errorf("SubmitReview not invoked on new SHA: submits=%d", gh.submits)
	}
}

// TestPipeline_Run_GateSkipsReview: when the guard evaluator returns a skip
// reason (here: state != "open"), the pipeline must not call the executor or
// submit a review. Proves the defense-in-depth layer protects future callers
// that forget the caller-side Evaluate.
func TestPipeline_Run_GateSkipsReview(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	exec := &fakeExecCounter{}
	gh := &fakeGHCounter{diff: "+line"}
	p := pipeline.New(s, gh, exec, &fakeNotify{})

	pr := &github.PullRequest{
		ID: 100, Number: 100, Title: "t", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "closed",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/100",
		Head: github.Branch{SHA: "abc"},
	}
	opts := pipeline.RunOptions{
		Primary: "claude", Fallback: "gemini",
		Guards: pipeline.GateConfig{SkipDrafts: true, SkipSelfAuthor: true, BotLogin: "heimdallm-bot"},
	}
	rev, err := p.Run(pr, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if exec.calls != 0 {
		t.Errorf("executor called on gate-skipped PR: calls=%d", exec.calls)
	}
	if gh.submits != 0 {
		t.Errorf("SubmitReview called on gate-skipped PR: submits=%d", gh.submits)
	}
	if rev != nil {
		t.Errorf("expected nil review on gate skip, got %+v", rev)
	}
}
