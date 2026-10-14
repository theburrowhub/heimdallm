package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/agentcatalog"
	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/harness/openrouter"
	"github.com/heimdallm/daemon/internal/quota"
	"github.com/heimdallm/daemon/internal/server"
)

func testFlowConfig() *config.Config {
	return &config.Config{AI: config.AIConfig{
		Primary:  "claude",
		Fallback: "codex",
		Agents: map[string]config.CLIAgentConfig{
			"copilot": {Model: "gpt-5.5", Effort: "high", ExtraFlags: "--allow-all-tools"},
		},
		Flows: map[string]config.FlowConfig{
			"mixed": {Name: "Mixed", Start: "a10", Nodes: map[string]config.FlowNode{
				"a10": {Type: config.NodeAgent, Agent: "copilot", Next: "a20"},
				"a20": {Type: config.NodeAgent, Agent: "openrouter", Next: "a30"},
				"a30": {Type: config.NodeAgent, Agent: "gemini", Next: "a40"},
				"a40": {Type: config.NodeAgent, Agent: "copilot"},
			}},
		},
		Repos: map[string]config.RepoAI{"acme/api": {Flow: "mixed"}},
	}}
}

func TestFlowAgentsAndWriteAgents(t *testing.T) {
	c := testFlowConfig()
	_, f := c.FlowForRepo("acme/api")
	if got := strings.Join(flowAgents(f), ","); got != "copilot,openrouter,gemini" {
		t.Errorf("flowAgents = %s", got)
	}
	if got := strings.Join(flowWriteAgents(c, "acme/api"), ","); got != "gemini" {
		t.Errorf("flowWriteAgents = %s (read-only agents cannot resolve conflicts)", got)
	}
	if got := strings.Join(flowWriteAgents(c, "other/repo"), ","); got != "claude,codex" {
		t.Errorf("legacy write agents = %s", got)
	}
}

func TestAgentExecOptions(t *testing.T) {
	c := testFlowConfig()
	o := agentExecOptions("copilot", c.AgentConfigFor("copilot"), "30m", "/work", true)
	if o.Model != "gpt-5.5" || o.Effort != "high" || o.WorkDir != "/work" || o.Timeout != 30*time.Minute {
		t.Errorf("copilot options = %+v", o)
	}
	if o.ExtraFlags != "" {
		t.Errorf("a forbidden extra flag must be dropped, got %q", o.ExtraFlags)
	}
	if o := agentExecOptions("claude", config.CLIAgentConfig{}, "", "", true); o.MaxTurns != config.DefaultLimitedMaxTurns {
		t.Errorf("claude caps = %+v", o)
	}
}

func TestMergeTrackAgentSpecFollowsFlow(t *testing.T) {
	c := testFlowConfig()
	c.AI.Flows["writers"] = config.FlowConfig{Start: "a1", Nodes: map[string]config.FlowNode{
		"a1": {Type: config.NodeAgent, Agent: "copilot", Next: "a2"},
		"a2": {Type: config.NodeAgent, Agent: "opencode", Next: "a3"},
		"a3": {Type: config.NodeAgent, Agent: "codex"},
	}}
	c.AI.Repos["acme/web"] = config.RepoAI{Flow: "writers"}
	spec := mergeTrackAgentSpec(&c, &sync.Mutex{})("acme/web")
	if spec.Primary != "opencode" || spec.Fallback != "codex" {
		t.Errorf("spec = %+v", spec)
	}
	spec = mergeTrackAgentSpec(&c, &sync.Mutex{})("acme/api")
	if spec.Primary != "gemini" || spec.Fallback != "" {
		t.Errorf("single writer spec = %+v", spec)
	}
}

type staticQuotas map[string]quota.Provider

func (s staticQuotas) Get(_ context.Context, agent string) quota.Provider { return s[agent] }

func TestFlowListingAndSimulation(t *testing.T) {
	c := testFlowConfig()
	listing := flowListing(c, nil)
	if listing["selected"] != config.DefaultFlowID || len(listing["flows"].(map[string]config.FlowConfig)) != 2 {
		t.Errorf("listing = %+v", listing)
	}
	if !strings.Contains(strings.Join(listing["write_capable"].([]string), ","), "claude") {
		t.Errorf("write_capable = %v", listing["write_capable"])
	}
	c.AI.Flow = "mixed"
	if flowListing(c, nil)["selected"] != "mixed" {
		t.Error("selected flow")
	}

	available := func(a string) bool { return a != "openrouter" }
	d, err := simulateFlow(context.Background(), c, server.FlowSimulation{Repo: "acme/api"}, staticQuotas{}, available)
	if err != nil || d.FlowID != "mixed" || strings.Join(d.Candidates, ",") != "copilot,gemini" {
		t.Errorf("simulate repo = %+v, %v", d, err)
	}
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	d, err = simulateFlow(context.Background(), c, server.FlowSimulation{Flow: config.DefaultFlowID, At: at}, staticQuotas{}, available)
	if err != nil || !d.At.Equal(at) || strings.Join(d.Candidates, ",") != "claude,codex" {
		t.Errorf("simulate default = %+v, %v", d, err)
	}
	if _, err := simulateFlow(context.Background(), c, server.FlowSimulation{Flow: "ghost"}, staticQuotas{}, available); err == nil {
		t.Error("an unknown flow must fail")
	}

	candidates := flowCandidates("mixed", c.AI.Flows["mixed"], "acme/api", staticQuotas{}, available)
	if got := strings.Join(candidates(), ","); got != "copilot,gemini" {
		t.Errorf("flowCandidates = %s", got)
	}
}

func TestNewQuotaServiceOpenRouter(t *testing.T) {
	keys := openrouter.NewKeyStore(t.TempDir())
	keys.Getenv = func(string) string { return "" }
	admin := &openrouter.Admin{Client: openrouter.NewClient(keys)}
	svc := newQuotaService(admin)
	if p := svc.Get(context.Background(), "openrouter"); p.Error != quota.ErrNotConfigured {
		t.Errorf("no key = %+v", p)
	}
	if err := keys.Set("sk-or-x"); err != nil {
		t.Fatal(err)
	}
	admin.Client.BaseURL = "http://127.0.0.1:1"
	svc = newQuotaService(admin)
	if p := svc.Get(context.Background(), "openrouter"); p.Error != quota.ErrUnavailable {
		t.Errorf("unreachable = %+v", p)
	}
	if p := newQuotaService(nil).Get(context.Background(), "openrouter"); p.Error != quota.ErrNotConfigured {
		t.Errorf("nil admin = %+v", p)
	}
}

func TestOverrideMapsCarryTheFlow(t *testing.T) {
	if got := repoAIOverrideMap(config.RepoAI{Flow: "night"}); got["flow"] != "night" {
		t.Errorf("repo override = %v", got)
	}
	if got := orgAIOverrideMap(config.OrgAI{Flow: "weekday"}); got["flow"] != "weekday" {
		t.Errorf("org override = %v", got)
	}
	if _, ok := repoAIOverrideMap(config.RepoAI{})["flow"]; ok {
		t.Error("no flow selected must not project one")
	}
}

func TestFlowListingMarksInstalledAgents(t *testing.T) {
	c := &config.Config{}
	c.AI.Primary = "claude"
	out := flowListing(c, map[string]bool{"claude": true, "openrouter": true})
	agents := out["agents"].([]FlowAgent)
	byID := map[string]FlowAgent{}
	for _, a := range agents {
		byID[a.ID] = a
	}
	if !byID["claude"].Installed || byID["codex"].Installed {
		t.Fatalf("installed flags wrong: %+v", agents)
	}
	if !byID["openrouter"].ReviewOnly || byID["claude"].ReviewOnly {
		t.Fatalf("review_only flags wrong: %+v", agents)
	}
	if !byID["claude"].CanDraft || byID["opencode"].CanDraft || byID["openrouter"].CanDraft {
		t.Fatalf("can_draft flags wrong: %+v", agents)
	}
}

func TestInstalledAgents(t *testing.T) {
	got := installedAgents([]agentcatalog.Agent{
		{ID: "claude", ConfigAgent: "claude", Installed: true},
		{ID: "cursor", ConfigAgent: "cursor_cli", Installed: true, Kind: agentcatalog.KindIDE},
		{ID: "openrouter", ConfigAgent: "openrouter", Configured: true},
		{ID: "gemini", ConfigAgent: "gemini"},
	})
	want := map[string]bool{"claude": true, "openrouter": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestSimulateFlowUsesUnsavedGraph(t *testing.T) {
	c := &config.Config{}
	g := config.FlowConfig{Name: "draft", Start: "a1", Nodes: map[string]config.FlowNode{"a1": {Type: config.NodeAgent, Agent: "gemini"}}}
	d, err := simulateFlow(context.Background(), c, server.FlowSimulation{Graph: &g}, nil, nil)
	if err != nil || d.FlowID != "draft" || !reflect.DeepEqual(d.Candidates, []string{"gemini"}) {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestAssistContextRunnableAgents(t *testing.T) {
	got := assistContext(context.Background(), []agentcatalog.Agent{
		{ConfigAgent: "claude", Installed: true, Models: []string{"opus"}},
		{ConfigAgent: "openrouter", Configured: true},
		{ConfigAgent: "cursor_cli", Kind: agentcatalog.KindIDE, Installed: true},
		{ConfigAgent: "gemini"},
		{ConfigAgent: "opencode", Installed: true},
	}, nil)
	if len(got.Agents) != 3 || !got.Agents[0].Runnable || got.Agents[1].Runnable || got.Agents[2].Runnable {
		t.Fatalf("%+v", got.Agents)
	}
}

type fakeRawExec struct {
	mu        sync.Mutex
	opts      executor.ExecOptions
	registerA time.Time // Terminate only succeeds after this instant
	terminate int
	done      chan struct{}
	started   chan struct{}
}

func (f *fakeRawExec) ExecuteRaw(cli, prompt string, opts executor.ExecOptions) ([]byte, error) {
	f.mu.Lock()
	f.opts = opts
	f.mu.Unlock()
	if f.started != nil {
		close(f.started)
	}
	if f.done == nil {
		return []byte("ok"), nil
	}
	<-f.done
	return nil, context.Canceled
}

func (f *fakeRawExec) TerminateExecution(string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminate++
	if time.Now().Before(f.registerA) {
		return false, nil
	}
	close(f.done)
	return true, nil
}

func TestExecRunnerStripsPermissionsAndCodexWorkDir(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		f := &fakeRawExec{}
		r := execRunner{exec: f, opts: func(_, workDir string) executor.ExecOptions {
			return executor.ExecOptions{WorkDir: workDir, Model: "m", DangerouslySkipPerms: true,
				PermissionMode: "bypassPermissions", ApprovalMode: "full-auto", ExtraFlags: "--x"}
		}}
		if _, err := r.Run(context.Background(), agent, "p", "/tmp/ws"); err != nil {
			t.Fatal(err)
		}
		o := f.opts
		if o.DangerouslySkipPerms || o.PermissionMode != "" || o.ApprovalMode != "" || o.ExtraFlags != "" || o.Model != "m" {
			t.Fatalf("%s: %+v", agent, o)
		}
		if wantDir := map[string]string{"claude": "/tmp/ws", "codex": ""}[agent]; o.WorkDir != wantDir {
			t.Fatalf("%s WorkDir = %q, want %q", agent, o.WorkDir, wantDir)
		}
	}
}

func TestExecRunnerCancelBeforeRegisterStillTerminates(t *testing.T) {
	f := &fakeRawExec{done: make(chan struct{}), registerA: time.Now().Add(150 * time.Millisecond)}
	r := execRunner{exec: f, opts: func(_, d string) executor.ExecOptions { return executor.ExecOptions{WorkDir: d} }}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	finished := make(chan struct{})
	go func() { _, _ = r.Run(ctx, "claude", "p", "/tmp/ws"); close(finished) }()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel landed before the execution registered and was lost")
	}
	if f.terminate < 2 {
		t.Fatalf("terminate attempts = %d, want a retry", f.terminate)
	}
}

func TestExecRunnerReturnsEarlyWhenAlreadyCancelled(t *testing.T) {
	f := &fakeRawExec{started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := execRunner{exec: f, opts: func(_, d string) executor.ExecOptions { return executor.ExecOptions{} }}
	if _, err := r.Run(ctx, "claude", "p", "/tmp/ws"); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	select {
	case <-f.started:
		t.Fatal("ExecuteRaw must not start for a cancelled context")
	default:
	}
}

// OpenRouter has no default model: like a missing key, a missing model makes
// it unavailable, so flows skip it instead of failing every review on it.
func TestFlowAgentAvailable_OpenRouterNeedsAModel(t *testing.T) {
	model := ""
	installed := map[string]bool{"openrouter": true, "claude": true}
	available := flowAgentAvailable(
		func(agent string) bool { return installed[agent] },
		func(agent string) string {
			if agent == "openrouter" {
				return model
			}
			return ""
		},
	)
	if available("openrouter") {
		t.Error("openrouter without a model must be unavailable")
	}
	model = "   "
	if available("openrouter") {
		t.Error("a blank model is no model")
	}
	if !available("claude") {
		t.Error("CLI agents keep their CLI default model")
	}
	model = "vendor/model"
	if !available("openrouter") {
		t.Error("openrouter with a model and a key is available")
	}
	installed["openrouter"] = false
	if available("openrouter") {
		t.Error("openrouter without a key stays unavailable")
	}

	f := config.FlowConfig{Name: "F", Start: "a1", Nodes: map[string]config.FlowNode{
		"a1": {Type: config.NodeAgent, Agent: "openrouter", Next: "a2"},
		"a2": {Type: config.NodeAgent, Agent: "claude"},
	}}
	installed["openrouter"] = true
	model = ""
	got := flowCandidates("f", f, "o/r", staticQuotas{}, available)()
	if strings.Join(got, ",") != "claude" {
		t.Errorf("candidates = %v, want claude only", got)
	}
}

func TestDetectedByAndAgentModelsSnapshot(t *testing.T) {
	detected := detectedBy(func(primary, fallback string) (string, error) {
		if primary == "claude" && fallback == "" {
			return "claude", nil
		}
		return "", errors.New("not installed")
	})
	if !detected("claude") || detected("gemini") {
		t.Error("detectedBy must follow Detect's error")
	}

	var models agentModels
	if models.model("openrouter") != "" {
		t.Error("an empty snapshot has no models")
	}
	models.store(&config.Config{AI: config.AIConfig{Agents: map[string]config.CLIAgentConfig{
		"openrouter": {Model: "vendor/model"},
	}}})
	if models.model("openrouter") != "vendor/model" || models.model("claude") != "" {
		t.Error("the snapshot must hold the configured models")
	}
	// A config reload stores a new snapshot: readers follow it.
	models.refresh(&config.Config{}, func(string) bool { return false })
	if models.model("openrouter") != "" {
		t.Error("the snapshot must follow a reload")
	}
}

func TestWarnModelRequiredWithoutModel(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	configured := func(a string) bool { return a == "openrouter" }
	warnModelRequiredWithoutModel(&config.Config{}, configured)
	if !strings.Contains(buf.String(), "agent=openrouter") {
		t.Errorf("want a warning for openrouter without a model, got %q", buf.String())
	}
	buf.Reset()
	warnModelRequiredWithoutModel(&config.Config{AI: config.AIConfig{Agents: map[string]config.CLIAgentConfig{
		"openrouter": {Model: "vendor/model"},
	}}}, configured)
	warnModelRequiredWithoutModel(&config.Config{}, func(string) bool { return false })
	if buf.Len() != 0 {
		t.Errorf("no warning with a model or without a key, got %q", buf.String())
	}
}
