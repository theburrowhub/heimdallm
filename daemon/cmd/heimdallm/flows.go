package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/heimdallm/daemon/internal/agentcatalog"
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
				// The client's errors never carry the key; logging them is
				// what tells a rejected key from an outage.
				slog.Warn("quota: openrouter key info failed", "err", err)
				return quota.Provider{Agent: "openrouter", Error: quota.ErrUnavailable}
			}
			return quota.CreditProvider("openrouter", info.Usage, info.Limit)
		}},
	)
}

// agentExecOptions resolves one agent's execution options from its settings,
// for whichever agent a review flow hands the review to.
func agentExecOptions(cli string, agentCfg config.CLIAgentConfig, globalTimeout, workDir string, limitExploration bool) executor.ExecOptions {
	maxTurns, softTurns := limitedExploration(cli, agentCfg.MaxTurns, limitExploration)
	extraFlags := agentCfg.ExtraFlags
	if extraFlags != "" {
		if err := executor.ValidateExtraFlagsForCLI(cli, extraFlags); err != nil {
			slog.Warn("agentExecOptions: extra_flags from config rejected", "agent", cli, "err", err)
			extraFlags = ""
		}
	}
	return executor.ExecOptions{
		Model:                agentCfg.Model,
		MaxTurns:             maxTurns,
		SoftMaxTurns:         softTurns,
		ApprovalMode:         agentCfg.ApprovalMode,
		ExtraFlags:           extraFlags,
		WorkDir:              workDir,
		Effort:               agentCfg.Effort,
		PermissionMode:       agentCfg.PermissionMode,
		Bare:                 agentCfg.Bare,
		DangerouslySkipPerms: agentCfg.DangerouslySkipPerms,
		NoSessionPersistence: agentCfg.NoSessionPersistence,
		Timeout:              resolveExecutionTimeout(globalTimeout, agentCfg.ExecutionTimeout),
		CustomValues:         customAgentValues(cli, agentCfg),
	}
}

// customAgentValues returns a custom agent's option values for the executor,
// which validates them against the agent's schema and turns them into flags.
// Built-in agents have none.
func customAgentValues(cli string, agentCfg config.CLIAgentConfig) *executor.CustomValues {
	if !agentCfg.IsCustom() || !executor.IsCustomAgent(cli) {
		return nil
	}
	values := make(map[string]any, len(agentCfg.Options))
	for k, v := range agentCfg.Options {
		values[k] = v
	}
	return executor.NewCustomValues(values)
}

// flowAgents lists the distinct agents a flow can pick.
func flowAgents(f config.FlowConfig) []string { return config.TopoAgentOrder(f) }

// modelRequiredAgents have no default model: without one configured they
// cannot review (OpenRouter's catalog changes too often for a built-in id).
// Keep in sync with knownProviderAgentIds in
// flutter_app/lib/core/models/cli_agent.dart.
var modelRequiredAgents = map[string]bool{"openrouter": true}

// flowAgentAvailable reports whether a flow may pick an agent: it is
// installed or configured (detected), and an agent that needs a model has
// one. modelFor returns the agent's live configured model.
func flowAgentAvailable(detected func(string) bool, modelFor func(string) string) func(string) bool {
	return func(agent string) bool {
		if !detected(agent) {
			return false
		}
		return !modelRequiredAgents[agent] || strings.TrimSpace(modelFor(agent)) != ""
	}
}

// detectedBy adapts an executor's Detect to flowAgentAvailable's probe.
func detectedBy(detect func(primary, fallback string) (string, error)) func(string) bool {
	return func(agent string) bool {
		_, err := detect(agent, "")
		return err == nil
	}
}

// agentModels is a lock-free snapshot of every agent's configured model.
// Flow evaluation reads it from callbacks that run deep inside agent
// selection, so it must never take the config mutex: a caller that already
// held it would deadlock the daemon. Every config load and reload stores a
// fresh snapshot.
type agentModels struct {
	p atomic.Pointer[map[string]string]
}

func (m *agentModels) store(c *config.Config) {
	snap := map[string]string{}
	for id := range c.AI.Agents {
		// The same lookup buildRunOpts uses for an agent's options.
		snap[id] = c.AgentConfigFor(id).Model
	}
	m.p.Store(&snap)
}

// refresh stores c's models and warns about every agent that needs a model
// and has none, using detected (the probe flow availability uses) to tell
// which agents are set up. Run it on every config load and reload, after
// the config itself is swapped in, so flows never see a model buildRunOpts
// lacks.
func (m *agentModels) refresh(c *config.Config, detected func(string) bool) {
	m.store(c)
	warnModelRequiredWithoutModel(c, detected)
}

func (m *agentModels) model(agent string) string {
	if snap := m.p.Load(); snap != nil {
		return (*snap)[agent]
	}
	return ""
}

// warnModelRequiredWithoutModel logs, at startup and on every reload, each
// configured agent that needs a model and has none: flows skip it, and as
// the legacy primary it fails every review until a model is set.
func warnModelRequiredWithoutModel(c *config.Config, configured func(agent string) bool) {
	for agent := range modelRequiredAgents {
		if !configured(agent) || strings.TrimSpace(c.AgentConfigFor(agent).Model) != "" {
			continue
		}
		slog.Warn("config: agent has no model; review flows skip it and it cannot review until one is set",
			"agent", agent, "set", "[ai.agents."+agent+"] model")
	}
}

// flowCandidates evaluates repo's flow now and logs the decision, for the
// pipeline's agent selection.
// flowEvalTimeout bounds one flow evaluation. Quota reads are cached and each
// is bounded on its own; this caps the worst case of several cold reads so
// agent selection never waits on a slow provider for long.
const flowEvalTimeout = 20 * time.Second

func flowCandidates(flowID string, flow config.FlowConfig, repo string, quotas flows.QuotaReader, available func(string) bool) func() []string {
	return func() []string {
		ctx, cancel := context.WithTimeout(context.Background(), flowEvalTimeout)
		defer cancel()
		d := flows.Evaluate(ctx, flowID, flow, time.Now(), quotas, available)
		slog.Info("review flow evaluated", "repo", repo, "flow", flowID, "candidates", d.Candidates)
		for _, s := range d.Path {
			slog.Debug("review flow step", "repo", repo, "flow", flowID, "node", s.Node, "type", s.Type,
				"agent", s.Agent, "added", s.Added, "reasons", s.Reasons)
		}
		return d.Candidates
	}
}

// FlowAgent is one agent offered by the flow editor.
type FlowAgent struct {
	ID           string `json:"id"`
	Installed    bool   `json:"installed"`
	ReviewOnly   bool   `json:"review_only"`
	WriteCapable bool   `json:"write_capable"`
	// CanDraft: the assistant can run this agent (flows.CanDraft).
	CanDraft bool `json:"can_draft"`
}

// installedAgents maps every review agent the catalog found installed (or,
// for API providers, configured) by its config agent id.
func installedAgents(agents []agentcatalog.Agent) map[string]bool {
	out := map[string]bool{}
	for _, a := range agents {
		if a.Kind == agentcatalog.KindIDE {
			continue
		}
		if a.Installed || a.Configured {
			out[a.ConfigAgent] = true
		}
	}
	return out
}

// flowListing is the GET /flows body: every flow (the default one
// included), the global selection and the agents the editor can offer.
func flowListing(c *config.Config, installed map[string]bool) map[string]any {
	selected := c.AI.Flow
	if selected == "" {
		selected = config.DefaultFlowID
	}
	var agents []FlowAgent
	for _, id := range executor.SupportedCLIs() {
		agents = append(agents, FlowAgent{
			ID:           id,
			Installed:    installed[id],
			ReviewOnly:   executor.IsHTTPOnly(id) || executor.IsCustomAgent(id), // custom agents only review too
			WriteCapable: executor.WriteCapable(id),
			CanDraft:     flows.CanDraft(id),
		})
	}
	return map[string]any{
		"flows":         c.AllFlows(),
		"selected":      selected,
		"default_id":    config.DefaultFlowID,
		"agents":        agents,
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
	if req.Graph != nil {
		name := req.Flow
		if name == "" {
			name = "draft"
		}
		return flows.Evaluate(ctx, name, *req.Graph, at, quotas, available), nil
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

// rawExecutor is the part of the executor the assistant runner needs.
type rawExecutor interface {
	ExecuteRaw(cli, prompt string, opts executor.ExecOptions) ([]byte, error)
	TerminateExecution(executionID string) (bool, error)
}

// assistCancelPoll is how often a cancelled run retries TerminateExecution
// until the executor has registered the execution.
const assistCancelPoll = 25 * time.Millisecond

// execRunner runs the assistant prompt through the executor with the
// agent's own model and limits, but none of its review permission settings,
// in an empty temp workspace, and kills the process group when ctx ends.
// That is all the daemon enforces: whether the agent may still use tools
// depends on its CLI's own settings (agents whose defaults allow everything
// are refused, see flows.CanDraft).
type execRunner struct {
	exec rawExecutor
	opts func(agent, workDir string) executor.ExecOptions
}

func (r execRunner) Run(ctx context.Context, agent, prompt, workDir string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts := r.opts(agent, workDir)
	// Drafting needs no tools: clear every permission flag that would widen
	// what the agent may do, the same fields OptionsForSelectedCLI scrubs.
	opts.DangerouslySkipPerms = false
	opts.PermissionMode = ""
	opts.ApprovalMode = ""
	opts.ExtraFlags = ""
	if agent == "codex" {
		// With no WorkDir the executor gives codex its own empty workspace and
		// waives the git-repo check; a plain temp dir would make codex abort
		// with "Not inside a trusted directory".
		opts.WorkDir = ""
	}
	opts.ExecutionID = "flow-assist-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	opts.Timeout = flows.DefaultAssistTimeout
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
		case <-stop:
			return
		}
		// The executor registers an execution only after resolving the CLI, so
		// a cancel that lands earlier finds nothing: retry until it takes.
		t := time.NewTicker(assistCancelPoll)
		defer t.Stop()
		for {
			if ok, _ := r.exec.TerminateExecution(opts.ExecutionID); ok {
				return
			}
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
	return r.exec.ExecuteRaw(agent, prompt, opts)
}

// assistContext lists the installed agents and their quotas for the prompt.
func assistContext(ctx context.Context, agents []agentcatalog.Agent, quotas []quota.Provider) flows.AssistContext {
	out := flows.AssistContext{Quotas: quotas, Now: time.Now()}
	for _, a := range agents {
		if a.Kind == agentcatalog.KindIDE || !(a.Installed || a.Configured) {
			continue
		}
		out.Agents = append(out.Agents, flows.AssistAgent{
			ID:       a.ConfigAgent,
			Models:   a.Models,
			Runnable: flows.CanDraft(a.ConfigAgent),
		})
	}
	return out
}
