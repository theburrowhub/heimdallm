package pipeline_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

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
	if err := f.failWith[cli]; err != nil {
		return nil, err
	}
	return &executor.ReviewResult{Summary: "ok", Severity: "low"}, nil
}

func flowPipeline(t *testing.T, exec *scriptedExec) (*pipeline.Pipeline, *fakePublisher) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	pub := &fakePublisher{}
	p := pipeline.New(s, &fakeGHCounter{diff: "+line"}, exec, &fakeNotify{})
	p.SetPublisher(pub)
	return p, pub
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

func TestRun_FlowStopsOnNonQuotaError(t *testing.T) {
	exec := &scriptedExec{
		installed: map[string]bool{"claude": true, "codex": true},
		failWith:  map[string]error{"claude": errors.New("parse JSON result: invalid character")},
	}
	p, _ := flowPipeline(t, exec)
	_, err := p.Run(budgetPR(51), pipeline.RunOptions{Primary: "claude", Candidates: func() []string { return []string{"claude", "codex"} }})
	if err == nil || strings.Join(exec.ran, ",") != "claude" {
		t.Fatalf("a broken review must not spend another agent: err=%v ran=%v", err, exec.ran)
	}
	exec.failWith["codex"] = errors.New("429 too many requests")
	exec.failWith["claude"] = errors.New("rate limit")
	exec.ran = nil
	if _, err := p.Run(budgetPR(52), pipeline.RunOptions{Primary: "claude", Candidates: func() []string { return []string{"claude", "codex"} }}); err == nil || strings.Join(exec.ran, ",") != "claude,codex" {
		t.Fatalf("every agent out of quota must fail after trying all: err=%v ran=%v", err, exec.ran)
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
