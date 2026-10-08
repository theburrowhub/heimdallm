package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/flows"
	"github.com/heimdallm/daemon/internal/harness/openrouter"
	"github.com/heimdallm/daemon/internal/quota"
	"github.com/heimdallm/daemon/internal/server"
)

// newQuotaService wires every agent whose remaining quota a flow can test.
func newQuotaService(admin *openrouter.Admin) *quota.Service {
	return quota.NewService(
		quota.NewClaudeSource(),
		quota.NewCodexSource(),
		quota.NewCopilotSource(),
		quota.NewGeminiSource(),
		quota.FuncSource{ID: "openrouter", Fn: func(ctx context.Context) quota.Provider {
			if admin == nil || admin.KeySource() == "" {
				return quota.Provider{Agent: "openrouter", Error: quota.ErrNotConfigured}
			}
			info, err := admin.Client.KeyInfo(ctx)
			if err != nil {
				return quota.Provider{Agent: "openrouter", Error: quota.ErrUnavailable}
			}
			return quota.CreditProvider("openrouter", info.Usage, info.Limit)
		}},
	)
}

// agentExecOptions resolves one agent's execution options from its settings,
// for whichever agent a review flow hands the review to.
func agentExecOptions(cli string, agentCfg config.CLIAgentConfig, globalTimeout, workDir string, limitExploration bool) executor.ExecOptions {
	maxTurns, effort := limitedExploration(cli, agentCfg.MaxTurns, agentCfg.Effort, limitExploration)
	extraFlags := agentCfg.ExtraFlags
	if extraFlags != "" {
		if err := executor.ValidateExtraFlagsForCLI(cli, extraFlags); err != nil {
			slog.Warn("buildRunOpts: extra_flags from config rejected", "agent", cli, "err", err)
			extraFlags = ""
		}
	}
	return executor.ExecOptions{
		Model:                agentCfg.Model,
		MaxTurns:             maxTurns,
		ApprovalMode:         agentCfg.ApprovalMode,
		ExtraFlags:           extraFlags,
		WorkDir:              workDir,
		Effort:               effort,
		PermissionMode:       agentCfg.PermissionMode,
		Bare:                 agentCfg.Bare,
		DangerouslySkipPerms: agentCfg.DangerouslySkipPerms,
		NoSessionPersistence: agentCfg.NoSessionPersistence,
		Timeout:              resolveExecutionTimeout(globalTimeout, agentCfg.ExecutionTimeout),
	}
}

// flowAgents lists the distinct agents a flow can pick.
func flowAgents(f config.FlowConfig) []string {
	var out []string
	seen := map[string]bool{}
	for _, key := range f.OrderedRuleKeys() {
		if a := f.Rules[key].Agent; !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// flowCandidates evaluates repo's flow now and logs the decision, for the
// pipeline's agent selection.
func flowCandidates(flowID string, flow config.FlowConfig, repo string, quotas flows.QuotaReader, available func(string) bool) func() []string {
	return func() []string {
		d := flows.Evaluate(context.Background(), flowID, flow, time.Now(), quotas, available)
		slog.Info("review flow evaluated", "repo", repo, "flow", flowID, "candidates", d.Candidates)
		for _, r := range d.Rules {
			slog.Debug("review flow rule", "repo", repo, "flow", flowID, "rule", r.Key, "agent", r.Agent,
				"matched", r.Matched, "available", r.Available, "reasons", r.Reasons)
		}
		return d.Candidates
	}
}

// flowListing is the GET /flows body: every flow (the default one included)
// and the global selection.
func flowListing(c *config.Config) map[string]any {
	selected := c.AI.Flow
	if selected == "" {
		selected = config.DefaultFlowID
	}
	return map[string]any{
		"flows":         c.AllFlows(),
		"selected":      selected,
		"default_id":    config.DefaultFlowID,
		"agents":        executor.SupportedCLIs(),
		"write_capable": writeCapableAgents(),
	}
}

func writeCapableAgents() []string {
	var out []string
	for _, a := range executor.SupportedCLIs() {
		if executor.WriteCapable(a) {
			out = append(out, a)
		}
	}
	return out
}

// simulateFlow evaluates the requested flow (or the one req.Repo resolves
// to) at req.At, defaulting to now.
func simulateFlow(ctx context.Context, c *config.Config, req server.FlowSimulation, quotas flows.QuotaReader, available func(string) bool) (flows.Decision, error) {
	at := req.At
	if at.IsZero() {
		at = time.Now()
	}
	if req.Flow != "" {
		f, ok := c.AllFlows()[req.Flow]
		if !ok {
			return flows.Decision{}, fmt.Errorf("no flow %q", req.Flow)
		}
		return flows.Evaluate(ctx, req.Flow, f, at, quotas, available), nil
	}
	id, f := c.FlowForRepo(req.Repo)
	return flows.Evaluate(ctx, id, f, at, quotas, available), nil
}
