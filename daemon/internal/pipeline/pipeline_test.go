package pipeline_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/sse"
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

// countNotify returns how many times `title` appears in the recorded
// fakeNotify events. Used by SHA-skip regression tests to assert no
// duplicate "PR Review Started" notifications fire when the pipeline
// short-circuits on an unchanged HEAD SHA (#322 Bug 3).
func countNotify(events []string, title string) int {
	n := 0
	for _, e := range events {
		if e == title {
			n++
		}
	}
	return n
}

// fakeTimeline is a TimelineFetcher stub that returns a canned event
// slice (or an error) so SHA-skip-bypass tests can drive the
// re-request decision deterministically. Used by tests for #322 Bug 5.
type fakeTimeline struct {
	events []github.TimelineEvent
	err    error
	calls  int
}

func (f *fakeTimeline) GetPRTimelineEventsForReviewer(_ string, _ int, _ string) ([]github.TimelineEvent, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.events, nil
}

// fakePublisher records every SSE event the pipeline emits so lifecycle
// tests can assert the exact (event_type, payload) pairs that hit the
// broker. Mirrors the *sse.Broker contract via duck-typing — no need to
// stand up a real broker for assertions. See #322 Bugs 3+4.
type fakePublisher struct {
	mu     sync.Mutex
	events []sse.Event
}

func (f *fakePublisher) Publish(e sse.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakePublisher) types() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.events))
	for i, e := range f.events {
		out[i] = e.Type
	}
	return out
}

func (f *fakePublisher) firstOf(eventType string) (sse.Event, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if e.Type == eventType {
			return e, true
		}
	}
	return sse.Event{}, false
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
	notify := &fakeNotify{}
	p := pipeline.New(s, gh, exec, notify)

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
	// Contract change for #322 Bug 4: SHA-skip now returns (nil, nil), the
	// same shape the gate-skip path uses, so the caller's defensive
	// `if rev == nil { return }` filter suppresses the false
	// EventReviewCompleted / activity_log row / "review done" log. The skip
	// itself stays visible via the slog.Info inside Run.
	if rev2 != nil {
		t.Errorf("expected nil review on SHA-skip (silent skip), got rev2=%+v", rev2)
	}

	// Regression for #322 Bug 3: the desktop notification must NOT fire on a
	// SHA-skip. Only the first run (which actually dispatched a review)
	// should have produced a "PR Review Started" / "PR Review Complete"
	// pair. The second run skipped, so no extra notify events.
	if startedCount := countNotify(notify.events, "PR Review Started"); startedCount != 1 {
		t.Errorf("notify(\"PR Review Started\") fired %d times across 1 real review + 1 SHA-skip; want exactly 1", startedCount)
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

// TestPipeline_Run_Tier3PathSkipsOnSameHeadSHA simulates the Tier 3 re-entry:
// after Tier 2 reviewed commit X, Tier 3 calls pipeline.Run again on the same
// PR at the same SHA (because GitHub's updated_at bumped for an unrelated
// reason — merge metadata, a comment, etc.). The HEAD-SHA guard must kick in
// and short-circuit the CLI/publish steps.
func TestPipeline_Run_Tier3PathSkipsOnSameHeadSHA(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	exec := &fakeExecCounter{}
	gh := &fakeGHCounter{diff: "+line"}
	notify := &fakeNotify{}
	p := pipeline.New(s, gh, exec, notify)

	prT2 := &github.PullRequest{
		ID: 900, Number: 900, Title: "t", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/900",
		Head: github.Branch{SHA: "sha-one"},
	}
	if _, err := p.Run(prT2, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("tier2 run: %v", err)
	}
	if exec.calls != 1 {
		t.Fatalf("expected first run to invoke CLI, got calls=%d", exec.calls)
	}

	// Tier 3 re-entry: same PR, same SHA, bumped updated_at.
	prT3 := *prT2
	prT3.UpdatedAt = prT2.UpdatedAt.Add(2 * time.Minute)
	rev3, err := p.Run(&prT3, pipeline.RunOptions{Primary: "claude"})
	if err != nil {
		t.Fatalf("tier3 run: %v", err)
	}
	if exec.calls != 1 {
		t.Errorf("Tier 3 re-run invoked CLI on same SHA: calls=%d", exec.calls)
	}
	if gh.submits != 1 {
		t.Errorf("Tier 3 re-run submitted review on same SHA: submits=%d", gh.submits)
	}
	// #322 Bug 4: Tier 3 SHA-skip must return (nil, nil) so the activity
	// recorder doesn't insert a fake review row each watch cycle.
	if rev3 != nil {
		t.Errorf("Tier 3 SHA-skip should return nil review, got %+v", rev3)
	}
	// #322 Bug 3: Tier 3 SHA-skip must not fire a fresh "PR Review Started"
	// notification — only the first run (which actually dispatched) should
	// have produced one.
	if startedCount := countNotify(notify.events, "PR Review Started"); startedCount != 1 {
		t.Errorf("notify(\"PR Review Started\") fired %d times across 1 real review + 1 Tier 3 SHA-skip; want exactly 1", startedCount)
	}
}

// ── #322 Bug 5: explicit re-request review bypasses the SHA skip ──────

// runFirstReview is a small helper used by the Bug 5 tests below to seed
// a previous review on the store via the real pipeline, so the second
// Run hits the SHA-skip branch with a realistic prevReview row.
func runFirstReview(t *testing.T, p *pipeline.Pipeline, pr *github.PullRequest) {
	t.Helper()
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("seed first review: %v", err)
	}
}

// TestPipeline_Run_RespectsExplicitReReviewOnSameSHA covers the
// happy-path bypass: operator presses "Re-request review" in the
// GitHub UI, the timeline records a review_requested newer than the
// previous review, the pipeline must re-run the review on the same
// HEAD SHA. Defends against the silent-skip behaviour observed on PR
// freepik-company/ai-api-specs#557 on 2026-04-24.
func TestPipeline_Run_RespectsExplicitReReviewOnSameSHA(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	exec := &fakeExecCounter{}
	gh := &fakeGHCounter{diff: "+line"}
	p := pipeline.New(s, gh, exec, &fakeNotify{})
	p.SetBotLogin("heimdallm-bot")

	pr := &github.PullRequest{
		ID: 557, Number: 557, Title: "feat: x", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now().Add(-1 * time.Hour),
		HTMLURL:   "https://github.com/org/repo/pull/557",
		Head:      github.Branch{SHA: "c527a2e4"},
	}
	runFirstReview(t, p, pr)
	if exec.calls != 1 {
		t.Fatalf("seed: expected exec.calls=1, got %d", exec.calls)
	}

	// Operator hits "Re-request review" — timeline records a
	// review_requested event clearly after the existing review's
	// CreatedAt. The +1 minute offset is deliberate: prevReview.CreatedAt
	// was sealed during runFirstReview a few microseconds ago, and the
	// bypass decision uses .After() (strict greater-than). A naked
	// time.Now() here would race with that sealed timestamp on fast
	// machines — pinning the offset keeps the test deterministic.
	tl := &fakeTimeline{events: []github.TimelineEvent{
		{Event: "review_requested", Actor: "alice", CreatedAt: time.Now().Add(1 * time.Minute)},
	}}
	p.SetTimelineFetcher(tl)

	pr.UpdatedAt = time.Now()
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("re-request run: %v", err)
	}
	if exec.calls != 2 {
		t.Errorf("re-request: expected exec.calls=2, got %d", exec.calls)
	}
	if gh.submits != 2 {
		t.Errorf("re-request: expected gh.submits=2, got %d", gh.submits)
	}
	if tl.calls == 0 {
		t.Errorf("timeline was not consulted on SHA-skip path")
	}
}

// TestPipeline_Run_IgnoresStaleReviewRequest covers the negative case:
// a review_requested whose timestamp predates the existing review is
// already-satisfied and must NOT bypass the SHA skip. Otherwise every
// PR that ever asked for the bot would re-review forever.
func TestPipeline_Run_IgnoresStaleReviewRequest(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	exec := &fakeExecCounter{}
	gh := &fakeGHCounter{diff: "+line"}
	p := pipeline.New(s, gh, exec, &fakeNotify{})
	p.SetBotLogin("heimdallm-bot")

	pr := &github.PullRequest{
		ID: 1, Number: 1, Title: "t", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now().Add(-1 * time.Hour),
		HTMLURL:   "https://github.com/org/repo/pull/1",
		Head:      github.Branch{SHA: "abc"},
	}
	runFirstReview(t, p, pr)

	// Stale request: predates the review we just performed.
	tl := &fakeTimeline{events: []github.TimelineEvent{
		{Event: "review_requested", Actor: "alice", CreatedAt: time.Now().Add(-2 * time.Hour)},
	}}
	p.SetTimelineFetcher(tl)

	pr.UpdatedAt = time.Now()
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if exec.calls != 1 {
		t.Errorf("stale request must NOT trigger re-review, got exec.calls=%d", exec.calls)
	}
}

// TestPipeline_Run_DismissAfterReRequestKeepsSkip covers the layered
// case: re-request was followed by a dismiss, so the operator no
// longer wants our review on this SHA. Newest event wins; skip stays.
func TestPipeline_Run_DismissAfterReRequestKeepsSkip(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	exec := &fakeExecCounter{}
	gh := &fakeGHCounter{diff: "+line"}
	p := pipeline.New(s, gh, exec, &fakeNotify{})
	p.SetBotLogin("heimdallm-bot")

	pr := &github.PullRequest{
		ID: 2, Number: 2, Title: "t", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now().Add(-1 * time.Hour),
		HTMLURL:   "https://github.com/org/repo/pull/2",
		Head:      github.Branch{SHA: "def"},
	}
	runFirstReview(t, p, pr)

	now := time.Now()
	tl := &fakeTimeline{events: []github.TimelineEvent{
		{Event: "review_requested", Actor: "alice", CreatedAt: now.Add(-10 * time.Minute)},
		{Event: "review_dismissed", Actor: "alice", CreatedAt: now.Add(-5 * time.Minute)},
	}}
	p.SetTimelineFetcher(tl)

	pr.UpdatedAt = now
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if exec.calls != 1 {
		t.Errorf("dismiss after re-request must keep the skip, got exec.calls=%d", exec.calls)
	}
}

// TestPipeline_Run_TimelineErrorKeepsSkip enforces the fail-closed
// posture: a transient timeline API error must NOT widen the cost
// surface by suddenly bypassing the SHA skip. Same rule as the
// HEAD-SHA resolver fail-closed in #245.
func TestPipeline_Run_TimelineErrorKeepsSkip(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	exec := &fakeExecCounter{}
	gh := &fakeGHCounter{diff: "+line"}
	p := pipeline.New(s, gh, exec, &fakeNotify{})
	p.SetBotLogin("heimdallm-bot")

	pr := &github.PullRequest{
		ID: 3, Number: 3, Title: "t", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now().Add(-1 * time.Hour),
		HTMLURL:   "https://github.com/org/repo/pull/3",
		Head:      github.Branch{SHA: "ghi"},
	}
	runFirstReview(t, p, pr)

	tl := &fakeTimeline{err: errors.New("github: 503 service unavailable")}
	p.SetTimelineFetcher(tl)

	pr.UpdatedAt = time.Now()
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if exec.calls != 1 {
		t.Errorf("timeline error must keep the skip (fail-closed), got exec.calls=%d", exec.calls)
	}
	if tl.calls == 0 {
		t.Errorf("timeline was not consulted")
	}
}

// ── #322 Bugs 3+4: pipeline-owned lifecycle SSEs ──────────────────────

// TestPipeline_Run_SHASkipEmitsReviewSkipped is the regression guard
// for the spinner-colgado UI bug from #322 Bug 3+4 review feedback:
// when Run short-circuits on an unchanged HEAD SHA, the publisher
// must receive a single review_skipped event (with reason
// sha_unchanged) and NOTHING ELSE — no review_started, no
// review_completed. The Flutter dashboard relies on review_skipped to
// stop the spinner and remove the PR from reviewingPRsProvider.
func TestPipeline_Run_SHASkipEmitsReviewSkipped(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	exec := &fakeExecCounter{}
	gh := &fakeGHCounter{diff: "+line"}
	pub := &fakePublisher{}
	p := pipeline.New(s, gh, exec, &fakeNotify{})
	p.SetPublisher(pub)

	pr := &github.PullRequest{
		ID: 42, Number: 42, Title: "t", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/42",
		Head: github.Branch{SHA: "deadbeef"},
	}
	// First run: real review. Pipeline should emit pr_detected,
	// review_started, review_completed (in that order).
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	wantFirst := []string{"pr_detected", "review_started", "review_completed"}
	if got := pub.types(); !equalStringSlices(got, wantFirst) {
		t.Fatalf("first run events: got %v, want %v", got, wantFirst)
	}

	// Second run: same SHA → skip path. Pipeline must emit ONE
	// review_skipped event (with reason sha_unchanged) and nothing
	// else. No review_started → no phantom Flutter spinner.
	pub.events = nil
	pr.UpdatedAt = time.Now().Add(5 * time.Minute)
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := pub.types(); !equalStringSlices(got, []string{"review_skipped"}) {
		t.Fatalf("second-run events: got %v, want exactly [review_skipped]", got)
	}
	ev, _ := pub.firstOf("review_skipped")
	wantReason := `"reason":"sha_unchanged"`
	if !strings.Contains(ev.Data, wantReason) {
		t.Errorf("review_skipped payload missing %s, got %q", wantReason, ev.Data)
	}
}

// TestPipeline_Run_LegacyBackfillEmitsReviewSkipped covers the
// legacy-row backfill branch: a previous review row with empty
// HeadSHA is backfilled and the run skips. Must emit
// review_skipped(legacy_backfill), nothing else.
func TestPipeline_Run_LegacyBackfillEmitsReviewSkipped(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	prRow := &store.PR{
		GithubID: 100, Repo: "org/repo", Number: 2, Title: "t",
		Author: "alice", State: "open",
		UpdatedAt: time.Now(), FetchedAt: time.Now(),
	}
	prID, _ := s.UpsertPR(prRow)
	if _, err := s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
		Severity: "low", CreatedAt: time.Now().Add(-1 * time.Hour),
		HeadSHA: "", // legacy row
	}); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	pub := &fakePublisher{}
	p := pipeline.New(s, &fakeGHCounter{diff: "+line"}, &fakeExecCounter{}, &fakeNotify{})
	p.SetPublisher(pub)

	pr := &github.PullRequest{
		ID: 100, Number: 2, Title: "t", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/2",
		Head: github.Branch{SHA: "abc123"},
	}
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := pub.types(); !equalStringSlices(got, []string{"review_skipped"}) {
		t.Fatalf("events: got %v, want [review_skipped]", got)
	}
	ev, _ := pub.firstOf("review_skipped")
	if !strings.Contains(ev.Data, `"reason":"legacy_backfill"`) {
		t.Errorf("review_skipped payload missing legacy_backfill reason, got %q", ev.Data)
	}
}

// TestPipeline_Run_GateSkipEmitsReviewSkipped covers the
// defense-in-depth Evaluate skip: a closed/draft/self-authored PR
// must surface a review_skipped event with the actual reason from
// the gate, not a fabricated one. Pre-#322 the trigger handler
// invented "not_open" for every nil return — now the pipeline owns
// the truth.
func TestPipeline_Run_GateSkipEmitsReviewSkipped(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	pub := &fakePublisher{}
	p := pipeline.New(s, &fakeGHCounter{diff: "+line"}, &fakeExecCounter{}, &fakeNotify{})
	p.SetPublisher(pub)

	pr := &github.PullRequest{
		ID: 200, Number: 200, Title: "t", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "closed", // not_open
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/200",
		Head: github.Branch{SHA: "abc"},
	}
	opts := pipeline.RunOptions{
		Primary: "claude",
		Guards:  pipeline.GateConfig{SkipDrafts: true, SkipSelfAuthor: true, BotLogin: "heimdallm-bot"},
	}
	if _, err := p.Run(pr, opts); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := pub.types(); !equalStringSlices(got, []string{"review_skipped"}) {
		t.Fatalf("events: got %v, want [review_skipped]", got)
	}
	ev, _ := pub.firstOf("review_skipped")
	if !strings.Contains(ev.Data, `"reason":"not_open"`) {
		t.Errorf("review_skipped payload missing not_open reason, got %q", ev.Data)
	}
}

// TestPipeline_Run_NilPublisherIsNoop guards the legacy contract: a
// pipeline with no publisher wired (every existing test that doesn't
// care about SSEs) must not panic on emit. Quietly drops events.
func TestPipeline_Run_NilPublisherIsNoop(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	p := pipeline.New(s, &fakeGHCounter{diff: "+line"}, &fakeExecCounter{}, &fakeNotify{})
	// No SetPublisher call — publisher stays nil.

	pr := &github.PullRequest{
		ID: 300, Number: 300, Title: "t", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/300",
		Head: github.Branch{SHA: "abc"},
	}
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("run with nil publisher: %v", err)
	}
}

// equalStringSlices is a tiny helper for ordered slice equality used by
// the lifecycle SSE tests above. Keeps the assertions readable without
// pulling in reflect.DeepEqual (which obscures element-level mismatches
// on failure).
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
