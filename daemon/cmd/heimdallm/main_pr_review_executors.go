package main

import (
	"context"
	"strings"
	"sync"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	issuepipeline "github.com/heimdallm/daemon/internal/issues"
)

// prReviewExecutor implements issuepipeline.ResponderExecutor by
// invoking the configured CLI in a review-only fashion. The agent
// produces a free-text reply; we trim and return it. The executor
// deliberately does NOT pass a WorkDir — the responder does not need
// the repository checkout and refusing access keeps the security
// blast radius small (matches the issue triage pipeline's posture
// from #478).
type prReviewExecutor struct {
	runner *executor.Executor
	cfg    **config.Config
	cfgMu  *sync.Mutex
}

func (p *prReviewExecutor) GenerateReviewResponse(_ context.Context, prompt string) (string, error) {
	primary, fallback, opts := p.dispatchInputs()
	cli, err := p.runner.Detect(primary, fallback)
	if err != nil {
		return "", err
	}
	raw, err := p.runner.ExecuteRaw(cli, prompt, opts)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func (p *prReviewExecutor) dispatchInputs() (string, string, executor.ExecOptions) {
	p.cfgMu.Lock()
	defer p.cfgMu.Unlock()
	c := *p.cfg
	// Use the global AI primary/fallback. Per-repo overrides for the
	// review-state vigilance flow are out of scope (#482 follow-up).
	return c.AI.Primary, c.AI.Fallback, executor.ExecOptions{}
}

// Compile-time check.
var _ issuepipeline.ResponderExecutor = (*prReviewExecutor)(nil)

// prFixExecutor implements issuepipeline.FixExecutor. This first
// cut of phase 3 generates an advisory response — the agent reads
// the CR feedback and writes a concrete fix plan (file paths,
// function names, change shape) as a PR comment. The actual git
// checkout + commit + push is a follow-up that will extend this
// executor's behaviour without changing the FixRunner cap/cooldown
// surface.
type prFixExecutor struct {
	runner *executor.Executor
	cfg    **config.Config
	cfgMu  *sync.Mutex
}

func (p *prFixExecutor) GenerateFixResponse(_ context.Context, prompt string) (string, error) {
	primary, fallback, opts := p.dispatchInputs()
	cli, err := p.runner.Detect(primary, fallback)
	if err != nil {
		return "", err
	}
	raw, err := p.runner.ExecuteRaw(cli, prompt, opts)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func (p *prFixExecutor) dispatchInputs() (string, string, executor.ExecOptions) {
	p.cfgMu.Lock()
	defer p.cfgMu.Unlock()
	c := *p.cfg
	return c.AI.Primary, c.AI.Fallback, executor.ExecOptions{}
}

// Compile-time check.
var _ issuepipeline.FixExecutor = (*prFixExecutor)(nil)
