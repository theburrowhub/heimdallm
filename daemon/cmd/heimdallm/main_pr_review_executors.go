package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	gh "github.com/heimdallm/daemon/internal/github"
	issuepipeline "github.com/heimdallm/daemon/internal/issues"
	"github.com/heimdallm/daemon/internal/repoctx"
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

// prFixExecutor implements issuepipeline.FixExecutor by delegating
// to Pipeline.RunFix — the actual checkout + agent + commit + push
// flow for #482 phase 3. Responsibilities split:
//
//   - This executor owns the worktree lifecycle (reserve via
//     repoctx, release on return) and the PR-head-ref fetch.
//   - Pipeline.RunFix owns the git plumbing and the agent run.
//   - issuepipeline.FixRunner owns the cap/cooldown/state-flip logic
//     and only calls Run once the guards have passed.
//
// repoctx.Manager is the same per-execution worktree manager used by
// auto_implement (#461), so concurrent fix runs on the same repo are
// properly isolated.
type prFixExecutor struct {
	pipeline   *issuepipeline.Pipeline
	repoCtx    *repoctx.Manager
	ghClient   *gh.Client
	ghToken    string
	cfg        **config.Config
	cfgMu      *sync.Mutex
}

func (p *prFixExecutor) RunFix(ctx context.Context, req issuepipeline.FixRequest) (issuepipeline.FixResult, error) {
	// Hydrate the missing fields. The FixRunner only knows the
	// store-side PR view; the head ref name + repo-scoped CLI inputs
	// come from here.
	full, err := p.ghClient.GetPR(req.Repo, req.PRNumber)
	if err != nil {
		return issuepipeline.FixResult{}, fmt.Errorf("get pr: %w", err)
	}
	headRef := strings.TrimSpace(full.Head.Ref)
	if headRef == "" {
		return issuepipeline.FixResult{}, fmt.Errorf("pr %s#%d has no head ref (cross-fork PR?)", req.Repo, req.PRNumber)
	}

	primary, fallback := p.cliInputs(req.Repo)

	// Reserve a per-execution worktree (#461). The Acquire path
	// requires WorktreeToken so different runs on the same repo
	// land on different paths; `fix-pr-N` is deterministic per PR
	// and prevents accidental collision when the fix runner re-fires
	// after a reviewer pushes another CR.
	handle, err := p.repoCtx.Acquire(ctx, repoctx.Request{
		Repo:          req.Repo,
		Token:         p.ghToken,
		Mode:          repoctx.ModeWrite,
		WorktreeToken: fmt.Sprintf("fix-pr-%d", req.PRNumber),
	})
	if err != nil {
		return issuepipeline.FixResult{}, fmt.Errorf("reserve worktree: %w", err)
	}
	defer handle.Release()

	req.HeadRef = headRef
	req.Token = p.ghToken
	req.ExecOpts = executor.ExecOptions{WorkDir: handle.Path()}
	req.CLIPrimary = primary
	req.CLIFallback = fallback

	return p.pipeline.RunFix(ctx, req)
}

func (p *prFixExecutor) cliInputs(repo string) (primary, fallback string) {
	p.cfgMu.Lock()
	defer p.cfgMu.Unlock()
	c := *p.cfg
	aiCfg := c.AIForRepo(repo)
	primary = aiCfg.Primary
	if primary == "" {
		primary = c.AI.Primary
	}
	fallback = aiCfg.Fallback
	if fallback == "" {
		fallback = c.AI.Fallback
	}
	return primary, fallback
}

// Compile-time check.
var _ issuepipeline.FixExecutor = (*prFixExecutor)(nil)
