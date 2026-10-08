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

// fakeAgentsExec reports only the CLIs in installed and records which one
// ran each review.
type fakeAgentsExec struct {
	installed map[string]bool
	ran       []string
}

func (f *fakeAgentsExec) Detect(primary, fallback string) (string, error) {
	for _, c := range []string{primary, fallback} {
		if c != "" && f.installed[c] {
			return c, nil
		}
	}
	return "", fmt.Errorf("no AI CLI available (tried %q, %q)", primary, fallback)
}

func (f *fakeAgentsExec) Execute(cli, _ string, _ executor.ExecOptions) (*executor.ReviewResult, error) {
	f.ran = append(f.ran, cli)
	return &executor.ReviewResult{Summary: "ok", Severity: "low"}, nil
}

func budgetPR(n int) *github.PullRequest {
	return &github.PullRequest{
		ID: int64(1000 + n), Number: n, Title: "t", Repo: "acme/api",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(),
		HTMLURL:   fmt.Sprintf("https://github.com/acme/api/pull/%d", n),
		Head:      github.Branch{SHA: fmt.Sprintf("sha-%d", n)},
	}
}

func newBudgetPipeline(t *testing.T, installed ...string) (*pipeline.Pipeline, *fakeAgentsExec, *fakePublisher) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	exec := &fakeAgentsExec{installed: map[string]bool{}}
	for _, c := range installed {
		exec.installed[c] = true
	}
	pub := &fakePublisher{}
	p := pipeline.New(s, &fakeGHCounter{diff: "+line"}, exec, &fakeNotify{})
	p.SetPublisher(pub)
	p.SetBotLogin("heimdallm-bot")
	return p, exec, pub
}

func lastSkip(t *testing.T, pub *fakePublisher) map[string]any {
	t.Helper()
	pub.mu.Lock()
	defer pub.mu.Unlock()
	for i := len(pub.events) - 1; i >= 0; i-- {
		if pub.events[i].Type == "review_skipped" {
			var m map[string]any
			if err := json.Unmarshal([]byte(pub.events[i].Data), &m); err != nil {
				t.Fatalf("decode skip: %v", err)
			}
			return m
		}
	}
	t.Fatal("no review_skipped event")
	return nil
}

func TestRun_GlobalBudgetDefersThenForceStillRuns(t *testing.T) {
	p, exec, pub := newBudgetPipeline(t, "claude")
	opts := pipeline.RunOptions{
		Primary: "claude",
		Budgets: pipeline.ReviewBudgets{Scopes: []pipeline.ReviewBudgetScope{
			{Kind: "global", Limits: pipeline.ReviewWindowLimits{PerMinute: 1}},
		}},
	}

	if rev, err := p.Run(budgetPR(1), opts); err != nil || rev == nil {
		t.Fatalf("first review: rev=%v err=%v", rev, err)
	}
	rev, err := p.Run(budgetPR(2), opts)
	if err != nil || rev != nil {
		t.Fatalf("second review must be deferred with (nil, nil), got rev=%v err=%v", rev, err)
	}
	if len(exec.ran) != 1 {
		t.Fatalf("agent ran %d times, want 1", len(exec.ran))
	}
	skip := lastSkip(t, pub)
	if skip["reason"] != "review_limit" || skip["limit_window"] != "minute" || skip["limit_scope"] != "global" {
		t.Errorf("skip = %v, want review_limit on the global minute window", skip)
	}
	retryAt, err := time.Parse(time.RFC3339, fmt.Sprint(skip["retry_at"]))
	if err != nil || retryAt.Before(time.Now()) || retryAt.After(time.Now().Add(61*time.Second)) {
		t.Errorf("retry_at = %v (%v), want within the next minute", skip["retry_at"], err)
	}

	opts.Force = true
	if rev, err := p.Run(budgetPR(2), opts); err != nil || rev == nil {
		t.Fatalf("forced review must run over budget: rev=%v err=%v", rev, err)
	}
}

func TestRun_RepoBudgetOnlyCountsThatRepo(t *testing.T) {
	p, exec, _ := newBudgetPipeline(t, "claude")
	budgets := pipeline.ReviewBudgets{Scopes: []pipeline.ReviewBudgetScope{
		{Kind: "repo", Key: "acme/api", Limits: pipeline.ReviewWindowLimits{PerDay: 1}},
	}}
	other := budgetPR(10)
	other.Repo = "acme/web"
	if _, err := p.Run(other, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Run(budgetPR(11), pipeline.RunOptions{Primary: "claude", Budgets: budgets}); err != nil {
		t.Fatal(err)
	}
	if len(exec.ran) != 2 {
		t.Fatalf("a review of another repo must not spend acme/api's budget; ran=%v", exec.ran)
	}
	if _, err := p.Run(budgetPR(12), pipeline.RunOptions{Primary: "claude", Budgets: budgets}); err != nil {
		t.Fatal(err)
	}
	if len(exec.ran) != 2 {
		t.Fatalf("second acme/api review must be deferred; ran=%v", exec.ran)
	}
}

func TestRun_AgentBudgetFallsBackThenDefers(t *testing.T) {
	p, exec, pub := newBudgetPipeline(t, "claude", "codex")
	opts := pipeline.RunOptions{
		Primary:  "claude",
		Fallback: "codex",
		Budgets: pipeline.ReviewBudgets{Agents: map[string]pipeline.ReviewWindowLimits{
			"claude": {PerHour: 1},
			"codex":  {PerHour: 1},
		}},
	}
	for i := 1; i <= 3; i++ {
		if _, err := p.Run(budgetPR(20+i), opts); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if got := strings.Join(exec.ran, ","); got != "claude,codex" {
		t.Fatalf("agents ran %q, want claude then codex (fallback once claude is spent)", got)
	}
	skip := lastSkip(t, pub)
	if skip["reason"] != "review_limit" || skip["limit_window"] != "hour" {
		t.Errorf("third review skip = %v, want review_limit (hour)", skip)
	}
	if scope := fmt.Sprint(skip["limit_scope"]); scope != "agent claude" && scope != "agent codex" {
		t.Errorf("limit_scope = %q, want an agent scope", scope)
	}
}

func TestRun_AgentBudgetNoAgentInstalledKeepsDetectError(t *testing.T) {
	p, _, _ := newBudgetPipeline(t)
	_, err := p.Run(budgetPR(30), pipeline.RunOptions{
		Primary: "claude",
		Budgets: pipeline.ReviewBudgets{Agents: map[string]pipeline.ReviewWindowLimits{"claude": {PerDay: 5}}},
	})
	if err == nil || !strings.Contains(err.Error(), "detect CLI") {
		t.Fatalf("err = %v, want the detect CLI error", err)
	}
}

func TestReviewBudgetStatus_ReportsUsage(t *testing.T) {
	p, _, _ := newBudgetPipeline(t, "claude")
	if _, err := p.Run(budgetPR(40), pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatal(err)
	}
	scopes := []pipeline.ReviewBudgetScope{
		{Kind: "global", Limits: pipeline.ReviewWindowLimits{PerMinute: 5, PerDay: 50}},
		{Kind: "agent", Key: "codex", Limits: pipeline.ReviewWindowLimits{PerHour: 2}},
	}
	got, err := p.ReviewBudgetStatus(scopes, time.Now().UTC())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(got) != 2 || len(got[0].Windows) != 2 || len(got[1].Windows) != 1 {
		t.Fatalf("status = %+v", got)
	}
	if w := got[0].Windows[0]; w.Window != "minute" || w.Used != 1 || w.Limit != 5 || w.ResetAt.IsZero() {
		t.Errorf("global minute = %+v, want 1/5 with a reset time", w)
	}
	if w := got[1].Windows[0]; w.Used != 0 || !w.ResetAt.IsZero() {
		t.Errorf("codex hour = %+v, want unused", w)
	}
}
