package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/store"
)

// scriptedExec fails the listed agents with the given errors and records
// every run with its options.
type scriptedExec struct {
	installed map[string]bool
	failWith  map[string]error
	ran       []string
	opts      map[string]executor.ExecOptions
	onRun     func(cli string) // called before the run's outcome
}

func (f *scriptedExec) Detect(primary, fallback string) (string, error) {
	for _, c := range []string{primary, fallback} {
		if c != "" && f.installed[c] {
			return c, nil
		}
	}
	return "", fmt.Errorf("no AI CLI available (tried %q, %q)", primary, fallback)
}

func (f *scriptedExec) Execute(cli, _ string, opts executor.ExecOptions) (*executor.ReviewResult, error) {
	f.ran = append(f.ran, cli)
	if f.opts == nil {
		f.opts = map[string]executor.ExecOptions{}
	}
	f.opts[cli] = opts
	if f.onRun != nil {
		f.onRun(cli)
	}
	if err := f.failWith[cli]; err != nil {
		return nil, err
	}
	return &executor.ReviewResult{Summary: "ok", Severity: "low"}, nil
}

func flowPipeline(t *testing.T, exec *scriptedExec) (*pipeline.Pipeline, *fakePublisher) {
	t.Helper()
	p, pub, _ := flowPipelineWithStore(t, exec)
	return p, pub
}

func flowPipelineWithStore(t *testing.T, exec *scriptedExec) (*pipeline.Pipeline, *fakePublisher, *store.Store) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	pub := &fakePublisher{}
	p := pipeline.New(s, &fakeGHCounter{diff: "+line"}, exec, &fakeNotify{})
	p.SetPublisher(pub)
	return p, pub, s
}

func TestRun_FlowFallsBackOnQuotaError(t *testing.T) {
	exec := &scriptedExec{
		installed: map[string]bool{"claude": true, "copilot": true, "codex": true},
		failWith:  map[string]error{"claude": errors.New("exit 1 (output: Claude AI usage limit reached)")},
	}
	p, pub := flowPipeline(t, exec)
	rev, err := p.Run(budgetPR(50), pipeline.RunOptions{
		Primary:    "claude",
		Candidates: func() []string { return []string{"claude", "copilot", "codex"} },
		AgentExecOpts: map[string]executor.ExecOptions{
			"claude":  {Model: "claude-opus"},
			"copilot": {Model: "gpt-5.5", Effort: "high"},
		},
	})
	if err != nil || rev == nil {
		t.Fatalf("Run = %v, %v", rev, err)
	}
	if got := strings.Join(exec.ran, ","); got != "claude,copilot" {
		t.Errorf("ran = %s, want claude then copilot", got)
	}
	if rev.CLIUsed != "copilot" {
		t.Errorf("review credited to %q, want copilot", rev.CLIUsed)
	}
	if o := exec.opts["copilot"]; o.Model != "gpt-5.5" || o.Effort != "high" || !o.ReportUsage || o.ExecutionID == "" {
		t.Errorf("copilot ran with %+v, want its own settings", o)
	}
	if ev, ok := pub.firstOf("review_agent_fallback"); !ok || !strings.Contains(ev.Data, `"from":"claude"`) || !strings.Contains(ev.Data, `"to":"copilot"`) {
		t.Errorf("fallback event = %+v (%v)", ev, ok)
	}
}

func TestRun_FlowFallsBackOnAgentFailure(t *testing.T) {
	// An agent that fails for its own reasons (unparseable output, a crash)
	// must not pin the review to itself: retrying the same agent first would
	// fail the same way on every retry while the next agent could review.
	exec := &scriptedExec{
		installed: map[string]bool{"copilot": true, "claude": true},
		failWith:  map[string]error{"copilot": errors.New(`executor: parse JSON result: invalid character '\n' in string literal`)},
	}
	p, pub := flowPipeline(t, exec)
	rev, err := p.Run(budgetPR(51), pipeline.RunOptions{Primary: "copilot", Candidates: func() []string { return []string{"copilot", "claude"} }})
	if err != nil || rev == nil || rev.CLIUsed != "claude" || strings.Join(exec.ran, ",") != "copilot,claude" {
		t.Fatalf("rev=%v err=%v ran=%v, want claude to review after copilot failed", rev, err, exec.ran)
	}
	if ev, ok := pub.firstOf("review_agent_fallback"); !ok || !strings.Contains(ev.Data, `"reason":"error"`) {
		t.Errorf("fallback event = %+v (%v), want reason error", ev, ok)
	}
}

func TestRun_FlowFailsAfterEveryAgentFailed(t *testing.T) {
	exec := &scriptedExec{
		installed: map[string]bool{"claude": true, "codex": true},
		failWith: map[string]error{
			"claude": errors.New("rate limit"),
			"codex":  errors.New("exit status 1"),
		},
	}
	p, pub := flowPipeline(t, exec)
	_, err := p.Run(budgetPR(52), pipeline.RunOptions{Primary: "claude", Candidates: func() []string { return []string{"claude", "codex"} }})
	if err == nil || strings.Join(exec.ran, ",") != "claude,codex" {
		t.Fatalf("every agent failing must fail after trying each once: err=%v ran=%v", err, exec.ran)
	}
	if !strings.Contains(err.Error(), "claude: rate limit") || !strings.Contains(err.Error(), "codex: exit status 1") {
		t.Errorf("err = %v, want every agent's failure", err)
	}
	if ev, ok := pub.firstOf("review_agent_fallback"); !ok || !strings.Contains(ev.Data, `"reason":"quota"`) {
		t.Errorf("fallback event = %+v (%v), want reason quota", ev, ok)
	}
}

func TestRun_FlowStopsOnContextCancellationAndLabelsTimeouts(t *testing.T) {
	exec := &scriptedExec{
		installed: map[string]bool{"claude": true, "codex": true},
		failWith:  map[string]error{"claude": fmt.Errorf("run: %w", context.Canceled)},
	}
	p, pub := flowPipeline(t, exec)
	opts := pipeline.RunOptions{Primary: "claude", Candidates: func() []string { return []string{"claude", "codex"} }}
	if _, err := p.Run(budgetPR(58), opts); err == nil || strings.Join(exec.ran, ",") != "claude" {
		t.Fatalf("a cancelled context must stop: err=%v ran=%v", err, exec.ran)
	}
	exec.ran = nil
	exec.failWith["claude"] = fmt.Errorf("run: %w", executor.ErrExecutionTimedOut)
	if _, err := p.Run(budgetPR(59), opts); err != nil || strings.Join(exec.ran, ",") != "claude,codex" {
		t.Fatalf("a timeout moves on: err=%v ran=%v", err, exec.ran)
	}
	if ev, ok := pub.firstOf("review_agent_fallback"); !ok || !strings.Contains(ev.Data, `"reason":"timeout"`) {
		t.Errorf("fallback event = %+v (%v), want reason timeout", ev, ok)
	}
}

func TestRun_FlowRetriesOfABrokenReviewUseOneAgent(t *testing.T) {
	// Every agent failed on the first run, so the review itself is the likely
	// problem: the automatic retry must not spend the whole flow again. A
	// quota failure still moves on, and a manual retry chains as before.
	exec := &scriptedExec{
		installed: map[string]bool{"claude": true, "codex": true},
		failWith: map[string]error{
			"claude": errors.New("exit status 1"),
			"codex":  errors.New("exit status 1"),
		},
	}
	p, _, st := flowPipelineWithStore(t, exec)
	pr := budgetPR(57)
	opts := pipeline.RunOptions{Primary: "claude", Candidates: func() []string { return []string{"claude", "codex"} }}
	if _, err := p.Run(pr, opts); err == nil || strings.Join(exec.ran, ",") != "claude,codex" {
		t.Fatalf("first run: err=%v ran=%v", err, exec.ran)
	}
	// What the scheduler sees once the retry cooldown has passed: the failed
	// attempt is on record, but long enough ago.
	elapse := func() {
		t.Helper()
		row, err := st.GetPRByGithubID(pr.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AdvanceReviewRetryBackoff(row.ID, pr.Head.SHA, time.Now().Add(-24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		exec.ran = nil
	}

	elapse()
	if _, err := p.Run(pr, opts); err == nil || strings.Join(exec.ran, ",") != "claude" {
		t.Fatalf("automatic retry: err=%v ran=%v, want only claude", err, exec.ran)
	}

	elapse()
	exec.failWith["claude"] = errors.New("usage limit reached")
	if _, err := p.Run(pr, opts); err == nil || strings.Join(exec.ran, ",") != "claude,codex" {
		t.Fatalf("quota failure on a retry still moves on: err=%v ran=%v", err, exec.ran)
	}

	exec.ran = nil
	exec.failWith["claude"] = errors.New("exit status 1")
	forced := opts
	forced.Force = true
	if _, err := p.Run(pr, forced); err == nil || strings.Join(exec.ran, ",") != "claude,codex" {
		t.Fatalf("manual retry chains: err=%v ran=%v", err, exec.ran)
	}
}

func TestRun_FlowDoesNotFallBackWhenCancelled(t *testing.T) {
	exec := &scriptedExec{
		installed: map[string]bool{"claude": true, "codex": true},
		failWith:  map[string]error{"claude": fmt.Errorf("run: %w", executor.ErrExecutionCancelled)},
	}
	p, _ := flowPipeline(t, exec)
	_, err := p.Run(budgetPR(56), pipeline.RunOptions{Primary: "claude", Candidates: func() []string { return []string{"claude", "codex"} }})
	if err == nil || strings.Join(exec.ran, ",") != "claude" {
		t.Fatalf("a cancelled review must stop: err=%v ran=%v", err, exec.ran)
	}
}

func TestRun_FlowWithNoAgentDefers(t *testing.T) {
	exec := &scriptedExec{installed: map[string]bool{"claude": true}}
	p, pub := flowPipeline(t, exec)
	for _, candidates := range [][]string{{}, {"copilot"}} {
		c := candidates
		rev, err := p.Run(budgetPR(53), pipeline.RunOptions{Primary: "claude", Candidates: func() []string { return c }})
		if err != nil || rev != nil || len(exec.ran) != 0 {
			t.Fatalf("candidates %v: rev=%v err=%v ran=%v", c, rev, err, exec.ran)
		}
		if skip := lastSkip(t, pub); skip["reason"] != "no_flow_agent" {
			t.Errorf("skip = %v", skip)
		}
	}
}

func TestRun_FlowFallbackRespectsAgentBudget(t *testing.T) {
	exec := &scriptedExec{
		installed: map[string]bool{"claude": true, "codex": true},
		failWith:  map[string]error{"claude": errors.New("usage limit")},
	}
	p, _ := flowPipeline(t, exec)
	// Fill codex's budget with one review, then a claude quota failure has
	// nowhere to go.
	if _, err := p.Run(budgetPR(54), pipeline.RunOptions{Primary: "codex"}); err != nil {
		t.Fatal(err)
	}
	exec.ran = nil
	_, err := p.Run(budgetPR(55), pipeline.RunOptions{
		Primary:    "claude",
		Candidates: func() []string { return []string{"claude", "codex"} },
		Budgets:    pipeline.ReviewBudgets{Agents: map[string]pipeline.ReviewWindowLimits{"codex": {PerHour: 1}}},
	})
	if err == nil || strings.Join(exec.ran, ",") != "claude" {
		t.Fatalf("codex is over budget up front, so only claude runs and its quota failure ends the review: err=%v ran=%v", err, exec.ran)
	}
}

// The next agent in the flow can fill its own review budget while the first
// one is running. The review then ends, and its error must say both why the
// first agent stopped and why the next one could not take over.
func TestRun_FlowFallbackBlockedMidRunExplainsBoth(t *testing.T) {
	exec := &scriptedExec{
		installed: map[string]bool{"claude": true, "codex": true},
		failWith:  map[string]error{"claude": errors.New("usage limit reached")},
	}
	p, _ := flowPipeline(t, exec)
	budgets := pipeline.ReviewBudgets{Agents: map[string]pipeline.ReviewWindowLimits{"codex": {PerHour: 1}}}
	exec.onRun = func(cli string) {
		if cli != "claude" {
			return
		}
		exec.onRun = nil
		// Another PR takes codex's only slot meanwhile.
		if _, err := p.Run(budgetPR(57), pipeline.RunOptions{Primary: "codex", Budgets: budgets}); err != nil {
			t.Errorf("concurrent codex review: %v", err)
		}
	}
	_, err := p.Run(budgetPR(56), pipeline.RunOptions{
		Primary:    "claude",
		Candidates: func() []string { return []string{"claude", "codex"} },
		Budgets:    budgets,
	})
	var budgetErr *pipeline.ReviewBudgetError
	if err == nil || !strings.Contains(err.Error(), "usage limit reached") || !errors.As(err, &budgetErr) || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("err = %v; want claude's quota error and codex's review limit", err)
	}
	if strings.Join(exec.ran, ",") != "claude,codex" {
		t.Errorf("runs = %v; codex only ran for the other PR", exec.ran)
	}
}
