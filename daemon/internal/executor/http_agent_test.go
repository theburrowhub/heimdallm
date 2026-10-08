package executor_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
)

type fakeHTTPAgent struct {
	configured bool
	answer     string
	err        error
	block      bool
	started    chan struct{}
	gotOpts    executor.ExecOptions
}

func (f *fakeHTTPAgent) Configured() bool { return f.configured }

func (f *fakeHTTPAgent) Review(ctx context.Context, prompt string, opts executor.ExecOptions) (string, *executor.Usage, error) {
	f.gotOpts = opts
	if f.started != nil {
		close(f.started)
	}
	if f.block {
		<-ctx.Done()
		return "", nil, ctx.Err()
	}
	return f.answer, &executor.Usage{InputTokens: 10, OutputTokens: 2, CostUSD: 0.01}, f.err
}

func TestHTTPAgent_DetectAndExecute(t *testing.T) {
	e := executor.New()
	if _, err := e.Detect("openrouter", ""); err == nil {
		t.Fatal("an unregistered HTTP agent is not available")
	}
	agent := &fakeHTTPAgent{answer: `{"summary":"ok","issues":[],"severity":"low"}`}
	if err := e.RegisterHTTPAgent("openrouter", agent); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Detect("openrouter", ""); err == nil {
		t.Fatal("an unconfigured HTTP agent is not available")
	}
	if _, err := e.Execute("openrouter", "p", executor.ExecOptions{}); err == nil {
		t.Fatal("executing an unconfigured HTTP agent must fail")
	}
	agent.configured = true
	if got, err := e.Detect("openrouter", "claude"); err != nil || got != "openrouter" {
		t.Fatalf("Detect = %q, %v", got, err)
	}
	res, err := e.Execute("openrouter", "p", executor.ExecOptions{Model: "anthropic/claude-sonnet-4.5", MaxTurns: 5})
	if err != nil || res.Summary != "ok" || res.Usage == nil || res.Usage.CostUSD != 0.01 {
		t.Fatalf("Execute = %+v, %v", res, err)
	}
	if agent.gotOpts.Model != "anthropic/claude-sonnet-4.5" || agent.gotOpts.MaxTurns != 5 {
		t.Errorf("options passed = %+v", agent.gotOpts)
	}
	if executor.ResolveCLIPath("openrouter") != "" {
		t.Error("an HTTP agent has no executable path")
	}
}

func TestHTTPAgent_ErrorsAndPolicy(t *testing.T) {
	e := executor.New()
	if err := e.RegisterHTTPAgent("claude", &fakeHTTPAgent{}); err == nil {
		t.Fatal("only http-only ids can be registered")
	}
	agent := &fakeHTTPAgent{configured: true, err: errors.New("upstream 500")}
	_ = e.RegisterHTTPAgent("openrouter", agent)
	if _, err := e.Execute("openrouter", "p", executor.ExecOptions{}); err == nil || !strings.Contains(err.Error(), "upstream 500") {
		t.Errorf("agent error = %v", err)
	}
	agent.err, agent.answer = nil, "not json"
	if _, err := e.Execute("openrouter", "p", executor.ExecOptions{}); err == nil {
		t.Error("a non-JSON answer must fail to parse")
	}
	if _, err := e.ExecuteRaw("openrouter", "p", executor.ExecOptions{}); !errors.Is(err, executor.ErrReviewOnlyAgent) {
		t.Errorf("ExecuteRaw = %v, want ErrReviewOnlyAgent", err)
	}
	if _, err := e.Execute("openrouter", "p", executor.ExecOptions{ExtraFlags: "--anything"}); err == nil {
		t.Error("the OpenRouter agent takes no extra flags")
	}
}

func TestHTTPAgent_CancelAndTimeout(t *testing.T) {
	e := executor.New()
	agent := &fakeHTTPAgent{configured: true, block: true, started: make(chan struct{})}
	_ = e.RegisterHTTPAgent("openrouter", agent)
	done := make(chan error, 1)
	go func() {
		_, err := e.Execute("openrouter", "p", executor.ExecOptions{ExecutionID: "pr-review:1"})
		done <- err
	}()
	<-agent.started
	if ok, err := e.TerminateExecution("pr-review:1"); !ok || err != nil {
		t.Fatalf("TerminateExecution = %v, %v", ok, err)
	}
	if err := <-done; !errors.Is(err, executor.ErrExecutionCancelled) {
		t.Fatalf("cancelled run = %v", err)
	}

	agent.started = make(chan struct{})
	go func() {
		_, err := e.Execute("openrouter", "p", executor.ExecOptions{})
		done <- err
	}()
	<-agent.started
	e.TerminateAll()
	if err := <-done; err == nil {
		t.Fatal("TerminateAll must stop an in-process run")
	}

	agent.started = make(chan struct{})
	_, err := e.Execute("openrouter", "p", executor.ExecOptions{Timeout: 20 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout = %v", err)
	}
}
