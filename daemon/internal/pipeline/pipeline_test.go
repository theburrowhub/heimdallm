package pipeline_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/heimdallr/daemon/internal/executor"
	"github.com/heimdallr/daemon/internal/github"
	"github.com/heimdallr/daemon/internal/pipeline"
	"github.com/heimdallr/daemon/internal/store"
)

type fakeGH struct {
	diff string
}

func (f *fakeGH) FetchDiff(repo string, number int) (string, error) {
	return f.diff, nil
}

func (f *fakeGH) SubmitReview(repo string, number int, body, event string) (int64, error) {
	return 12345, nil // fake GitHub review ID
}

type fakeExec struct{}

func (f *fakeExec) Detect(primary, fallback string) (string, error) {
	return "fake_claude", nil
}

func (f *fakeExec) Execute(cli, prompt string) (*executor.ReviewResult, error) {
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

	rev, err := p.Run(pr, "claude", "gemini")
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
