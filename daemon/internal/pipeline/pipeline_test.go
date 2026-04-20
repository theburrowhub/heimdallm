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

func (f *fakeGH) PostComment(repo string, number int, body string) error {
	return nil
}

func (f *fakeGH) FetchComments(repo string, number int) ([]github.Comment, error) {
	return f.comments, nil
}

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

func (f *fakeGHCommentsError) PostComment(repo string, number int, body string) error {
	return nil
}

func (f *fakeGHCommentsError) FetchComments(repo string, number int) ([]github.Comment, error) {
	return nil, fmt.Errorf("network error")
}

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
