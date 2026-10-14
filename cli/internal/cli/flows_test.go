package cli

import (
	"bytes"
	"io"
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
		Selected: "weekday",
		Agents: []api.FlowAgent{{ID: "claude", Installed: true, WriteCapable: true}, {ID: "codex", Installed: true, WriteCapable: true},
			{ID: "aider", WriteCapable: true}, {ID: "gemini", Installed: true}},
		Flows: map[string]api.Flow{
			"weekday": {Name: "Weekday\x1b[2J", Start: "c1", Nodes: map[string]api.FlowNode{
				"c1": {Type: "condition", Yes: "a1", No: "a2", Quota: map[string]api.QuotaCondition{
					"q": {Agent: "claude", Window: "session", Op: "below", Percent: 50},
				}},
				"a1": {Type: "agent", Agent: "claude"},
				"a2": {Type: "agent", Agent: "codex"},
			}},
			"empty": {},
		},
	})
	out := buf.String()
	for _, want := range []string{
		"weekday — Weekday", "(global)", "c1: if claude session quota < 50% → yes: a1 / no: a2",
		"a1: claude → end", "a2: codex → end", "empty", "(no steps)", "Conflict resolution uses only: claude, codex",
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
	yes, no := true, false
	var buf bytes.Buffer
	printFlowDecision(&buf, &api.FlowDecision{
		FlowID: "weekday", FlowName: "Weekday", At: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC),
		Candidates: []string{"copilot", "codex"},
		Path: []api.PathStep{
			{Node: "c1", Type: "condition", Holds: &no, Reasons: []string{"claude session quota below 50% does not hold (used 70%)"}},
			{Node: "a1", Type: "agent", Agent: "copilot", Available: &yes, Added: true, Reasons: []string{"schedule holds"}},
			{Node: "a2", Type: "agent", Agent: "gemini", Available: &no, Reasons: []string{"gemini is not installed"}},
			{Node: "a3", Type: "agent", Agent: "codex", Available: &yes},
			{Node: "z", Type: "weird"},
		},
	}, false)
	out := buf.String()
	for _, want := range []string{
		"Flow weekday — Weekday at", "Reviews with copilot, then codex if it runs out of quota",
		"c1 condition → no", "used 70%", "a1 copilot ✓ added", "a2 gemini ⚠", "a3 codex · already listed", "  z\n", "UTC",
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
	if as, _, err := cmd.Find([]string{"assist"}); err != nil || as.Flags().Lookup("flow") == nil || as.Flags().Lookup("agent") == nil || as.Flags().Lookup("apply") == nil {
		t.Fatalf("assist = %+v, %v", as, err)
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
			_, _ = w.Write([]byte(`{"selected":"default","flows":{"default":{"start":"a1","nodes":{"a1":{"type":"agent","agent":"claude"}}}}}`))
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

func TestPrintFlowDecisionShowsPath(t *testing.T) {
	yes, no := true, false
	d := &api.FlowDecision{FlowID: "night", Candidates: []string{"codex"}, Path: []api.PathStep{
		{Node: "c1", Type: "condition", Holds: &yes, Reasons: []string{"schedule 22:00-06:00 holds"}},
		{Node: "a1", Type: "agent", Agent: "codex", Available: &yes, Added: true},
		{Node: "a2", Type: "agent", Agent: "gemini", Available: &no, Reasons: []string{"gemini is not installed or not configured"}},
		{Node: "c2", Type: "condition"},
	}}
	var buf bytes.Buffer
	printFlowDecision(&buf, d, true)
	out := buf.String()
	for _, want := range []string{"Reviews with codex", "c1 condition → yes", "a1 codex ✓ added", "a2 gemini ⚠", "not installed", "c2 condition → ?"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
}

// runFlows runs `flows <args>` against a fake daemon and captures its output.
func runFlows(t *testing.T, daemonURL string, args ...string) (string, error) {
	t.Helper()
	return runFlowsWithInput(t, daemonURL, "", args...)
}

// runFlowsWithInput is runFlows with stdin set to input.
func runFlowsWithInput(t *testing.T, daemonURL, input string, args ...string) (string, error) {
	t.Helper()
	cmd := newFlowsCmd()
	cmd.SetIn(strings.NewReader(input))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(contextWithClient(api.New(daemonURL, "")))
	return buf.String(), err
}

func TestFlowsAssistPrintsPlanAndApplies(t *testing.T) {
	var put bool
	var assistBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/flows":
			_, _ = w.Write([]byte(`{"flows":{"night":{"name":"Night","start":"a1","nodes":{"a1":{"type":"agent","agent":"claude"}}}},"selected":"night","default_id":"default","agents":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/flows/assist":
			b, _ := io.ReadAll(r.Body)
			assistBody = string(b)
			_, _ = w.Write([]byte(`{"plan":"Codex first.","agent":"claude","warnings":["careful"],"flow":{"name":"Night","start":"a1","nodes":{"a1":{"type":"agent","agent":"codex"}}}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/flows/night":
			put = true
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()
	out, err := runFlows(t, ts.URL, "assist", "codex first", "--flow", "night", "--agent", "claude", "--apply")
	if err == nil || put || !strings.Contains(err.Error(), "--force") || !strings.Contains(out, "⚠ careful") {
		t.Fatalf("a draft with warnings must not be saved without --force: err=%v put=%v out=\n%s", err, put, out)
	}
	out, err = runFlowsWithInput(t, ts.URL, "y\n", "assist", "codex first", "--flow", "night", "--apply", "--force")
	if err != nil || !put || !strings.Contains(out, "Codex first.") || !strings.Contains(out, "a1: codex") ||
		!strings.Contains(out, "Save this draft as night? [y/N]") || !strings.Contains(out, "Saved as night.") {
		t.Fatalf("err=%v put=%v out=\n%s", err, put, out)
	}
	if !strings.Contains(assistBody, `"flow":{"name":"Night"`) || !strings.Contains(assistBody, `"agent":"claude"`) {
		t.Errorf("the current flow and the default agent must be sent: %s", assistBody)
	}

	put = false
	out, err = runFlowsWithInput(t, ts.URL, "n\n", "assist", "codex first", "--flow", "night", "--apply", "--force")
	if err != nil || put || !strings.HasSuffix(out, "Not saved.\n") {
		t.Fatalf("declining must not save: err=%v put=%v out=\n%s", err, put, out)
	}
	out, err = runFlows(t, ts.URL, "assist", "codex first", "--flow", "night", "--apply", "--force", "--yes")
	if err != nil || !put || strings.Contains(out, "[y/N]") {
		t.Fatalf("--yes saves without asking: err=%v put=%v out=\n%s", err, put, out)
	}

	put = false
	out, err = runFlows(t, ts.URL, "assist", "codex first", "--flow", "night")
	if err != nil || put || !strings.Contains(out, "Not saved.") {
		t.Fatalf("without --apply nothing is saved: err=%v put=%v out=\n%s", err, put, out)
	}
}

func TestFlowsAssistGuardsTheFlowID(t *testing.T) {
	var put, assist int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/flows":
			_, _ = w.Write([]byte(`{"flows":{"night":{"name":"Night","start":"a1","nodes":{"a1":{"type":"agent","agent":"claude"}}}},"agents":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/flows/assist":
			assist++
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(string(b), `"flow"`) {
				t.Errorf("a new flow has no current flow: %s", b)
			}
			_, _ = w.Write([]byte(`{"plan":"First line.\n\nSecond\u001b[2J line.","agent":"codex","flow":{"name":"Day","start":"a1","nodes":{"a1":{"type":"agent","agent":"codex"}}}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/flows/day":
			put++
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()
	if _, err := runFlows(t, ts.URL, "assist", "x", "--flow", "nigth", "--apply"); err == nil || !strings.Contains(err.Error(), "there is no flow nigth") || assist != 0 {
		t.Fatalf("a mistyped id must fail before drafting: %v (assist %d)", err, assist)
	}
	if _, err := runFlows(t, ts.URL, "assist", "x", "--flow", "night", "--new"); err == nil || !strings.Contains(err.Error(), "already exists") || assist != 0 {
		t.Fatalf("--new on an existing id must fail: %v (assist %d)", err, assist)
	}
	out, err := runFlows(t, ts.URL, "assist", "x", "--flow", "day", "--new", "--agent", "codex", "--apply", "--yes")
	if err != nil || put != 1 || !strings.Contains(out, "  First line.\n  Second[2J line.\n") {
		t.Fatalf("err=%v put=%d out=\n%s", err, put, out)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			t.Errorf("an empty draft must not be saved")
		}
		_, _ = w.Write([]byte(`{"plan":"p","flow":{"name":"x"}}`))
	}))
	defer empty.Close()
	if _, err := runFlows(t, empty.URL, "assist", "x", "--flow", "x", "--new", "--apply", "--yes"); err == nil || !strings.Contains(err.Error(), "no steps") {
		t.Fatalf("empty draft: %v", err)
	}
}

func TestFlowsAssistErrors(t *testing.T) {
	if _, err := runFlows(t, "http://127.0.0.1:1", "assist", "x", "--apply"); err == nil || !strings.Contains(err.Error(), "--apply needs --flow") {
		t.Fatalf("%v", err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"down"}`, http.StatusServiceUnavailable)
	}))
	defer down.Close()
	if _, err := runFlows(t, down.URL, "assist", "x"); err == nil || !strings.Contains(err.Error(), "drafting flow") {
		t.Errorf("assist error = %v", err)
	}
	if _, err := runFlows(t, down.URL, "assist", "x", "--flow", "n"); err == nil || !strings.Contains(err.Error(), "fetching flows") {
		t.Errorf("listing error = %v", err)
	}
}
