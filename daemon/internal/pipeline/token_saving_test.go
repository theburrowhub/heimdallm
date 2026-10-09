package pipeline_test

import (
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/store"
)

// fakeGHCompare is a GitHub fake with the incremental-diff capability.
type fakeGHCompare struct {
	fakeGHCounter
	comments    []github.Comment
	compareDiff string
	linear      bool
	compareErr  error
	fullCalls   int
	compareFrom string
}

func (f *fakeGHCompare) FetchDiff(repo string, number int) (string, error) {
	f.fullCalls++
	return f.diff, nil
}

func (f *fakeGHCompare) FetchComments(repo string, number int) ([]github.Comment, error) {
	return f.comments, nil
}

func (f *fakeGHCompare) FetchCompareDiff(repo, base, head string) (string, bool, error) {
	f.compareFrom = base
	return f.compareDiff, f.linear, f.compareErr
}

// fakePromptExec records the prompts it was given and optionally reports usage.
type fakePromptExec struct {
	prompts []string
	opts    []executor.ExecOptions
	usage   *executor.Usage
}

func (f *fakePromptExec) Detect(primary, fallback string) (string, error) { return "claude", nil }

func (f *fakePromptExec) Execute(cli, prompt string, opts executor.ExecOptions) (*executor.ReviewResult, error) {
	f.prompts = append(f.prompts, prompt)
	f.opts = append(f.opts, opts)
	return &executor.ReviewResult{
		Summary: "ok", Severity: "low",
		Issues: []executor.Issue{{File: "a.go", Line: 1, Description: "bug", Severity: "low"}},
		Usage:  f.usage,
	}, nil
}

func tokenPR(sha string) *github.PullRequest {
	return &github.PullRequest{
		ID: 900, Number: 900, Title: "feat", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/900",
		Head: github.Branch{SHA: sha},
	}
}

func allSavings() pipeline.TokenSaving {
	return pipeline.TokenSaving{IncrementalDiff: true, FilterNoise: true, CompactPrompt: true}
}

func TestRun_IncrementalDiffOnReReview(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	gh := &fakeGHCompare{
		fakeGHCounter: fakeGHCounter{diff: fileDiff("full.go", "+everything") + fileDiff("go.sum", "+noise")},
		compareDiff:   fileDiff("new.go", "+only the new commit"),
		linear:        true,
	}
	exec := &fakePromptExec{}
	p := pipeline.New(s, gh, exec, &fakeNotify{})
	p.SetBotLogin("heimdallm-bot")

	pr := tokenPR("aaaaaaa1")
	opts := pipeline.RunOptions{Primary: "claude", TokenSaving: allSavings(), Force: true}
	rev, err := p.Run(pr, opts)
	if err != nil || rev == nil {
		t.Fatalf("first review: %v", err)
	}
	first := exec.prompts[0]
	if !strings.Contains(first, "full.go") || strings.Contains(first, "diff --git a/go.sum") {
		t.Errorf("first review must use the filtered full diff:\n%s", first)
	}
	if !strings.Contains(first, "Review this pull request as a senior engineer.") {
		t.Error("compact template not used")
	}
	if !exec.opts[0].ReportUsage {
		t.Error("ReportUsage must be requested")
	}

	pr.Head.SHA = "bbbbbbb2"
	pr.UpdatedAt = time.Now().Add(time.Minute)
	if _, err := p.Run(pr, opts); err != nil {
		t.Fatalf("re-review: %v", err)
	}
	second := exec.prompts[1]
	if gh.compareFrom != "aaaaaaa1" {
		t.Errorf("compare base = %q, want the last reviewed commit", gh.compareFrom)
	}
	if !strings.Contains(second, "only the new commit") || strings.Contains(second, "+everything") {
		t.Errorf("re-review must use the incremental diff:\n%s", second)
	}
	if !strings.Contains(second, "ONLY the commits pushed since your last review (aaaaaaa1)") {
		t.Errorf("incremental note missing:\n%s", second)
	}
	if !strings.Contains(second, "[LOW] a.go:1") {
		t.Errorf("previous findings must still be in the prompt:\n%s", second)
	}
	if gh.fullCalls != 1 {
		t.Errorf("full diff fetched %d times, want only for the first review", gh.fullCalls)
	}
}

func TestRun_IncrementalDiffFallsBackWhenNotLinear(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gh := &fakeGHCompare{fakeGHCounter: fakeGHCounter{diff: "+full diff"}, linear: false}
	exec := &fakePromptExec{}
	p := pipeline.New(s, gh, exec, &fakeNotify{})
	opts := pipeline.RunOptions{Primary: "claude", TokenSaving: allSavings(), Force: true}
	pr := tokenPR("aaaaaaa1")
	if _, err := p.Run(pr, opts); err != nil {
		t.Fatal(err)
	}
	pr.Head.SHA = "bbbbbbb2"
	if _, err := p.Run(pr, opts); err != nil {
		t.Fatal(err)
	}
	if gh.fullCalls != 2 || strings.Contains(exec.prompts[1], "ONLY the commits") {
		t.Errorf("non-linear range must use the full diff: fullCalls=%d", gh.fullCalls)
	}
}

func TestRun_TokenSavingOffKeepsDefaultPrompt(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gh := &fakeGHCompare{fakeGHCounter: fakeGHCounter{diff: fileDiff("go.sum", "+x") + fileDiff("a.go", "+y")}, linear: true}
	exec := &fakePromptExec{}
	p := pipeline.New(s, gh, exec, &fakeNotify{})
	if _, err := p.Run(tokenPR("aaaaaaa1"), pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(exec.prompts[0], "You are a senior software engineer performing a pull request code review.") ||
		!strings.Contains(exec.prompts[0], "diff --git a/go.sum") {
		t.Errorf("with every measure off the prompt must be the original one:\n%s", exec.prompts[0])
	}
}

func TestRun_CompactCommentsSkipBotAndContextDuplicates(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gh := &fakeGHCompare{fakeGHCounter: fakeGHCounter{diff: "+x"}}
	exec := &fakePromptExec{}
	p := pipeline.New(s, gh, exec, &fakeNotify{})
	p.SetBotLogin("heimdallm-bot")
	opts := pipeline.RunOptions{Primary: "claude", TokenSaving: pipeline.TokenSaving{CompactPrompt: true}, Force: true}

	pr := tokenPR("aaaaaaa1")
	long := strings.Repeat("long ", 400)
	gh.comments = []github.Comment{
		{Author: "carol", Body: "early concern " + long, CreatedAt: time.Now().Add(-time.Hour)},
		{Author: "heimdallm-bot", Body: "BOT REVIEW BODY", CreatedAt: time.Now().Add(-time.Hour)},
	}
	if _, err := p.Run(pr, opts); err != nil {
		t.Fatal(err)
	}
	first := exec.prompts[0]
	if strings.Contains(first, "BOT REVIEW BODY") {
		t.Error("the bot's own comments must be left out")
	}
	if !strings.Contains(first, "early concern") || !strings.Contains(first, "…(truncated)") {
		t.Errorf("long comment must be kept but trimmed:\n%s", first)
	}

	gh.comments = append(gh.comments, github.Comment{Author: "dave", Body: "NEW AFTER REVIEW", CreatedAt: time.Now().Add(time.Hour)})
	pr.Head.SHA = "bbbbbbb2"
	if _, err := p.Run(pr, opts); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(exec.prompts[1], "NEW AFTER REVIEW"); n != 1 {
		t.Errorf("a comment made after the last review must appear once (in the re-review context), got %d", n)
	}
}

func TestRun_StoresTokenUsage(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	exec := &fakePromptExec{usage: &executor.Usage{InputTokens: 1000, OutputTokens: 50, CacheReadTokens: 7, CostUSD: 0.02}}
	p := pipeline.New(s, &fakeGHCompare{fakeGHCounter: fakeGHCounter{diff: "+x"}}, exec, &fakeNotify{})
	rev, err := p.Run(tokenPR("aaaaaaa1"), pipeline.RunOptions{Primary: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetReview(rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != 1000 || got.OutputTokens != 50 || got.CacheReadTokens != 7 || got.CostUSD != 0.02 ||
		got.TokensEstimated || got.PromptBytes != int64(len(exec.prompts[0])) {
		t.Errorf("stored usage = %+v", got)
	}

	exec.usage = nil
	rev2, err := p.Run(tokenPR("ccccccc3"), pipeline.RunOptions{Primary: "claude", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	got2, _ := s.GetReview(rev2.ID)
	if !got2.TokensEstimated || got2.InputTokens == 0 {
		t.Errorf("an agent without usage must get an estimate: %+v", got2)
	}
}
