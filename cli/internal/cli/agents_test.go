package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestPrintAgents(t *testing.T) {
	var buf bytes.Buffer
	printAgents(&buf, &api.CLIAgentCatalog{
		ScannedAt: time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC),
		Agents: []api.CLIAgent{
			{ID: "claude", Name: "Claude Code", Installed: true, Version: "2.1.292", Configured: true},
			{ID: "cursor", Name: "Cursor IDE", Kind: "ide", Installed: true, ConfigAgent: "cursor_cli"},
			{ID: "copilot", Name: "GitHub Copilot CLI", InstallHint: "npm install -g @github/copilot\x1b[31m"},
			{ID: "opencode", Name: "OpenCode", Installed: true, Models: []string{"a", "b"}},
		},
	})
	out := buf.String()
	for _, want := range []string{
		"installed 2.1.292", "configured", "reviews via cursor_cli", "not installed",
		"install: npm install -g @github/copilot", "2 models", "Scanned",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("control bytes reached the terminal: %q", out)
	}
	buf.Reset()
	printAgents(&buf, &api.CLIAgentCatalog{Agents: []api.CLIAgent{{ID: "x", Name: "X", Installed: true}}})
	if strings.Contains(buf.String(), "Scanned") {
		t.Error("no scan time must print no Scanned line")
	}
}

func TestAgentsCommandIsRegistered(t *testing.T) {
	cmd := newAgentsCmd()
	if cmd.Use != "agents" || cmd.Flags().Lookup("rescan") == nil {
		t.Fatalf("agents command = %+v", cmd)
	}
}
