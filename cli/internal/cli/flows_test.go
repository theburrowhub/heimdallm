package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestPrintFlows(t *testing.T) {
	var buf bytes.Buffer
	printFlows(&buf, &api.FlowListing{
		Selected:     "weekday",
		WriteCapable: []string{"claude", "codex"},
		Flows: map[string]api.Flow{
			"weekday": {Name: "Weekday\x1b[2J", Rules: map[string]api.FlowRule{
				"20": {Agent: "codex"},
				"10": {Agent: "claude", Quota: map[string]api.QuotaCondition{
					"q": {Agent: "claude", Window: "session", Op: "below", Percent: 50},
				}},
			}},
			"empty": {},
		},
	})
	out := buf.String()
	for _, want := range []string{
		"weekday — Weekday", "(global)", "1. claude when claude session quota < 50%",
		"2. codex always", "empty", "(no rules)", "Conflict resolution uses only: claude, codex",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("control bytes reached the terminal: %q", out)
	}
}

func TestPrintFlowDecision(t *testing.T) {
	var buf bytes.Buffer
	printFlowDecision(&buf, &api.FlowDecision{
		FlowID: "weekday", FlowName: "Weekday", At: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC),
		Candidates: []string{"copilot", "codex"},
		Rules: []api.FlowRuleResult{
			{Agent: "claude", Reasons: []string{"claude session quota below 50% does not hold (used 70%)"}},
			{Agent: "copilot", Matched: true, Available: true, Reasons: []string{"schedule holds"}},
			{Agent: "gemini", Matched: true, Reasons: []string{"gemini is not installed"}},
		},
	}, false)
	out := buf.String()
	for _, want := range []string{
		"Flow weekday — Weekday at", "Reviews with copilot, then codex if it runs out of quota",
		"✗ claude", "used 70%", "✓ copilot", "⚠ gemini", "UTC",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	buf.Reset()
	printFlowDecision(&buf, &api.FlowDecision{FlowID: "x", Candidates: []string{"codex"}}, true)
	if !strings.Contains(buf.String(), "Reviews with codex\n") || strings.Contains(buf.String(), " at ") {
		t.Errorf("single candidate:\n%s", buf.String())
	}
	buf.Reset()
	printFlowDecision(&buf, &api.FlowDecision{FlowID: "x"}, true)
	if !strings.Contains(buf.String(), "No agent would review now") || !strings.Contains(buf.String(), "the review would wait") {
		t.Errorf("no candidates:\n%s", buf.String())
	}
	buf.Reset()
	printFlowDecision(&buf, &api.FlowDecision{FlowID: "x"}, false)
	if !strings.Contains(buf.String(), "No agent would review at that time") || strings.Contains(buf.String(), "review now") {
		t.Errorf("no candidates with --at:\n%s", buf.String())
	}
}

func TestPrintQuotas(t *testing.T) {
	var buf bytes.Buffer
	printQuotas(&buf, []api.AgentQuota{
		{Agent: "claude", Available: true, Windows: []api.QuotaWindow{{Kind: "session", UsedPercent: 34}}},
		{Agent: "cursor_cli", Error: "unsupported"},
	})
	for _, want := range []string{"claude", "5h 34%", "unknown (unsupported)"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in:\n%s", want, buf.String())
		}
	}
}

func TestFlowsCommandIsRegistered(t *testing.T) {
	cmd := newFlowsCmd()
	if cmd.Use != "flows" {
		t.Fatalf("flows command = %+v", cmd)
	}
	sim, _, err := cmd.Find([]string{"simulate"})
	if err != nil || sim.Flags().Lookup("flow") == nil || sim.Flags().Lookup("repo") == nil || sim.Flags().Lookup("at") == nil {
		t.Fatalf("simulate = %+v, %v", sim, err)
	}
	if q, _, err := cmd.Find([]string{"quotas"}); err != nil || q.Use != "quotas" {
		t.Fatalf("quotas = %+v, %v", q, err)
	}
	found := false
	for _, c := range NewRootCmd("test").Commands() {
		found = found || c.Use == "flows"
	}
	if !found {
		t.Error("flows is not registered on the root command")
	}
}

func TestCfgFlowLines(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if got := strings.Join(cfgAILines(map[string]any{"ai_flow": ""}), "\n"); !strings.Contains(got, "default (primary/fallback)") {
		t.Errorf("no flow selected:\n%s", got)
	}
	// A daemon without review flows does not send ai_flow: no Flow line.
	if got := strings.Join(cfgAILines(map[string]any{"ai_primary": "claude"}), "\n"); strings.Contains(got, "Flow") {
		t.Errorf("older daemon:\n%s", got)
	}
	if got := strings.Join(cfgAILines(map[string]any{"ai_flow": "weekday"}), "\n"); !strings.Contains(got, "weekday") {
		t.Errorf("flow:\n%s", got)
	}
	org := strings.Join(cfgOrgLines(map[string]any{
		"org_overrides": map[string]any{"acme": map[string]any{"flow": "night"}},
	}), "\n")
	if !strings.Contains(org, "Flow") || !strings.Contains(org, "night") {
		t.Errorf("org flow:\n%s", org)
	}
	repo := strings.Join(cfgRepoLines(map[string]any{
		"repositories":   []any{"acme/a"},
		"repo_overrides": map[string]any{"acme/a": map[string]any{"flow": "night"}},
	}), "\n")
	if !strings.Contains(repo, "night") {
		t.Errorf("repo flow:\n%s", repo)
	}
}

func TestFlowsCommandsRun(t *testing.T) {
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, `{"error":"down"}`, http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/flows":
			_, _ = w.Write([]byte(`{"selected":"default","flows":{"default":{"rules":{"10":{"agent":"claude"}}}}}`))
		case "/flows/simulate":
			_, _ = w.Write([]byte(`{"flow_id":"default","candidates":["claude"]}`))
		case "/quotas":
			_, _ = w.Write([]byte(`[{"agent":"claude","available":true}]`))
		}
	}))
	defer srv.Close()
	ctx := contextWithClient(api.New(srv.URL, ""))

	root := newFlowsCmd()
	sim, _, _ := root.Find([]string{"simulate"})
	quotas, _, _ := root.Find([]string{"quotas"})
	for _, c := range []*cobra.Command{root, sim, quotas} {
		c.SetContext(ctx)
		if err := c.RunE(c, nil); err != nil {
			t.Errorf("%s: %v", c.Use, err)
		}
	}
	if err := sim.Flags().Set("at", "2026-10-08T09:00:00+02:00"); err != nil {
		t.Fatal(err)
	}
	if err := sim.RunE(sim, nil); err != nil {
		t.Errorf("simulate --at: %v", err)
	}
	_ = sim.Flags().Set("at", "tomorrow")
	if err := sim.RunE(sim, nil); err == nil || !strings.Contains(err.Error(), "RFC 3339") {
		t.Errorf("bad --at = %v", err)
	}
	_ = sim.Flags().Set("at", "")

	fail = true
	for _, c := range []*cobra.Command{root, sim, quotas} {
		if err := c.RunE(c, nil); err == nil {
			t.Errorf("%s must report a daemon error", c.Use)
		}
	}
}
